package compression

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

type Algorithm string

const (
	AlgorithmGzip   Algorithm = "gzip"
	AlgorithmZstd   Algorithm = "zstd"
	AlgorithmBrotli Algorithm = "brotli"
)

const (
	BestCompression    = 9
	BestSpeed          = 1
	DefaultCompression = 5
	NoCompression      = 0
)

type Config struct {
	Skipper          func(*gin.Context) bool
	ExcludedPaths    []string
	ExcludedExts     []string
	MinLength        int
	Algorithm        Algorithm
	CompressionLevel int
}

type CompressorWriter interface {
	Write([]byte) (int, error)
	Close() error
	Reset(io.Writer)
	Flush() error
	CompressedSize() int64
}

type responseWriter struct {
	gin.ResponseWriter
	compressor     CompressorWriter
	statusWritten  bool
	status         int
	minLength      int
	shouldCompress bool
	written        int64
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

	algorithm := cfg.Algorithm
	if algorithm == "" {
		algorithm = AlgorithmGzip
	}

	excludedPaths := newExcludedPaths(cfg.ExcludedPaths)
	excludedExts := newExcludedExtensions(cfg.ExcludedExts)

	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		encoding := negotiateEncoding(c.Request, algorithm)
		if encoding == "" {
			c.Next()
			return
		}

		if excludedPaths.Contains(c.Request.URL.Path) || excludedExts.Contains(filepath.Ext(c.Request.URL.Path)) {
			c.Next()
			return
		}

		var compressor CompressorWriter
		switch encoding {
		case "zstd":
			compressor = getZstdWriter(level)
		case "br":
			compressor = getBrotliWriter(level)
		default:
			compressor = getGzipWriter(level)
		}
		compressor.Reset(c.Writer)

		rw := &responseWriter{
			ResponseWriter: c.Writer,
			compressor:     compressor,
			minLength:      minLength,
		}
		c.Writer = rw

		c.Header("Content-Encoding", encoding)
		c.Writer.Header().Add("Vary", "Accept-Encoding")

		c.Next()

		if etag := c.Writer.Header().Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
			c.Writer.Header().Set("ETag", "W/"+etag)
		}

		finalSize := rw.compressor.CompressedSize()

		switch {
		case rw.status >= http.StatusBadRequest:
			c.Writer.Header().Del("Content-Encoding")
			c.Writer.Header().Del("Vary")
			rw.compressor.Reset(io.Discard)
		case !rw.shouldCompress:
			c.Writer.Header().Del("Content-Encoding")
			c.Writer.Header().Del("Vary")
			rw.compressor.Reset(io.Discard)
		case rw.written == 0:
			rw.compressor.Reset(io.Discard)
		}

		_ = rw.compressor.Close()
		putCompressor(encoding, compressor)

		if rw.shouldCompress && finalSize > 0 {
			c.Writer.Header().Set("Content-Length", formatInt(int(finalSize)))
		}
	}
}

func (rw *responseWriter) Write(data []byte) (int, error) {
	if !rw.statusWritten {
		rw.status = rw.ResponseWriter.Status()
	}

	if rw.status >= http.StatusBadRequest {
		return rw.ResponseWriter.Write(data)
	}

	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		rw.shouldCompress = false
		return rw.ResponseWriter.Write(data)
	}

	rw.written += int64(len(data))

	if rw.Header().Get("Content-Length") != "" {
		if contentLen, err := parseInt(rw.Header().Get("Content-Length")); err == nil {
			if contentLen < rw.minLength {
				rw.shouldCompress = false
				rw.Header().Del("Content-Encoding")
				return rw.ResponseWriter.Write(data)
			}
			rw.shouldCompress = true
			rw.Header().Del("Content-Length")
		}
	}

	if !rw.shouldCompress && int64(len(data)) >= int64(rw.minLength) {
		rw.shouldCompress = true
	} else if !rw.shouldCompress {
		rw.Header().Del("Content-Encoding")
		return rw.ResponseWriter.Write(data)
	}

	n, err := rw.compressor.Write(data)
	return n, err
}

func (rw *responseWriter) WriteString(s string) (int, error) {
	return rw.Write([]byte(s))
}

