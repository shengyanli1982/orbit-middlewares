package requestid

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

type Config struct {
	Skipper    func(*gin.Context) bool
	HeaderName string
}

type Option func(*Config)

func WithHeaderName(name string) Option {
	return func(c *Config) {
		c.HeaderName = name
	}
}

func WithSkipper(fn func(*gin.Context) bool) Option {
	return func(c *Config) {
		c.Skipper = fn
	}
}

func New(cfg Config) gin.HandlerFunc {
	headerName := cfg.HeaderName
	if headerName == "" {
		headerName = "X-Request-ID"
	}

	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		requestID := c.GetHeader(headerName)
		if requestID == "" {
			requestID = generateID()
		}

		c.Set("request_id", requestID)
		c.Header(headerName, requestID)

		c.Next()
	}
}

func RequestID(opts ...Option) gin.HandlerFunc {
	cfg := Config{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return New(cfg)
}

// generateID 生成一个16字节的随机ID，使用hex编码
// 使用 crypto/rand 确保密码学安全的随机数
// hex.EncodeToString 比 fmt.Sprintf 更高效，减少内存分配
func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
