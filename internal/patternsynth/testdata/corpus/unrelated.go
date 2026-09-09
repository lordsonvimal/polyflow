//go:build ignore

// Never imports the target package: every call below superficially resembles a
// route registration and none of them may be sampled or matched.
package unrelated

func lookups(cache Cache, r Engine) {
	value := cache.Get("key")
	_ = value
	r.GET("/users", listUsers)
	r.Get("/users", listUsers)
}
