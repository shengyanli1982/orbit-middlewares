// Package compression 提供基于 gzip 的 HTTP 响应压缩。
//
// 遵循 HTTP 压缩规范（RFC 9110）：
//   - 设置 Content-Encoding 和 Vary: Accept-Encoding
//   - 压缩时 ETag 转换为弱校验器
//   - 压缩后设置 Content-Length
//   - 不压缩错误响应和已压缩内容
//
// 示例：
//
//	r := gin.New()
//	r.Use(compression.New(compression.Config{
//	    MinLength:        1024,
//	    CompressionLevel: compression.DefaultCompression,
//	}))
package compression