func (rw *responseWriter) Status() int {
	if rw.statusWritten {
		return rw.status
	}
	return rw.ResponseWriter.Status()
}

func (rw *responseWriter) Size() int {
	return int(rw.written)
}

func (rw *responseWriter) Written() bool {
	return rw.ResponseWriter.Written()
}

func (rw *responseWriter) WriteHeaderNow() {
	rw.ResponseWriter.WriteHeaderNow()
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.statusWritten = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Flush() {
	rw.compressor.Flush()
	rw.ResponseWriter.Flush()
}

func negotiateEncoding(req *http.Request, preferred Algorithm) string {
	acceptEncoding := req.Header.Get("Accept-Encoding")
	if acceptEncoding == "" {
		return ""
	}

	if strings.Contains(req.Header.Get("Connection"), "Upgrade") {
		return ""
	}

	supported := parseAcceptEncoding(acceptEncoding)

	switch preferred {
	case AlgorithmZstd:
		if q, ok := supported["zstd"]; ok && q > 0 {
			return "zstd"
		}
		return ""
	case AlgorithmBrotli:
		if q, ok := supported["br"]; ok && q > 0 {
			return "br"
		}
		return ""
	default:
		if q, ok := supported["gzip"]; ok && q > 0 {
			return "gzip"
		}
		if q, ok := supported["br"]; ok && q > 0 {
			return "br"
		}
		if q, ok := supported["zstd"]; ok && q > 0 {
			return "zstd"
		}
		return ""
	}
}

func parseAcceptEncoding(header string) map[string]float64 {
	result := make(map[string]float64)
	parts := strings.Split(header, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		encoding := part
		qvalue := 1.0
		if idx := strings.Index(part, ";"); idx != -1 {
			encoding = strings.TrimSpace(part[:idx])
			qpart := strings.TrimSpace(part[idx+1:])
			if strings.HasPrefix(qpart, "q=") {
				if q, err := parseFloat(strings.TrimPrefix(qpart, "q=")); err == nil {
					qvalue = q
				}
			}
		}
		result[encoding] = qvalue
	}
	return result
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

func parseFloat(s string) (float64, error) {
	var n float64
	var decimal float64 = 1
	seenDot := false
	negative := false
	for _, c := range s {
		if c == '-' {
			negative = true
			continue
		}
		if c == '.' {
			seenDot = true
			continue
		}
		if c < '0' || c > '9' {
			continue
		}
		if seenDot {
			decimal /= 10
			n += float64(c-'0') * decimal
		} else {
			n = n*10 + float64(c-'0')
		}
	}
	if negative {
		n = -n
	}
	return n, nil
}

func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	var result []byte
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		result = append([]byte{byte('0' + n%10)}, result...)
		n /= 10
	}
	if negative {
		result = append([]byte{'-'}, result...)
	}
	return string(result)
}

var (
	gzipPool   sync.Pool
	zstdPool   sync.Pool
	brotliPool sync.Pool
)

func getGzipWriter(level int) *gzipWriter {
	gz := gzipPool.Get().(*gzipWriter)
	gz.ResetLevel(level)
	return gz
}

func putGzipWriter(gz *gzipWriter) {
	gz.Reset(io.Discard)
	gzipPool.Put(gz)
}

func getZstdWriter(level int) *zstdWriter {
	z := zstdPool.Get().(*zstdWriter)
	z.ResetLevel(level)
	return z
}

func putZstdWriter(z *zstdWriter) {
	z.Reset(io.Discard)
	zstdPool.Put(z)
}

func getBrotliWriter(level int) *brotliWriter {
	b := brotliPool.Get().(*brotliWriter)
	b.ResetLevel(level)
	return b
}

func putBrotliWriter(b *brotliWriter) {
	b.Reset(io.Discard)
	brotliPool.Put(b)
}

func putCompressor(encoding string, c CompressorWriter) {
	switch encoding {
	case "zstd":
		if z, ok := c.(*zstdWriter); ok {
			putZstdWriter(z)
		}
	case "br":
		if b, ok := c.(*brotliWriter); ok {
			putBrotliWriter(b)
		}
	default:
		if g, ok := c.(*gzipWriter); ok {
			putGzipWriter(g)
		}
	}
}
