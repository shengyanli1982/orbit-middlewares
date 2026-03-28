package ratelimiter

import (
	"net/http"
	"sync"
	"time"

	"github.com/coocood/freecache"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type Mode int

const (
	ModeGlobal Mode = iota
	ModeIP
)

type Config struct {
	Skipper     func(*gin.Context) bool
	Mode        Mode
	QPS         float64
	Burst       int
	TTL         time.Duration
	IPExtractor func(*gin.Context) string
}

type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// limiter 限流器实例
// ipCache: freecache缓存，用于快速判断IP是否见过（比bigcache更高效）
// ipLimiters: 存储每个IP的限流器实例，使用sync.Map支持并发安全
type limiter struct {
	cfg        Config
	global     *rate.Limiter
	ipCache    *freecache.Cache
	ipLimiters sync.Map
}

func New(cfg Config) gin.HandlerFunc {
	if cfg.IPExtractor == nil {
		cfg.IPExtractor = func(c *gin.Context) string {
			return c.ClientIP()
		}
	}

	if cfg.TTL == 0 {
		cfg.TTL = 5 * time.Minute
	}

	ipCache := freecache.NewCache(256 * 1024 * 1024)

	l := &limiter{
		cfg:     cfg,
		global:  rate.NewLimiter(rate.Limit(cfg.QPS), cfg.Burst),
		ipCache: ipCache,
	}

	go l.cleanupExpired()

	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		if cfg.Mode == ModeGlobal {
			if !l.global.Allow() {
				c.Header("X-RateLimit-Limit", formatFloat(cfg.QPS))
				c.Header("Retry-After", "1")
				c.String(http.StatusTooManyRequests, "[429] rate limit exceeded")
				c.Abort()
				return
			}
		} else {
			key := cfg.IPExtractor(c)
			if key == "" {
				c.Next()
				return
			}

			if !l.allowIP(key) {
				c.Header("X-RateLimit-Limit", formatFloat(cfg.QPS))
				c.Header("Retry-After", "1")
				c.String(http.StatusTooManyRequests, "[429] rate limit exceeded")
				c.Abort()
				return
			}
		}

		c.Next()
	}
}

// allowIP 检查并更新IP的限流状态
// 1. 先查sync.Map获取该IP的limiter
// 2. 同时更新freecache中的时间戳（用于cleanupExpired判断过期）
// 3. 调用rate.Limiter.Allow()检查是否允许通过
func (l *limiter) allowIP(key string) bool {
	now := time.Now()

	if v, ok := l.ipLimiters.Load(key); ok {
		il := v.(*ipLimiter)
		il.lastSeen = now
		_ = l.ipCache.Set([]byte(key), []byte("1"), int(l.cfg.TTL.Seconds()))
		return il.limiter.Allow()
	}

	// 新IP，创建新的limiter并缓存
	il := &ipLimiter{
		limiter:  rate.NewLimiter(rate.Limit(l.cfg.QPS), l.cfg.Burst),
		lastSeen: now,
	}
	l.ipLimiters.Store(key, il)
	_ = l.ipCache.Set([]byte(key), []byte("1"), int(l.cfg.TTL.Seconds()))
	return il.limiter.Allow()
}

// cleanupExpired 后台goroutine定期清理过期的IP限流记录
// 每分钟执行一次，删除超过TTL未被访问的IP记录
// 同时清理sync.Map和freecache中的数据
func (l *limiter) cleanupExpired() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		l.ipLimiters.Range(func(key, value any) bool {
			il := value.(*ipLimiter)
			if time.Since(il.lastSeen) > l.cfg.TTL {
				l.ipLimiters.Delete(key)
				_ = l.ipCache.Del([]byte(key.(string)))
			}
			return true
		})
	}
}

func formatFloat(f float64) string {
	if f >= 1 {
		return "1"
	}
	return "0"
}
