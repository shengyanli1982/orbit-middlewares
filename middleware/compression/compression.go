// Package compression 实现 Gin 的 gzip 压缩中间件。
//
// 设计思路：
//   - Content-Encoding: gzip 在决定压缩时才设置（延迟设置）
//   - 延迟决策：请求体先缓冲，达到 MinLength 后再决定是否压缩
//   - 请求体小于 MinLength 时，不提交响应头，可安全移除提示
//   - 错误响应（4xx/5xx）不压缩
//   - 已压缩的内容直接透传
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

// Config 压缩中间件配置。
type Config struct {
	Skipper          func(*gin.Context) bool
	ExcludedPaths    []string
	ExcludedExts     []string
	MinLength        int
	CompressionLevel int
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		MinLength:        1024,
		CompressionLevel: DefaultCompression,
	}
}

// countingWriter 统计写入字节数，用于更新 Content-Length。
type countingWriter struct {
	w       io.Writer
	written int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.written += int64(n)
	return n, err
}

// gzipWriter 包装 ResponseWriter，缓冲请求体后决定是否压缩。
// 使用 sync.Pool 复用，避免每次请求堆分配。
type gzipWriter struct {
	gin.ResponseWriter
	writer  *gzip.Writer
	counter *countingWriter
	buf     bytes.Buffer
	status  int
	minLen  int

	decided     bool
	compressing bool
}

var _ http.Hijacker = (*gzipWriter)(nil)

// reset 重置 gzipWriter 的所有状态，准备复用。
func (g *gzipWriter) reset(w gin.ResponseWriter, writer *gzip.Writer, cw *countingWriter, minLen int) {
	g.ResponseWriter = w
	g.writer = writer
	g.counter = cw
	g.buf.Reset()
	g.status = w.Status()
	g.minLen = minLen
	g.decided = false
	g.compressing = false
}

func (g *gzipWriter) WriteString(s string) (int, error) {
	return g.Write([]byte(s))
}

// startCompress 在决定压缩时调用，设置响应头并开始 gzip 输出。
func (g *gzipWriter) startCompress() {
	g.decided = true
	g.compressing = true
	g.Header().Set(headerContentEncoding, gzipEncoding)
	g.Header().Add(headerVary, headerAcceptEncoding)
}

// skipCompress 在决定不压缩时调用，标记已完成决策。
func (g *gzipWriter) skipCompress() {
	g.decided = true
	g.compressing = false
}

// Write 实现 io.Writer。策略：
//  1. 已决策：直接走快速路径（压缩或透传）
//  2. 错误响应：不压缩，透传
//  3. 上游已使用非 gzip 编码：透传
//  4. 上游已使用 gzip 编码：直接透传
//  5. 存在 Content-Length：根据长度快速决策，不缓冲
//  6. 其他情况：缓冲数据，达到阈值后标记压缩
func (g *gzipWriter) Write(data []byte) (int, error) {
	if g.decided {
		if g.compressing {
			return g.writer.Write(data)
		}
		return g.ResponseWriter.Write(data)
	}

	if !g.decided {
		g.status = g.ResponseWriter.Status()
	}

	// 错误响应，不压缩
	if g.status >= http.StatusBadRequest {
		g.skipCompress()
		g.removeGzipHeaders()
		return g.ResponseWriter.Write(data)
	}

	// 检查上游是否已压缩
	if ce := g.Header().Get(headerContentEncoding); ce != "" && ce != gzipEncoding {
		g.skipCompress()
		return g.ResponseWriter.Write(data)
	} else if ce == gzipEncoding {
		// 上游已设置 gzip：若数据确实是 gzip 格式，直接透传
		if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
			g.skipCompress()
			return g.ResponseWriter.Write(data)
		}
	}

	// 根据 Content-Length 快速决策
	if clStr := g.Header().Get("Content-Length"); clStr != "" {
		if cl, err := strconv.Atoi(clStr); err == nil {
			if cl < g.minLen {
				g.skipCompress()
				return g.ResponseWriter.Write(data)
			}
			g.Header().Del("Content-Length")
			g.startCompress()
			return g.writer.Write(data)
		}
	}

	// 通过缓冲决定
	if len(data) >= g.minLen {
		g.startCompress()
		return g.writer.Write(data)
	}

	// 缓冲，等待达到阈值
	n, err := g.buf.Write(data)
	if err != nil || g.buf.Len() < g.minLen {
		return n, err
	}
	// 达到阈值，压缩所有缓冲数据
	g.startCompress()
	_, werr := g.writer.Write(g.buf.Bytes())
	if werr != nil {
		return n, werr
	}
	return n, nil
}

// Status 返回当前 HTTP 状态码。
func (g *gzipWriter) Status() int {
	return g.status
}

