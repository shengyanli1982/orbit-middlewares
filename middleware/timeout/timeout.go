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

// New 超时控制中间件
// 使用context实现请求超时控制
// 1. 创建带超时的context
// 2. 在goroutine中执行下游处理
// 3. 等待完成或超时，任一先发生
func New(cfg Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		// 创建带超时的context
		ctx, cancel := context.WithTimeout(c.Request.Context(), cfg.Timeout)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)

		// 在独立goroutine中执行后续处理
		done := make(chan struct{})
		go func() {
			c.Next()
			close(done)
		}()

		// 等待处理完成或超时
		select {
		case <-done:
			// 正常完成，取消context
			cancel()
		case <-ctx.Done():
			// 超时发生，返回504
			c.String(http.StatusGatewayTimeout, "[504] request timeout")
			c.Abort()
		}
	}
}
