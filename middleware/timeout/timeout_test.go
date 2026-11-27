package timeout

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestTimeout_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: 100 * time.Millisecond,
		Engine:  router,
	}
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "ok", recorder.Body.String())
}

func TestTimeout_Exceeded(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: 50 * time.Millisecond,
		Engine:  router,
	}
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		time.Sleep(200 * time.Millisecond)
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusGatewayTimeout, recorder.Code)
	// 超时响应体遵循项目统一文本约定 "[code] msg"
	assert.Equal(t, "[504] request timeout", recorder.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"))
}

func TestTimeout_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: 50 * time.Millisecond,
		Engine:  router,
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/skip"
		},
	}
	router.Use(New(cfg))
	router.GET("/skip", func(c *gin.Context) {
		time.Sleep(100 * time.Millisecond)
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
	assert.Equal(t, http.StatusOK, recorder2.Code)
}

// TestTimeout_NoConcurrentWrite 验证超时后不会并发写 ResponseWriter（-race 检测）。
func TestTimeout_NoConcurrentWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: 30 * time.Millisecond,
		Engine:  router,
	}
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		time.Sleep(100 * time.Millisecond)
		c.String(http.StatusOK, "ok")
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			_ = recorder.Code
		}()
	}
	wg.Wait()
}

// Timeout 零值/负值会使 context.WithTimeout 立即过期，全部请求 504。
// 无效配置必须在构造期 panic（与 cfg.Engine 校验及项目"无效配置构造时 panic"约定一致）。
func TestTimeout_InvalidTimeoutPanics(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()

	assert.Panics(t, func() {
		New(Config{Engine: router})
	}, "Timeout 零值应在构造时 panic")

	assert.Panics(t, func() {
		New(Config{Engine: router, Timeout: -time.Second})
	}, "Timeout 负值应在构造时 panic")

	// 合法配置不得被误伤
	assert.NotPanics(t, func() {
		New(Config{Engine: router, Timeout: time.Second})
	}, "正数 Timeout 配置不应 panic")
}

// handler panic（gin.New() 无 Recovery 场景）会穿透子 goroutine 内的
// Engine.ServeHTTP 逃逸，导致整个进程崩溃（原生 net/http 为 per-connection 隔离）。
// 中间件在子 goroutine 内 recover，经 bufferWriter 返回 500，
// 进程存活，后续请求不受影响。
func TestTimeout_PanicHandlerRecovered(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: time.Second,
		Engine:  router,
	}
	router.Use(New(cfg))
	router.GET("/panic", func(c *gin.Context) {
		panic("boom")
	})
	router.GET("/ok", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Equal(t, "[500] internal server error", recorder.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"))

	// 进程存活 + 后续请求正常
	req2 := httptest.NewRequest(http.MethodGet, "/ok", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusOK, recorder2.Code)
	assert.Equal(t, "ok", recorder2.Body.String())
}

// flushTo 曾用 Add 合并缓冲头。当外层真实 writer 已预置同名头
// （典型场景：timeout 之前的中间件直写真实 writer，随后又被 Engine 重放
// 写入缓冲 writer），Add 会使最终响应携带该头的多个值（如两个不同值的
// X-Request-Id）。flushTo 以 Set 语义合并：内层（缓冲）值覆盖
// 外层预置值，响应头保持单值。
func TestTimeout_FlushToOverwritesPresetHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: time.Second,
		Engine:  router,
	}
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		// 内层 handler 写缓冲 writer
		c.Writer.Header().Set("X-Request-Id", "inner-id")
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	// 外层在真实 writer 上预置同名头（模拟 timeout 之前的中间件直写真实 writer）
	recorder.Header().Set("X-Request-Id", "outer-id")
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, []string{"inner-id"}, recorder.Header().Values("X-Request-Id"),
		"同名头应为单值，且内层缓冲值覆盖外层预置值")
}

// 同头合并的补充分支：缓冲内同名头的多值（如多条 Set-Cookie）必须完整保留且整体
// 替换外层预置值——Set 语义只针对"外层预置 vs 内层缓冲"的覆盖关系，
// 不得把内层多值压缩为单值。
func TestTimeout_FlushToPreservesMultiValueHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: time.Second,
		Engine:  router,
	}
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.Writer.Header().Add("Set-Cookie", "a=1")
		c.Writer.Header().Add("Set-Cookie", "b=2")
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	recorder.Header().Add("Set-Cookie", "stale=0")
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, []string{"a=1", "b=2"}, recorder.Header().Values("Set-Cookie"),
		"内层多值头应完整保留，并整体替换外层预置值")
}

