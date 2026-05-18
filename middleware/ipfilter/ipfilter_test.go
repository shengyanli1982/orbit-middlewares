package ipfilter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestIPLimiter_AllowedIP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		AllowedIPs: []string{"192.168.1.1"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestIPLimiter_BlockedIP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		BlockedIPs: []string{"192.168.1.1"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "[403]")
}

func TestIPLimiter_NoRestrictions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestIPLimiter_BlockedFirst(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		AllowedIPs: []string{"192.168.1.1"},
		BlockedIPs: []string{"192.168.1.1"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestIPLimiter_AllowedCIDR(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		AllowedIPs: []string{"192.168.1.0/24"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// 在 CIDR 范围内，应该允许
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.100:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusOK, recorder.Code)

	// 不在 CIDR 范围内，应该拒绝
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "192.168.2.1:1234"
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusForbidden, recorder2.Code)
}

func TestIPLimiter_BlockedCIDR(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		BlockedIPs: []string{"10.0.0.0/8"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// 在黑名单 CIDR 范围内，应该拒绝
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "10.1.2.3:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "[403]")

	// 不在黑名单 CIDR 范围内，应该允许
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "192.168.1.1:1234"
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusOK, recorder2.Code)
}

func TestIPLimiter_MixedExactAndCIDR(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		AllowedIPs: []string{"192.168.1.0/24", "10.0.0.1"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	cases := []struct {
		remoteAddr string
		wantCode   int
	}{
		{"192.168.1.50:1234", http.StatusOK},    // 在 CIDR 内
		{"10.0.0.1:1234", http.StatusOK},         // 精确匹配
		{"10.0.0.2:1234", http.StatusForbidden},  // 不在白名单
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = tc.remoteAddr
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, tc.wantCode, rec.Code, "remoteAddr=%s", tc.remoteAddr)
	}
}

func TestIPLimiter_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		BlockedIPs: []string{"192.168.1.1"},
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/skip"
		},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/skip", func(c *gin.Context) {
		c.String(http.StatusOK, "skipped")
	})
	router.GET("/normal", func(c *gin.Context) {
		c.String(http.StatusOK, "normal")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/skip", nil)
	req1.RemoteAddr = "192.168.1.1:1234"
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/normal", nil)
	req2.RemoteAddr = "192.168.1.1:1234"
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusForbidden, recorder2.Code)
}
