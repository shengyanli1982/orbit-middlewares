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
	// numShards 分片数量。必须为 2 的幂：getShardIndex 依赖位与取模
	// 保证 32 位平台下索引非负（测试 TestGetShardIndex_HighBitHashStaysInRange 钉住该契约）。
	numShards = 256
)

// ReasonRateLimited 限流超限拒绝的原因常量，作为 RejectHandler 的 reason 参数。
// 值遵循全局 "<domain>.<cause>" 拒绝原因命名方案（domain 自证拒绝来源，cause 为
// snake_case 状态描述、域内主语自明时省略主语），规则详见 README。
const ReasonRateLimited = "ratelimit.exceeded"

// Config 限流中间件配置。
type Config struct {
	Skipper func(*gin.Context) bool
	Mode    Mode
	// QPS 每秒补充的令牌数，必须 > 0，否则 New panic。
	QPS float64
	// Burst 令牌桶容量，必须 > 0，否则 New panic。
	Burst int
	// TTL IP 模式下条目的空闲存活时间，零值默认 5 分钟。
	TTL time.Duration
	// IPExtractor 提取限流键（客户端 IP），nil 时默认使用 c.ClientIP()。
	//
	// 安全边界（AUD-05）：gin 默认信任所有代理，c.ClientIP() 会优先采信
	// X-Forwarded-For / X-Real-IP 等可被客户端伪造的头。在直接暴露于公网的
	// 部署中，攻击者可逐请求变换伪造头获取新的限流桶，从而绕过按 IP 限流。
	// 使用默认提取器时，必须通过 router.SetTrustedProxies（或
	// SetTrustedPlatform）按真实网络拓扑配置信任范围，ClientIP 的结果才可信；
	// 或自定义本字段，改用不可伪造的键（如认证主体 ID）。
	IPExtractor func(*gin.Context) string
	// RejectHandler 自定义拒绝时的响应逻辑（可选）。
	//
	// 契约：回调被调用前，X-RateLimit-Limit 与 Retry-After 响应头已写入
	// （可经 c.Writer.Header() 读取或覆盖）；reason 为本包导出的拒绝原因
	// 常量；回调负责写入状态码与响应体；回调返回后框架无条件调用
	// c.Abort() 终止后续处理器，无论回调内部是否已自行 Abort。为 nil 时
	// 返回默认响应（c.String 文本格式 "[code] msg"）。
	RejectHandler func(c *gin.Context, reason string)
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
//
// 当 QPS <= 0（含 NaN）或 Burst <= 0 时 panic：无效配置会让
// rate.Limiter 恒拒绝，导致所有请求被 429 锁死（AUD-08）。
// 与项目内其他中间件（requestsize/compression/security）的构造期校验约定一致。
func New(cfg Config) (gin.HandlerFunc, func()) {
	// 使用 !(QPS > 0) 而非 QPS <= 0，以同时拒绝 NaN。
	if !(cfg.QPS > 0) {
		panic("ratelimiter: QPS must be > 0")
	}
	if cfg.Burst <= 0 {
		panic("ratelimiter: Burst must be > 0")
	}

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
			cfg.RejectHandler(c, ReasonRateLimited)
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
	// 在 uint32 域内用位与代替 int(h) % numShards（AUD-09）：
	// 32 位平台上 h >= 2^31 时 int(h) 为负，会产生负索引导致 panic。
	// 位与结果恒在 [0, numShards)，且对 2 的幂与取模等价。
	return int(h & (numShards - 1))
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

// cleanupExpired 定期删除 TTL 内未活跃的 IP 限流条目，防止分片 map 无限增长。
//
// 设计说明（AUD-17，已接受）：本函数的 Range 读取 lastSeen 与 Delete 之间、
// 以及与 allowIP 的 Load/LoadOrStore 之间存在 TOCTOU 窗口——若清理判定条目过期
// 的同时恰有同 IP 请求刷新了 lastSeen，Delete 仍会删除该条目，随后的请求会
// 获得全新满桶。后果是 TTL 边界处同一 IP 短时间内最多双倍放行（旧桶余量 +
// 新桶满额），属轻微 over-admission：无崩溃、无泄漏、无状态破坏。
// 消除该窗口需要 per-entry 锁或带版本的条件删除，会给 allow 热路径增加
// 同步开销，权衡后接受现状、仅文档化。
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
