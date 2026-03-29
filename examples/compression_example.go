package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit-middlewares/middleware/compression"
)

type DemoService struct{}

func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	g.GET("/ping", func(c *gin.Context) {
		requestID, _ := c.Get("request_id")
		c.JSON(http.StatusOK, gin.H{
			"message":    "pong",
			"request_id": requestID,
		})
	})
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	r.Use(compression.New(compression.Config{
		MinLength: 1024,
	}))

	r.GET("/hello", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	r.GET("/json", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "Hello, World!",
			"time":    time.Now().Unix(),
		})
	})

	fmt.Println("Compression middleware example server running on :8080")
	fmt.Println("Test with: curl -H 'Accept-Encoding: gzip' http://localhost:8080/hello")

	if err := r.Run(":8080"); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}
