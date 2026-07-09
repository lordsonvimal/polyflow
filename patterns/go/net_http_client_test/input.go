//go:build ignore

package main

import "net/http"

func fetch() {
	http.Get("http://api/users")
	http.Post("http://api/users", "application/json", nil)
	req, _ := http.NewRequest("PUT", "http://api/users/1", nil)
	_ = req
}
