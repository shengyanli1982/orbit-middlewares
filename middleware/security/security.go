package security

import (
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

type securityHeaders struct {
	skipper             func(*gin.Context) bool
	xFrameOptions       string
	xContentTypeOptions string
	hstsHeader          string
	csp                 string
	xssProtection       string
	referrerPolicy      string
	permissionsPolicy   string
}

func New(cfg Config) gin.HandlerFunc {
	h := &securityHeaders{
		skipper:             cfg.Skipper,
		xFrameOptions:       cfg.XFrameOptions,
		xContentTypeOptions: cfg.XContentTypeOptions,
		csp:                 cfg.CSP,
		xssProtection:       cfg.XSSProtection,
		referrerPolicy:      cfg.ReferrerPolicy,
		permissionsPolicy:   cfg.PermissionsPolicy,
	}

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
		h.hstsHeader = b.String()
	}

	if h.csp == "" {
		h.csp = "default-src 'self'"
	}

	return h.handle
}

func (h *securityHeaders) handle(c *gin.Context) {
	if h.skipper != nil && h.skipper(c) {
		c.Next()
		return
	}

	if h.xFrameOptions != "" {
		c.Header("X-Frame-Options", h.xFrameOptions)
	}

	if h.xContentTypeOptions != "" {
		c.Header("X-Content-Type-Options", h.xContentTypeOptions)
	}

	if h.hstsHeader != "" {
		c.Header("Strict-Transport-Security", h.hstsHeader)
	}

	if h.csp != "" {
		c.Header("Content-Security-Policy", h.csp)
	}

	if h.xssProtection != "" {
		c.Header("X-XSS-Protection", h.xssProtection)
	}

	if h.referrerPolicy != "" {
		c.Header("Referrer-Policy", h.referrerPolicy)
	}

	if h.permissionsPolicy != "" {
		c.Header("Permissions-Policy", h.permissionsPolicy)
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
