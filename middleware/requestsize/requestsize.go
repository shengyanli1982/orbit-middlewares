package requestsize

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Config 请求体大小限制配置。
type Config struct {
	Skipper func(*gin.Context) bool
	MaxSize int64
}

// New 创建请求体大小限制中间件。
func New(cfg Config) gin.HandlerFunc {
	if cfg.MaxSize <= 0 {
		panic("requestsize: MaxSize must be > 0")
	}
	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		// Content-Length 超限时快速拒绝
		if c.Request.ContentLength > cfg.MaxSize {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}

		// 包装 Body，限制读取大小（防止 chunked transfer 绕过）
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cfg.MaxSize)
		c.Next()
	}
}
