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

// countingWriter 包装 io.Writer，统计实际写入字节数（即压缩后大小）。
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
	writer            *gzip.Writer
	counter           *countingWriter // 统计压缩后实际写入字节数
	level             int
	statusWritten     bool
	status            int
	minLength         int
	shouldCompress    bool
	written           int64 // 原始数据大小（用于 minLength 判断）
	contentLenChecked bool
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

// DefaultConfig 返回合理的默认配置。
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
	// 校验 CompressionLevel 范围：gzip 支持 -2 (HuffmanOnly) 到 9 (BestCompression)
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

		// 重置 counter，指向底层 ResponseWriter
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
		gz.contentLenChecked = false

		c.Writer = gz

		c.Header("Content-Encoding", "gzip")
		c.Writer.Header().Add("Vary", "Accept-Encoding")

		c.Next()

		if etag := c.Writer.Header().Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
			c.Writer.Header().Set("ETag", "W/"+etag)
		}

		// Close gzip writer，将剩余缓冲数据刷入底层 writer
		_ = gz.writer.Close()

		switch {
		case gz.status >= http.StatusBadRequest:
			c.Writer.Header().Del("Content-Encoding")
			removeVaryValue(c.Writer.Header(), "Accept-Encoding")
		case !gz.shouldCompress:
			c.Writer.Header().Del("Content-Encoding")
			removeVaryValue(c.Writer.Header(), "Accept-Encoding")
		case gz.counter.written > 0:
			// 使用压缩后实际写入字节数设置 Content-Length
			c.Writer.Header().Set("Content-Length", strconv.FormatInt(gz.counter.written, 10))
		}

		// 重置 writer 到 Discard，避免持有对 ResponseWriter 的引用
		gz.writer.Reset(io.Discard)
		gz.counter.w = io.Discard
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
	// 直接走压缩路径的快速判断，避免 string→[]byte 转换
	if !g.statusWritten {
		g.status = g.ResponseWriter.Status()
	}
	if g.status >= http.StatusBadRequest {
		return g.ResponseWriter.WriteString(s)
	}
	// 已确认需要压缩，直接写入 gzip writer，避免 []byte 转换 alloc。
	// 同步更新 g.written，保证 Size() 返回值与 Write 路径一致。
	if g.shouldCompress {
		g.written += int64(len(s))
		return io.WriteString(g.writer, s)
	}
	// 尚未决策，回退到通用 Write 路径（含 minLength 判断）
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
