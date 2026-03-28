package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/ratelimiter"
)

// DemoService 示例服务，实现 orbit.Service 接口
type DemoService struct{}

// RegisterGroup 注册路由组
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 全局限流模式演示
	g.GET("/global", func(c *gin.Context) {
		c.String(http.StatusOK, "全局限流演示 - QPS=10, Burst=20")
	})

	// IP限流模式演示
	g.GET("/per-ip", func(c *gin.Context) {
		c.String(http.StatusOK, fmt.Sprintf("IP限流演示 - QPS=5, Burst=10, 客户端IP: %s", c.ClientIP()))
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册全局限流中间件
	// ModeGlobal: 所有请求共享同一个令牌桶
	// QPS=10: 每秒允许10个请求
	// Burst=20: 允许最大突发20个请求
	engine.RegisterMiddleware(ratelimiter.New(ratelimiter.Config{
		Mode:  ratelimiter.ModeGlobal,
		QPS:   10,
		Burst: 20,
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
