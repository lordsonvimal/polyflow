//go:build ignore

// Frozen fixture corpus for Tier PS. Invented package, invented service names.
package main

import "github.com/example/orion/router"

func setup() {
	r := router.New()
	r.Get("/users", listUsers)
	r.Post("/users", createUser)
	r.Delete("/users/{id}", deleteUser)
	r.Use(logging)          // callable key: `logging` is declared below
	r.SetLimit(maxInFlight) // no key: a value, neither literal nor callable
	r.Group("/admin", func(r router.Router) {
		r.Get("/stats", adminStats)
	})
	mountWillow(r) // the router crosses a function boundary as an argument
}

// A package value arriving as a parameter, and the call that hands it over:
// neither is a `receiver.Method(...)` call site, and both are how a real corpus
// registers most of its routes.
func mountWillow(r router.Router) {
	r.Get("/willow/mount", index)
}

func logging(next router.Handler) router.Handler { return next }
