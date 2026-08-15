package requestsize

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func BenchmarkRequestSize_WithinLimit(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 1024 * 1024,
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

func BenchmarkRequestSize_ExceedLimit(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 100,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.ContentLength = 1024
		recorder := httptest.NewRecorder()
		for pb.Next() {
			router.ServeHTTP(recorder, req)
		}
	})
}

func BenchmarkRequestSize_MemAllocation(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 1024 * 1024,
	}

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
