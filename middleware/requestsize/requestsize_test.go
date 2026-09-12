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
	// AUD-12：响应体必须遵循项目统一约定 "[code] msg"，不允许空 body
	assert.Equal(t, "[413] request entity too large", recorder.Body.String())
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

// TestRequestSizeLimiter_NoBodyNotWrapped T4：net/http server 对无 body 的请求
// （GET 等）置 http.NoBody，对其包装 MaxBytesReader 无意义；断言中间件跳过
// 包装后 Body 保持原值（省一次 *maxBytesReader 分配）。
func TestRequestSizeLimiter_NoBodyNotWrapped(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 100,
	}

	router := gin.New()
	router.Use(New(cfg))
	var seenBody io.ReadCloser
	router.GET("/test", func(c *gin.Context) {
		seenBody = c.Request.Body
		c.String(http.StatusOK, "ok")
	})

	// httptest.NewRequest(GET, nil) 产生 Body == http.NoBody（与真实 server 一致）
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, seenBody == http.NoBody,
		"无 body 请求的 Body 应保持 http.NoBody，不被 MaxBytesReader 包装")

	// Body == nil 同样不包装（防御分支）
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Body = nil
	seenBody = http.NoBody // 置为非 nil 以区分 handler 未执行
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusOK, recorder2.Code)
	assert.Nil(t, seenBody, "Body==nil 的请求不应被包装")
}

// TestRequestSizeLimiter_RealBodyStillWrapped T4：带真实 body 的 POST 必须仍被
// MaxBytesReader 包装——Content-Length 未知（-1）时超限只能在读取期暴露，
// 断言 413 行为不变（若未包装，io.ReadAll 将成功并返回 200）。
func TestRequestSizeLimiter_RealBodyStillWrapped(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 10,
	}

	router := gin.New()
	router.Use(New(cfg))
	var bodyWasNoBody bool
	router.POST("/test", func(c *gin.Context) {
		bodyWasNoBody = c.Request.Body == http.NoBody || c.Request.Body == nil
		_, err := io.ReadAll(c.Request.Body)
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		c.String(http.StatusOK, "ok")
	})

	body := strings.NewReader("this body exceeds the ten byte limit")
	req := httptest.NewRequest(http.MethodPost, "/test", body)
	req.ContentLength = -1 // 长度未知：绕过 Content-Length 快拒，强制走 MaxBytesReader 读取路径
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.False(t, bodyWasNoBody, "真实 body 不应退化为 NoBody/nil")
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
}

// TestRequestSize_RejectHandler_CustomResponse 钉住统一拒绝契约：
// Content-Length 快拒路径上自定义 RejectHandler 生效（状态码+响应体），
// 且回调收到 ReasonEntityTooLarge。
// nil RejectHandler 的默认响应由既有 TestRequestSizeLimiter_LargeBody 精确钉住。
func TestRequestSize_RejectHandler_CustomResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotReason string
	cfg := Config{
		MaxSize: 10,
		RejectHandler: func(c *gin.Context, reason string) {
			gotReason = reason
			c.JSON(http.StatusTeapot, gin.H{"reason": reason})
		},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.POST("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader("this body is too large"))
	req.ContentLength = int64(len("this body is too large"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusTeapot, recorder.Code, "回调写入的状态码应生效")
	assert.JSONEq(t, `{"reason":"request.entity_too_large"}`, recorder.Body.String(), "回调写入的响应体应生效")
	assert.Equal(t, ReasonEntityTooLarge, gotReason, "回调应收到 ReasonEntityTooLarge")
}

// TestRequestSize_RejectHandler_AbortEnforced 钉住统一拒绝契约：
// 回调内不调用 Abort，框架仍无条件 Abort，后续路由处理器不得执行。
func TestRequestSize_RejectHandler_AbortEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)

	routeHits := 0
	cfg := Config{
		MaxSize: 10,
		RejectHandler: func(c *gin.Context, reason string) {
			// 故意不调用 c.Abort()
			c.String(http.StatusRequestEntityTooLarge, "rejected by callback: "+reason)
		},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.POST("/test", func(c *gin.Context) {
		routeHits++
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader("this body is too large"))
	req.ContentLength = int64(len("this body is too large"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	assert.Equal(t, "rejected by callback: request.entity_too_large", recorder.Body.String())
	assert.Equal(t, 0, routeHits, "拒绝后框架必须 Abort，路由处理器不得执行")
}

// TestRequestSize_RejectHandler_NotCalledOnChunkedPath 钉住覆盖范围例外：
// chunked 传输无 Content-Length，超限由 MaxBytesReader 在下游 handler 读取
// body 时暴露（413 由下游自行返回），RejectHandler 不得被调用。
func TestRequestSize_RejectHandler_NotCalledOnChunkedPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	callbackHits := 0
	cfg := Config{
		MaxSize: 10,
		RejectHandler: func(c *gin.Context, reason string) {
			callbackHits++
			c.String(http.StatusTeapot, "should not be called")
		},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.POST("/test", func(c *gin.Context) {
		// 下游 handler 读取 body，触发 MaxBytesReader 超限错误并自行返回 413
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				c.AbortWithStatus(http.StatusRequestEntityTooLarge)
				return
			}
		}
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader("this body is too large for the limit"))
	req.Header.Set("Transfer-Encoding", "chunked")
	req.ContentLength = -1 // 模拟 chunked：绕过 Content-Length 快拒
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	assert.Equal(t, 0, callbackHits, "MaxBytesReader 路径的 413 由下游触发，RejectHandler 不得被调用")
}
