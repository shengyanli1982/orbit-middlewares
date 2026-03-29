package compression

// Compression middleware provides HTTP response compression using gzip.
// It implements end-to-end compression as per HTTP specification (RFC 9110).
//
// # HTTP Compliance
//
// The middleware follows HTTP compression specifications:
//
//   - Sets Content-Encoding header to "gzip"
//   - Sets Vary: Accept-Encoding header for proper caching
//   - Converts ETag to weak validator (W/"etag") when compressing
//   - Sets Content-Length after compression to reflect compressed size
//   - Does not compress error responses (4xx, 5xx)
//   - Skips already compressed content (detected by gzip magic bytes 0x1f 0x8b)
//
// # Usage
//
//	r := gin.New()
//	r.Use(compression.New(compression.Config{
//	    MinLength:       1024,
//	    CompressionLevel: compression.DefaultCompression,
//	}))
