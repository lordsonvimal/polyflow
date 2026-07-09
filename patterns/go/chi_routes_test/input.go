//go:build ignore

package main

import "github.com/go-chi/chi/v5"

func setup() {
	r := chi.NewRouter()
	r.Get("/users", listUsers)
	r.Post("/users", createUser)
	r.Put("/users/{id}", updateUser)
	r.Delete("/users/{id}", deleteUser)
	r.Route("/admin", func(r chi.Router) {
		r.Get("/stats", adminStats)
	})
}
