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

// idBufPool 复用 16 字节缓冲区，减少每次 generateID 的堆分配。
// crypto/rand.Read 会覆盖整个切片，不存在数据残留问题。
var idBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 16)
		return &b
	},
}

// generateID 从 crypto/rand 读取 16 字节生成 hex 字符串。
// 使用 sync.Pool 复用底层缓冲区，减少 GC 压力；安全性由 crypto/rand 保证。
func generateID() (string, error) {
	bp := idBufPool.Get().(*[]byte)
	defer idBufPool.Put(bp)
	b := *bp
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// DefaultConfig 返回合理的默认配置。
func DefaultConfig() Config {
	return Config{
		HeaderName: "X-Request-ID",
	}
}

// New 返回请求 ID 中间件。
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
				// crypto/rand 失败极为罕见，降级为空字符串并继续
				id = ""
			}
			requestID = id
		}

		c.Set("request_id", requestID)
		c.Header(headerName, requestID)

		c.Next()
	}
}
