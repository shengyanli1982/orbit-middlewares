package ratelimiter

import (
	"math"
	"net/http"
	"strconv"
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

type limiter struct {
	cfg      Config
	global   *rate.Limiter
	shards   [numShards]sync.Map
	stopCh   chan struct{}
	stopOnce sync.Once
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
		stopCh: make(chan struct{}),
	}

	// 启动后台清理goroutine
	go l.cleanupExpired()

	return func(c *gin.Context) {
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
			// Reserve Delay() > 0: bucket 中当前没有可用 token，需要等待补充
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
// 返回值: (是否可放行, 等待时间, Reservation)
//   - ok=false: 请求的 token 数超过了 burst 上限（非常罕见）
//   - delay>0: bucket 中没有可用 token，需要等待
//   - delay==0 && ok==true: 可以立即放行
func (l *limiter) allowIP(ipStr string) (bool, time.Duration, *rate.Reservation) {
	now := time.Now()
	shardIdx := getShardIndex(ipStr)

	if v, ok := l.shards[shardIdx].Load(ipStr); ok {
		il := v.(*ipLimiter)
		il.lastSeen = now
		r := il.limiter.Reserve()
		if !r.OK() {
			return false, 0, nil
		}
		return true, r.Delay(), r
	}

	il := &ipLimiter{
		limiter:  rate.NewLimiter(rate.Limit(l.cfg.QPS), l.cfg.Burst),
		lastSeen: now,
	}
	l.shards[shardIdx].Store(ipStr, il)
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
}

func (l *limiter) Stop() {
	l.stopOnce.Do(func() {
		close(l.stopCh)
	})
}
