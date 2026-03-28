package timeout

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type Config struct {
	Skipper func(*gin.Context) bool
	Timeout time.Duration
}

func New(cfg Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), cfg.Timeout)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)

		done := make(chan struct{})
		go func() {
			c.Next()
			close(done)
		}()

		select {
		case <-done:
			cancel()
		case <-ctx.Done():
			c.String(http.StatusGatewayTimeout, "[504] request timeout")
			c.Abort()
		}
	}
}
