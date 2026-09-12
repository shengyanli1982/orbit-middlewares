package ratelimiter

import (
	"hash/fnv"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRateLimiter_GlobalMode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeGlobal,
		QPS:   1,
		Burst: 1,
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusOK, recorder.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusTooManyRequests, recorder2.Code)
}

func TestRateLimiter_IPMode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1,
		Burst: 1,
		IPExtractor: func(*gin.Context) string {
			return "test-key"
		},
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusOK, recorder.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusTooManyRequests, recorder2.Code)
}

func TestRateLimiter_DifferentIPs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1,
		Burst: 1,
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.RemoteAddr = "192.168.1.1:1234"
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "192.168.1.2:1234"
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)

	assert.Equal(t, http.StatusOK, recorder1.Code)
	assert.Equal(t, http.StatusOK, recorder2.Code)
}

func TestRateLimiter_RefillTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1,
		Burst: 1,
		IPExtractor: func(*gin.Context) string {
			return "test-key"
		},
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusTooManyRequests, recorder2.Code)

	time.Sleep(1100 * time.Millisecond)

	req3 := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder3 := httptest.NewRecorder()
	router.ServeHTTP(recorder3, req3)
	assert.Equal(t, http.StatusOK, recorder3.Code)
}

func TestRateLimiter_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1,
		Burst: 1,
		IPExtractor: func(*gin.Context) string {
			return "test-key"
		},
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/skip"
		},
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/skip", func(c *gin.Context) {
		c.String(http.StatusOK, "skipped")
	})
	router.GET("/normal", func(c *gin.Context) {
		c.String(http.StatusOK, "normal")
	})

	// 先耗尽令牌：/normal 放行并消耗唯一 token
	req1 := httptest.NewRequest(http.MethodGet, "/normal", nil)
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)

	// 令牌耗尽后 /skip 仍被 Skipper 放行
	req2 := httptest.NewRequest(http.MethodGet, "/skip", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusOK, recorder2.Code)

	// 令牌耗尽后 /normal 被拒绝
	req3 := httptest.NewRequest(http.MethodGet, "/normal", nil)
	recorder3 := httptest.NewRecorder()
	router.ServeHTTP(recorder3, req3)
	assert.Equal(t, http.StatusTooManyRequests, recorder3.Code)
}

// TestNew_InvalidConfigPanics 钉住 AUD-08：零值/非法 Config 必须在构造时 panic，
// 否则 rate.NewLimiter(0, 0) 会恒拒绝，导致 100% 锁死。
func TestNew_InvalidConfigPanics(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name      string
		cfg       Config
		wantPanic string
	}{
		{
			name:      "zero config",
			cfg:       Config{},
			wantPanic: "ratelimiter: QPS must be > 0",
		},
		{
			name:      "negative QPS",
			cfg:       Config{QPS: -1, Burst: 1},
			wantPanic: "ratelimiter: QPS must be > 0",
		},
		{
			name:      "NaN QPS",
			cfg:       Config{QPS: math.NaN(), Burst: 1},
			wantPanic: "ratelimiter: QPS must be > 0",
		},
		{
			name:      "zero Burst",
			cfg:       Config{QPS: 1, Burst: 0},
			wantPanic: "ratelimiter: Burst must be > 0",
		},
		{
			name:      "negative Burst",
			cfg:       Config{QPS: 1, Burst: -5},
			wantPanic: "ratelimiter: Burst must be > 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.PanicsWithValue(t, tt.wantPanic, func() {
				New(tt.cfg)
			})
		})
	}
}

// TestNew_ValidConfigDoesNotPanic 钉住校验边界：最小合法配置不应 panic。
func TestNew_ValidConfigDoesNotPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	assert.NotPanics(t, func() {
		handler, stop := New(Config{Mode: ModeGlobal, QPS: 1, Burst: 1})
		defer stop()
		assert.NotNil(t, handler)
	})
}

// fnvHash32a 使用标准库独立计算 FNV-1a 32 位哈希，
// 用于交叉验证 getShardIndex 的内部实现。
func fnvHash32a(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// TestGetShardIndex_HighBitHashStaysInRange 钉住 AUD-09：
// 对哈希高位为 1（h >= 2^31）的输入，分片索引必须落在 [0, numShards)
// 且等于 uint32 域内取模的数学结果。
// 旧实现 int(h) % numShards 在 32 位平台上 int(h) 为负，产生负索引导致 panic
// （amd64 上 int 为 64 位，恒非负，缺陷仅在 GOARCH=386 等平台显形）。
func TestGetShardIndex_HighBitHashStaysInRange(t *testing.T) {
	var (
		highBitIPs []string
		highBitHs  []uint32
	)
	// 扫描候选 IP，挑选 FNV-1a 哈希高位为 1 的样本
	for a := 0; a < 256 && len(highBitIPs) < 64; a++ {
		for b := 0; b < 256 && len(highBitIPs) < 64; b++ {
			ip := "10.0." + strconv.Itoa(a) + "." + strconv.Itoa(b)
			if h := fnvHash32a(ip); h >= 1<<31 {
				highBitIPs = append(highBitIPs, ip)
				highBitHs = append(highBitHs, h)
			}
		}
	}
	if !assert.NotEmpty(t, highBitIPs, "测试前提：必须找到高位哈希样本") {
		return
	}

	for i, ip := range highBitIPs {
		h := highBitHs[i]
		idx := getShardIndex(ip)
		assert.GreaterOrEqual(t, idx, 0,
			"getShardIndex(%q) 返回负索引（hash=0x%08x）", ip, h)
		assert.Less(t, idx, numShards,
			"getShardIndex(%q) 越界（hash=0x%08x）", ip, h)
		assert.Equal(t, int(h%numShards), idx,
			"getShardIndex(%q) 与 uint32 域取模结果不一致（hash=0x%08x）", ip, h)
	}
}

// newRejectTestRouter 构造 QPS=2/Burst=1 的全局模式路由：
// 第 1 个请求放行并耗尽令牌，第 2 个请求触发 reject 路径。
func newRejectTestRouter(t *testing.T, cfg Config, routeHit func()) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler, stop := New(cfg)
	t.Cleanup(stop)
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		if routeHit != nil {
			routeHit()
		}
		c.String(http.StatusOK, "ok")
	})
	return router
}

