package security

import (
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/pprof"
	"testing"

	"github.com/gin-gonic/gin"
)

func BenchmarkSecurityHeaders_AllHeaders(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := DefaultConfig()
	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	f, err := os.Create("security_all_headers_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		recorder := httptest.NewRecorder()
		for pb.Next() {
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

	f, err := os.Create("security_minimal_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		recorder := httptest.NewRecorder()
		for pb.Next() {
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

	f, err := os.Create("security_no_headers_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		recorder := httptest.NewRecorder()
		for pb.Next() {
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
		XSSProtection:         "1; mode=block",
		ReferrerPolicy:        "no-referrer",
		PermissionsPolicy:     "geolocation=(), microphone=(), camera=(), payment=()",
	}
	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	f, err := os.Create("security_hsts_full_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		recorder := httptest.NewRecorder()
		for pb.Next() {
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

	f, err := os.Create("security_mem.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}

	runtime.GC()
	pprof.WriteHeapProfile(f)
}
