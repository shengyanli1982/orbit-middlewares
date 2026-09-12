// apikey 认证示例：校验请求头或 query 参数中的 API Key。
//
// 运行: go run ./apikey
// 测试: curl -i -H "X-API-Key: key1" http://127.0.0.1:8080/protected
//
//	curl -i "http://127.0.0.1:8080/protected?api_key=key2"
//	curl -i http://127.0.0.1:8080/public （Skipper 跳过认证）
package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/auth"
)

// DemoService 示例服务。
type DemoService struct{}

// RegisterGroup 注册路由组。
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 需要 API Key 认证的受保护路由
	g.GET("/protected", func(c *gin.Context) {
		c.String(http.StatusOK, "API Key认证成功 - 受保护资源")
	})

	// 公开路由（通过 Skipper 跳过认证）
	g.GET("/public", func(c *gin.Context) {
		c.String(http.StatusOK, "公开资源")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册 API Key 认证中间件
	// HeaderName: 从该请求头读取 API Key（为空时默认 X-API-Key）
	// QueryParam: 也支持从该 query 参数读取
	// APIKeys: 有效 Key 白名单（也可改用 Validator 自定义校验逻辑）
	engine.RegisterMiddleware(auth.APIKeyAuth(auth.APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		QueryParam: "api_key",
		APIKeys:    []string{"key1", "key2", "key3"},
		Skipper: func(c *gin.Context) bool {
			// 服务路由直接挂载在根路径下，无额外前缀
			return c.Request.URL.Path == "/public"
		},
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
