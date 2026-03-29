package compression

import (
	"compress/gzip"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

const (
	BestCompression    = gzip.BestCompression
	BestSpeed          = gzip.BestSpeed
	DefaultCompression = gzip.DefaultCompression
	NoCompression      = gzip.NoCompression
)

type Config struct {
	Skipper          func(*gin.Context) bool
	ExcludedPaths    []string
	ExcludedExts     []string
	MinLength        int
	CompressionLevel int
	EnableZstd       bool
	ZstdLevel        int
	EnableBrotli     bool
	BrotliLevel      int
}

type gzipWriter struct {
	gin.ResponseWriter
	writer          *gzip.Writer
	statusWritten   bool
	status          int
	minLength       int
	shouldCompress  bool
	written         int64
	skipCompression bool
}

var gzipPool = sync.Pool{
	New: func() interface{} {
		return &gzipWriterPooled{}
	},
}

type gzipWriterPooled struct {
	writer *gzip.Writer
}

func (g *gzipWriterPooled) reset(w io.Writer, level int) {
	if g.writer == nil {
		g.writer, _ = gzip.NewWriterLevel(w, level)
	} else {
		g.writer.Reset(w)
	}
}

func New(cfg Config) gin.HandlerFunc {
	minLength := cfg.MinLength
	if minLength <= 0 {
		minLength = 1024
	}

	level := cfg.CompressionLevel
	if level == 0 {
		level = DefaultCompression
	}

	excludedPaths := newExcludedPaths(cfg.ExcludedPaths)
	excludedExts := newExcludedExtensions(cfg.ExcludedExts)

	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		if !shouldCompress(c.Request, excludedPaths, excludedExts) {
			c.Next()
			return
		}

		pooled := gzipPool.Get().(*gzipWriterPooled)
		pooled.reset(c.Writer, level)

		gw := &gzipWriter{
			ResponseWriter: c.Writer,
			writer:         pooled.writer,
			minLength:      minLength,
		}
		c.Writer = gw

		c.Header("Content-Encoding", "gzip")
		c.Writer.Header().Add("Vary", "Accept-Encoding")

		c.Next()

		if etag := c.Writer.Header().Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
			c.Writer.Header().Set("ETag", "W/"+etag)
		}

		if gw.status >= http.StatusBadRequest {
			gw.Header().Del("Content-Encoding")
			gw.Header().Del("Vary")
			pooled.writer.Reset(io.Discard)
		} else if !gw.shouldCompress {
			gw.Header().Del("Content-Encoding")
			gw.Header().Del("Vary")
			pooled.writer.Reset(io.Discard)
		} else if gw.written == 0 {
			pooled.writer.Reset(io.Discard)
		} else if gw.skipCompression {
			gw.Header().Del("Content-Encoding")
			gw.Header().Del("Vary")
			pooled.writer.Reset(io.Discard)
		}

		_ = pooled.writer.Close()
		gzipPool.Put(pooled)
	}
}

func (g *gzipWriter) Write(data []byte) (int, error) {
	if !g.statusWritten {
		g.status = g.ResponseWriter.Status()
	}

	if g.status >= http.StatusBadRequest {
		return g.ResponseWriter.Write(data)
	}

	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		g.skipCompression = true
		g.shouldCompress = false
		return g.ResponseWriter.Write(data)
	}

	g.written += int64(len(data))

	if g.Header().Get("Content-Length") != "" {
		if contentLen, err := parseInt(g.Header().Get("Content-Length")); err == nil {
			if contentLen < g.minLength {
				g.shouldCompress = false
				g.Header().Del("Content-Encoding")
				return g.ResponseWriter.Write(data)
			}
			g.shouldCompress = true
			g.Header().Del("Content-Length")
		}
	}

	if !g.shouldCompress && int64(len(data)) >= int64(g.minLength) {
		g.shouldCompress = true
	} else if !g.shouldCompress {
		g.Header().Del("Content-Encoding")
		return g.ResponseWriter.Write(data)
	}

	n, err := g.writer.Write(data)
	return n, err
}

func (g *gzipWriter) WriteString(s string) (int, error) {
	return g.Write([]byte(s))
}

func (g *gzipWriter) Status() int {
	if g.statusWritten {
		return g.status
	}
	return g.ResponseWriter.Status()
}

func (g *gzipWriter) Size() int {
	return int(g.written)
}

func (g *gzipWriter) Written() bool {
	return g.ResponseWriter.Written()
}

func (g *gzipWriter) WriteHeaderNow() {
	g.ResponseWriter.WriteHeaderNow()
}

func (g *gzipWriter) WriteHeader(code int) {
	g.status = code
	g.statusWritten = true
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Flush() {
	_ = g.writer.Flush()
	g.ResponseWriter.Flush()
}

func shouldCompress(req *http.Request, paths excludedPaths, exts excludedExtensions) bool {
	if !strings.Contains(req.Header.Get("Accept-Encoding"), "gzip") ||
		strings.Contains(req.Header.Get("Connection"), "Upgrade") {
		return false
	}

	if paths.Contains(req.URL.Path) || exts.Contains(filepath.Ext(req.URL.Path)) {
		return false
	}

	return true
}

type excludedPaths []string

func newExcludedPaths(paths []string) excludedPaths {
	return excludedPaths(paths)
}

func (e excludedPaths) Contains(path string) bool {
	for _, p := range e {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

type excludedExtensions map[string]struct{}

func newExcludedExtensions(exts []string) excludedExtensions {
	res := make(excludedExtensions, len(exts))
	for _, e := range exts {
		res[e] = struct{}{}
	}
	return res
}

func (e excludedExtensions) Contains(ext string) bool {
	_, ok := e[ext]
	return ok
}

func parseInt(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, nil
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
