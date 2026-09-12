package compression

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestCompression_NoAcceptEncoding(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Header().Get("Content-Encoding"))
}

func TestCompression_WithAcceptEncoding(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
	assert.NotEmpty(t, w.Header().Get("Vary"))

	if w.Header().Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(w.Body)
		assert.NoError(t, err)
		body, err := io.ReadAll(gr)
		assert.NoError(t, err)
		assert.Equal(t, "Hello, World!", string(body))
	}
}

func TestCompression_Skipper(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/skip"
		},
		MinLength: 1,
	}))
	r.GET("/skip", func(c *gin.Context) {
		c.String(http.StatusOK, "Skipped")
	})
	r.GET("/normal", func(c *gin.Context) {
		c.String(http.StatusOK, "Normal")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/skip", nil)
	req1.Header.Set("Accept-Encoding", "gzip")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	assert.Empty(t, w1.Header().Get("Content-Encoding"))

	req2 := httptest.NewRequest(http.MethodGet, "/normal", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	assert.Equal(t, "gzip", w2.Header().Get("Content-Encoding"))
}

func TestCompression_ExcludedPaths(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		ExcludedPaths: []string{"/api/"},
		MinLength:     1,
	}))
	r.GET("/api/test", func(c *gin.Context) {
		c.String(http.StatusOK, "API endpoint")
	})
	r.GET("/normal", func(c *gin.Context) {
		c.String(http.StatusOK, "Normal endpoint")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req1.Header.Set("Accept-Encoding", "gzip")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	assert.Empty(t, w1.Header().Get("Content-Encoding"))

	req2 := httptest.NewRequest(http.MethodGet, "/normal", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	assert.Equal(t, "gzip", w2.Header().Get("Content-Encoding"))
}

func TestCompression_ExcludedExtensions(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		ExcludedExts: []string{".png", ".jpg"},
		MinLength:    1,
	}))
	r.GET("/image.png", func(c *gin.Context) {
		c.Data(http.StatusOK, "image/png", []byte("fake png data"))
	})
	r.GET("/data", func(c *gin.Context) {
		c.String(http.StatusOK, "String data")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/image.png", nil)
	req1.Header.Set("Accept-Encoding", "gzip")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	assert.Empty(t, w1.Header().Get("Content-Encoding"))

	req2 := httptest.NewRequest(http.MethodGet, "/data", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	assert.Equal(t, "gzip", w2.Header().Get("Content-Encoding"))
}

func TestCompression_ErrorResponse(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))
	r.GET("/error", func(c *gin.Context) {
		c.String(http.StatusInternalServerError, "Error occurred")
	})

	req := httptest.NewRequest(http.MethodGet, "/error", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Empty(t, w.Header().Get("Content-Encoding"))
}

func TestCompression_WebSocketUpgrade(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))
	r.GET("/ws", func(c *gin.Context) {
		c.String(http.StatusOK, "WebSocket endpoint")
	})

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Empty(t, w.Header().Get("Content-Encoding"))
}

func TestCompression_MinLength(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 100,
	}))
	r.GET("/short", func(c *gin.Context) {
		c.String(http.StatusOK, "Short")
	})
	r.GET("/long", func(c *gin.Context) {
		c.String(http.StatusOK, "This is a much longer response that should be compressed because it exceeds the minimum length threshold")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/short", nil)
	req1.Header.Set("Accept-Encoding", "gzip")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	assert.Empty(t, w1.Header().Get("Content-Encoding"))

	req2 := httptest.NewRequest(http.MethodGet, "/long", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	assert.Equal(t, "gzip", w2.Header().Get("Content-Encoding"))
}

func TestCompression_ETag(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.Header("ETag", `"abc123"`)
		c.String(http.StatusOK, "Hello")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, `W/"abc123"`, w.Header().Get("ETag"))
}

func TestCompression_AlreadyCompressed(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.Header("Content-Encoding", "gzip")
		gzData := bytes.Repeat([]byte{0x1f, 0x8b}, 10)
		c.Data(http.StatusOK, "application/octet-stream", gzData)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Empty(t, w.Header().Get("Content-Encoding"))
}

func TestCompression_CompressionLevel(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		CompressionLevel: BestSpeed,
		MinLength:        1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
}

// TestCompression_NoMiddlewareContentLengthAfterCompression AUD-14 回归测试（修正假绿语义）：
// 压缩路径不得由中间件设置 Content-Length——真实线路上 finish 的 Close 触发首次写入时
// net/http 已固化响应头（WriteHeader 时克隆 header map），Close 之后再 Set 必然无效。
// 线路分帧由 net/http 决定：<2KB 的压缩输出在 handler 结束时自动补 CL，否则 chunked
// （见 TestCompression_RealHTTP_LargeCompressed_ChunkedNoContentLength）。
// 旧断言 NotEmpty(CL) 仅在 httptest.Recorder 下成立（Recorder.Header() 是活 map），属假绿。
func TestCompression_NoMiddlewareContentLengthAfterCompression(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "This is a test response that should be compressed")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
	assert.Empty(t, w.Header().Get("Content-Length"),
		"middleware must not set Content-Length after compression; framing is net/http's job")
	assert.Equal(t, "This is a test response that should be compressed",
		string(gunzipBytes(t, w.Body.Bytes())))
}

