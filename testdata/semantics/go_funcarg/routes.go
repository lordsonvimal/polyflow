package funcargtest

import "net/http"

// routes registers handlers by passing them as function values — the B.1
// pattern: the callee (webHandler) is in the same service, but the
// arguments (viewLogin, processQueue) are the important targets.
func routes() {
	webHandler(viewLogin, 0)  // routes → viewLogin via func_arg
	go worker(processQueue)   // routes → processQueue via func_arg
}

// webHandler simulates a gorilla/mux-style registration that accepts a
// handler function directly (no interface wrapping).
func webHandler(fn func(http.ResponseWriter, *http.Request), level int) {}

// worker simulates a goroutine-spawned background job dispatcher.
func worker(fn func()) { fn() }

func viewLogin(w http.ResponseWriter, r *http.Request) {}
func processQueue()                                    {}
