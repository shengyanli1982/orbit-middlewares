// timeout 示例：为请求设置处理时限，超时返回 504 Gateway Timeout。
//
// 运行: go run ./timeout
// 测试: curl -i http://127.0.0.1:8080/fast （立即返回 200）
//
//	curl -i http://127.0.0.1:8080/slow （触发 5 秒超时，返回 504；
//	为防止 goroutine 泄漏，响应会在 7 秒处理协程结束后才完整送达）
package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/timeout"
)

// DemoService 示例服务。
type DemoService struct{}

// RegisterGroup 注册路由组。
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 快速响应 - 不会触发超时
	g.GET("/fast", func(c *gin.Context) {
		c.String(http.StatusOK, "快速响应")
	})

	// 慢速响应 - 处理耗时 7 秒，会触发 5 秒超时
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
	// Engine: 必填，中间件通过它在子 goroutine 中创建隔离的请求上下文
	// Timeout: 必填且必须大于 0，超过该时长返回 504
	engine.RegisterMiddleware(timeout.New(timeout.Config{
		Engine:  engine.GetGinEngine(),
		Timeout: 5 * time.Second,
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
