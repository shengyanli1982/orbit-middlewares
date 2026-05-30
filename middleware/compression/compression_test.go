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

func TestCompression_ContentLengthAfterCompression(t *testing.T) {
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
	assert.NotEmpty(t, w.Header().Get("Content-Length"))
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
