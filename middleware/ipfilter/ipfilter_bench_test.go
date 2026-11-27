package ipfilter

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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

func BenchmarkIPFilter_BlockedIPs_LinearSearch(b *testing.B) {
	gin.SetMode(gin.TestMode)

	blockedIPs := make([]string, 100)
	for i := 0; i < 100; i++ {
		blockedIPs[i] = fmt.Sprintf("192.168.1.%d", i)
	}

	cfg := Config{
		BlockedIPs: blockedIPs,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.168.1.50:1234"
		w := newBenchWriter() // 拒绝路径每轮干净 writer，防 Body 无界增长抬高 GC heap goal
		for pb.Next() {
			w.reset()
			router.ServeHTTP(w, req)
		}
	})
}

func BenchmarkIPFilter_AllowedIPs_LinearSearch(b *testing.B) {
	gin.SetMode(gin.TestMode)

	allowedIPs := make([]string, 100)
	for i := 0; i < 100; i++ {
		allowedIPs[i] = fmt.Sprintf("192.168.1.%d", i)
	}

	cfg := Config{
		AllowedIPs: allowedIPs,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.168.1.50:1234"
		recorder := httptest.NewRecorder() // 每轮 Reset，防 Body 跨轮累积抬高 GC heap goal
		for pb.Next() {
			resetRecorder(recorder)
			router.ServeHTTP(recorder, req)
		}
	})
}

func BenchmarkIPFilter_NoMatch(b *testing.B) {
	gin.SetMode(gin.TestMode)

	blockedIPs := make([]string, 100)
	for i := 0; i < 100; i++ {
		blockedIPs[i] = fmt.Sprintf("10.0.%d.1", i)
	}

	cfg := Config{
		BlockedIPs: blockedIPs,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.168.1.1:1234"
		recorder := httptest.NewRecorder() // 每轮 Reset，防 Body 跨轮累积抬高 GC heap goal
		for pb.Next() {
			resetRecorder(recorder)
			router.ServeHTTP(recorder, req)
		}
	})
}

func BenchmarkIPFilter_MemAllocation(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		BlockedIPs: []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.168.1.1:1234"
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}
}

// BenchmarkIPFilter_IPSetContains_Only 直调 ipSet.contains 度量精确 IP
// map 命中路径的孤立成本（实现已由线性扫描切换为 map 查找，
// 原 LinearSearch_Only 合成基准度量的是已不存在的算法，予以替换）。
func BenchmarkIPFilter_IPSetContains_Only(b *testing.B) {
	blockedIPs := make([]string, 100)
	for i := 0; i < 100; i++ {
		blockedIPs[i] = fmt.Sprintf("192.168.1.%d", i)
	}

	s := newIPSet(blockedIPs)
	clientIP := "192.168.1.50"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.contains(clientIP)
	}
}

// BenchmarkIPFilter_BlockedCIDR 覆盖 CIDR 匹配路径：exactIPs 未命中 →
// netip.ParseAddr（值类型，零分配）→ 线性扫描 prefixes → Contains 命中 → 403。
// 生产黑名单常见形态（封禁整个网段）。
func BenchmarkIPFilter_BlockedCIDR(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		BlockedIPs: []string{"10.0.0.0/8"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "10.1.2.3:1234"
		w := newBenchWriter() // 拒绝路径每轮干净 writer，防 Body 无界增长抬高 GC heap goal
		for pb.Next() {
			w.reset()
			router.ServeHTTP(w, req)
		}
	})
}

// BenchmarkIPFilter_AllowedCIDR 覆盖白名单模式 + CIDR 放行路径：
// blocked 空集快速未命中 → allowed 集合 netip.ParseAddr + Prefix.Contains 命中 → Next。
func BenchmarkIPFilter_AllowedCIDR(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		AllowedIPs: []string{"192.168.0.0/16"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.168.1.50:1234"
		recorder := httptest.NewRecorder() // 每轮 Reset，防 Body 跨轮累积抬高 GC heap goal
		for pb.Next() {
			resetRecorder(recorder)
			router.ServeHTTP(recorder, req)
		}
	})
}
