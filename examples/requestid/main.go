// requestid 示例：为每个请求生成或透传唯一请求 ID。
//
// 运行: go run ./requestid
// 测试: curl -i http://127.0.0.1:8080/demo （响应头包含 X-Request-ID）
package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/requestid"
)

// DemoService 示例服务，实现 orbit.Service 接口。
type DemoService struct{}

// RegisterGroup 注册路由组。服务路由直接挂载在根路径下，无额外前缀。
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	g.GET("/demo", func(c *gin.Context) {
		// requestid 中间件会将请求 ID 写入 gin.Context 的 "request_id" 键
		requestID := c.GetString("request_id")
		c.String(http.StatusOK, fmt.Sprintf("请求ID: %s", requestID))
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册 requestid 中间件
	// HeaderName: 从该请求头读取已有 ID；缺失时生成新 ID，并写回同名响应头
	engine.RegisterMiddleware(requestid.New(requestid.Config{
		HeaderName: "X-Request-ID",
	}))

	engine.RegisterService(&DemoService{})

	// Run 非阻塞，默认监听 127.0.0.1:8080
	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
