package requestsize

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRequestSizeLimiter_SmallBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 100,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.POST("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	body := strings.NewReader("small body")
	req := httptest.NewRequest(http.MethodPost, "/test", body)
	req.Header.Set("Content-Type", "application/octet-stream")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

// TestRequestSizeLimiter_LargeBody 测试 Content-Length 超限时快速拒绝（无需读取 body）
func TestRequestSizeLimiter_LargeBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 10,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.POST("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	body := strings.NewReader("this body is too large")
	req := httptest.NewRequest(http.MethodPost, "/test", body)
	req.Header.Set("Content-Type", "application/octet-stream")
	// 显式设置 Content-Length 超限，触发快速拒绝路径
	req.ContentLength = int64(len("this body is too large"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
}

// TestRequestSizeLimiter_ChunkedLargeBody 测试 chunked transfer 超限时被 MaxBytesReader 拦截
// chunked 请求 Content-Length = -1，绕过快速拒绝，需要 MaxBytesReader 在读取时限制
func TestRequestSizeLimiter_ChunkedLargeBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 10,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.POST("/test", func(c *gin.Context) {
		// 下游 handler 读取 body，触发 MaxBytesReader 超限错误
		_, err := io.ReadAll(c.Request.Body)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				c.AbortWithStatus(http.StatusRequestEntityTooLarge)
				return
			}
		}
		c.String(http.StatusOK, "ok")
	})

	body := strings.NewReader("this body is too large for the limit")
	req := httptest.NewRequest(http.MethodPost, "/test", body)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Transfer-Encoding", "chunked")
	req.ContentLength = -1 // 模拟 chunked：无 Content-Length
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
}

// TestRequestSizeLimiter_ChunkedSmallBody 测试 chunked transfer 未超限时正常通过
func TestRequestSizeLimiter_ChunkedSmallBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 100,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.POST("/test", func(c *gin.Context) {
		data, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		c.String(http.StatusOK, string(data))
	})

	body := strings.NewReader("small")
	req := httptest.NewRequest(http.MethodPost, "/test", body)
	req.Header.Set("Transfer-Encoding", "chunked")
	req.ContentLength = -1
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "small", recorder.Body.String())
}
