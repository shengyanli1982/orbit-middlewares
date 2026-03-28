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

func JWTAuth(cfg JWTAuthConfig) gin.HandlerFunc {
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

		tokenString := parts[1]

		var keyFunc jwt.Keyfunc
		if cfg.KeyFunc != nil {
			keyFunc = cfg.KeyFunc
		} else {
			keyFunc = func(token *jwt.Token) (interface{}, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrSignatureInvalid
				}
				return cfg.Secret, nil
			}
		}

		token, err := jwt.Parse(tokenString, keyFunc)
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