// Size 返回已写入的字节数。
func (g *gzipWriter) Size() int {
	return g.ResponseWriter.Size()
}

// Written 返回是否已写入响应体。
func (g *gzipWriter) Written() bool {
	return g.ResponseWriter.Written()
}

// WriteHeaderNow 提交响应头。
func (g *gzipWriter) WriteHeaderNow() {
	g.ResponseWriter.WriteHeaderNow()
}

// WriteHeader 记录状态码，不提交响应头。提交由 WriteHeaderNow 完成。
func (g *gzipWriter) WriteHeader(code int) {
	g.status = code
	g.ResponseWriter.WriteHeader(code)
}

// Flush 刷新 gzip writer 和底层 writer。
func (g *gzipWriter) Flush() {
	if g.compressing {
		_ = g.writer.Flush()
	}
	g.ResponseWriter.Flush()
}

// Hijack 实现 http.Hijacker，用于 WebSocket 等场景。
func (g *gzipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return g.ResponseWriter.Hijack()
}

// removeGzipHeaders 移除所有压缩相关头，仅在响应头未提交前调用。
func (g *gzipWriter) removeGzipHeaders() {
	g.Header().Del(headerContentEncoding)
	g.Header().Del(headerVary)
	g.Header().Del("ETag")
}

// finish 完成响应写入，清理资源。由中间件 defer 调用。
func (g *gzipWriter) finish() {
	switch {
	case g.compressing:
		// ETag 弱化
		if etag := g.Header().Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
			g.Header().Set("ETag", "W/"+etag)
		}
		_ = g.writer.Close()
		g.Header().Set("Content-Length", strconv.FormatInt(g.counter.written, 10))

	default:
		// 不压缩：移除预设置的压缩头（错误响应在 Write 中已移除）
		g.removeGzipHeaders()
		// 重置 gzip writer 到 discard，避免 Close 写入空 gzip 流
		g.writer.Reset(io.Discard)
		_ = g.writer.Close()
		// 写入缓冲数据（可能为空）
		_, _ = g.ResponseWriter.Write(g.buf.Bytes())
		if g.ResponseWriter.Size() >= 0 {
			g.Header().Set("Content-Length", strconv.Itoa(g.ResponseWriter.Size()))
		}
	}
}

// gzBundle 用于对象池复用的 gzip writer 和计数器。
type gzBundle struct {
	gz      *gzip.Writer
	counter *countingWriter
}

var gzBundlePool = sync.Pool{
	New: func() interface{} {
		cw := &countingWriter{w: io.Discard}
		gz, _ := gzip.NewWriterLevel(cw, DefaultCompression)
		return &gzBundle{gz: gz, counter: cw}
	},
}

var gzipWriterPool = sync.Pool{
	New: func() interface{} {
		return &gzipWriter{}
	},
}

// excludedPaths 不压缩的路径前缀列表。
type excludedPaths []string

func newExcludedPaths(paths []string) excludedPaths {
	return excludedPaths(paths)
}

// Contains 返回是否匹配任一排除前缀。
func (e excludedPaths) Contains(path string) bool {
	for _, p := range e {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// excludedExtensions 不压缩的文件扩展名集合。
type excludedExtensions map[string]struct{}

func newExcludedExtensions(exts []string) excludedExtensions {
	res := make(excludedExtensions, len(exts))
	for _, e := range exts {
		res[e] = struct{}{}
	}
	return res
}

// Contains 返回是否匹配任一排除扩展名。
func (e excludedExtensions) Contains(ext string) bool {
	_, ok := e[ext]
	return ok
}

// shouldCompress 判断响应是否应该压缩。
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

// New 创建压缩中间件。
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

		bundle := gzBundlePool.Get().(*gzBundle)
		gw := gzipWriterPool.Get().(*gzipWriter)

		// 重置 gzip writer 输出到 ResponseWriter
		bundle.counter.w = c.Writer
		bundle.counter.written = 0
		if bundle.gz == nil {
			bundle.gz, _ = gzip.NewWriterLevel(bundle.counter, level)
		} else {
			bundle.gz.Reset(bundle.counter)
		}

		gw.reset(c.Writer, bundle.gz, bundle.counter, minLength)
		c.Writer = gw

		// 预先设置 Content-Encoding，若最终不压缩则在 defer 中移除
		c.Header(headerContentEncoding, gzipEncoding)
		c.Writer.Header().Add(headerVary, headerAcceptEncoding)

		defer func() {
			gw.finish()

			// 释放引用，避免持有 c.Writer
			bundle.counter.w = io.Discard
			gw.ResponseWriter = nil
			gw.writer = nil
			gw.counter = nil
			gzBundlePool.Put(bundle)
			gzipWriterPool.Put(gw)
		}()

		c.Next()
	}
}
