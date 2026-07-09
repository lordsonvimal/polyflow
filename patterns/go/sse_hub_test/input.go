//go:build ignore

package main

// chessleap game/hub.go shape: channel fan-out hub feeding SSE writers.
func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, 8)
	h.mu.Lock()
	h.subs[ch] = true
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan Event) {
	delete(h.subs, ch)
}

func (h *Hub) Broadcast(evt Event) {
	for ch := range h.subs {
		ch <- evt
	}
}

func handleMove(hub *Hub, move Move) {
	hub.Broadcast(Event{Kind: "move", Data: move})
}

func streamGame(hub *Hub, w Writer) {
	ch := hub.Subscribe()
	for evt := range ch {
		writeSSE(w, evt)
	}
}
