// compression 示例：对超过阈值的响应做 GZIP 压缩（直接使用 gin，不依赖 orbit）。
//
// 运行: go run ./compression
// 测试: curl -sI -H "Accept-Encoding: gzip" http://localhost:8080/hello
//
//	响应头包含 Content-Encoding: gzip；/json 响应体小于阈值，不会被压缩
package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit-middlewares/middleware/compression"
)

func main() {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	// MinLength: 响应体超过该字节数才压缩（<=0 时默认 1024）
	// CompressionLevel: 为空（0）时默认 gzip.DefaultCompression
	r.Use(compression.New(compression.Config{
		MinLength: 1024,
	}))

	// 响应体约 1.4KB，超过 MinLength，会被压缩
	r.GET("/hello", func(c *gin.Context) {
		c.String(http.StatusOK, strings.Repeat("Hello, World! ", 100))
	})

	// 小 JSON 响应（< MinLength）不会被压缩
	r.GET("/json", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "Hello, World!",
			"time":    time.Now().Unix(),
		})
	})

	fmt.Println("Compression middleware example server running on :8080")
	fmt.Println("Test with: curl -sI -H 'Accept-Encoding: gzip' http://localhost:8080/hello")

	if err := r.Run(":8080"); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}
