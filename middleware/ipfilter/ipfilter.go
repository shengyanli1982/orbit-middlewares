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

// ipFilter IP过滤器，使用map存储实现O(1)查找
// blockedIPs: 黑名单IP集合
// allowedIPs: 白名单IP集合（若为空则不启用白名单）
// hasAllowed: 标记是否启用了白名单模式
type ipFilter struct {
	skipper    func(*gin.Context) bool
	blockedIPs map[string]struct{}
	allowedIPs map[string]struct{}
	hasAllowed bool
}

func New(cfg Config) gin.HandlerFunc {
	f := &ipFilter{
		skipper:    cfg.Skipper,
		blockedIPs: make(map[string]struct{}, len(cfg.BlockedIPs)),
		allowedIPs: make(map[string]struct{}, len(cfg.AllowedIPs)),
		hasAllowed: len(cfg.AllowedIPs) > 0,
	}

	for _, ip := range cfg.BlockedIPs {
		f.blockedIPs[ip] = struct{}{}
	}
	for _, ip := range cfg.AllowedIPs {
		f.allowedIPs[ip] = struct{}{}
	}

	return func(c *gin.Context) {
		if f.skipper != nil && f.skipper(c) {
			c.Next()
			return
		}

		clientIP := c.ClientIP()

		if _, blocked := f.blockedIPs[clientIP]; blocked {
			c.String(http.StatusForbidden, "[403] ip blocked")
			c.Abort()
			return
		}

		if f.hasAllowed {
			if _, allowed := f.allowedIPs[clientIP]; !allowed {
				c.String(http.StatusForbidden, "[403] ip not allowed")
				c.Abort()
				return
			}
		}

		c.Next()
	}
}
