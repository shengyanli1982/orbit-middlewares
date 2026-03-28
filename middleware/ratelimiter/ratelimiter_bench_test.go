package ratelimiter

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func BenchmarkRateLimiter_GlobalMode(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeGlobal,
		QPS:   1000,
		Burst: 1000,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	f, err := os.Create("ratelimiter_global_cpu.prof")
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
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	f, err := os.Create("ratelimiter_ip_cpu.prof")
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

func BenchmarkRateLimiter_IPModeManyKeys(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1000,
		Burst: 1000,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	f, err := os.Create("ratelimiter_manykeys_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for i := 0; pb.Next(); i++ {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = fmt.Sprintf("192.168.1.%d:1234", i%256)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
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
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	f, err := os.Create("ratelimiter_mem.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = fmt.Sprintf("192.168.1.%d:1234", i%256)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}

	runtime.GC()
	pprof.WriteHeapProfile(f)
}

func BenchmarkRateLimiter_AllowIP(b *testing.B) {
	l := &limiter{
		cfg: Config{
			QPS:   100,
			Burst: 10,
			TTL:   5 * time.Minute,
		},
	}

	f, err := os.Create("ratelimiter_allowip_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("192.168.1.%d", i%256)
		l.allowIP(key)
	}
}
