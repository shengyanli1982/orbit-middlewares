package ratelimiter

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRateLimiter_Basic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		QPS:   1,
		Burst: 1,
		KeyExtractor: func(*gin.Context) string {
			return "test-key"
		},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)

	assert.Equal(t, http.StatusTooManyRequests, recorder2.Code)
}

func TestRateLimiter_DifferentKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		QPS:   1,
		Burst: 1,
		KeyExtractor: func(c *gin.Context) string {
			return c.ClientIP()
		},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.RemoteAddr = "192.168.1.1:1234"
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "192.168.1.2:1234"
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)

	assert.Equal(t, http.StatusOK, recorder1.Code)
	assert.Equal(t, http.StatusOK, recorder2.Code)
}

func TestRateLimiter_RefillTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		QPS:   1,
		Burst: 1,
		KeyExtractor: func(*gin.Context) string {
			return "test-key"
		},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusTooManyRequests, recorder2.Code)

	time.Sleep(1100 * time.Millisecond)

	req3 := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder3 := httptest.NewRecorder()
	router.ServeHTTP(recorder3, req3)
	assert.Equal(t, http.StatusOK, recorder3.Code)
}

func TestRateLimiter_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		QPS:   0,
		Burst: 0,
		KeyExtractor: func(*gin.Context) string {
			return "test-key"
		},
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
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/normal", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusTooManyRequests, recorder2.Code)
}
