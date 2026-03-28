package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/auth"
)

// DemoService 示例服务
type DemoService struct{}

// RegisterGroup 注册路由组
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 需要API Key认证的受保护路由
	g.GET("/protected", func(c *gin.Context) {
		c.String(http.StatusOK, "API Key认证成功 - 受保护资源")
	})

	// 公开路由
	g.GET("/public", func(c *gin.Context) {
		c.String(http.StatusOK, "公开资源")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册API Key认证中间件
	// HeaderName: 从哪个Header读取API Key（默认 X-API-Key）
	// QueryParam: 从哪个Query参数读取API Key（可选）
	// APIKeys: 有效的API Key列表（可使用自定义Validator替代）
	engine.RegisterMiddleware(auth.APIKeyAuth(auth.APIKeyAuthConfig{
		HeaderName: "X-API-Key", // 从 X-API-Key Header 读取
		QueryParam: "api_key",   // 也支持从 query 参数读取
		APIKeys:    []string{"key1", "key2", "key3"},
		Skipper: func(c *gin.Context) bool {
			// 跳过 /demo/public 路由的认证
			return c.Request.URL.Path == "/demo/public"
		},
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}

// 示例：使用自定义Validator进行更复杂的API Key验证
// func main() {
// 	engine := orbit.NewEngine(orbit.NewConfig(), orbit.NewOptions())
//
// 	engine.RegisterMiddleware(auth.APIKeyAuth(auth.APIKeyAuthConfig{
// 		HeaderName: "X-API-Key",
// 		Validator: func(key string, c *gin.Context) bool {
// 			// 自定义验证逻辑，例如从数据库验证
// 			return validateKeyFromDB(key)
// 		},
// 	}))
// }
