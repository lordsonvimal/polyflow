//go:build ignore

package main

import (
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{}

func main() {
	r := gin.Default()
	r.GET("/notifications", serveNotifications)
	r.GET("/health", healthCheck)
	r.Run(":8080")
}

// serveNotifications upgrades the connection and hands it to the read pump.
// The Upgrade() call site itself carries no path — the route registration
// above is what names "/notifications".
func serveNotifications(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	go readPump(conn)
}

func readPump(conn *websocket.Conn) {
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			return
		}
	}
}

// healthCheck is a plain HTTP handler at a different path — must never be
// joined by the ws_connect rule (PW.1 gate 4).
func healthCheck(c *gin.Context) {
	c.JSON(200, gin.H{"status": "ok"})
}
