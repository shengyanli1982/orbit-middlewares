package ratelimiter

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type Config struct {
	Skipper      func(*gin.Context) bool
	QPS          float64
	Burst        int
	KeyExtractor func(*gin.Context) string
}

type limiter struct {
	cfg      Config
	limiters sync.Map
}

func New(cfg Config) gin.HandlerFunc {
	if cfg.KeyExtractor == nil {
		cfg.KeyExtractor = func(c *gin.Context) string {
			return c.ClientIP()
		}
	}

	l := &limiter{cfg: cfg}

	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		key := cfg.KeyExtractor(c)
		if key == "" {
			c.Next()
			return
		}

		if !l.allow(key) {
			c.Header("X-RateLimit-Limit", formatQPS(cfg.QPS))
			c.Header("Retry-After", "1")
			c.String(http.StatusTooManyRequests, "[429] rate limit exceeded")
			c.Abort()
			return
		}

		c.Next()
	}
}

func (l *limiter) allow(key string) bool {
	lim := l.getLimiter(key)
	return lim.Allow()
}

func (l *limiter) getLimiter(key string) *rate.Limiter {
	if v, ok := l.limiters.Load(key); ok {
		return v.(*rate.Limiter)
	}

	lim := rate.NewLimiter(rate.Limit(l.cfg.QPS), l.cfg.Burst)
	actual, _ := l.limiters.LoadOrStore(key, lim)
	return actual.(*rate.Limiter)
}

func formatQPS(qps float64) string {
	if qps >= 1 {
		return "1"
	}
	return "0"
}
