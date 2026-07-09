//go:build ignore

package main

// chi and net/http shapes must NOT match the gin patterns: chi verbs are
// TitleCase, chi's Group takes a func literal (not a string), and generic
// encoders/loggers reuse method names like JSON with different arity.
func chiRoutes(r Router) {
	r.Get("/users", listUsers)
	r.Post("/users", createUser)
	r.Route("/admin", func(r Router) {
		r.Get("/stats", adminStats)
	})
	r.Group(func(r Router) {
		r.Get("/nested", nested)
	})
	mux.HandleFunc("/legacy", legacyHandler)
}

func respond(w Writer, enc Encoder) {
	enc.JSON(payload)               // 1 arg — not gin's c.JSON(status, body)
	dec.ShouldBindJSON(req, extra)  // 2 args, non-pointer — not gin's bind
}
