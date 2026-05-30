package requestid

import (
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/gin-gonic/gin"
)

// Config 请求 ID 中间件配置。
type Config struct {
	Skipper    func(*gin.Context) bool
	HeaderName string
}

var idBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 16)
		return &b
	},
}

// generateID 使用 crypto/rand 生成 16 字节的 hex 字符串 ID。
func generateID() (string, error) {
	bp := idBufPool.Get().(*[]byte)
	defer idBufPool.Put(bp)
	b := *bp
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		HeaderName: "X-Request-ID",
	}
}

// New 创建请求 ID 中间件。
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
			id, err := generateID()
			if err != nil {
				// crypto/rand 失败极为罕见，降级为空字符串
				id = ""
			}
			requestID = id
		}

		c.Set("request_id", requestID)
		c.Header(headerName, requestID)

		c.Next()
	}
}
