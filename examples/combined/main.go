// combined 示例：多中间件组合使用的完整链路。
//
// 运行: go run ./combined
// 测试: curl -i -H "X-API-Key: admin-key" http://127.0.0.1:8080/full-chain
//
//	curl -i http://127.0.0.1:8080/rate-only （Skipper 跳过认证）
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

// DemoService 示例服务 - 演示多中间件组合使用。
type DemoService struct{}

// RegisterGroup 注册路由组。服务路由直接挂载在根路径下，无额外前缀。
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 完整中间件链演示（含认证）
	g.GET("/full-chain", func(c *gin.Context) {
		requestID := c.GetString("request_id")
		c.String(http.StatusOK, fmt.Sprintf("请求ID: %s, 客户端IP: %s", requestID, c.ClientIP()))
	})

	// 仅需要认证的路由
	g.GET("/auth-only", func(c *gin.Context) {
		c.String(http.StatusOK, "认证通过")
	})

	// 仅需要限流的路由（Skipper 跳过认证）
	g.GET("/rate-only", func(c *gin.Context) {
		c.String(http.StatusOK, "限流检查通过")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 中间件注册顺序很重要：
	// 框架内置 Recovery/CORS 最先执行，随后按注册顺序执行用户中间件，
	// 最后是框架内置的 AccessLogger 与 Metrics（启用时）。
	// timeout 必须为用户中间件首位：其内部经 Engine.ServeHTTP 重放完整
	// 中间件链（递归守卫只防 timeout 自身），注册在其之前的用户中间件
	// 每请求会执行两次（副作用翻倍、响应头重复）。

	// 超时控制：超过 10 秒返回 504；Engine 与 Timeout 均为必填。
	// 必须注册在用户中间件首位（原因见上方顺序说明）。
	engine.RegisterMiddleware(timeout.New(timeout.Config{
		Engine:  engine.GetGinEngine(),
		Timeout: 10 * time.Second,
	}))

	// 请求 ID：为每个请求生成或透传唯一 ID
	engine.RegisterMiddleware(requestid.New(requestid.DefaultConfig()))

	// IP 过滤：拦截黑名单 IP（条目必须为合法 IP/CIDR）
	engine.RegisterMiddleware(ipfilter.New(ipfilter.Config{
		BlockedIPs: []string{"192.168.1.100"},
	}))

	// 请求大小限制：超过 10MB 返回 413
	engine.RegisterMiddleware(requestsize.New(requestsize.Config{
		MaxSize: 10 * 1024 * 1024,
	}))

	// 限流：按 IP 限流；New 返回 (handler, stop)，stop 必须调用
	rateLimitHandler, stop := ratelimiter.New(ratelimiter.Config{
		Mode:  ratelimiter.ModeIP,
		QPS:   100,
		Burst: 200,
	})
	defer stop()
	engine.RegisterMiddleware(rateLimitHandler)

	// 认证：API Key 校验，部分路由通过 Skipper 跳过
	engine.RegisterMiddleware(auth.APIKeyAuth(auth.APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    []string{"admin-key", "user-key"},
		Skipper: func(c *gin.Context) bool {
			// 服务路由直接挂载在根路径下，无额外前缀
			return c.Request.URL.Path == "/rate-only"
		},
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
