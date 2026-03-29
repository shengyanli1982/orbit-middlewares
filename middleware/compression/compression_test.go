package compression

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestCompression_ZstdAlgorithm(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		Algorithm: AlgorithmZstd,
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, zstd")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "zstd", w.Header().Get("Content-Encoding"))
	assert.NotEmpty(t, w.Header().Get("Vary"))
}

func TestCompression_BrotliAlgorithm(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		Algorithm: AlgorithmBrotli,
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "br", w.Header().Get("Content-Encoding"))
	assert.NotEmpty(t, w.Header().Get("Vary"))
}

func TestCompression_AcceptEncodingQValues(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		Algorithm: AlgorithmBrotli,
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip;q=0.5, br;q=1.0, zstd;q=0.8")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "br", w.Header().Get("Content-Encoding"))
}

func TestCompression_AcceptEncodingGzipPrefersHigherQ(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		Algorithm: AlgorithmGzip,
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip;q=0.5, br;q=1.0")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
}

func TestCompression_ContentLengthAfterCompression(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		Algorithm: AlgorithmGzip,
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

func TestCompression_NoCompressionWhenNoMatch(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{
		Algorithm: AlgorithmZstd,
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
	assert.Empty(t, w.Header().Get("Content-Encoding"))
}
