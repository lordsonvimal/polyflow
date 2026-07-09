//go:build ignore

package main

// Route shapes mirrored from real Gin services (chessleap, mysycamore,
// dsw-manager): engine routes, grouped routes with middleware, and
// bind/respond handler bodies.
func setup() {
	r := gin.Default()
	r.Use(gin.Recovery())
	r.GET("/health", healthCheck)
	r.POST("/games", createGame)

	api := r.Group("/api/v1")
	api.GET("/games/:id", getGame)
	api.DELETE("/games/:id", deleteGame)
	r.Run(":8080")
}

func createGame(c *gin.Context) {
	var req CreateGameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, req)
}
