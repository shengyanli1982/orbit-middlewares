package timeout

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type contextKey struct{}

// ReasonTimeout 请求超时拒绝的原因常量，作为 RejectHandler 的 reason 参数。
// 值遵循全局 "<domain>.<cause>" 拒绝原因命名方案（domain=request 自证拒绝来源，
// cause 为 snake_case 状态描述），规则详见 README。
const ReasonTimeout = "request.timeout"

// Config 超时中间件配置。
type Config struct {
	Skipper func(*gin.Context) bool
	Timeout time.Duration
	// Engine 是当前 gin.Engine 实例，用于在子 goroutine 中创建全新的 gin.Context。
	Engine *gin.Engine
	// RejectHandler 自定义拒绝时的响应逻辑（可选）。
	//
	// 契约：reason 为本包导出的拒绝原因常量；回调负责写入状态码与响应体；
	// 回调返回后框架无条件调用 c.Abort() 终止后续处理器，无论回调内部是否
	// 已自行 Abort。为 nil 时返回默认响应（c.String 文本格式 "[code] msg"）。
	//
	// 覆盖范围：仅处理 504 超时路径（外层 handler，真实 writer）；handler
	// panic→500 的恢复路径经子 goroutine 缓冲 writer 写入，保持默认响应，
	// 不触发本回调。
	RejectHandler func(c *gin.Context, reason string)
}

// bufferWriter 缓冲响应写入器，隔离子 goroutine 与真实 writer。
type bufferWriter struct {
	mu      sync.Mutex
	header  http.Header
	buf     bytes.Buffer
	code    int
	written bool
}

// bufferWriterMaxCap 是 bufferWriter 归还池时的 buf 容量上限（64KB）。
// 超限缓冲直接丢弃不归还，防止单次大响应导致内存被池长期滞留。
const bufferWriterMaxCap = 64 << 10

// bufferWriterPool 复用 bufferWriter，消除每请求的结构体与 header map 分配，
// 并保留 buf 已分配容量，避免首次 Write 的 grow 分配。
var bufferWriterPool = sync.Pool{
	New: func() any {
		return &bufferWriter{
			header: make(http.Header),
			code:   http.StatusOK,
		}
	},
}

// getBufferWriter 从池中取出处于初始状态的 bufferWriter（miss 时由 New 创建）。
// 归还前已由 putBufferWriter 复位，与 New 产出状态一致，取出后无需再清理。
func getBufferWriter() *bufferWriter {
	return bufferWriterPool.Get().(*bufferWriter)
}

// putBufferWriter 复位 bufferWriter 并归还池。
//
// 调用前提（由 finishChan 同步协议保证）：子 goroutine 已退出，bw 不再被
// 任何方引用读写，因此复位无需持锁；mu 也不会停留在加锁状态（所有加锁
// 路径均 defer Unlock，panic 时同样释放）。
//
// 清理语义：
//   - header 用 clear 仅删 map 条目，不触碰 slice 值——flushTo 以 dst[k]=vv
//     零拷贝整体替换，slice 所有权已转移给真实 writer，清零内容会写穿
//     已提交的响应头；
//   - buf.Reset 保留已分配容量供下次请求复用；
//   - 容量超限不归还（丢弃交 GC 回收），防大响应内存滞留。
func putBufferWriter(bw *bufferWriter) {
	if bw.buf.Cap() > bufferWriterMaxCap {
		return
	}
	clear(bw.header)
	bw.buf.Reset()
	bw.code = http.StatusOK
	bw.written = false
	bufferWriterPool.Put(bw)
}

func (bw *bufferWriter) Header() http.Header { return bw.header }

func (bw *bufferWriter) WriteHeader(code int) {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	if !bw.written {
		bw.code = code
		bw.written = true
	}
}

func (bw *bufferWriter) Write(b []byte) (int, error) {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	bw.written = true
	return bw.buf.Write(b)
}