// serveOnce 向 /test 发送一次 GET 请求并返回 recorder。
func serveOnce(router *gin.Engine) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

// TestRateLimiter_RejectHandler_CustomResponse 钉住 AUD-18 与统一拒绝契约：
// 自定义 RejectHandler 写入的状态码与响应体生效，回调收到 ReasonRateLimited。
func TestRateLimiter_RejectHandler_CustomResponse(t *testing.T) {
	var gotReason string
	cfg := Config{
		Mode:  ModeGlobal,
		QPS:   2,
		Burst: 1,
		RejectHandler: func(c *gin.Context, reason string) {
			gotReason = reason
			c.String(http.StatusServiceUnavailable, "custom reject")
		},
	}
	router := newRejectTestRouter(t, cfg, nil)

	assert.Equal(t, http.StatusOK, serveOnce(router).Code)

	recorder := serveOnce(router)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, "custom reject", recorder.Body.String())
	assert.Equal(t, ReasonRateLimited, gotReason, "回调应收到 ReasonRateLimited")
}

// TestRateLimiter_RejectHandler_SeesPresetHeaders 钉住 AUD-18：
// 回调执行前 X-RateLimit-Limit 与 Retry-After 已写入响应头，回调内可见，
// 且最终随响应返回；回调收到 ReasonRateLimited。
// 注意：gin 的 c.GetHeader 读的是请求头，响应头必须经 c.Writer.Header() 读取。
func TestRateLimiter_RejectHandler_SeesPresetHeaders(t *testing.T) {
	var gotLimit, gotRetry, gotReason string
	cfg := Config{
		Mode:  ModeGlobal,
		QPS:   2,
		Burst: 1,
		RejectHandler: func(c *gin.Context, reason string) {
			gotReason = reason
			gotLimit = c.Writer.Header().Get("X-RateLimit-Limit")
			gotRetry = c.Writer.Header().Get("Retry-After")
			c.Status(http.StatusTooManyRequests)
		},
	}
	router := newRejectTestRouter(t, cfg, nil)

	assert.Equal(t, http.StatusOK, serveOnce(router).Code)

	recorder := serveOnce(router)
	assert.Equal(t, http.StatusTooManyRequests, recorder.Code)

	assert.Equal(t, ReasonRateLimited, gotReason, "回调应收到 ReasonRateLimited")
	// QPS=2 → X-RateLimit-Limit=ceil(2)="2"；Burst=1 耗尽后 delay≈0.5s → Retry-After=ceil(0.5)="1"
	assert.Equal(t, "2", gotLimit, "回调内应可见预设 X-RateLimit-Limit")
	assert.Equal(t, "1", gotRetry, "回调内应可见预设 Retry-After")
	// 预设头最终写入响应
	assert.Equal(t, "2", recorder.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "1", recorder.Header().Get("Retry-After"))
}

// TestRateLimiter_RejectHandler_AbortEnforced 钉住 AUD-18 与统一拒绝契约：
// 回调内不调用 Abort，外层 reject 仍无条件 Abort，后续路由处理器不得执行。
func TestRateLimiter_RejectHandler_AbortEnforced(t *testing.T) {
	routeHits := 0
	var gotReason string
	cfg := Config{
		Mode:  ModeGlobal,
		QPS:   2,
		Burst: 1,
		RejectHandler: func(c *gin.Context, reason string) {
			gotReason = reason
			// 故意不调用 c.Abort()
			c.String(http.StatusTooManyRequests, "rejected by callback")
		},
	}
	router := newRejectTestRouter(t, cfg, func() { routeHits++ })

	assert.Equal(t, http.StatusOK, serveOnce(router).Code)
	assert.Equal(t, 1, routeHits, "放行请求应到达路由处理器")

	recorder := serveOnce(router)
	assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
	assert.Equal(t, "rejected by callback", recorder.Body.String())
	assert.Equal(t, ReasonRateLimited, gotReason, "回调应收到 ReasonRateLimited")
	assert.Equal(t, 1, routeHits, "拒绝后外层必须 Abort，路由处理器不得再次执行")
}

// TestRateLimiter_RejectHandler_NilUsesDefault 钉住 AUD-18：
// 未配置 RejectHandler 时使用默认 429 响应体。
func TestRateLimiter_RejectHandler_NilUsesDefault(t *testing.T) {
	cfg := Config{
		Mode:  ModeGlobal,
		QPS:   2,
		Burst: 1,
	}
	router := newRejectTestRouter(t, cfg, nil)

	assert.Equal(t, http.StatusOK, serveOnce(router).Code)

	recorder := serveOnce(router)
	assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
	assert.Equal(t, "[429] rate limit exceeded", recorder.Body.String())
	assert.Equal(t, "2", recorder.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "1", recorder.Header().Get("Retry-After"))
}
