package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/auth"
	"github.com/shengyanli1982/orbit-middlewares/middleware/ipfilter"
	"github.com/shengyanli1982/orbit-middlewares/middleware/ratelimiter"
	"github.com/shengyanli1982/orbit-middlewares/middleware/requestid"
	"github.com/shengyanli1982/orbit-middlewares/middleware/requestsize"
	"github.com/shengyanli1982/orbit-middlewares/middleware/timeout"
)

// DemoService 示例服务 - 演示多中间件组合使用
type DemoService struct{}

// RegisterGroup 注册路由组
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 完整中间件链演示
	g.GET("/full-chain", func(c *gin.Context) {
		requestID, _ := c.Get("request_id")
		c.String(http.StatusOK, fmt.Sprintf("请求ID: %v, 客户端IP: %s", requestID, c.ClientIP()))
	})

	// 仅需要认证的路由
	g.GET("/auth-only", func(c *gin.Context) {
		c.String(http.StatusOK, "认证通过")
	})

	// 仅需要限流的路由
	g.GET("/rate-only", func(c *gin.Context) {
		c.String(http.StatusOK, "限流检查通过")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 中间件注册顺序很重要
	// orbit 框架的中间件执行顺序：
	// 1. Recovery（框架内置）- 捕获panic
	// 2. BodyBuffer（框架内置）- 请求体缓存
	// 3. CORS（框架内置）- 跨域处理
	// 4. 用户注册的自定义中间件
	// 5. AccessLogger（框架内置）- 访问日志
	// 6. Metrics（框架内置，如果启用）

	// 注册请求ID中间件（最先注册，最后执行）
	engine.RegisterMiddleware(requestid.RequestID())

	// 注册IP过滤中间件
	engine.RegisterMiddleware(ipfilter.New(ipfilter.Config{
		BlockedIPs: []string{"192.168.1.100"},
	}))

	// 注册请求大小限制中间件
	engine.RegisterMiddleware(requestsize.New(requestsize.Config{
		MaxSize: 10 * 1024 * 1024,
	}))

	// 注册超时控制中间件
	engine.RegisterMiddleware(timeout.New(timeout.Config{
		Timeout: 10 * time.Second,
	}))

	// 注册限流中间件
	engine.RegisterMiddleware(ratelimiter.New(ratelimiter.Config{
		Mode:  ratelimiter.ModeIP,
		QPS:   100,
		Burst: 200,
	}))

	// 注册认证中间件（最后注册，最先执行）
	engine.RegisterMiddleware(auth.APIKeyAuth(auth.APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    []string{"admin-key", "user-key"},
		Skipper: func(c *gin.Context) bool {
			// 跳过某些路由的认证
			path := c.Request.URL.Path
			return path == "/demo/rate-only" || path == "/demo/full-chain"
		},
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
