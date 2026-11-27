package security

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// resetRecorder 把 recorder 恢复到本轮干净状态：清空 Body 防逐轮累积，清空响应头
// 防跨轮残留。标准库 ResponseRecorder 无 Reset 方法，但 Body 与 HeaderMap 均为导出
// 字段，直接复位即可消除无界增长；未导出的 wroteHeader 自第 2 轮起保持 true，仅
// 跳过 recorder 自身的记录逻辑（基准不断言响应内容），不改变被测调用路径。
func resetRecorder(rw *httptest.ResponseRecorder) {
	rw.Body.Reset()
	clear(rw.Header())
}

func BenchmarkSecurityHeaders_AllHeaders(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := DefaultConfig()
	router := gin.New()
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

func BenchmarkSecurityHeaders_MinimalHeaders(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		XFrameOptions:       "DENY",
		XContentTypeOptions: "nosniff",
	}
	router := gin.New()
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

func BenchmarkSecurityHeaders_NoHeaders(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{}
	router := gin.New()
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

func BenchmarkSecurityHeaders_SkipperEnabled(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		XFrameOptions:       "DENY",
		XContentTypeOptions: "nosniff",
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/skip"
		},
	}
	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	router.GET("/skip", func(c *gin.Context) {
		c.String(http.StatusOK, "skipped")
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/skip", nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}
}

func BenchmarkSecurityHeaders_HSTSFullyConfigured(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		XFrameOptions:         "DENY",
		XContentTypeOptions:   "nosniff",
		HSTSMaxAge:            63072000,
		HSTSIncludeSubDomains: true,
		HSTSPreload:           true,
		CSP:                   "default-src 'none'; script-src 'none'; object-src 'none'",
		XSSProtection:         "0",
		ReferrerPolicy:        "no-referrer",
		PermissionsPolicy:     "geolocation=(), microphone=(), camera=(), payment=()",
	}
	router := gin.New()
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

func BenchmarkSecurityHeaders_MemAllocation(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := DefaultConfig()
	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}
}