// flushTo 将缓冲响应刷写到真实 writer。
//
// AUD-04：头合并按 key 整体替换（等价于首值 Set + 余值 Add）：外层真实
// writer 可能已预置同名头（如 timeout 之前的中间件直写真实 writer 后又被
// Engine 重放），逐值 Add 会使最终响应携带多个值（如两个 X-Request-Id）；
// 整体替换令内层（缓冲）值成为唯一权威，并天然保留缓冲内多值（如多条
// Set-Cookie）。
//
// 零拷贝安全性：flushTo 仅在收到 finishChan 后调用（子 goroutine 已退出，
// happens-before 已建立），bw 此后不再被任何方读写，切片所有权随之转移给
// 真实 writer，不存在别名写穿路径。整体替换在生产形态（每请求全新
// writer，见 MemAllocation 基准）下零额外分配，优于逐值 Add（31 vs 32
// allocs/op）。
//
// 与池化的交互：bw 归还池时 putBufferWriter 仅删除 header 的 map 条目，
// 不触碰（更不清零）已转移所有权的 slice 值；buf 数据在 w.Write 中已被
// 下游（bufio/net/http）拷贝，Reset 后复用底层数组不会写穿已提交响应。
func (bw *bufferWriter) flushTo(w http.ResponseWriter) {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	dst := w.Header()
	for k, vv := range bw.header {
		dst[k] = vv
	}
	w.WriteHeader(bw.code)
	if bw.buf.Len() > 0 {
		w.Write(bw.buf.Bytes()) //nolint:errcheck
	}
}

// New 创建超时中间件。
//
// 原理：子 goroutine 通过 Engine.ServeHTTP 处理请求，创建独立的 gin.Context 和
// bufferWriter，与主 goroutine 完全隔离。正常完成时刷写缓冲响应，超时时返回 504。
//
// 注册位置：必须注册在所有中间件的首位。Engine.ServeHTTP 会重放完整中间件链
// （递归守卫只防 timeout 自身），注册在其之前的中间件每请求会执行两次
// （副作用翻倍、响应头重复）。
func New(cfg Config) gin.HandlerFunc {
	if cfg.Engine == nil {
		panic("timeout: cfg.Engine must not be nil")
	}
	if cfg.Timeout <= 0 {
		panic("timeout: cfg.Timeout must be > 0")
	}

	// reject 构造期捕获 cfg.RejectHandler，仅在 504 超时拒绝路径调用，
	// 正常完成路径零新增开销（统一拒绝契约）。
	reject := func(c *gin.Context) {
		if cfg.RejectHandler != nil {
			cfg.RejectHandler(c, ReasonTimeout)
		} else {
			c.String(http.StatusGatewayTimeout, "[504] request timeout")
		}
		c.Abort()
	}

	return func(c *gin.Context) {
		// 已在超时子请求中，防递归
		if c.Request.Context().Value(contextKey{}) != nil {
			c.Next()
			return
		}

		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), cfg.Timeout)
		defer cancel()

		// 设置标记，防止子请求递归
		ctxWithMark := context.WithValue(ctx, contextKey{}, true)
		reqWithCtx := c.Request.WithContext(ctxWithMark)

		// 子 goroutine 使用独立的 bufferWriter，与主 goroutine 隔离（对象池复用）
		bw := getBufferWriter()
		finishChan := make(chan struct{}, 1)

		go func() {
			defer func() { finishChan <- struct{}{} }()
			// AUD-03：gin.New() 无 Recovery 时，handler panic 会穿透 ServeHTTP
			// 逃逸子 goroutine 并崩溃整个进程。此处 recover 后经 bw 返回 500；
			// 该 defer 按 LIFO 先于 finishChan 通知执行，bw/finishChan 既有
			// 同步协议不变（外层只在收到 finishChan 后才读 bw）。
			defer func() {
				if recover() != nil {
					bw.Header().Set("Content-Type", "text/plain; charset=utf-8")
					bw.WriteHeader(http.StatusInternalServerError)
					_, _ = bw.Write([]byte("[500] internal server error"))
				}
			}()
			// 通过 engine.ServeHTTP 创建全新的 gin.Context
			cfg.Engine.ServeHTTP(bw, reqWithCtx)
		}()

		select {
		case <-finishChan:
			// 正常完成，刷写缓冲响应
			bw.flushTo(c.Writer)
			// flushTo 完成后归还：子 goroutine 已退出（finishChan 在子 goroutine
			// 最后一个 defer 中发信），bw 不再被任何方引用。刻意不用 defer Put——
			// 若外层在超时分支 <-finishChan 之前意外 panic，defer 会把子 goroutine
			// 仍在写入的 bw 归还池，引发跨请求数据竞争；显式 Put 使 panic 路径
			// 仅丢弃对象（牺牲一次复用换正确性）。子 goroutine panic（AUD-03）
			// 已在子 goroutine 内 recover，外层仍正常走本分支，归还路径不受影响。
			putBufferWriter(bw)
			c.Abort()
		case <-ctx.Done():
			// 超时，拒绝（默认 504，项目统一 "[code] msg" 文本约定）
			reject(c)
			// 等待子 goroutine 退出，防止泄漏
			<-finishChan
			// 子 goroutine 已退出，bw 不再被引用，安全归还（未刷写的缓冲内容随复位丢弃）
			putBufferWriter(bw)
		}
	}
}
