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
	HuffmanOnly        = gzip.HuffmanOnly

	// Deprecated: NoCompression 的数值为 0，与 Config.CompressionLevel 的零值无法区分，
	// 而中间件将 CompressionLevel==0 一律视为 DefaultCompression，因此该级别无法通过
	// Config 选用。若确需 stored（仅打包不压缩）输出，请绕过本中间件，
	// 自行使用 gzip.NewWriterLevel(w, gzip.NoCompression) 处理响应。
	NoCompression = gzip.NoCompression
)

const (
	headerAcceptEncoding  = "Accept-Encoding"
	headerContentEncoding = "Content-Encoding"
	headerVary            = "Vary"
	gzipEncoding          = "gzip"
)

// Config 压缩中间件配置。
type Config struct {
	Skipper       func(*gin.Context) bool
	ExcludedPaths []string
	ExcludedExts  []string
	MinLength     int
	// CompressionLevel 为 gzip 压缩级别，取值 [-2, 9]。
	// 零值（未配置）按 DefaultCompression(-1) 处理，
	// 因此 gzip.NoCompression(0) 无法通过该字段指定（见 NoCompression 的 Deprecated 说明）。
	CompressionLevel int
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		MinLength:        1024,
		CompressionLevel: DefaultCompression,
	}
}

// writerBox 是可重绑定的写入目标转发器。池化的 gzip.Writer 绑定 writerBox
// 而非直接绑定 c.Writer：请求开始时重绑 w，结束时置回 io.Discard，
// 以一次指针写入的代价释放对 c.Writer 的引用，避免池对象滞留请求上下文。
type writerBox struct {
	w io.Writer
}

func (wb *writerBox) Write(p []byte) (int, error) {
	return wb.w.Write(p)
}

// gzipWriter 包装 ResponseWriter，缓冲请求体后决定是否压缩。
// 使用 sync.Pool 复用，避免每次请求堆分配。
type gzipWriter struct {
	gin.ResponseWriter
	writer *gzip.Writer
	// box 是池化 writer 的固定输出目标转发器（每请求重绑 w），
	// 仅保留供 startCompress 的惰性 Reset 使用。
	box    *writerBox
	buf    bytes.Buffer
	status int
	minLen int

	decided     bool
	compressing bool
	// needsReset 为 true 表示 writer 为池化复用对象，可能残留未关闭
	// 或已关闭的流状态，首次压缩写入前必须先 Reset；skip 路径下
	// 整个请求都不会触发 Reset（见 startCompress）。
	needsReset bool
}

var _ http.Hijacker = (*gzipWriter)(nil)

// reset 重置 gzipWriter 的所有状态，准备复用。
// needsReset 为 true 表示 writer 是池化复用对象，须在首次压缩写入前
// 惰性 Reset；为 false 表示 writer 是本请求新构造的，可直接写入。
func (g *gzipWriter) reset(w gin.ResponseWriter, writer *gzip.Writer, box *writerBox, minLen int, needsReset bool) {
	g.ResponseWriter = w
	g.writer = writer
	g.box = box
	g.buf.Reset()
	g.status = w.Status()
	g.minLen = minLen
	g.decided = false
	g.compressing = false
	g.needsReset = needsReset
}

func (g *gzipWriter) WriteString(s string) (int, error) {
	return g.Write([]byte(s))
}

