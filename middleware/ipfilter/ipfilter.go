package ipfilter

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type Config struct {
	Skipper    func(*gin.Context) bool
	AllowedIPs []string
	BlockedIPs []string
}

func New(cfg Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		clientIP := c.ClientIP()

		for _, ip := range cfg.BlockedIPs {
			if ip == clientIP {
				c.String(http.StatusForbidden, "[403] ip blocked")
				c.Abort()
				return
			}
		}

		if len(cfg.AllowedIPs) > 0 {
			allowed := false
			for _, ip := range cfg.AllowedIPs {
				if ip == clientIP {
					allowed = true
					break
				}
			}
			if !allowed {
				c.String(http.StatusForbidden, "[403] ip not allowed")
				c.Abort()
				return
			}
		}

		c.Next()
	}
}
