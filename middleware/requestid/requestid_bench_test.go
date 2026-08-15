package requestid

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func BenchmarkRequestID_ExistingHeader(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		HeaderName: "X-Request-ID",
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Request-ID", "existing-id-12345")
		recorder := httptest.NewRecorder()
		for pb.Next() {
			router.ServeHTTP(recorder, req)
		}
	})
}

func BenchmarkRequestID_GenerateID(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		HeaderName: "X-Request-ID",
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

func BenchmarkRequestID_MemAllocation(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		HeaderName: "X-Request-ID",
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
