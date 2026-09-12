// jwt 认证示例：校验 Authorization: Bearer <token> 中的 JWT。
//
// 运行: go run ./jwt （启动时打印一个演示 token）
// 测试: curl -i -H "Authorization: Bearer <token>" http://127.0.0.1:8080/protected
//
//	curl -i http://127.0.0.1:8080/public （Skipper 跳过认证）
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

// DemoService 示例服务。
type DemoService struct{}

// RegisterGroup 注册路由组。
func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	// 需要 JWT 认证的受保护路由
	g.GET("/protected", func(c *gin.Context) {
		// 中间件校验通过后会将 claims 写入 gin.Context 的 "jwt_claims" 键
		claims, exists := c.Get("jwt_claims")
		if !exists {
			c.String(http.StatusUnauthorized, "未获取到JWT claims")
			return
		}
		c.String(http.StatusOK, fmt.Sprintf("受保护资源 - Claims: %v", claims))
	})

	// 公开路由（通过 Skipper 跳过认证）
	g.GET("/public", func(c *gin.Context) {
		c.String(http.StatusOK, "公开资源")
	})
}

// generateDemoToken 生成一个用于本地测试的 HS256 签名 token。
func generateDemoToken(secret []byte) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "user123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return token.SignedString(secret)
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions().EnableMetric()
	engine := orbit.NewEngine(config, opts)

	// JWT 密钥（生产环境应从配置或环境变量读取，禁止硬编码）
	jwtSecret := []byte("your-secret-key")

	demoToken, err := generateDemoToken(jwtSecret)
	if err != nil {
		fmt.Printf("生成演示 token 失败: %v\n", err)
		return
	}
	fmt.Printf("演示 token: %s\n", demoToken)
	fmt.Println(`测试: curl -i -H "Authorization: Bearer <token>" http://127.0.0.1:8080/protected`)

	// 注册 JWT 认证中间件
	// Secret: 必填，用于验证 HMAC 签名
	// Skipper: 可选，返回 true 时跳过认证
	engine.RegisterMiddleware(auth.JWTAuth(auth.JWTAuthConfig{
		Secret: jwtSecret,
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
