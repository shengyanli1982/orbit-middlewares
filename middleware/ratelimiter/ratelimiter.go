package ratelimiter

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// Mode 限流模式。
type Mode int

const (
	// ModeGlobal 全局共享限流器。
	ModeGlobal Mode = iota
	// ModeIP 按 IP 独立限流。
	ModeIP
)

const (
	// numShards 分片数量。
	numShards = 256
)

// Config 限流中间件配置。
type Config struct {
	Skipper     func(*gin.Context) bool
	Mode        Mode
	QPS         float64
	Burst       int
	TTL         time.Duration
	IPExtractor func(*gin.Context) string
	RejectHandler func(*gin.Context)
}

type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen atomic.Int64 // UnixNano
}

type limiter struct {
	cfg      Config
	global   *rate.Limiter
	shards   [numShards]sync.Map
	stopCh   chan struct{}
	stopOnce sync.Once
}

// New 创建限流中间件，返回 (handler, stop)。
// stop 用于停止后台清理 goroutine，应在不再需要时调用。
func New(cfg Config) (gin.HandlerFunc, func()) {
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
		stopCh: make(chan struct{}),
	}

	if cfg.Mode == ModeIP {
		go l.cleanupExpired()
	}

	limitHeader := strconv.Itoa(int(math.Ceil(cfg.QPS)))

	reject := func(c *gin.Context, retryAfter string) {
		c.Header("X-RateLimit-Limit", limitHeader)
		c.Header("Retry-After", retryAfter)
		if cfg.RejectHandler != nil {
			cfg.RejectHandler(c)
		} else {
			c.String(http.StatusTooManyRequests, "[429] rate limit exceeded")
		}
		c.Abort()
	}

	handler := func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		if cfg.Mode == ModeGlobal {
			if l.global.Allow() {
				c.Next()
				return
			}
			r := l.global.Reserve()
			if !r.OK() {
				reject(c, "0")
				return
			}
			delay := r.Delay()
			r.Cancel()
			if delay > 0 {
				reject(c, strconv.FormatInt(int64(math.Ceil(delay.Seconds())), 10))
				return
			}
		} else {
			key := cfg.IPExtractor(c)
			if key == "" {
				c.Next()
				return
			}

			ok, delay, r := l.allowIP(key)
			if !ok {
				reject(c, "0")
				return
			}
			if delay > 0 {
				r.Cancel()
				reject(c, strconv.FormatInt(int64(math.Ceil(delay.Seconds())), 10))
				return
			}
		}

		c.Next()
	}

	return handler, l.Stop
}

// getShardIndex 使用 FNV-1a hash 计算 IP 的分片索引。
func getShardIndex(ipStr string) int {
	const (
		fnvOffset32 uint32 = 2166136261
		fnvPrime32  uint32 = 16777619
	)
	h := fnvOffset32
	for i := 0; i < len(ipStr); i++ {
		h ^= uint32(ipStr[i])
		h *= fnvPrime32
	}
	return int(h) % numShards
}

// allowIP 检查并更新 IP 的限流状态。
// 返回：(是否放行, 等待时间, Reservation)
func (l *limiter) allowIP(ipStr string) (bool, time.Duration, *rate.Reservation) {
	now := time.Now()
	shardIdx := getShardIndex(ipStr)

	if v, ok := l.shards[shardIdx].Load(ipStr); ok {
		il := v.(*ipLimiter)
		il.lastSeen.Store(now.UnixNano())
		if il.limiter.Allow() {
			return true, 0, nil
		}
		r := il.limiter.Reserve()
		if !r.OK() {
			return false, 0, nil
		}
		return true, r.Delay(), r
	}

	// LoadOrStore 避免并发场景下重复创建 limiter。
	newIL := &ipLimiter{
		limiter: rate.NewLimiter(rate.Limit(l.cfg.QPS), l.cfg.Burst),
	}
	newIL.lastSeen.Store(now.UnixNano())
	actual, loaded := l.shards[shardIdx].LoadOrStore(ipStr, newIL)
	il := actual.(*ipLimiter)
	if loaded {
		// 已被其他 goroutine 存储，更新 lastSeen 并使用已有 limiter
		il.lastSeen.Store(now.UnixNano())
	}
	if il.limiter.Allow() {
		return true, 0, nil
	}
	r := il.limiter.Reserve()
	if !r.OK() {
		return false, 0, nil
	}
	return true, r.Delay(), r
}

func (l *limiter) cleanupExpired() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			ttl := l.cfg.TTL
			for i := range l.shards {
				l.shards[i].Range(func(key, value any) bool {
					il := value.(*ipLimiter)
					lastSeen := time.Unix(0, il.lastSeen.Load())
					if now.Sub(lastSeen) > ttl {
						l.shards[i].Delete(key)
					}
					return true
				})
			}
		}
	}
}

func (l *limiter) Stop() {
	l.stopOnce.Do(func() {
		close(l.stopCh)
	})
}
