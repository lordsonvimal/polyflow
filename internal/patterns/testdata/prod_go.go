package svc

import "net/http"

func FetchData() {
	http.Get("http://example.com/x")
}
