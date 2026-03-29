package compression

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func BenchmarkCompression_FullMiddleware(b *testing.B) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

func BenchmarkCompression_LargeResponse(b *testing.B) {
	r := gin.New()
	r.Use(New(Config{
		MinLength:        1,
		CompressionLevel: DefaultCompression,
	}))

	largeData := bytes.Repeat([]byte("A"), 64*1024)

	r.GET("/test", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/plain", largeData)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

func BenchmarkCompression_MultipleChunks(b *testing.B) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))

	r.GET("/test", func(c *gin.Context) {
		for i := 0; i < 100; i++ {
			c.Writer.Write([]byte("chunk data\n"))
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

func BenchmarkCompression_SkipNoAcceptEncoding(b *testing.B) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

func BenchmarkCompression_SkipExcludedPath(b *testing.B) {
	r := gin.New()
	r.Use(New(Config{
		ExcludedPaths: []string{"/api/"},
		MinLength:     1,
	}))
	r.GET("/api/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

func BenchmarkCompression_SkipMinLength(b *testing.B) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1024,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "Short")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

func BenchmarkCompression_AlreadyCompressed(b *testing.B) {
	r := gin.New()
	r.Use(New(Config{
		MinLength: 1,
	}))
	r.GET("/test", func(c *gin.Context) {
		c.Header("Content-Encoding", "gzip")
		gzData := bytes.Repeat([]byte{0x1f, 0x8b}, 100)
		c.Data(http.StatusOK, "application/octet-stream", gzData)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

func BenchmarkCompression_GzipWriterReuse(b *testing.B) {
	gz := gzip.NewWriter(io.Discard)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		gz.Reset(io.Discard)
		gz.Write([]byte("Hello, World!"))
		gz.Close()
	}
}

func BenchmarkCompression_HeaderLookup(b *testing.B) {
	h := http.Header{}
	h.Set("Accept-Encoding", "gzip")
	h.Set("Content-Length", "100")

	path := "/test"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = strings.Contains(h.Get("Accept-Encoding"), "gzip")
		_ = strings.Contains(h.Get("Connection"), "Upgrade")
		_ = filepath.Ext(path)
	}
}

func BenchmarkCompression_SyncPoolGetPut(b *testing.B) {
	var pool sync.Pool
	pool.New = func() interface{} {
		gz, _ := gzip.NewWriterLevel(io.Discard, DefaultCompression)
		return &gzipWriter{writer: gz}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		g := pool.Get().(*gzipWriter)
		g.writer.Reset(io.Discard)
		pool.Put(g)
	}
}
