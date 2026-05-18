package requestid

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.Use(New(Config{}))
	router.GET("/test", func(c *gin.Context) {
		requestID := c.GetString("request_id")
		c.String(http.StatusOK, requestID)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.NotEmpty(t, recorder.Body.String())
	assert.NotEmpty(t, recorder.Header().Get("X-Request-ID"))
}

func TestRequestID_HeaderExists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.Use(New(Config{}))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.NotEmpty(t, recorder.Header().Get("X-Request-ID"))
}

func TestRequestID_CustomHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.Use(New(Config{HeaderName: "X-Custom-ID"}))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.NotEmpty(t, recorder.Header().Get("X-Custom-ID"))
}

func TestRequestID_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.Use(New(Config{
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/skip"
		},
	}))
	router.GET("/skip", func(c *gin.Context) {
		_, exists := c.Get("request_id")
		assert.False(t, exists)
		c.String(http.StatusOK, "skipped")
	})
	router.GET("/normal", func(c *gin.Context) {
		_, exists := c.Get("request_id")
		assert.True(t, exists)
		c.String(http.StatusOK, "normal")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/skip", nil)
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)
	assert.Empty(t, recorder1.Header().Get("X-Request-ID"))

	req2 := httptest.NewRequest(http.MethodGet, "/normal", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusOK, recorder2.Code)
	assert.NotEmpty(t, recorder2.Header().Get("X-Request-ID"))
}

// TestRequestID_Concurrent 验证并发生成 request ID 无 data race（-race 检测）。
func TestRequestID_Concurrent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(New(Config{}))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString("request_id"))
	})

	var wg sync.WaitGroup
	ids := make([]string, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			ids[idx] = recorder.Body.String()
		}()
	}
	wg.Wait()

	// 每个请求应生成唯一 ID（32位十六进制）
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		assert.Len(t, id, 32, "request ID 应为 32 位十六进制字符串")
		assert.False(t, seen[id], "request ID 应唯一，发现重复: %s", id)
		seen[id] = true
	}
}
