package compression

import (
	"compress/gzip"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
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
}

type gzipWriter struct {
	gin.ResponseWriter
	writer            *gzip.Writer
	statusWritten     bool
	status            int
	minLength         int
	shouldCompress    bool
	written           int64
	contentLenChecked bool
}

var gzipPool = sync.Pool{
	New: func() interface{} {
		gz, _ := gzip.NewWriterLevel(io.Discard, DefaultCompression)
		return &gzipWriter{writer: gz}
	},
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

		gz := gzipPool.Get().(*gzipWriter)
		gz.ResponseWriter = c.Writer
		gz.writer.Reset(c.Writer)
		if level != DefaultCompression {
			gz.writer, _ = gzip.NewWriterLevel(c.Writer, level)
		}
		gz.minLength = minLength
		gz.written = 0
		gz.shouldCompress = false
		gz.statusWritten = false
		gz.status = http.StatusOK
		gz.contentLenChecked = false

		c.Writer = gz

		c.Header("Content-Encoding", "gzip")
		c.Writer.Header().Add("Vary", "Accept-Encoding")

		c.Next()

		if etag := c.Writer.Header().Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
			c.Writer.Header().Set("ETag", "W/"+etag)
		}

		switch {
		case gz.status >= http.StatusBadRequest:
			c.Writer.Header().Del("Content-Encoding")
			c.Writer.Header().Del("Vary")
		case !gz.shouldCompress:
			c.Writer.Header().Del("Content-Encoding")
			c.Writer.Header().Del("Vary")
		case gz.written > 0:
			c.Writer.Header().Set("Content-Length", strconv.Itoa(int(gz.written)))
		}

		_ = gz.writer.Close()
		gz.writer.Reset(io.Discard)
		gz.ResponseWriter = nil
		gzipPool.Put(gz)
	}
}

func shouldCompress(req *http.Request, paths excludedPaths, exts excludedExtensions) bool {
	if !strings.Contains(req.Header.Get("Accept-Encoding"), "gzip") {
		return false
	}

	if strings.Contains(req.Header.Get("Connection"), "Upgrade") {
		return false
	}

	if paths.Contains(req.URL.Path) || exts.Contains(filepath.Ext(req.URL.Path)) {
		return false
	}

	return true
}

func (g *gzipWriter) Write(data []byte) (int, error) {
	if !g.statusWritten {
		g.status = g.ResponseWriter.Status()
	}

	if g.status >= http.StatusBadRequest {
		return g.ResponseWriter.Write(data)
	}

	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		g.shouldCompress = false
		return g.ResponseWriter.Write(data)
	}

	g.written += int64(len(data))

	if !g.contentLenChecked {
		g.contentLenChecked = true
		if contentLen := g.Header().Get("Content-Length"); contentLen != "" {
			if n, err := strconv.Atoi(contentLen); err == nil {
				if n < g.minLength {
					g.shouldCompress = false
					g.Header().Del("Content-Encoding")
					return g.ResponseWriter.Write(data)
				}
				g.shouldCompress = true
				g.Header().Del("Content-Length")
			}
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