func TestCompression_AcceptEncodingNotGzip(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "deflate, br")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Header().Get("Content-Encoding"))
}

func TestCompression_VaryHeader(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	vary := w.Header().Get("Vary")
	assert.Contains(t, vary, "Accept-Encoding")
}

// TestCompression_RealHTTP_SmallJSON_NoFalseGzipHeader 回归测试：
// 使用真实 HTTP server 验证小 JSON 响应不会携带 Content-Encoding: gzip 但 body 未压缩。
// httptest.NewRecorder 不会 commit headers 到 wire，无法暴露 handlerHeader.Clone() 后无法修改的 bug。
func TestCompression_RealHTTP_SmallJSON_NoFalseGzipHeader(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1024,
	}))
	r.POST("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"errorCode":    0,
			"errorMessage": "success",
			"data":         gin.H{"count": 42},
		})
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	client := &http.Client{
		Transport: &http.Transport{
			DisableCompression: true,
		},
	}

	resp, err := client.Post(srv.URL+"/test", "application/json", bytes.NewReader([]byte("{}")))
	assert.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Content-Encoding"),
		"small JSON response should NOT have Content-Encoding: gzip")

	body, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)
	assert.NotEmpty(t, body)
	assert.Contains(t, string(body), "success")
}

// TestCompression_RealHTTP_LargeJSON_GzipEncoded 验证大响应确实被压缩
func TestCompression_RealHTTP_LargeJSON_GzipEncoded(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 100,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"errorCode":    0,
			"errorMessage": "success",
			"data":         strings.Repeat("x", 2000),
		})
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	client := &http.Client{
		Transport: &http.Transport{
			DisableCompression: true,
		},
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := client.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))

	gr, err := gzip.NewReader(resp.Body)
	assert.NoError(t, err)
	defer gr.Close()
	body, err := io.ReadAll(gr)
	assert.NoError(t, err)
	assert.Contains(t, string(body), "success")
}

// compressOnce 以指定 CompressionLevel 对 payload 压缩一次，返回完整 gzip 字节流。
func compressOnce(t *testing.T, level int, payload []byte) []byte {
	t.Helper()
	r := gin.New()
	r.Use(New(Config{
		CompressionLevel: level,
		MinLength:        1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/plain", payload)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
	return w.Body.Bytes()
}

// gunzipBytes 解压 gzip 字节流并返回原始内容。
func gunzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(data))
	assert.NoError(t, err)
	defer gr.Close()
	out, err := io.ReadAll(gr)
	assert.NoError(t, err)
	return out
}

// stdlibGzip 使用标准库以指定 level 直接压缩 payload，作为字节级对照。
// gzip.Writer.Reset 与新建 writer 输出字节等价（标准库文档承诺，init 全量重建状态）。
func stdlibGzip(t *testing.T, level int, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, level)
	assert.NoError(t, err)
	_, err = gz.Write(payload)
	assert.NoError(t, err)
	assert.NoError(t, gz.Close())
	return buf.Bytes()
}

// mixedPayload 生成确定性的词级重复混合文本，保证不同 deflate 级别产生不同输出。
func mixedPayload(n int) []byte {
	words := []string{
		"kline", "close", "open", "high", "low", "volume", "amount",
		"TdxHq", "price", "data", "0123456789", "abcdefg", "market",
		"security", "transaction", "minute", "tick",
	}
	var buf bytes.Buffer
	x := uint32(12345)
	for buf.Len() < n {
		x = x*1103515245 + 12345 // LCG 确定性伪随机
		buf.WriteString(words[(x>>16)%uint32(len(words))])
		buf.WriteByte(' ')
	}
	return buf.Bytes()[:n]
}

