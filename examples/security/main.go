// security 示例：为响应注入安全相关的 HTTP 头。
//
// 运行: go run ./security
// 测试: curl -i http://127.0.0.1:8080/test （查看 X-Content-Type-Options 等响应头）
package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/security"
)

// DemoService 示例服务。
type DemoService struct{}

// RegisterGroup 注册路由组。
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	g.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Security headers enabled")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// DefaultConfig 提供一组均衡的默认安全头
	// 也可按需使用 security.StrictConfig() / security.LaxConfig()
	engine.RegisterMiddleware(security.New(security.DefaultConfig()))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
