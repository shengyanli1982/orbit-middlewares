package security

import (
	"net/textproto"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type Config struct {
	Skipper func(*gin.Context) bool

	XFrameOptions         string
	XContentTypeOptions   string
	HSTSMaxAge            int64
	HSTSIncludeSubDomains bool
	HSTSPreload           bool
	CSP                   string
	XSSProtection         string
	ReferrerPolicy        string
	PermissionsPolicy     string
}

// headerEntry 存储预计算的 canonical key 与对应值，避免每次请求重复规范化。
type headerEntry struct {
	key   string // textproto.CanonicalMIMEHeaderKey 预计算结果
	value string
}

type securityHeaders struct {
	skipper func(*gin.Context) bool
	// headers 仅包含值非空的条目，handler 直接遍历写入，无需逐字段判断。
	headers []headerEntry
}

func New(cfg Config) gin.HandlerFunc {
	// 校验所有 header 值，防止 HTTP Header Injection
	validateHeaderValue(cfg.XFrameOptions)
	validateHeaderValue(cfg.XContentTypeOptions)
	validateHeaderValue(cfg.CSP)
	validateHeaderValue(cfg.XSSProtection)
	validateHeaderValue(cfg.ReferrerPolicy)
	validateHeaderValue(cfg.PermissionsPolicy)

	if cfg.CSP == "" {
		cfg.CSP = "default-src 'self'"
	}

	// 预计算 HSTS 值
	var hstsValue string
	if cfg.HSTSMaxAge > 0 {
		var b strings.Builder
		b.WriteString("max-age=")
		b.WriteString(strconv.FormatInt(cfg.HSTSMaxAge, 10))
		if cfg.HSTSIncludeSubDomains {
			b.WriteString("; includeSubDomains")
		}
		if cfg.HSTSPreload {
			b.WriteString("; preload")
		}
		hstsValue = b.String()
	}

	// 按固定顺序构建非空 header 列表，key 在初始化时规范化一次。
	type rawEntry struct{ key, value string }
	candidates := []rawEntry{
		{"X-Frame-Options", cfg.XFrameOptions},
		{"X-Content-Type-Options", cfg.XContentTypeOptions},
		{"Strict-Transport-Security", hstsValue},
		{"Content-Security-Policy", cfg.CSP},
		{"X-XSS-Protection", cfg.XSSProtection},
		{"Referrer-Policy", cfg.ReferrerPolicy},
		{"Permissions-Policy", cfg.PermissionsPolicy},
	}

	entries := make([]headerEntry, 0, len(candidates))
	for _, c := range candidates {
		if c.value != "" {
			entries = append(entries, headerEntry{
				key:   textproto.CanonicalMIMEHeaderKey(c.key),
				value: c.value,
			})
		}
	}

	h := &securityHeaders{
		skipper: cfg.Skipper,
		headers: entries,
	}

	return h.handle
}

func (h *securityHeaders) handle(c *gin.Context) {
	if h.skipper != nil && h.skipper(c) {
		c.Next()
		return
	}

	// 直接写底层 http.Header map，key 已预计算为 canonical 形式，
	// 绕过 gin c.Header() 内部的 textproto.CanonicalMIMEHeaderKey 调用。
	hdr := c.Writer.Header()
	for i := range h.headers {
		hdr[h.headers[i].key] = []string{h.headers[i].value}
	}

	c.Next()
}

func DefaultConfig() Config {
	return Config{
		XFrameOptions:         "DENY",
		XContentTypeOptions:   "nosniff",
		HSTSMaxAge:            31536000,
		HSTSIncludeSubDomains: true,
		CSP:                   "default-src 'self'",
		XSSProtection:         "1; mode=block",
		ReferrerPolicy:        "strict-origin-when-cross-origin",
		PermissionsPolicy:     "geolocation=(), microphone=(), camera=()",
	}
}

func StrictConfig() Config {
	return Config{
		XFrameOptions:         "DENY",
		XContentTypeOptions:   "nosniff",
		HSTSMaxAge:            63072000,
		HSTSIncludeSubDomains: true,
		HSTSPreload:           true,
		CSP:                   "default-src 'none'; script-src 'none'; object-src 'none'",
		XSSProtection:         "1; mode=block",
		ReferrerPolicy:        "no-referrer",
		PermissionsPolicy:     "geolocation=(), microphone=(), camera=(), payment=()",
	}
}

func LaxConfig() Config {
	return Config{
		XFrameOptions:       "SAMEORIGIN",
		XContentTypeOptions: "nosniff",
		CSP:                 "default-src 'self' 'unsafe-inline' 'unsafe-eval'",
		ReferrerPolicy:      "strict-origin-when-cross-origin",
	}
}

// validateHeaderValue 校验 header 值不包含 CR/LF，防止 HTTP Header Injection。
func validateHeaderValue(v string) {
	if strings.ContainsAny(v, "\r\n") {
		panic("security: header value contains invalid characters (CR/LF)")
	}
}
