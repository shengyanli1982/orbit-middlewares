package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/ipfilter"
)

// DemoService 示例服务
type DemoService struct{}

// RegisterGroup 注册路由组
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// IP过滤演示路由
	g.GET("/filtered", func(c *gin.Context) {
		c.String(http.StatusOK, "IP未被拦截")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册IP过滤中间件
	// BlockedIPs: 黑名单IP列表（优先级高）
	// AllowedIPs: 白名单IP列表（为空则不启用白名单模式）
	// 过滤逻辑：
	// 1. 如果IP在黑名单中，直接拒绝（403 Forbidden）
	// 2. 如果启用了白名单且IP不在白名单中，拒绝访问
	// 3. 否则允许访问
	engine.RegisterMiddleware(ipfilter.New(ipfilter.Config{
		BlockedIPs: []string{"192.168.1.100", "10.0.0.50"},
		AllowedIPs: []string{"192.168.1.0/24", "10.0.0.1"},
		Skipper: func(c *gin.Context) bool {
			// 跳过健康检查端点
			return c.Request.URL.Path == "/ping"
		},
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
