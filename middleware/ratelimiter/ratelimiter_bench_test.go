package ratelimiter

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// benchWriter 是基准专用的干净状态 ResponseWriter：Header 返回每轮由 reset 清空的
// map，Write 丢弃响应体，WriteHeader 记录状态码。复用 recorder 且从不 Reset 会让
// Body 逐轮无界增长，常驻大 buffer 抬高 GC heap goal、抑制 GC 触发，令基准跨版本
// 对比失真；本 writer 零 body 驻留，脚手架开销可忽略。先例与实测依据见 autoindex
// 基准的 benchWriter 注释。
type benchWriter struct {
	header http.Header
	status int
}

// newBenchWriter 创建预分配 header map 的 benchWriter。
func newBenchWriter() *benchWriter {
	return &benchWriter{header: make(http.Header, 8)}
}

// reset 把 writer 恢复到「本轮尚未写过任何内容」的干净状态。
func (w *benchWriter) reset() {
	clear(w.header)
	w.status = 0
}

// Header 返回可变的响应头 map（每轮由 reset 清空，不跨轮残留）。
func (w *benchWriter) Header() http.Header { return w.header }

// Write 丢弃响应体内容，仅返回成功（零 body 驻留）。
func (w *benchWriter) Write(p []byte) (int, error) { return len(p), nil }

// WriteHeader 记录状态码；与 http.ResponseWriter 一致，首次写入后不再改变。
func (w *benchWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

// resetRecorder 把 recorder 恢复到本轮干净状态：清空 Body 防逐轮累积，清空响应头
// 防跨轮残留。标准库 ResponseRecorder 无 Reset 方法，但 Body 与 HeaderMap 均为导出
// 字段，直接复位即可消除无界增长；未导出的 wroteHeader 自第 2 轮起保持 true，仅
// 跳过 recorder 自身的记录逻辑（基准不断言响应内容），不改变被测调用路径。
func resetRecorder(rw *httptest.ResponseRecorder) {
	rw.Body.Reset()
	clear(rw.Header())
}

// manyKeysRequests 预生成 256 个不同源 IP 的 GET 请求，把 Sprintf 与 NewRequest
// 的 harness 开销移出测量循环（每 goroutine 在循环前构造一次并轮转复用；GET 无
// body、被测路径只读不改请求，goroutine 内复用安全）。
func manyKeysRequests() []*http.Request {
	reqs := make([]*http.Request, 256)
	for i := range reqs {
		reqs[i] = httptest.NewRequest(http.MethodGet, "/test", nil)
		reqs[i].RemoteAddr = fmt.Sprintf("192.168.1.%d:1234", i)
	}
	return reqs
}

func BenchmarkRateLimiter_GlobalMode(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeGlobal,
		QPS:   1000,
		Burst: 1000,
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		w := newBenchWriter() // 拒绝风暴路径每轮干净 writer，防 Body 无界增长抬高 GC heap goal
		for pb.Next() {
			w.reset()
			router.ServeHTTP(w, req)
		}
	})
}

func BenchmarkRateLimiter_IPMode(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1000,
		Burst: 1000,
		IPExtractor: func(*gin.Context) string {
			return "192.168.1.1"
		},
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		w := newBenchWriter() // 拒绝风暴路径每轮干净 writer，防 Body 无界增长抬高 GC heap goal
		for pb.Next() {
			w.reset()
			router.ServeHTTP(w, req)
		}
	})
}

func BenchmarkRateLimiter_IPModeManyKeys(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1000,
		Burst: 1000,
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		reqs := manyKeysRequests()
		w := newBenchWriter() // 拒绝风暴路径每轮干净 writer，防 Body 无界增长抬高 GC heap goal
		for i := 0; pb.Next(); i++ {
			w.reset()
			router.ServeHTTP(w, reqs[i%256])
		}
	})
}

func BenchmarkRateLimiter_MemAllocation(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   100,
		Burst: 10,
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = fmt.Sprintf("192.168.1.%d:1234", i%256)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}
}

func BenchmarkRateLimiter_AllowIP(b *testing.B) {
	l := &limiter{
		cfg: Config{
			QPS:   100,
			Burst: 10,
			TTL:   5 * time.Minute,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("192.168.1.%d", i%256)
		l.allowIP(key)
	}
}

// ---- allow 主路径基准（生产稳态：流量低于限速，令牌桶不被打空）----
//
// 上方 GlobalMode / IPMode / IPModeManyKeys / AllowIP 使用小容量令牌桶，
// 在 b.N 高速迭代下桶必然耗尽，实际度量的是拒绝风暴路径
// （Allow()=false → Reserve() → delay>0 → reject，含 Reservation 分配、
// FormatInt、c.String、3 次锁操作）。以下 *_AllowPath 基准将 QPS/Burst
// 设为足够大，使每次调用都命中 Allow()==true 的放行快路径，
// 两组基准须对照解读，不可混用。

func BenchmarkRateLimiter_GlobalMode_AllowPath(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeGlobal,
		QPS:   1e9,
		Burst: 1e9,
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		recorder := httptest.NewRecorder() // 每轮 Reset，防 Body 跨轮累积抬高 GC heap goal
		for pb.Next() {
			resetRecorder(recorder)
			router.ServeHTTP(recorder, req)
		}
	})
}

func BenchmarkRateLimiter_IPMode_AllowPath(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1e9,
		Burst: 1e9,
		IPExtractor: func(*gin.Context) string {
			return "192.168.1.1"
		},
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		recorder := httptest.NewRecorder() // 每轮 Reset，防 Body 跨轮累积抬高 GC heap goal
		for pb.Next() {
			resetRecorder(recorder)
			router.ServeHTTP(recorder, req)
		}
	})
}

// BenchmarkRateLimiter_IPModeManyKeys_AllowPath 度量 256 个 key 分布在
// 256 个分片上的并发放行路径（分片竞争主形态：sync.Map Load + 分片内
// rate.Limiter 锁 + gin ClientIP 解析）。
func BenchmarkRateLimiter_IPModeManyKeys_AllowPath(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1e9,
		Burst: 1e9,
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		reqs := manyKeysRequests()
		recorder := httptest.NewRecorder() // 每轮 Reset，防 Body 跨轮累积抬高 GC heap goal
		for i := 0; pb.Next(); i++ {
			resetRecorder(recorder)
			router.ServeHTTP(recorder, reqs[i%256])
		}
	})
}

// BenchmarkRateLimiter_AllowIP_AllowPath 直调 allowIP，桶容量充足，
// 命中 Load→lastSeen.Store→Allow()==true→return (true,0,nil) 零分配快路径。
// 与 BenchmarkRateLimiter_AllowIP（拒绝风暴形态）对照。
func BenchmarkRateLimiter_AllowIP_AllowPath(b *testing.B) {
	l := &limiter{
		cfg: Config{
			QPS:   1e9,
			Burst: 1e9,
			TTL:   5 * time.Minute,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("192.168.1.%d", i%256)
		l.allowIP(key)
	}
}
