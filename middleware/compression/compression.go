// Package compression 实现 Gin 的 gzip 压缩中间件。
//
// 设计思路：
//   - Content-Encoding: gzip 在 c.Next() 前预先设置，作为提示
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

// Write 实现 io.Writer。策略：
//  1. 错误响应：不压缩，透传并移除 gzip 头
//  2. 上游已使用非 gzip 编码：透传，移除 gzip 头
//  3. 上游已使用 gzip 编码：直接透传
//  4. 存在 Content-Length：根据长度快速决策，不缓冲
//  5. 其他情况：缓冲数据，达到阈值后标记压缩
func (g *gzipWriter) Write(data []byte) (int, error) {
	if !g.statusWritten {
		g.status = g.ResponseWriter.Status()
	}

	// 错误响应，不压缩
	if g.status >= http.StatusBadRequest {
		g.removeGzipHeaders()
		return g.ResponseWriter.Write(data)
	}

	// 检查上游是否已压缩
	if ce := g.Header().Get(headerContentEncoding); ce != "" && ce != gzipEncoding {
	// 其他编码（如 br、deflate）：透传，移除 gzip 头
		g.removeGzipHeaders()
		return g.ResponseWriter.Write(data)
	} else if ce == gzipEncoding {
		// 上游已设置 gzip：若数据确实是 gzip 格式，直接透传
		if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
			return g.ResponseWriter.Write(data)
		}
		// 已设置 gzip 但数据不匹配，继续尝试压缩
	}

	// 根据 Content-Length 快速决策
	if clStr := g.Header().Get("Content-Length"); clStr != "" {
		if cl, err := strconv.Atoi(clStr); err == nil {
			if cl < g.minLength {
				return g.ResponseWriter.Write(data)
			}
			g.shouldCompress = true
			g.Header().Del("Content-Length")
		}
	}

	// 通过缓冲决定
	if !g.shouldCompress {
		if len(data) >= g.minLength {
			// 单次写入超阈值，开始压缩
			g.shouldCompress = true
		} else {
			// 缓冲，等待达到阈值
			n, err := g.buf.Write(data)
			if err != nil || g.buf.Len() < g.minLength {
				return n, err
			}
			// 达到阈值，压缩所有缓冲数据
			g.shouldCompress = true
			data = g.buf.Bytes()
		}
	}

	// 提交点：即将写入压缩数据，提前弱化 ETag
	if etag := g.Header().Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		g.Header().Set("ETag", "W/"+etag)
	}

	return g.writer.Write(data)
}

// Status 返回当前 HTTP 状态码。
func (g *gzipWriter) Status() int {
	if g.statusWritten {
		return g.status
	}
	return g.ResponseWriter.Status()
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
	g.statusWritten = true
	g.ResponseWriter.WriteHeader(code) // Only updates underlying status, doesn't commit
}

// Flush 刷新 gzip writer 和底层 writer。
func (g *gzipWriter) Flush() {
	if g.shouldCompress {
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

// gzBundle 用于对象池复用的 gzip writer 和计数器。
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

		bundle := gzipPool.Get().(*gzBundle)

		// 重置 gzip writer 输出到 ResponseWriter
		bundle.counter.w = c.Writer
		bundle.counter.written = 0
		if bundle.gz == nil {
			bundle.gz, _ = gzip.NewWriterLevel(bundle.counter, level)
		} else {
			bundle.gz.Reset(bundle.counter)
		}

		// 预先设置 Content-Encoding，若最终不压缩则在 defer 中移除
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
				// 错误响应：移除压缩头
				gw.removeGzipHeaders()
				bundle.gz.Reset(io.Discard)

			case !gw.shouldCompress:
				// 不压缩：移除提示头，写入缓冲数据
				gw.Header().Del(headerContentEncoding)
				gw.Header().Del(headerVary)
				gw.Header().Del("ETag")
				_, _ = gw.ResponseWriter.Write(gw.buf.Bytes())
				bundle.gz.Reset(io.Discard)

			default:
				// 压缩成功，ETag 已在 Write 中弱化
			}

			// 关闭 gzip writer，写入尾部（若未使用则无操作）
			_ = bundle.gz.Close()

			// 根据实际写入字节数设置 Content-Length
			if gw.shouldCompress {
				c.Header("Content-Length", strconv.FormatInt(bundle.counter.written, 10))
			} else if gw.ResponseWriter.Size() >= 0 {
				c.Header("Content-Length", strconv.Itoa(gw.ResponseWriter.Size()))
			}

			// 释放引用，避免持有 c.Writer
			bundle.counter.w = io.Discard
			gzipPool.Put(bundle)
		}()

		c.Next()
	}
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
