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
	// Engine 是当前 gin.Engine 实例。
	// 中间件在子 goroutine 中通过 Engine.ServeHTTP 创建全新的 gin.Context，
	// 彻底隔离并发访问（gin.Context 不是并发安全的）。
	Engine *gin.Engine
}

// bufferWriter 缓冲 ResponseWriter，供子 goroutine 独立写入，
// 与主 goroutine 的真实 writer 完全隔离，消除并发写 data race。
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

// flushTo 将缓冲响应刷写到真实 writer（仅正常完成路径调用，单 goroutine）。
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

// New 超时控制中间件。
//
// 实现原理（彻底消除 gin.Context 并发 data race）：
//   - gin.Context 不是并发安全的，任何在 goroutine 里调用 c.Next() 的方案都有 data race。
//   - 本方案：子 goroutine 通过 cfg.Engine.ServeHTTP(bufferWriter, request) 处理请求，
//     engine 会创建全新的 gin.Context，完全不共享原始 c，彻底隔离并发访问。
//   - 子 goroutine 写入独立的 bufferWriter，主 goroutine 写入真实 writer，两者完全隔离。
//   - 通过 context key 标记"已在超时子请求中"，防止超时中间件在子请求里再次起 goroutine（递归）。
//   - 正常完成时，主 goroutine 将 bufferWriter 内容刷写到真实 writer。
//   - 超时时，主 goroutine 直接向真实 writer 写 504，子 goroutine 的写入被丢弃。
//   - finishChan 等待子 goroutine 退出，防止 goroutine 泄漏。
//
// 注意：cfg.Engine 必须是当前请求所属的 gin.Engine 实例。
func New(cfg Config) gin.HandlerFunc {
	if cfg.Engine == nil {
		panic("timeout.New: cfg.Engine must not be nil")
	}

	return func(c *gin.Context) {
		// 检测是否已在超时子请求中（防递归）
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

		// 在 context 中设置标记，防止子请求再次进入超时逻辑
		ctxWithMark := context.WithValue(ctx, contextKey{}, true)
		ctxWithKeys := ctxWithMark
		for k, v := range c.Keys {
			ctxWithKeys = context.WithValue(ctxWithKeys, ctxKey(k), v)
		}
		reqWithCtx := c.Request.WithContext(ctxWithKeys)

		// 子 goroutine 写入独立的 bufferWriter，与主 goroutine 完全隔离
		bw := newBufferWriter()
		finishChan := make(chan struct{}, 1)

		go func() {
			defer func() { finishChan <- struct{}{} }()
			// 通过 engine.ServeHTTP 创建全新的 gin.Context，完全不碰原始 c
			cfg.Engine.ServeHTTP(bw, reqWithCtx)
		}()

		select {
		case <-finishChan:
			// 正常完成：将缓冲响应刷写到真实 writer
			bw.flushTo(c.Writer)
			c.Abort()
		case <-ctx.Done():
			// 超时：直接向真实 writer 写 504，子 goroutine 的写入被丢弃
			c.AbortWithStatusJSON(http.StatusGatewayTimeout, gin.H{
				"message": "[504] request timeout",
			})
			// 等待子 goroutine 退出，防止泄漏
			<-finishChan
		}
	}
}
