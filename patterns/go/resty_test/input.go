//go:build ignore

package main

func fetch(client *resty.Client) {
	client.Get("/api/users")
	client.Post("/api/users")
}
