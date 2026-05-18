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
	lastSeen atomic.Int64 // UnixNano
}

type limiter struct {
	cfg      Config
	global   *rate.Limiter
	shards   [numShards]sync.Map
	stopCh   chan struct{}
	stopOnce sync.Once
}

// New 创建限流中间件，返回 (handler, stop) 元组。
// stop 函数用于停止后台清理 goroutine，调用方应在不再需要时调用。
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

	// 启动后台清理goroutine
	go l.cleanupExpired()

	handler := func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		if cfg.Mode == ModeGlobal {
			r := l.global.Reserve()
			// Reserve OK() == false: 请求的 token 数超过了 burst 上限（非常罕见）
			if !r.OK() {
				c.Header("X-RateLimit-Limit", strconv.Itoa(int(math.Ceil(cfg.QPS))))
				c.Header("Retry-After", "0")
				c.String(http.StatusTooManyRequests, "[429] rate limit exceeded")
				c.Abort()
				return
			}
			// Reserve Delay() > 0: bucket 中当前没有可用 token，需要等待补充
			if r.Delay() > 0 {
				r.Cancel()
				c.Header("X-RateLimit-Limit", strconv.Itoa(int(math.Ceil(cfg.QPS))))
				c.Header("Retry-After", strconv.FormatInt(int64(math.Ceil(r.Delay().Seconds())), 10))
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

			ok, delay, r := l.allowIP(key)
			// Reserve OK() == false: 请求的 token 数超过了 burst 上限（非常罕见）
			if !ok {
				c.Header("X-RateLimit-Limit", strconv.Itoa(int(math.Ceil(cfg.QPS))))
				c.Header("Retry-After", "0")
				c.String(http.StatusTooManyRequests, "[429] rate limit exceeded")
				c.Abort()
				return
			}
			// Reserve Delay() > 0: bucket 中没有可用 token，需要等待补充
			if delay > 0 {
				r.Cancel()
				c.Header("X-RateLimit-Limit", strconv.Itoa(int(math.Ceil(cfg.QPS))))
				c.Header("Retry-After", strconv.FormatInt(int64(math.Ceil(delay.Seconds())), 10))
				c.String(http.StatusTooManyRequests, "[429] rate limit exceeded")
				c.Abort()
				return
			}
		}

		c.Next()
	}

	return handler, l.Stop
}

// getShardIndex 对 IP 字符串使用内联 FNV-1a hash 取模 256。
// 内联计算避免 fnv.New32a() 的堆分配，每次调用节省 1 alloc。
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

// allowIP 检查并更新IP的限流状态
// 返回值: (是否可放行, 等待时间, Reservation)
//   - ok=false: 请求的 token 数超过了 burst 上限（非常罕见）
//   - delay>0: bucket 中没有可用 token，需要等待
//   - delay==0 && ok==true: 可以立即放行
func (l *limiter) allowIP(ipStr string) (bool, time.Duration, *rate.Reservation) {
	now := time.Now()
	shardIdx := getShardIndex(ipStr)

	if v, ok := l.shards[shardIdx].Load(ipStr); ok {
		il := v.(*ipLimiter)
		il.lastSeen.Store(now.UnixNano())
		r := il.limiter.Reserve()
		if !r.OK() {
			return false, 0, nil
		}
		return true, r.Delay(), r
	}

	// 使用 LoadOrStore 避免并发场景下重复创建 limiter。
	// 若两个 goroutine 同时到达此处，只有一个能成功 Store，另一个使用已存储的值。
	newIL := &ipLimiter{
		limiter: rate.NewLimiter(rate.Limit(l.cfg.QPS), l.cfg.Burst),
	}
	newIL.lastSeen.Store(now.UnixNano())
	actual, loaded := l.shards[shardIdx].LoadOrStore(ipStr, newIL)
	il := actual.(*ipLimiter)
	if loaded {
		// 另一个 goroutine 已存储，更新 lastSeen 并使用已有 limiter
		il.lastSeen.Store(now.UnixNano())
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
