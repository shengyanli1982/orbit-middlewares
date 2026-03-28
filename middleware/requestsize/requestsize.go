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
	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		if c.Request.ContentLength > cfg.MaxSize {
			c.String(http.StatusRequestEntityTooLarge, "[413] request body too large")
			c.Abort()
			return
		}

		c.Next()
	}
}
