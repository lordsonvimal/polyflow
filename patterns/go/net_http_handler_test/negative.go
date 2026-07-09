//go:build ignore

package negative

func setup(mux Mux, pattern string) {
	mux.Handle(pattern, handler) // non-literal path — must not match
	errs.HandleAll(callback)
	worker.Handle(job)
}