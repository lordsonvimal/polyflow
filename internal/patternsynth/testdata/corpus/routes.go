//go:build ignore

// Frozen fixture corpus for Tier PS. Invented package, invented service names.
package main

import "github.com/example/orion/router"

func setup() {
	r := router.New()
	r.Get("/users", listUsers)
	r.Post("/users", createUser)
	r.Delete("/users/{id}", deleteUser)
	r.Use(logging) // no literal argument: nothing to extract, must be rejected
	r.Group("/admin", func(r router.Router) {
		r.Get("/stats", adminStats)
	})
}
