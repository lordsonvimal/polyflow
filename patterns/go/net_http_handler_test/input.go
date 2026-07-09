//go:build ignore

package main

import "net/http"

func routes(mux *http.ServeMux) {
	mux.HandleFunc("/api/users", handleUsers)
	mux.Handle("/api/health", http.HandlerFunc(handleHealth))
}
