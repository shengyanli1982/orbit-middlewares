package requestid

import (
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

var pool []byte
var poolIdx uint32

func init() {
	pool = make([]byte, 4096)
	rand.Read(pool)
}

type Config struct {
	Skipper    func(*gin.Context) bool
	HeaderName string
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
			idx := atomic.AddUint32(&poolIdx, 16)
			if idx+16 > uint32(len(pool)) {
				rand.Read(pool)
				atomic.StoreUint32(&poolIdx, 0)
				idx = 0
			}
			requestID = hex.EncodeToString(pool[idx : idx+16])
		}

		c.Set("request_id", requestID)
		c.Header(headerName, requestID)

		c.Next()
	}
}