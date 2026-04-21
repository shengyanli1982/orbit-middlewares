package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/security"
)

type DemoService struct{}

func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	g.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Security headers enabled")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	engine.RegisterMiddleware(security.New(security.DefaultConfig()))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
