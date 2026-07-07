package main

import "github.com/go-chi/chi/v5"

func main() {
	r := chi.NewRouter()
	r.Get("/users", listUsers)
	r.Post("/users", createUser)
	r.Route("/admin", func(r chi.Router) {
		r.Get("/stats", adminStats)
	})
}
