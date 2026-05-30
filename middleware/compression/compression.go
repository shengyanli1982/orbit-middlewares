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

const maxBufCap = 65536

type Config struct {
	Skipper          func(*gin.Context) bool
	ExcludedPaths    []string
	ExcludedExts     []string
	MinLength        int
	CompressionLevel int
}

type countingWriter struct {
	w       io.Writer
	written int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.written += int64(n)
	return n, err
}

type gzipWriter struct {
	gin.ResponseWriter
	writer          *gzip.Writer
	counter         *countingWriter
	level           int
	statusWritten   bool
	status          int
	minLength       int
	shouldCompress  bool
	written         int64
	headerCommitted bool
	buf             []byte
	gzClosed        bool
}

var gzipPool = sync.Pool{
	New: func() interface{} {
		gz, _ := gzip.NewWriterLevel(io.Discard, DefaultCompression)
		return &gzipWriter{
			writer:  gz,
			counter: &countingWriter{w: io.Discard},
			level:   DefaultCompression,
		}
	},
}

func DefaultConfig() Config {
	return Config{
		MinLength:        1024,
		CompressionLevel: DefaultCompression,
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
	if level < -2 || level > 9 {
		panic("compression: CompressionLevel must be between -2 and 9")
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

		gz.counter.w = c.Writer
		gz.counter.written = 0

		if gz.level != level {
			_ = gz.writer.Close()
			var err error
			gz.writer, err = gzip.NewWriterLevel(gz.counter, level)
			if err != nil {
				gz.writer, _ = gzip.NewWriterLevel(gz.counter, DefaultCompression)
				gz.level = DefaultCompression
			} else {
				gz.level = level
			}
		} else {
			gz.writer.Reset(gz.counter)
		}

		gz.minLength = minLength
		gz.written = 0
		gz.shouldCompress = false
		gz.statusWritten = false
		gz.status = http.StatusOK
		gz.headerCommitted = false
		gz.gzClosed = false
		gz.buf = gz.buf[:0]
		if cap(gz.buf) < minLength {
			gz.buf = make([]byte, 0, minLength)
		}

		c.Writer = gz

		c.Writer.Header().Add("Vary", "Accept-Encoding")

		c.Next()

		gz.flush()

		c.Writer = gz.ResponseWriter

		// ETag weakening is now handled inside commitHeaders() before header commit

		switch {
		case gz.status >= http.StatusBadRequest:
			if !gz.headerCommitted {
				c.Writer.Header().Del("Content-Encoding")
				c.Writer.Header().Del("Content-Length")
				removeVaryValue(c.Writer.Header(), "Accept-Encoding")
			}
		case !gz.shouldCompress:
			c.Writer.Header().Del("Content-Encoding")
			c.Writer.Header().Del("Content-Length")
			removeVaryValue(c.Writer.Header(), "Accept-Encoding")
			if len(gz.buf) > 0 {
				_, _ = gz.ResponseWriter.Write(gz.buf)
			}
		case gz.counter.written > 0:
			c.Writer.Header().Set("Content-Length", strconv.FormatInt(gz.counter.written, 10))
		}

		gz.writer.Reset(io.Discard)
		gz.counter.w = io.Discard
		gz.ResponseWriter = nil
		if cap(gz.buf) > maxBufCap {
			gz.buf = nil
		}
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

func (g *gzipWriter) commitHeaders() {
	if etag := g.ResponseWriter.Header().Get("ETag"); etag != "" {
		if !strings.HasPrefix(etag, "W/") {
			g.ResponseWriter.Header().Set("ETag", "W/"+etag)
		}
	}
	g.ResponseWriter.WriteHeader(g.status)
	g.ResponseWriter.WriteHeaderNow()
}

func (g *gzipWriter) Write(data []byte) (int, error) {
	if !g.statusWritten {
		g.status = g.ResponseWriter.Status()
	}

	if g.status >= http.StatusBadRequest {
		if !g.headerCommitted && len(g.buf) > 0 {
			g.commitHeaders()
			g.headerCommitted = true
			g.ResponseWriter.Write(g.buf)
			g.buf = g.buf[:0]
		}
		g.headerCommitted = true
		return g.ResponseWriter.Write(data)
	}

	dataLen := len(data)
	g.written += int64(dataLen)

	if !g.headerCommitted {
		g.buf = append(g.buf, data...)

		if len(g.buf) >= g.minLength {
			if g.ResponseWriter.Header().Get("Content-Encoding") != "" {
				g.shouldCompress = false
				g.commitHeaders()
				g.headerCommitted = true
				_, err := g.ResponseWriter.Write(g.buf)
				g.buf = g.buf[:0]
				if err != nil {
					return 0, err
				}
				return dataLen, nil
			}

			if len(g.buf) >= 2 && g.buf[0] == 0x1f && g.buf[1] == 0x8b {
				g.shouldCompress = false
				g.commitHeaders()
				g.headerCommitted = true
				_, err := g.ResponseWriter.Write(g.buf)
				g.buf = g.buf[:0]
				if err != nil {
					return 0, err
				}
				return dataLen, nil
			}

			g.shouldCompress = true
			g.Header().Set("Content-Encoding", "gzip")
			g.Header().Del("Content-Length")
			g.commitHeaders()
			g.headerCommitted = true

			if _, err := g.writer.Write(g.buf); err != nil {
				return 0, err
			}
			g.buf = g.buf[:0]
			return dataLen, nil
		}

		return dataLen, nil
	}

	if !g.shouldCompress {
		return g.ResponseWriter.Write(data)
	}
	return g.writer.Write(data)
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
}

func (g *gzipWriter) WriteHeader(code int) {
	g.status = code
	g.statusWritten = true
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) flush() {
	if !g.headerCommitted {
		g.headerCommitted = true

		if g.status >= http.StatusBadRequest {
			g.commitHeaders()
			if len(g.buf) > 0 {
				_, _ = g.ResponseWriter.Write(g.buf)
				g.buf = g.buf[:0]
			}
			return
		}

		g.commitHeaders()
		if len(g.buf) > 0 {
			if g.ResponseWriter.Header().Get("Content-Encoding") == "" {
				if len(g.buf) >= 2 && g.buf[0] == 0x1f && g.buf[1] == 0x8b {
					g.ResponseWriter.Header().Set("Content-Encoding", "gzip")
				}
			}
			_, _ = g.ResponseWriter.Write(g.buf)
			g.buf = g.buf[:0]
		}
		return
	}

	if g.shouldCompress && !g.gzClosed {
		_ = g.writer.Close()
		g.gzClosed = true
	}
}

func (g *gzipWriter) Flush() {
	if g.headerCommitted && g.shouldCompress {
		_ = g.writer.Flush()
	}
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

func removeVaryValue(h http.Header, value string) {
	vals := h.Values("Vary")
	remaining := vals[:0]
	for _, v := range vals {
		if !strings.EqualFold(v, value) {
			remaining = append(remaining, v)
		}
	}
	if len(remaining) > 0 {
		h["Vary"] = remaining
	} else {
		h.Del("Vary")
	}
}
