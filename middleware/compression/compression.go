// Package compression implements gzip compression middleware for Gin.
//
// Design (inspired by gin-contrib/gzip):
//   - Content-Encoding: gzip is set EAGERLY before c.Next() as a HINT to the client.
//   - The compression decision is LAZY: body data is buffered until MinLength is reached.
//   - If body < MinLength, headers are never committed, so we can safely strip the hint.
//   - Error responses (4xx/5xx) bypass compression entirely (no double-gzip risk).
//   - Already-compressed responses (e.g. handler set Content-Encoding: gzip) are passed through.
package compression

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"net"
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
	HuffmanOnly        = gzip.HuffmanOnly
)

const (
	headerAcceptEncoding  = "Accept-Encoding"
	headerContentEncoding = "Content-Encoding"
	headerVary            = "Vary"
	gzipEncoding          = "gzip"
)

// Config holds middleware configuration.
type Config struct {
	Skipper          func(*gin.Context) bool
	ExcludedPaths    []string
	ExcludedExts     []string
	MinLength        int
	CompressionLevel int
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		MinLength:        1024,
		CompressionLevel: DefaultCompression,
	}
}

// countingWriter wraps an io.Writer and tracks the number of bytes written through it.
// Used to measure compressed output size (for accurate Content-Length).
type countingWriter struct {
	w       io.Writer
	written int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.written += int64(n)
	return n, err
}

// gzipWriter wraps gin.ResponseWriter and buffers body data before deciding to compress.
type gzipWriter struct {
	gin.ResponseWriter
	writer        *gzip.Writer
	counter       *countingWriter
	statusWritten bool
	status        int
	minLength     int
	shouldCompress bool
	buf           bytes.Buffer
}

var _ http.Hijacker = (*gzipWriter)(nil)

func (g *gzipWriter) WriteString(s string) (int, error) {
	return g.Write([]byte(s))
}

// Write implements io.Writer. The strategy:
//  1. Status >= 400 (error response): pass through uncompressed, remove gzip headers.
//  2. Already-compressed upstream (Content-Encoding != gzip): pass through, remove gzip headers.
//  3. Already-compressed upstream (Content-Encoding == gzip with gzip magic): pass through.
//  4. Content-Length header present: use it as a fast-path decision (no buffering).
//  5. Otherwise: buffer data. Once buf >= minWidth, mark shouldCompress=true and flush buf+zlib.
func (g *gzipWriter) Write(data []byte) (int, error) {
	if !g.statusWritten {
		g.status = g.ResponseWriter.Status()
	}

	// Error responses: don't compress, pass through
	if g.status >= http.StatusBadRequest {
		g.removeGzipHeaders()
		return g.ResponseWriter.Write(data)
	}

	// Check if response is already compressed by upstream
	if ce := g.Header().Get(headerContentEncoding); ce != "" && ce != gzipEncoding {
		// Different encoding (e.g. br, deflate): pass through, remove our gzip headers
		g.removeGzipHeaders()
		return g.ResponseWriter.Write(data)
	} else if ce == gzipEncoding {
		// Handler already set gzip: if the data is actual gzip, pass through AS-IS
		// (don't remove the handler's CE: gzip, just skip double-compression)
		if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
			return g.ResponseWriter.Write(data)
		}
		// Data doesn't look like gzip despite CE: gzip - fall through to try compressing
	}

	// Use Content-Length as fast-path decision (no need to buffer when we know the size)
	if clStr := g.Header().Get("Content-Length"); clStr != "" {
		if cl, err := strconv.Atoi(clStr); err == nil {
			if cl < g.minLength {
				return g.ResponseWriter.Write(data)
			}
			g.shouldCompress = true
			g.Header().Del("Content-Length")
		}
	}

	// Decide via buffering
	if !g.shouldCompress {
		if len(data) >= g.minLength {
			// Single write exceeds threshold: compress
			g.shouldCompress = true
		} else {
			// Buffer and wait for threshold
			n, err := g.buf.Write(data)
			if err != nil || g.buf.Len() < g.minLength {
				return n, err
			}
			// Threshold reached: compress everything buffered
			g.shouldCompress = true
			data = g.buf.Bytes()
		}
	}

	// COMMIT POINT: we are about to write compressed data. The gzip.Writer's Write
	// will flow through to the underlying gin.ResponseWriter.Write, which internally
	// calls WriteHeaderNow() and commits headers. Weaken ETag before this happens
	// so the header map contains the weak version when committed.
	if etag := g.Header().Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		g.Header().Set("ETag", "W/"+etag)
	}

	return g.writer.Write(data)
}

// Status returns the current HTTP status code (explicitly set or underlying).
func (g *gzipWriter) Status() int {
	if g.statusWritten {
		return g.status
	}
	return g.ResponseWriter.Status()
}

// Size returns bytes written to the underlying writer (uncompressed size).
func (g *gzipWriter) Size() int {
	return g.ResponseWriter.Size()
}

// Written returns true if the response body was already written.
func (g *gzipWriter) Written() bool {
	return g.ResponseWriter.Written()
}

// WriteHeaderNow delegates to the underlying writer.
// Note: This commits headers to the HTTP connection. If body is buffered but not
// yet compressed, the caller should be aware the headers include Content-Encoding
// (which can still be modified until the HTTP flush after handler returns).
func (g *gzipWriter) WriteHeaderNow() {
	g.ResponseWriter.WriteHeaderNow()
}

