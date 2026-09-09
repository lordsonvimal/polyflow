//go:build ignore

// A second file, to prove distinct-file counting and per-file attribution: the
// router value arrives as a parameter typed by the package rather than from a
// constructor call.
package handlers

import "github.com/example/orion/router"

func Mount(r router.Router) {
	r.Get("/willow", index)
	r.Put("/willow/{id}", update)
}
