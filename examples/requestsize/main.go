// requestsize 示例：限制请求体最大大小，超限返回 413 Request Entity Too Large。
//
// 运行: go run ./requestsize
// 测试: curl -i -X POST --data-binary @大文件 http://127.0.0.1:8080/upload
package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/requestsize"
)

// DemoService 示例服务。
type DemoService struct{}

// RegisterGroup 注册路由组。
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	g.POST("/upload", func(c *gin.Context) {
		c.String(http.StatusOK, "上传成功")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册请求大小限制中间件（全局生效）
	// MaxSize: 必填且必须大于 0；基于 http.MaxBytesReader 限制实际读取，
	// 可防止 chunked 传输绕过，超过限制返回 413
	engine.RegisterMiddleware(requestsize.New(requestsize.Config{
		MaxSize: 10 * 1024 * 1024, // 10MB
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
