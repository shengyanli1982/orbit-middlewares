package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// API key 拒绝原因常量。值遵循全局 "<domain>.<cause>" 拒绝原因命名方案：domain（auth）
// 自证拒绝来源，cause 带 apikey 主语前缀与 token 主语区分，规则详见 README。
const (
	// ReasonMissingAPIKey 请求缺少 API key 拒绝的原因常量，作为 RejectHandler 的 reason 参数。
	ReasonMissingAPIKey = "auth.apikey_missing"
	// ReasonInvalidAPIKey API key 无效拒绝的原因常量，作为 RejectHandler 的 reason 参数。
	ReasonInvalidAPIKey = "auth.apikey_invalid"
)

// APIKeyAuthConfig API Key 认证配置。
type APIKeyAuthConfig struct {
	Skipper    func(*gin.Context) bool
	HeaderName string
	QueryParam string
	APIKeys    []string
	Validator  func(key string, c *gin.Context) bool
	// RejectHandler 自定义拒绝时的响应逻辑（可选）。
	//
	// 契约：reason 为本包导出的拒绝原因常量；回调负责写入状态码与响应体；
	// 回调返回后框架无条件调用 c.Abort() 终止后续处理器，无论回调内部是否
	// 已自行 Abort。为 nil 时返回默认响应（c.String 文本格式 "[code] msg"）。
	RejectHandler func(c *gin.Context, reason string)
}

// apiKeyAuth API Key 认证引擎。
type apiKeyAuth struct {
	skipper    func(*gin.Context) bool
	headerName string
	queryParam string
	apiKeys    map[string]struct{}
	hasAPIKeys bool
	validator  func(key string, c *gin.Context) bool
}

// APIKeyAuth 返回 API Key 认证中间件。密钥按 Query 优先、Header 其次提取。
// 当 APIKeys 与 Validator 均未配置时 panic：此时任何 key 都无法通过校验，
// 全部请求被 401 拒绝（配置锁死），无效配置必须在构造期暴露
// （fail-fast，与 JWTAuth 零值校验一致）。
func APIKeyAuth(cfg APIKeyAuthConfig) gin.HandlerFunc {
	if cfg.Validator == nil && len(cfg.APIKeys) == 0 {
		panic("auth: APIKeyAuthConfig requires either APIKeys or Validator")
	}
	if cfg.HeaderName == "" {
		cfg.HeaderName = "X-API-Key"
	}

	auth := &apiKeyAuth{
		skipper:    cfg.Skipper,
		headerName: cfg.HeaderName,
		queryParam: cfg.QueryParam,
		apiKeys:    make(map[string]struct{}, len(cfg.APIKeys)),
		hasAPIKeys: len(cfg.APIKeys) > 0,
		validator:  cfg.Validator,
	}

	for _, k := range cfg.APIKeys {
		auth.apiKeys[k] = struct{}{}
	}

	// reject 构造期捕获 cfg.RejectHandler，仅在拒绝路径调用，
	// 放行主路径零新增开销（统一拒绝契约）。
	reject := func(c *gin.Context, reason, defaultMsg string) {
		if cfg.RejectHandler != nil {
			cfg.RejectHandler(c, reason)
		} else {
			c.String(http.StatusUnauthorized, defaultMsg)
		}
		c.Abort()
	}

	return func(c *gin.Context) {
		if auth.skipper != nil && auth.skipper(c) {
			c.Next()
			return
		}

		key := extractAPIKey(c, auth.headerName, auth.queryParam)
		if key == "" {
			reject(c, ReasonMissingAPIKey, "[401] unauthorized, reason: missing api key")
			return
		}

		valid := false
		if auth.validator != nil {
			valid = auth.validator(key, c)
		} else if auth.hasAPIKeys {
			_, valid = auth.apiKeys[key]
		}

		if !valid {
			reject(c, ReasonInvalidAPIKey, "[401] unauthorized, reason: invalid api key")
			return
		}

		c.Next()
	}
}

// extractAPIKey 从 Query 或 Header 提取 API Key，Query 优先。
func extractAPIKey(c *gin.Context, headerName, queryParam string) string {
	// 优先从 Query 参数获取
	if queryParam != "" {
		key := c.Query(queryParam)
		if key != "" {
			return key
		}
	}

	// 其次从 Header 获取
	if headerName != "" {
		key := c.GetHeader(headerName)
		if key != "" {
			return key
		}
	}

	return ""
}