// 并发回归：recover 分支写 bufferWriter 与外层 flushTo 之间仅靠
// finishChan 建立 happens-before，用 -race 钉住无数据竞争、无死锁。
func TestTimeout_PanicHandlerConcurrent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: time.Second,
		Engine:  router,
	}
	router.Use(New(cfg))
	router.GET("/panic", func(c *gin.Context) {
		panic("boom")
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/panic", nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			assert.Equal(t, http.StatusInternalServerError, recorder.Code)
			assert.Equal(t, "[500] internal server error", recorder.Body.String())
		}()
	}
	wg.Wait()
}

// 池化重用回归：连续两个请求，第一个写入自定义头/多值头/非 200 状态码/响应体
// 后，第二个请求不得看到第一个的任何残留（header、body、code、written 标记）。
// 同时钉住所有权转移语义：归还池的清理不得写穿已刷写给第一个 recorder 的
// slice 值（flushTo 的 dst[k]=vv 零拷贝整体替换）。
func TestTimeout_PoolReuseNoResidue(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: time.Second,
		Engine:  router,
	}
	router.Use(New(cfg))
	router.GET("/first", func(c *gin.Context) {
		c.Writer.Header().Set("X-First", "leak-me")
		c.Writer.Header().Add("Set-Cookie", "session=abc")
		c.String(http.StatusCreated, "first-body")
	})
	router.GET("/second", func(c *gin.Context) {
		c.String(http.StatusOK, "second-body")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/first", nil)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	assert.Equal(t, http.StatusCreated, rec1.Code)
	assert.Equal(t, "first-body", rec1.Body.String())
	assert.Equal(t, "leak-me", rec1.Header().Get("X-First"))

	req2 := httptest.NewRequest(http.MethodGet, "/second", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusOK, rec2.Code, "状态码必须复位为默认 200，不得残留 201")
	assert.Equal(t, "second-body", rec2.Body.String(), "响应体不得残留第一个请求的内容")
	assert.Empty(t, rec2.Header().Get("X-First"), "单值头不得残留在池化对象中")
	assert.Empty(t, rec2.Header().Values("Set-Cookie"), "多值头不得残留在池化对象中")

	// 所有权已转移给 rec1 的 slice 值不得被池化清理破坏
	assert.Equal(t, []string{"leak-me"}, rec1.Header().Values("X-First"),
		"归还池的 clear 不得触碰已转移所有权的 slice 值")
	assert.Equal(t, []string{"session=abc"}, rec1.Header().Values("Set-Cookie"))
}

// 超限缓冲不归还池：写入超过 bufferWriterMaxCap 的响应后归还，
// 同 goroutine（同 P）下一次取出的对象 buf 容量不得超过上限。
func TestPutBufferWriterOversizedNotPooled(t *testing.T) {
	bw := getBufferWriter()
	_, _ = bw.Write(make([]byte, bufferWriterMaxCap+1))
	putBufferWriter(bw) // 超限，应丢弃不归还

	next := getBufferWriter()
	assert.LessOrEqual(t, next.buf.Cap(), bufferWriterMaxCap,
		"超限缓冲不得归还池，防止大响应内存滞留")
	putBufferWriter(next)
}

// TestTimeout_RejectHandler_CustomResponse 钉住统一拒绝契约：
// 504 超时路径上自定义 RejectHandler 生效（状态码+响应体），
// 且回调收到 ReasonTimeout。
// nil RejectHandler 的默认响应由既有 TestTimeout_Exceeded 精确钉住。
func TestTimeout_RejectHandler_CustomResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotReason string
	router := gin.New()
	cfg := Config{
		Timeout: 50 * time.Millisecond,
		Engine:  router,
		RejectHandler: func(c *gin.Context, reason string) {
			gotReason = reason
			c.JSON(http.StatusTeapot, gin.H{"reason": reason})
		},
	}
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		time.Sleep(150 * time.Millisecond)
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusTeapot, recorder.Code, "回调写入的状态码应生效")
	assert.JSONEq(t, `{"reason":"request.timeout"}`, recorder.Body.String(), "回调写入的响应体应生效")
	assert.Equal(t, ReasonTimeout, gotReason, "回调应收到 ReasonTimeout")
}

