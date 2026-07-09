package main

import "github.com/gin-gonic/gin"

func main() {
	r := gin.Default()
	r.POST("/move", handleMove)
	r.GET("/stream", streamGame)
	r.Run(":8080")
}

// handleMove applies a move and fans it out to every connected SSE client.
func handleMove(c *gin.Context) {
	move := parseMove(c)
	gameHub.Broadcast(Event{Kind: "move", Data: move})
	c.JSON(200, move)
}

func parseMove(c *gin.Context) Move {
	var move Move
	c.ShouldBindJSON(&move)
	return move
}

// streamGame is the per-connection SSE writer fed by the hub.
func streamGame(c *gin.Context) {
	ch := gameHub.Subscribe()
	defer gameHub.Unsubscribe(ch)
	for evt := range ch {
		writeSSE(c, evt)
	}
}

func writeSSE(c *gin.Context, evt Event) {
	c.SSEvent("message", evt)
}
