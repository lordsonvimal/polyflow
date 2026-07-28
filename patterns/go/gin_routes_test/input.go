//go:build ignore

package main

// Route shapes mirrored from real Gin services (chessleap, svc-b,
// svc-c-mgr): engine routes, grouped routes with middleware, and
// bind/respond handler bodies.
func setup() {
	r := gin.Default()
	r.Use(gin.Recovery())
	r.GET("/health", healthCheck)
	r.POST("/games", createGame)

	api := r.Group("/api/v1")
	api.GET("/games/:id", getGame)
	api.DELETE("/games/:id", deleteGame)
	r.Match([]string{"GET", "HEAD"}, "/status", healthCheck)
	r.Group("/user").Use(optionalAuth).POST("", createUser)
	registerUserRoutes(api, userHandler)
	r.Run(":8080")
}

// X.9: a registrar receiving its group across a function boundary. The caller
// passes `api` (→ /api/v1); EnrichRouteGroups seeds `rg` from it so this route
// composes to /api/v1/users.
func registerUserRoutes(rg *gin.RouterGroup, h *UserHandler) {
	rg.GET("/users", listUsers)
}

func createGame(c *gin.Context) {
	var req CreateGameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, req)
}
