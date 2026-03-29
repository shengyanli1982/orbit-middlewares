package compression

// Compression middleware provides HTTP response compression using gzip, zstd, or brotli algorithms.
// It implements end-to-end compression as per HTTP specification (RFC 9110).
//
// # Algorithm Selection
//
// The middleware supports three compression algorithms that are negotiated based on client preferences:
//
//   - gzip: Most widely compatible, uses compress/gzip from Go standard library
//   - zstd: Better compression ratio and speed, uses github.com/klauspost/compress/zstd
//   - br (brotli): Good compression ratio, uses github.com/andybalholm/brotli
//
// # Configuration
//
// Algorithm selection is mutually exclusive. Set the Algorithm field in Config to specify
// which algorithm to use. The middleware will only use that algorithm if the client
// advertises support via Accept-Encoding header.
//
// # HTTP Compliance
//
// The middleware follows HTTP compression specifications:
//
//   - Sets Content-Encoding header to the selected algorithm
//   - Sets Vary: Accept-Encoding header for proper caching
//   - Converts ETag to weak validator (W/"etag") when compressing
//   - Sets Content-Length after compression to reflect compressed size
//   - Does not compress error responses (4xx, 5xx)
//   - Skips already compressed content (detected by gzip magic bytes)
//
// # Usage
//
//	r := gin.New()
//	r.Use(compression.New(compression.Config{
//	    Algorithm:       compression.AlgorithmGzip,
//	    MinLength:       1024,
//	    CompressionLevel: compression.DefaultCompression,
//	}))
