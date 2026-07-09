//go:build ignore

package main

// Plain functions named Subscribe (not hub methods), broadcasts with wrong
// arity, and value receivers on unrelated helpers must not match.
func Subscribe() chan int {
	return make(chan int)
}

func run(tv Television, radio Radio) {
	tv.Broadcast()
	radio.Broadcast(signal, band)
	stream.Subscribe(topic)
}