// startCompress 在决定压缩时调用，设置响应头并开始 gzip 输出。
// 此处是进入 gzip 流的唯一入口，负责守住惰性 Reset 不变式：
// 任何对 writer 的 Write/Flush/Close 之前必已 Reset。池化复用的
// writer 在首次压缩写入前才在此清零 flate 流状态；skip 路径
// 全程不经过本函数，因而不付出 Reset 开销。
func (g *gzipWriter) startCompress() {
	if g.needsReset {
		// Reset 使 writer 从任意残留状态（未关闭流/已关闭/出错）
		// 恢复为全新构造状态。box 已在请求开始时重绑当前
		// c.Writer，此处目标不变，仅清零 flate 内部状态。
		g.writer.Reset(g.box)
		g.needsReset = false
	}
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

// skipAndWrite 决定不压缩并按序写入：先把已缓冲字节排空到底层
// ResponseWriter，再写入 data。排空必须发生在首次真实写入之前，
// 保证客户端收到的字节与 handler 写入顺序一致且零丢失。
func (g *gzipWriter) skipAndWrite(data []byte) (int, error) {
	g.skipCompress()
	if g.buf.Len() > 0 {
		_, err := g.ResponseWriter.Write(g.buf.Bytes())
		g.buf.Reset()
		if err != nil {
			return 0, err
		}
	}
	return g.ResponseWriter.Write(data)
}

// compressAndWrite 决定压缩并按序写入：先把已缓冲字节排空到 gzip 流，
// 再写入 data，保证已缓冲数据不被丢弃。
func (g *gzipWriter) compressAndWrite(data []byte) (int, error) {
	g.startCompress()
	if g.buf.Len() > 0 {
		_, err := g.writer.Write(g.buf.Bytes())
		g.buf.Reset()
		if err != nil {
			return 0, err
		}
	}
	return g.writer.Write(data)
}

// Write 实现 io.Writer。策略：
//  1. 已决策：直接走快速路径（压缩或透传）
//  2. 错误响应：不压缩，透传
//  3. 上游已使用非 gzip 编码：透传
//  4. 上游已使用 gzip 编码：直接透传
//  5. 存在 Content-Length：根据长度快速决策，不缓冲
//  6. 其他情况：缓冲数据，达到阈值后标记压缩
//
// 不变式：任一决策切换（2-6）前，必须先把已缓冲字节排空到所选 sink
// （skipAndWrite / compressAndWrite），保证字节零丢失且顺序正确。
func (g *gzipWriter) Write(data []byte) (int, error) {
	if g.decided {
		if g.compressing {
			return g.writer.Write(data)
		}
		return g.ResponseWriter.Write(data)
	}

	g.status = g.ResponseWriter.Status()

	// 错误响应，不压缩；先移除压缩头再排空缓冲，
	// 确保真实线路上头提交时不携带 Content-Encoding
	if g.status >= http.StatusBadRequest {
		g.removeGzipHeaders()
		return g.skipAndWrite(data)
	}

	// 检查上游是否已压缩
	if ce := g.Header().Get(headerContentEncoding); ce != "" && ce != gzipEncoding {
		return g.skipAndWrite(data)
	} else if ce == gzipEncoding {
		// 上游已设置 gzip：若数据确实是 gzip 格式，直接透传
		if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
			return g.skipAndWrite(data)
		}
	}

	// 根据 Content-Length 快速决策
	if clStr := g.Header().Get("Content-Length"); clStr != "" {
		if cl, err := strconv.Atoi(clStr); err == nil {
			if cl < g.minLen {
				return g.skipAndWrite(data)
			}
			g.Header().Del("Content-Length")
			return g.compressAndWrite(data)
		}
	}

	// 通过缓冲决定
	if len(data) >= g.minLen {
		return g.compressAndWrite(data)
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
	g.buf.Reset()
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
//
// 决策前（buf 仍在累积）Flush 会提交底层响应头，此后编码决策无法再安全
// 变更（头已上线路），且流式响应本就无法安全压缩。因此该场景下先移除
// 预设置的压缩头、把已缓冲字节排空直通写出（保持写入顺序、零丢失），
// 并转入永久 skip 决策；已决策的 Flush 行为不变。
func (g *gzipWriter) Flush() {
	if !g.decided {
		g.removeGzipHeaders()
		g.skipCompress()
		if g.buf.Len() > 0 {
			_, _ = g.ResponseWriter.Write(g.buf.Bytes())
			g.buf.Reset()
		}
	}
	if g.compressing {
		_ = g.writer.Flush()
	}
	g.ResponseWriter.Flush()
}

// Hijack 实现 http.Hijacker，用于 WebSocket 等场景。
func (g *gzipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return g.ResponseWriter.Hijack()
}

// removeGzipHeaders 移除中间件预设置的压缩相关头，仅在响应头未提交前调用。
// 不触碰 handler 自设的 ETag：非压缩直通路径下强校验器仍然有效，
// 且 RFC 7232 §4.1 要求 304 等响应携带校验器；ETag 弱化仅发生在实际压缩路径（见 finish）。
func (g *gzipWriter) removeGzipHeaders() {
	g.Header().Del(headerContentEncoding)
	g.Header().Del(headerVary)
}

// finish 完成响应写入，清理资源。由中间件 defer 调用。
func (g *gzipWriter) finish() {
	switch {
	case g.compressing:
		// ETag 弱化（必须在 Close 触发首次真实写入前设置）
		if etag := g.Header().Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
			g.Header().Set("ETag", "W/"+etag)
		}
		// Close 触发首次真实写入后 net/http 即固化响应头，此后任何
		// Set("Content-Length") 都不会上线路：分帧交由 net/http 决定
		// （压缩输出 <2KB 时自动补 CL，否则 chunked），中间件不再设置。
		_ = g.writer.Close()

	default:
		// 不压缩：移除预设置的压缩头（错误响应在 Write 中已移除）。
		// writer 不做 Reset/Close 清理：defer 已将 box.w 置回
		// io.Discard 解除对 c.Writer 的引用；gzip.Writer.Reset 可从
		// 任意状态（含未 Close 的残留流）恢复重用，下次取用压缩时
		// 由 startCompress 按需 Reset。skip 路径由此免除两次 flate
		// 哈希表清零（实测 ~20µs，为 skip 响应的主要开销）。
		g.removeGzipHeaders()
		// 写入缓冲数据（可能为空）；Content-Length 由 net/http 按线路规则处理
		_, _ = g.ResponseWriter.Write(g.buf.Bytes())
	}
}

// gzBundle 用于对象池复用的 gzip writer 和写入目标转发器。
// level 记录 gz 的构造压缩级别：gzip.Writer.Reset 保留构造期 level，
// 因此池中对象被不同 CompressionLevel 配置取用时必须检测并重建。
type gzBundle struct {
	gz    *gzip.Writer
	box   *writerBox
	level int
}

var gzBundlePool = sync.Pool{
	New: func() any {
		box := &writerBox{w: io.Discard}
		gz, _ := gzip.NewWriterLevel(box, DefaultCompression)
		return &gzBundle{gz: gz, box: box, level: DefaultCompression}
	},
}

var gzipWriterPool = sync.Pool{
	New: func() any {
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
		// 0 是 Config.CompressionLevel 的零值，视为 DefaultCompression；
		// gzip.NoCompression 同为 0，无法经此字段选用（见其 Deprecated 说明）。
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

		// 重绑输出目标到当前 ResponseWriter；池对象的构造 level 与配置
		// 不符时重建（Reset 保留构造期 level，无法改级）。
		// 惰性 Reset：此处不再起始 Reset（flate 哈希表清零 ~10µs），
		// 池化复用的 writer 由 startCompress 在首次压缩写入前按需
		// Reset，skip 响应全程零 Reset；新构造 writer 已初始化可直接写入。
		bundle.box.w = c.Writer
		needsReset := true
		if bundle.gz == nil || bundle.level != level {
			bundle.gz, _ = gzip.NewWriterLevel(bundle.box, level)
			bundle.level = level
			needsReset = false
		}

		gw.reset(c.Writer, bundle.gz, bundle.box, minLength, needsReset)
		c.Writer = gw

		// 预先设置 Content-Encoding，若最终不压缩则在 defer 中移除
		c.Header(headerContentEncoding, gzipEncoding)
		c.Writer.Header().Add(headerVary, headerAcceptEncoding)

		defer func() {
			gw.finish()

			// 释放引用，避免持有 c.Writer
			bundle.box.w = io.Discard
			gw.ResponseWriter = nil
			gw.writer = nil
			gw.box = nil
			gzBundlePool.Put(bundle)
			gzipWriterPool.Put(gw)
		}()

		c.Next()
	}
}
