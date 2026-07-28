package svc

import "net/http"

func FetchData() {
	http.Get("/api/x")
}
