package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/timeout"
)

// DemoService 示例服务
type DemoService struct{}

// RegisterGroup 注册路由组
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 快速响应 - 不会触发超时
	g.GET("/fast", func(c *gin.Context) {
		c.String(http.StatusOK, "快速响应")
	})

	// 慢速响应 - 会触发5秒超时
	g.GET("/slow", func(c *gin.Context) {
		time.Sleep(7 * time.Second)
		c.String(http.StatusOK, "慢速响应完成")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册超时控制中间件
	// Timeout=5s: 设置请求超时时间为5秒
	// 当请求处理时间超过5秒时，中间件会返回504网关超时错误
	// 使用 context 实现超时控制，确保资源正确释放
	engine.RegisterMiddleware(timeout.New(timeout.Config{
		Timeout: 5 * time.Second,
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