// TestTimeout_RejectHandler_AbortEnforced 钉住统一拒绝契约：
// 回调内不调用 Abort，框架仍无条件 Abort，外层链的后续中间件不得执行。
// 子 goroutine 重放链带有 contextKey 标记，据此区分外层与重放的执行计数。
func TestTimeout_RejectHandler_AbortEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)

	outerHits := 0
	router := gin.New()
	cfg := Config{
		Timeout: 50 * time.Millisecond,
		Engine:  router,
		RejectHandler: func(c *gin.Context, reason string) {
			// 故意不调用 c.Abort()
			c.String(http.StatusGatewayTimeout, "rejected by callback: "+reason)
		},
	}
	router.Use(New(cfg))
	router.Use(func(c *gin.Context) {
		if c.Request.Context().Value(contextKey{}) == nil {
			outerHits++
		}
		c.Next()
	})
	router.GET("/test", func(c *gin.Context) {
		time.Sleep(150 * time.Millisecond)
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusGatewayTimeout, recorder.Code)
	assert.Equal(t, "rejected by callback: request.timeout", recorder.Body.String())
	assert.Equal(t, 0, outerHits, "超时拒绝后框架必须 Abort，外层后续中间件不得执行")
}

// TestTimeout_RejectHandler_PanicPathNotAffected 钉住覆盖范围例外：
// handler panic→500 恢复路径经子 goroutine 缓冲 writer，保持默认响应，
// RejectHandler 不得被触发（仅 504 超时路径走回调）。
func TestTimeout_RejectHandler_PanicPathNotAffected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	callbackHits := 0
	router := gin.New()
	cfg := Config{
		Timeout: time.Second,
		Engine:  router,
		RejectHandler: func(c *gin.Context, reason string) {
			callbackHits++
			c.String(http.StatusTeapot, "should not be called: "+reason)
		},
	}
	router.Use(New(cfg))
	router.GET("/panic", func(c *gin.Context) {
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Equal(t, "[500] internal server error", recorder.Body.String(),
		"panic 恢复路径应保持默认响应")
	assert.Equal(t, 0, callbackHits, "panic→500 路径不得触发 RejectHandler")
}

// gin v1.10.1 的 responseWriter.Flush 硬断言底层 writer 实现
// http.Flusher（response_writer.go：w.ResponseWriter.(http.Flusher).Flush()），
// bufferWriter 缺失 Flush 方法时，SSE/streaming handler 调 c.Writer.Flush()
// 即 interface conversion panic——被 recover 接住后走脏恢复路径，
// 产出 200 + "data: first\n\n[500] internal server error"。
// Flush 为 no-op 后：不 panic、完整 body 经 flushTo 一次性落地、无
// [500] 尾巴。
func TestTimeout_SSEFlushNoPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: time.Second,
		Engine:  router,
	}
	router.Use(New(cfg))
	router.GET("/sse", func(c *gin.Context) {
		// 典型 SSE 形态：手动设头 + Write + Flush 逐事件推送
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = c.Writer.Write([]byte("data: first\n\n"))
		c.Writer.Flush() // bufferWriter 未实现 Flush 时此处会 interface conversion panic
		_, _ = c.Writer.Write([]byte("data: second\n\n"))
		c.Writer.Flush()
	})

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "data: first\n\ndata: second\n\n", recorder.Body.String(),
		"完整 SSE body 应落地，不得被恢复路径截断或污染")
	assert.NotContains(t, recorder.Body.String(), "[500]", "不得出现 [500] 尾巴")
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"),
		"handler 设置的 SSE 头应经 flushTo 刷出到真实 writer")
}

// handler 先写部分 body 再 panic 时，若 recover 分支仅追加错误文案，
// WriteHeader(500) 会被 written 守卫挡下（状态停留 200），"[500]..." 追加在
// 半成品 body 后，产出撕裂响应——200 + "partial...[500] internal
// server error"。正确行为是整体重写：Reset 丢弃半成品、强制 500 + 默认文案，
// 无 partial 残留。既有 TestTimeout_PanicHandlerRecovered（panic-before-write
// 干净路径）行为不变。
func TestTimeout_PanicAfterPartialWriteRewritesBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	cfg := Config{
		Timeout: time.Second,
		Engine:  router,
	}
	router.Use(New(cfg))
	router.GET("/panic-partial", func(c *gin.Context) {
		c.String(http.StatusOK, "partial body before boom")
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic-partial", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusInternalServerError, recorder.Code,
		"半成品后 panic 必须整体重写为 500，不得残留 handler 已写的 200")
	assert.Equal(t, "[500] internal server error", recorder.Body.String(),
		"body 必须为干净的整体重写，无 partial 残留、无追加尾巴")
	assert.Equal(t, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"),
		"Content-Type 应被恢复路径统一覆盖")
}
