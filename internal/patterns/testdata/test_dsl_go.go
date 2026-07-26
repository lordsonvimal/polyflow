package svc

import (
	"net/http"
	"testing"
)

func TestFetch(t *testing.T) {
	http.Get("http://example.com/x")
}

func TestFetchSubtests(t *testing.T) {
	t.Run("fetches", func(t *testing.T) {
		http.Get("http://example.com/y")
	})
}
