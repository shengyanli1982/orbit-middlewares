package timeout

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

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
		recorder := httptest.NewRecorder()
		for pb.Next() {
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
		recorder := httptest.NewRecorder()
		for pb.Next() {
			router.ServeHTTP(recorder, req)
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
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}
}
