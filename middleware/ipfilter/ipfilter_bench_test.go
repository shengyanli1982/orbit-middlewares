package ipfilter

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
		recorder := httptest.NewRecorder()
		for pb.Next() {
			router.ServeHTTP(recorder, req)
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
		recorder := httptest.NewRecorder()
		for pb.Next() {
			router.ServeHTTP(recorder, req)
		}
	})
}
