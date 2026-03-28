package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/requestid"
)

// DemoService 示例服务，实现 orbit.Service 接口
// 用于演示如何在 orbit 框架中注册自定义路由组
type DemoService struct{}

// RegisterGroup 注册路由组
// g: Gin 路由组实例，用于定义 API 路由
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 注册 /demo 路由，处理请求ID功能演示
	g.GET("/demo", func(c *gin.Context) {
		// 从 context 中获取由 requestid 中间件设置的请求ID
		requestID, exists := c.Get("request_id")
		if !exists {
			requestID = "not_set"
		}
		c.String(http.StatusOK, fmt.Sprintf("请求ID: %v", requestID))
	})
}

func main() {
	// 创建 orbit 配置（使用默认配置）
	config := orbit.NewConfig()

	// 创建 orbit 选项，启用指标收集功能
	// EnableMetric() 会自动注册 /metrics 端点用于 Prometheus 监控
	opts := orbit.NewOptions().EnableMetric()

	// 创建 orbit 引擎实例
	// Engine 是 orbit 框架的核心协调器，负责管理 HTTP 服务器生命周期
	engine := orbit.NewEngine(config, opts)

	// 注册 requestid 中间件
	// 该中间件会为每个请求生成唯一的请求ID（如果请求中未提供）
	// 请求ID会通过 "X-Request-ID" 响应头返回给客户端
	// 同时会将请求ID存储在 gin.Context 中供后续处理使用
	engine.RegisterMiddleware(requestid.RequestID(
		requestid.WithHeaderName("X-Request-ID"), // 自定义请求ID header 名称
	))

	// 注册自定义服务
	engine.RegisterService(&DemoService{})

	// 启动 HTTP 服务器
	// 默认监听 8080 端口
	engine.Run()

	// 保持服务运行 30 秒以便测试
	time.Sleep(30 * time.Second)

	// 优雅停止服务
	engine.Stop()
}
