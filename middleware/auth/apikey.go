package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type APIKeyAuthConfig struct {
	Skipper    func(*gin.Context) bool
	HeaderName string
	QueryParam string
	APIKeys    []string
	Validator  func(key string, c *gin.Context) bool
}

func APIKeyAuth(cfg APIKeyAuthConfig) gin.HandlerFunc {
	if cfg.HeaderName == "" {
		cfg.HeaderName = "X-API-Key"
	}

	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		key := extractAPIKey(c, cfg)
		if key == "" {
			c.String(http.StatusUnauthorized, "[401] unauthorized, reason: missing api key")
			c.Abort()
			return
		}

		valid := false
		if cfg.Validator != nil {
			valid = cfg.Validator(key, c)
		} else if cfg.APIKeys != nil {
			for _, k := range cfg.APIKeys {
				if k == key {
					valid = true
					break
				}
			}
		}

		if !valid {
			c.String(http.StatusUnauthorized, "[401] unauthorized, reason: invalid api key")
			c.Abort()
			return
		}

		c.Next()
	}
}

func extractAPIKey(c *gin.Context, cfg APIKeyAuthConfig) string {
	if cfg.QueryParam != "" {
		key := c.Query(cfg.QueryParam)
		if key != "" {
			return key
		}
	}

	if cfg.HeaderName != "" {
		key := c.GetHeader(cfg.HeaderName)
		if key != "" {
			return key
		}
	}

	return ""
}
