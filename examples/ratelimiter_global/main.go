// ratelimiter 全局限流示例：所有请求共享同一个令牌桶。
//
// 运行: go run ./ratelimiter_global
// 测试: 连续快速请求 curl -i http://127.0.0.1:8080/global ，突发超过 Burst 后返回 429
package main

import (
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
	g.GET("/global", func(c *gin.Context) {
		c.String(http.StatusOK, "全局限流演示 - QPS=10, Burst=20")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册全局限流中间件
	// ModeGlobal: 所有请求共享同一个令牌桶
	// QPS/Burst: 必须大于 0
	// New 返回 (handler, stop)，stop 用于释放后台资源，必须调用
	rateLimitHandler, stop := ratelimiter.New(ratelimiter.Config{
		Mode:  ratelimiter.ModeGlobal,
		QPS:   10,
		Burst: 20,
	})
	defer stop()
	engine.RegisterMiddleware(rateLimitHandler)

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
