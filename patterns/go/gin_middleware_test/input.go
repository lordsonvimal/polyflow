//go:build ignore

package main

// Gin middleware registrations: on the engine and on a route group.
func setup() {
	r := gin.Default()
	r.Use(gin.Recovery())

	protected := r.Group("/api")
	protected.Use(authMiddleware.Authenticate())
	protected.GET("/me", getMe)
}
