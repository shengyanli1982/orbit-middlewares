package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type JWTAuthConfig struct {
	Skipper func(*gin.Context) bool
	Secret  []byte
	KeyFunc jwt.Keyfunc
}

func JWTAuth(cfg JWTAuthConfig) gin.HandlerFunc {
	keyFunc := cfg.KeyFunc
	secret := cfg.Secret
	if keyFunc == nil {
		keyFunc = func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return secret, nil
		}
	}

	headerName := "Authorization"
	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		authHeader := c.GetHeader(headerName)
		if len(authHeader) < 8 {
			c.String(http.StatusUnauthorized, "[401] unauthorized, reason: missing token")
			c.Abort()
			return
		}

		authHeaderLen := len(authHeader)
		if authHeaderLen > 7 && authHeader[0] == 'B' && authHeader[1] == 'e' && authHeader[2] == 'a' && authHeader[3] == 'r' && authHeader[4] == 'e' && authHeader[5] == 'r' && authHeader[6] == ' ' {
			tokenStr := authHeader[7:]
			if len(tokenStr) > 0 {
				token, err := jwt.Parse(tokenStr, keyFunc)
				if err == nil && token.Valid {
					if claims, ok := token.Claims.(jwt.MapClaims); ok {
						c.Set("jwt_claims", claims)
					}
					c.Next()
					return
				}
			}
		}
		c.String(http.StatusUnauthorized, "[401] unauthorized, reason: invalid token")
		c.Abort()
		return
	}
}
