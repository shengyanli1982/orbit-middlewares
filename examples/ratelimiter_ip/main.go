// ratelimiter IP 限流示例：每个客户端 IP 独立持有一个令牌桶。
//
// 运行: go run ./ratelimiter_ip
// 测试: 连续快速请求 curl -i http://127.0.0.1:8080/per-ip ，突发超过 Burst 后返回 429
package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/ratelimiter"
)

// DemoService 示例服务。
type DemoService struct{}

// RegisterGroup 注册路由组。
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	g.GET("/per-ip", func(c *gin.Context) {
		c.String(http.StatusOK, fmt.Sprintf("IP限流演示 - QPS=5, Burst=10, 客户端IP: %s", c.ClientIP()))
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册 IP 限流中间件
	// ModeIP: 每个 IP 独立使用一个令牌桶
	// TTL: IP 限流记录的存活时间，过期后由后台协程清理（为 0 时默认 5 分钟）
	// New 返回 (handler, stop)，stop 用于结束后台清理协程，必须调用
	rateLimitHandler, stop := ratelimiter.New(ratelimiter.Config{
		Mode:  ratelimiter.ModeIP,
		QPS:   5,
		Burst: 10,
		TTL:   10 * time.Minute,
	})
	defer stop()
	engine.RegisterMiddleware(rateLimitHandler)

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
