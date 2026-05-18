package requestsize

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type Config struct {
	Skipper func(*gin.Context) bool
	MaxSize int64
}

func New(cfg Config) gin.HandlerFunc {
	if cfg.MaxSize <= 0 {
		panic("requestsize: MaxSize must be > 0")
	}
	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		// 快速拒绝：Content-Length 明确超限，避免不必要的 body 读取
		if c.Request.ContentLength > cfg.MaxSize {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}

		// 包装 body，限制实际读取大小（防止 chunked transfer 绕过）
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cfg.MaxSize)
		c.Next()
	}
}