// TestCompression_CompressionLevelActuallyApplied 回归测试（CompressionLevel 死配置修复）：
// 配置的 CompressionLevel 必须实际贯通到 gzip.Writer 构造链。旧实现中
// gzBundlePool.New 硬编码 DefaultCompression，且 gzip.Writer.Reset 保留构造期
// level（z.init(w, z.level)），导致配置值编译通过但运行期永不生效。
func TestCompression_CompressionLevelActuallyApplied(t *testing.T) {
	payload := mixedPayload(4096)

	fast := compressOnce(t, BestSpeed, payload)
	best := compressOnce(t, BestCompression, payload)

	// 两者都必须是合法 gzip，解压还原与原始载荷一致
	assert.Equal(t, payload, gunzipBytes(t, fast))
	assert.Equal(t, payload, gunzipBytes(t, best))

	// level1 输出应大于 level9 —— 压缩级别实际影响产物
	assert.Greater(t, len(fast), len(best),
		"BestSpeed output should be larger than BestCompression when level is actually applied")

	// 字节级对照：中间件输出应与标准库对应级别直接压缩完全一致
	assert.True(t, bytes.Equal(stdlibGzip(t, BestSpeed, payload), fast),
		"middleware output must byte-match stdlib BestSpeed compression, got %d bytes vs %d bytes",
		len(fast), len(stdlibGzip(t, BestSpeed, payload)))
	assert.True(t, bytes.Equal(stdlibGzip(t, BestCompression, payload), best),
		"middleware output must byte-match stdlib BestCompression compression, got %d bytes vs %d bytes",
		len(best), len(stdlibGzip(t, BestCompression, payload)))
}

// TestCompression_ZeroLevelKeepsDefaultBehavior 向后兼容红线：
// CompressionLevel 未配置（零值）时行为必须与现状默认（DefaultCompression）完全一致。
func TestCompression_ZeroLevelKeepsDefaultBehavior(t *testing.T) {
	payload := mixedPayload(4096)

	zero := compressOnce(t, 0, payload)
	explicit := compressOnce(t, DefaultCompression, payload)

	assert.Equal(t, payload, gunzipBytes(t, zero))
	assert.Equal(t, explicit, zero,
		"zero-value CompressionLevel must behave identically to DefaultCompression")
	assert.True(t, bytes.Equal(stdlibGzip(t, DefaultCompression, payload), zero),
		"default output must byte-match stdlib DefaultCompression")
}

// TestCompression_PooledWriterReuseAfterSkip 钉住惰性 Reset 不变式：
// skip 响应不再对池化 gzip.Writer 做 Reset/Close 清理，对象带着残留状态
// （已关闭流或从未写入的全新流）直接回池；下一次取用并决定压缩时，
// startCompress 的惰性 Reset 必须保证产出合法 gzip 流（magic、flate 流、
// CRC32/ISIZE 全部正确），且与标准库全新 writer 直接压缩字节级一致。
func TestCompression_PooledWriterReuseAfterSkip(t *testing.T) {
	payload := mixedPayload(4096) // >= MinLength → 压缩路径
	short := []byte("short")      // < MinLength → skip 缓冲路径

	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/compress", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/plain", payload)
	})
	r.GET("/skip", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/plain", short)
	})

	doReq := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 同一 goroutine 顺序请求，经真实 sync.Pool 复用对象，覆盖残留状态组合：
	// 全新未写入→skip→压缩、已 Close→skip（不清理回池）→压缩、压缩→压缩。
	sequence := []string{
		"/skip",     // 全新 writer 未写入即回池
		"/compress", // 对干净 writer 惰性 Reset
		"/skip",     // 已 Close 状态 writer 不清理直接回池
		"/skip",     // 连续 skip
		"/compress", // 关键新路径：从「已 Close 后又被 skip」的残留状态惰性 Reset
		"/compress", // 压缩紧接压缩（Close→Reset）回归对照
		"/skip",     // 压缩后再 skip
		"/compress", // skip 后再压缩
	}

	std := stdlibGzip(t, DefaultCompression, payload)
	for i, path := range sequence {
		w := doReq(path)
		if path == "/skip" {
			assert.Empty(t, w.Header().Get(headerContentEncoding),
				"request #%d: skip response must not carry Content-Encoding", i)
			assert.Equal(t, short, w.Body.Bytes(),
				"request #%d: skip response body must pass through unchanged", i)
			continue
		}
		assert.Equal(t, gzipEncoding, w.Header().Get(headerContentEncoding),
			"request #%d: compressed response must carry Content-Encoding: gzip", i)
		body := w.Body.Bytes()
		// gzip.NewReader 校验 magic/header，ReadAll 校验 flate 流与 CRC32/ISIZE footer
		assert.Equal(t, payload, gunzipBytes(t, body),
			"request #%d: pooled writer reused after skip must still produce valid gzip", i)
		// 字节级一致：惰性 Reset（init 全量重建状态）输出必须与全新 writer 等价
		assert.True(t, bytes.Equal(std, body),
			"request #%d: lazy-reset output must byte-match stdlib fresh compression (%d vs %d bytes)",
			i, len(std), len(body))
	}
}