// WriteHeader records the status code and updates the underlying writer's status,
// but does NOT commit headers. This allows ETag weakening and other header mutations
// to occur in deferred cleanup after the handler finishes (gin-contrib/gzip pattern).
// In gin, WriteHeader only updates an internal status field; actual header commitment
// happens later in WriteHeaderNow() which is called from Write().
func (g *gzipWriter) WriteHeader(code int) {
	g.status = code
	g.statusWritten = true
	g.ResponseWriter.WriteHeader(code) // Only updates underlying status, doesn't commit
}

// Flush flushes the gzip writer (if compressing) then the underlying writer.
func (g *gzipWriter) Flush() {
	if g.shouldCompress {
		_ = g.writer.Flush()
	}
	g.ResponseWriter.Flush()
}

// Hijack implements http.Hijacker by delegating.
func (g *gzipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return g.ResponseWriter.Hijack()
}

// removeGzipHeaders removes all compression-related headers.
// Safe to call only before headers are committed (before first Write to underlying writer).
func (g *gzipWriter) removeGzipHeaders() {
	g.Header().Del(headerContentEncoding)
	g.Header().Del(headerVary)
	g.Header().Del("ETag")
}

// gzipPool manages a pool of gzip.Writer + countingWriter pairs.
type gzBundle struct {
	gz      *gzip.Writer
	counter *countingWriter
}

var gzipPool = sync.Pool{
	New: func() interface{} {
		cw := &countingWriter{w: io.Discard}
		gz, _ := gzip.NewWriterLevel(cw, DefaultCompression)
		return &gzBundle{gz: gz, counter: cw}
	},
}

// New creates the compression middleware handler.
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

		bundle := gzipPool.Get().(*gzBundle)

		// Reset gzip writer to output to the ResponseWriter (via countingWriter)
		bundle.counter.w = c.Writer
		bundle.counter.written = 0
		if bundle.gz == nil {
			bundle.gz, _ = gzip.NewWriterLevel(bundle.counter, level)
		} else {
			bundle.gz.Reset(bundle.counter)
		}

		// EAGER: set Content-Encoding as a HINT. If body ends up small or status
		// is >= 400, we'll strip these headers in deferred cleanup (which runs
		// BEFORE Go's http server flushes headers to wire).
		c.Header(headerContentEncoding, gzipEncoding)
		c.Writer.Header().Add(headerVary, headerAcceptEncoding)

		gw := &gzipWriter{
			ResponseWriter: c.Writer,
			writer:         bundle.gz,
			counter:        bundle.counter,
			minLength:      minLength,
			status:         c.Writer.Status(),
		}
		c.Writer = gw

		defer func() {
			switch {
			case gw.status >= http.StatusBadRequest:
				// Error response: strip compression headers, reset gzip
				gw.removeGzipHeaders()
				bundle.gz.Reset(io.Discard)

			case !gw.shouldCompress:
				// Body was too small to compress. Since gw.Write only buffered (never
				// wrote to the underlying writer), headers have NOT been committed.
				// Safe to strip the Content-Encoding hint.
				gw.Header().Del(headerContentEncoding)
				gw.Header().Del(headerVary)
				gw.Header().Del("ETag")
				_, _ = gw.ResponseWriter.Write(gw.buf.Bytes())
				bundle.gz.Reset(io.Discard)

			default:
				// Compressed successfully. ETag weakening was already done in
				// gzipWriter.Write before the first compressing write.
				// Will Close() below to write gzip footer.
			}

			// Close the gzip writer (writes footer if used, or no-ops if Reset'd)
			_ = bundle.gz.Close()

			// Set Content-Length based on actual bytes written
			if gw.shouldCompress {
				c.Header("Content-Length", strconv.FormatInt(bundle.counter.written, 10))
			} else if gw.ResponseWriter.Size() >= 0 {
				c.Header("Content-Length", strconv.Itoa(gw.ResponseWriter.Size()))
			}

			// Release countingWriter's reference to c.Writer (avoid keeping ResponseWriter alive)
			bundle.counter.w = io.Discard
			gzipPool.Put(bundle)
		}()

		c.Next()
	}
}

// shouldCompress decides whether a response should be compressed based on the request.
func shouldCompress(req *http.Request, paths excludedPaths, exts excludedExtensions) bool {
	if !strings.Contains(req.Header.Get(headerAcceptEncoding), gzipEncoding) {
		return false
	}
	if strings.Contains(req.Header.Get("Connection"), "Upgrade") {
		return false
	}
	if paths.Contains(req.URL.Path) {
		return false
	}
	if exts.Contains(filepath.Ext(req.URL.Path)) {
		return false
	}
	return true
}

// excludedPaths is a list of path prefixes to skip compression for.
type excludedPaths []string

func newExcludedPaths(paths []string) excludedPaths {
	return excludedPaths(paths)
}

// Contains returns true if the given path matches any excluded prefix.
func (e excludedPaths) Contains(path string) bool {
	for _, p := range e {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// excludedExtensions is a set of file extensions (e.g. ".png") to skip compression for.
type excludedExtensions map[string]struct{}

func newExcludedExtensions(exts []string) excludedExtensions {
	res := make(excludedExtensions, len(exts))
	for _, e := range exts {
		res[e] = struct{}{}
	}
	return res
}

// Contains returns true if the given extension is in the exclusion set.
func (e excludedExtensions) Contains(ext string) bool {
	_, ok := e[ext]
	return ok
}
