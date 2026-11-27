package timeout

import (
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
// 基准的 benchWriter 注释。被测路径不调用 http.ResponseController 系列，无需实现
// SetWriteDeadline 等扩展方法。
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

func BenchmarkTimeout_NoTimeout(b *testing.B) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: 5 * time.Second,
		Engine:  router,
	}

	router.Use(New(cfg))
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

func BenchmarkTimeout_Triggered(b *testing.B) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: 1 * time.Millisecond,
		Engine:  router,
	}

	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		time.Sleep(10 * time.Millisecond)
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		w := newBenchWriter() // 拒绝路径每轮干净 writer，防 Body 无界增长抬高 GC heap goal
		for pb.Next() {
			w.reset()
			router.ServeHTTP(w, req)
		}
	})
}

func BenchmarkTimeout_MemAllocation(b *testing.B) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: 5 * time.Second,
		Engine:  router,
	}

	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}
}
