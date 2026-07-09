//go:build ignore

package negative

// Gin-style uppercase verbs and non-route Get calls must not match chi patterns.
func routes(r Engine) {
	r.GET("/users", listUsers)
	r.POST("/users", createUser)
	value := cache.Get("key")
	_ = value
	http.Get("http://example.com")
}