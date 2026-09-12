package ratelimiter

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
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
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
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

func BenchmarkRateLimiter_IPModeManyKeys(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1000,
		Burst: 1000,
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

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
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = fmt.Sprintf("192.168.1.%d:1234", i%256)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}
}

func BenchmarkRateLimiter_AllowIP(b *testing.B) {
	l := &limiter{
		cfg: Config{
			QPS:   100,
			Burst: 10,
			TTL:   5 * time.Minute,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("192.168.1.%d", i%256)
		l.allowIP(key)
	}
}

// ---- allow 主路径基准（生产稳态：流量低于限速，令牌桶不被打空）----
//
// 上方 GlobalMode / IPMode / IPModeManyKeys / AllowIP 使用小容量令牌桶，
// 在 b.N 高速迭代下桶必然耗尽，实际度量的是拒绝风暴路径
// （Allow()=false → Reserve() → delay>0 → reject，含 Reservation 分配、
// FormatInt、c.String、3 次锁操作）。以下 *_AllowPath 基准将 QPS/Burst
// 设为足够大，使每次调用都命中 Allow()==true 的放行快路径，
// 两组基准须对照解读，不可混用。

func BenchmarkRateLimiter_GlobalMode_AllowPath(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeGlobal,
		QPS:   1e9,
		Burst: 1e9,
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
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

func BenchmarkRateLimiter_IPMode_AllowPath(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1e9,
		Burst: 1e9,
		IPExtractor: func(*gin.Context) string {
			return "192.168.1.1"
		},
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
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

// BenchmarkRateLimiter_IPModeManyKeys_AllowPath 度量 256 个 key 分布在
// 256 个分片上的并发放行路径（分片竞争主形态：sync.Map Load + 分片内
// rate.Limiter 锁 + gin ClientIP 解析）。
func BenchmarkRateLimiter_IPModeManyKeys_AllowPath(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Mode:  ModeIP,
		QPS:   1e9,
		Burst: 1e9,
	}

	router := gin.New()
	handler, stop := New(cfg)
	defer stop()
	router.Use(handler)
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

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

// BenchmarkRateLimiter_AllowIP_AllowPath 直调 allowIP，桶容量充足，
// 命中 Load→lastSeen.Store→Allow()==true→return (true,0,nil) 零分配快路径。
// 与 BenchmarkRateLimiter_AllowIP（拒绝风暴形态）对照。
func BenchmarkRateLimiter_AllowIP_AllowPath(b *testing.B) {
	l := &limiter{
		cfg: Config{
			QPS:   1e9,
			Burst: 1e9,
			TTL:   5 * time.Minute,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("192.168.1.%d", i%256)
		l.allowIP(key)
	}
}
