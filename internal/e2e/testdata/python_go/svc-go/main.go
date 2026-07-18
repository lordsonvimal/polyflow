//go:build ignore

package main

import "github.com/gin-gonic/gin"

func main() {
	r := gin.Default()
	r.GET("/users", listUsers)
	r.POST("/users", createUser)
	r.Run(":8080")
}

func listUsers(c *gin.Context) {
	c.JSON(200, []string{})
}

func createUser(c *gin.Context) {
	c.JSON(201, map[string]string{})
}
