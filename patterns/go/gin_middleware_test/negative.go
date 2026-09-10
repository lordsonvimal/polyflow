//go:build ignore

package main

// Structural negatives: a chained receiver (call, not identifier) and a
// lowercase selector are not Gin's `ident.Use(mw)`.
func x() {
	r.Group("/u").Use(optionalAuth).POST("", createUser)
	obj.use(thing)
}
