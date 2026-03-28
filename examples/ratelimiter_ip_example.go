package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/ratelimiter"
)

// DemoService 示例服务
type DemoService struct{}

// RegisterGroup 注册路由组
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 模拟耗时操作
	g.GET("/slow", func(c *gin.Context) {
		time.Sleep(2 * time.Second)
		c.String(http.StatusOK, "慢速响应完成")
	})

	// IP限流演示
	g.GET("/per-ip", func(c *gin.Context) {
		c.String(http.StatusOK, fmt.Sprintf("IP限流演示 - QPS=5, Burst=10, 客户端IP: %s", c.ClientIP()))
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册IP限流中间件
	// ModeIP: 每个IP独立使用一个令牌桶
	// QPS=5: 每个IP每秒允许5个请求
	// Burst=10: 每个IP允许最大突发10个请求
	// TTL=10m: IP限流记录10分钟后自动清理
	engine.RegisterMiddleware(ratelimiter.New(ratelimiter.Config{
		Mode:  ratelimiter.ModeIP,
		QPS:   5,
		Burst: 10,
		TTL:   10 * time.Minute,
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
