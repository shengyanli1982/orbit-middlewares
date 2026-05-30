package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// APIKeyAuthConfig API Key 认证配置。
type APIKeyAuthConfig struct {
	Skipper    func(*gin.Context) bool
	HeaderName string
	QueryParam string
	APIKeys    []string
	Validator  func(key string, c *gin.Context) bool
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

func APIKeyAuth(cfg APIKeyAuthConfig) gin.HandlerFunc {
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

	return func(c *gin.Context) {
		if auth.skipper != nil && auth.skipper(c) {
			c.Next()
			return
		}

		key := extractAPIKey(c, auth.headerName, auth.queryParam)
		if key == "" {
			c.String(http.StatusUnauthorized, "[401] unauthorized, reason: missing api key")
			c.Abort()
			return
		}

		valid := false
		if auth.validator != nil {
			valid = auth.validator(key, c)
		} else if auth.hasAPIKeys {
			_, valid = auth.apiKeys[key]
		}

		if !valid {
			c.String(http.StatusUnauthorized, "[401] unauthorized, reason: invalid api key")
			c.Abort()
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
