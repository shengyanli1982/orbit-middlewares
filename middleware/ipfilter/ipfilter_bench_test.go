package ipfilter

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/pprof"
	"testing"

	"github.com/gin-gonic/gin"
)

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

	f, err := os.Create("ipfilter_blocked_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.168.1.50:1234"
		recorder := httptest.NewRecorder()
		for pb.Next() {
			router.ServeHTTP(recorder, req)
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

	f, err := os.Create("ipfilter_allowed_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.168.1.50:1234"
		recorder := httptest.NewRecorder()
		for pb.Next() {
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

	f, err := os.Create("ipfilter_nomatch_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.168.1.1:1234"
		recorder := httptest.NewRecorder()
		for pb.Next() {
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

	f, err := os.Create("ipfilter_mem.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.168.1.1:1234"
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}

	runtime.GC()
	pprof.WriteHeapProfile(f)
}

func BenchmarkIPFilter_LinearSearch_Only(b *testing.B) {
	blockedIPs := make([]string, 100)
	for i := 0; i < 100; i++ {
		blockedIPs[i] = fmt.Sprintf("192.168.1.%d", i)
	}

	clientIP := "192.168.1.50"

	f, err := os.Create("ipfilter_linear_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, ip := range blockedIPs {
			if ip == clientIP {
				break
			}
		}
	}
}
