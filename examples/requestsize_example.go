package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/requestsize"
)

// DemoService 示例服务
type DemoService struct{}

// RegisterGroup 注册路由组
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 上传小文件（限制1MB）
	g.POST("/upload/small", func(c *gin.Context) {
		c.String(http.StatusOK, "小文件上传成功")
	})

	// 上传大文件（限制10MB）
	g.POST("/upload/large", func(c *gin.Context) {
		c.String(http.StatusOK, "大文件上传成功")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册请求大小限制中间件（全局）
	// MaxSize: 最大请求体大小（字节）
	// 超过限制返回 413 Request Entity Too Large
	engine.RegisterMiddleware(requestsize.New(requestsize.Config{
		MaxSize: 10 * 1024 * 1024, // 10MB
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}

// 注意：requestsize 中间件只检查 Content-Length 头
// 如果需要更复杂的请求体大小控制，建议在 handler 中使用
// io.LimitReader 或使用其他中间件方案
