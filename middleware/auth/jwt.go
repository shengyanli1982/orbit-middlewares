package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type JWTAuthConfig struct {
	Skipper func(*gin.Context) bool
	Secret  []byte
	KeyFunc jwt.Keyfunc
}

// JWTAuth JWT认证中间件
// keyfunc在handler外部创建一次，避免每次请求都分配闭包
func JWTAuth(cfg JWTAuthConfig) gin.HandlerFunc {
	keyFunc := cfg.KeyFunc
	if keyFunc == nil {
		// 未提供KeyFunc时，创建默认的HMAC验证闭包
		// secret通过值捕获而非引用，避免闭包捕获指针问题
		secret := cfg.Secret
		keyFunc = func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return secret, nil
		}
	}

	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.String(http.StatusUnauthorized, "[401] unauthorized, reason: missing token")
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			c.String(http.StatusUnauthorized, "[401] unauthorized, reason: invalid token format")
			c.Abort()
			return
		}

		token, err := jwt.Parse(parts[1], keyFunc)
		if err != nil {
			c.String(http.StatusUnauthorized, "[401] unauthorized, reason: invalid token")
			c.Abort()
			return
		}

		if !token.Valid {
			c.String(http.StatusUnauthorized, "[401] unauthorized, reason: invalid token")
			c.Abort()
			return
		}

		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			c.Set("jwt_claims", claims)
		}

		c.Next()
	}
}
