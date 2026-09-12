// ipfilter 示例：按客户端 IP 黑名单拦截请求，命中返回 403 Forbidden。
//
// 运行: go run ./ipfilter
// 测试: curl -i http://127.0.0.1:8080/filtered （本机 IP 不在黑名单，正常返回 200）
package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/ipfilter"
)

// DemoService 示例服务。
type DemoService struct{}

// RegisterGroup 注册路由组。
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	g.GET("/filtered", func(c *gin.Context) {
		c.String(http.StatusOK, "IP未被拦截")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// 注册 IP 过滤中间件（黑名单模式）
	// BlockedIPs: 命中黑名单返回 403，支持精确 IP 与 CIDR，条目必须合法
	// AllowedIPs: 一旦设置即启用白名单模式，不在白名单内的 IP 一律拒绝
	// （示例仅用黑名单，否则本机 127.0.0.1 会被白名单拦截）
	engine.RegisterMiddleware(ipfilter.New(ipfilter.Config{
		BlockedIPs: []string{"192.168.1.100", "10.0.0.0/24"},
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
