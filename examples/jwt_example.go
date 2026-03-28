package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/auth"
)

// DemoService 示例服务
type DemoService struct{}

// RegisterGroup 注册路由组
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 需要JWT认证的受保护路由
	// 该路由会验证请求中的 JWT token
	g.GET("/protected", func(c *gin.Context) {
		// 获取JWT claims（由中间件设置）
		claims, exists := c.Get("jwt_claims")
		if !exists {
			c.String(http.StatusUnauthorized, "未获取到JWT claims")
			return
		}
		c.String(http.StatusOK, fmt.Sprintf("受保护资源 - Claims: %v", claims))
	})

	// 公开路由（通过Skipper跳过认证）
	g.GET("/public", func(c *gin.Context) {
		c.String(http.StatusOK, "公开资源")
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// JWT密钥（生产环境应从配置或环境变量读取）
	jwtSecret := []byte("your-secret-key")

	// 注册JWT认证中间件
	// Secret: 用于验证HMAC签名
	// Skipper: 可选函数，用于跳过特定路由的认证检查
	engine.RegisterMiddleware(auth.JWTAuth(auth.JWTAuthConfig{
		Secret: jwtSecret,
		Skipper: func(c *gin.Context) bool {
			// 跳过 /public 路由的JWT认证
			return c.Request.URL.Path == "/demo/public"
		},
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}

// 示例：生成JWT token（仅供参考，实际使用时放在单独的工具函数中）
func generateDemoToken(secret []byte) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "user123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tokenString, _ := token.SignedString(secret)
	return tokenString
}
