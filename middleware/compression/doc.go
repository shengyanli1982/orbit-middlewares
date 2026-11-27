// Package compression 提供基于 gzip 的 HTTP 响应压缩。
//
// 遵循 HTTP 压缩规范（RFC 9110）：
//   - handler 运行前预置 Content-Encoding: gzip 与 Vary: Accept-Encoding；
//     决定不压缩时在响应头提交前撤销——Vary 仅精确移除本中间件添加的
//     Accept-Encoding 值，保留其他写入方设置的值。Flush / WriteHeaderNow
//     在未决策即提交响应头的路径上有守卫先撤销再转发，避免
//     「头携带 gzip 而 body 未压缩」上线路
//   - 压缩时 ETag 转换为弱校验器；非压缩直通路径不触碰 handler 自设的 ETag
//   - 压缩响应不设置 Content-Length，分帧由 net/http 决定
//     （压缩输出 <2KB 时自动补 CL，否则使用 chunked 编码）
//   - 不压缩错误响应（4xx/5xx）、206 分片响应（保护 Content-Range 语义）、
//     304 条件响应与已压缩内容
//
// 示例：
//
//	r := gin.New()
//	r.Use(compression.New(compression.Config{
//	    MinLength:        1024,
//	    CompressionLevel: compression.DefaultCompression,
//	}))
package compression
