package security

import (
	"net/http"
	"net/http/httptest"
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

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}
}
