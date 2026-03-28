package ratelimiter

import (
	"net/http"
	"sync"
	"time"
	"unsafe"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type Mode int

const (
	ModeGlobal Mode = iota
	ModeIP
)

const (
	// numShards 分片数量，使用 256 个分片
	numShards = 256
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
// 使用分片 map 减少锁竞争
// 每个分片独立 sync.Map，减少同一把锁的竞争
type limiter struct {
	cfg    Config
	global *rate.Limiter
	shards [numShards]sync.Map
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

	l := &limiter{
		cfg:    cfg,
		global: rate.NewLimiter(rate.Limit(cfg.QPS), cfg.Burst),
	}

	// 启动后台清理goroutine
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

// StringToBytes 将字符串转换为字节切片（零拷贝）。
// 注意：返回切片只允许只读访问，不可修改。
func StringToBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}

	x := (*[2]uintptr)(unsafe.Pointer(&s))
	h := [3]uintptr{x[0], x[1], x[1]}
	return *(*[]byte)(unsafe.Pointer(&h))
}

//go:inline
func extractLastOctet(ipStr string) int {
	b := StringToBytes(ipStr)
	n := len(b)
	if n == 0 {
		return 0
	}

	p := n - 1
	for p >= 0 && b[p] != '.' {
		p--
	}
	if p < 0 {
		return 0
	}
	p++

	result := 0
	for p < n {
		c := b[p]
		if c < '0' || c > '9' {
			break
		}
		result = result*10 + int(c-'0')
		p++
	}
	return result
}

//go:inline
func getShardIndex(ipStr string) int {
	return extractLastOctet(ipStr) % numShards
}

// allowIP 检查并更新IP的限流状态
func (l *limiter) allowIP(ipStr string) bool {
	now := time.Now()
	shardIdx := getShardIndex(ipStr)

	// 尝试从当前分片加载
	if v, ok := l.shards[shardIdx].Load(ipStr); ok {
		il := v.(*ipLimiter)
		il.lastSeen = now
		return il.limiter.Allow()
	}

	// 新IP，创建新的limiter
	il := &ipLimiter{
		limiter:  rate.NewLimiter(rate.Limit(l.cfg.QPS), l.cfg.Burst),
		lastSeen: now,
	}
	l.shards[shardIdx].Store(ipStr, il)
	return il.limiter.Allow()
}

// cleanupExpired 后台goroutine定期清理过期的IP限流记录
func (l *limiter) cleanupExpired() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		for i := range l.shards {
			l.shards[i].Range(func(key, value any) bool {
				il := value.(*ipLimiter)
				if time.Since(il.lastSeen) > l.cfg.TTL {
					l.shards[i].Delete(key)
				}
				return true
			})
		}
	}
}

// formatFloat 将QPS转换为字符串
func formatFloat(f float64) string {
	if f >= 1 {
		return "1"
	}
	return "0"
}
