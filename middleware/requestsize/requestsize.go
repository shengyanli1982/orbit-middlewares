package requestsize

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ReasonEntityTooLarge 请求体大小超限拒绝的原因常量，作为 RejectHandler 的 reason 参数。
// 值遵循全局 "<domain>.<cause>" 拒绝原因命名方案（domain=request 自证拒绝来源，
// cause 为 snake_case 状态描述），规则详见 README。
const ReasonEntityTooLarge = "request.entity_too_large"

// Config 请求体大小限制配置。
type Config struct {
	Skipper func(*gin.Context) bool
	MaxSize int64
	// RejectHandler 自定义拒绝时的响应逻辑（可选）。
	//
	// 契约：reason 为本包导出的拒绝原因常量；回调负责写入状态码与响应体；
	// 回调返回后框架无条件调用 c.Abort() 终止后续处理器，无论回调内部是否
	// 已自行 Abort。为 nil 时返回默认响应（c.String 文本格式 "[code] msg"）。
	//
	// 覆盖范围：仅处理本中间件依据 Content-Length 直接拒绝的快拒路径；
	// chunked 传输无 Content-Length 时，MaxBytesReader 超限的 413 由下游
	// handler 读取 body 时触发，不经过本回调。
	RejectHandler func(c *gin.Context, reason string)
}

// New 创建请求体大小限制中间件。
func New(cfg Config) gin.HandlerFunc {
	if cfg.MaxSize <= 0 {
		panic("requestsize: MaxSize must be > 0")
	}

	// reject 构造期捕获 cfg.RejectHandler，仅在拒绝路径调用，
	// 放行主路径零新增开销（统一拒绝契约）。
	reject := func(c *gin.Context) {
		if cfg.RejectHandler != nil {
			cfg.RejectHandler(c, ReasonEntityTooLarge)
		} else {
			c.String(http.StatusRequestEntityTooLarge, "[413] request entity too large")
		}
		c.Abort()
	}

	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		// Content-Length 超限时快速拒绝
		if c.Request.ContentLength > cfg.MaxSize {
			reject(c)
			return
		}

		// 包装 Body，限制读取大小（防止 chunked transfer 绕过）。
		// net/http server 对无 body 的请求置 http.NoBody，对其包装 MaxBytesReader
		// 无意义且白白产生一次 *maxBytesReader 分配（本中间件热路径唯一自有
		// 分配，pprof 占 51.05%），故跳过。
		if body := c.Request.Body; body != nil && body != http.NoBody {
			c.Request.Body = http.MaxBytesReader(c.Writer, body, cfg.MaxSize)
		}
		c.Next()
	}
}
