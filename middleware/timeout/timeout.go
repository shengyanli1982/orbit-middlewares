package timeout

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type contextKey struct{}

type ctxKey string

// Config 超时中间件配置。
type Config struct {
	Skipper func(*gin.Context) bool
	Timeout time.Duration
	// Engine 是当前 gin.Engine 实例，用于在子 goroutine 中创建全新的 gin.Context。
	Engine *gin.Engine
}

// bufferWriter 缓冲响应写入器，隔离子 goroutine 与真实 writer。
type bufferWriter struct {
	mu      sync.Mutex
	header  http.Header
	buf     bytes.Buffer
	code    int
	written bool
}

func newBufferWriter() *bufferWriter {
	return &bufferWriter{
		header: make(http.Header),
		code:   http.StatusOK,
	}
}

func (bw *bufferWriter) Header() http.Header { return bw.header }

func (bw *bufferWriter) WriteHeader(code int) {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	if !bw.written {
		bw.code = code
		bw.written = true
	}
}

func (bw *bufferWriter) Write(b []byte) (int, error) {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	bw.written = true
	return bw.buf.Write(b)
}

// flushTo 将缓冲响应刷写到真实 writer。
func (bw *bufferWriter) flushTo(w http.ResponseWriter) {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	for k, vv := range bw.header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(bw.code)
	if bw.buf.Len() > 0 {
		w.Write(bw.buf.Bytes()) //nolint:errcheck
	}
}

// New 创建超时中间件。
//
// 原理：子 goroutine 通过 Engine.ServeHTTP 处理请求，创建独立的 gin.Context 和
// bufferWriter，与主 goroutine 完全隔离。正常完成时刷写缓冲响应，超时时返回 504。
func New(cfg Config) gin.HandlerFunc {
	if cfg.Engine == nil {
		panic("timeout.New: cfg.Engine must not be nil")
	}

	return func(c *gin.Context) {
		// 已在超时子请求中，防递归
		if c.Request.Context().Value(contextKey{}) != nil {
			c.Next()
			return
		}

		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), cfg.Timeout)
		defer cancel()

		// 设置标记，防止子请求递归
		ctxWithMark := context.WithValue(ctx, contextKey{}, true)
		ctxWithKeys := ctxWithMark
		for k, v := range c.Keys {
			ctxWithKeys = context.WithValue(ctxWithKeys, ctxKey(k), v)
		}
		reqWithCtx := c.Request.WithContext(ctxWithKeys)

		// 子 goroutine 使用独立的 bufferWriter，与主 goroutine 隔离
		bw := newBufferWriter()
		finishChan := make(chan struct{}, 1)

		go func() {
			defer func() { finishChan <- struct{}{} }()
			// 通过 engine.ServeHTTP 创建全新的 gin.Context
			cfg.Engine.ServeHTTP(bw, reqWithCtx)
		}()

		select {
		case <-finishChan:
			// 正常完成，刷写缓冲响应
			bw.flushTo(c.Writer)
			c.Abort()
		case <-ctx.Done():
			// 超时，返回 504
			c.AbortWithStatusJSON(http.StatusGatewayTimeout, gin.H{
				"message": "[504] request timeout",
			})
			// 等待子 goroutine 退出，防止泄漏
			<-finishChan
		}
	}
}
