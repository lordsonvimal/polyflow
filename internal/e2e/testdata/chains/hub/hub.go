package main

import "sync"

// Hub is the chessleap-style broadcast hub: channel fan-out to
// per-connection SSE writers.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]bool
}

var gameHub = &Hub{subs: make(map[chan Event]bool)}

func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, 8)
	h.mu.Lock()
	h.subs[ch] = true
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *Hub) Broadcast(evt Event) {
	h.mu.Lock()
	for ch := range h.subs {
		ch <- evt
	}
	h.mu.Unlock()
}
