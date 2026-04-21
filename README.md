# Orbit Middlewares

[![Go Report Card](https://goreportcard.com/badge/github.com/shengyanli1982/orbit-middlewares)](https://goreportcard.com/report/github.com/shengyanli1982/orbit-middlewares)
[![Build Status](https://github.com/shengyanli1982/orbit-middlewares/actions/workflows/test.yaml/badge.svg)](https://github.com/shengyanli1982/orbit-middlewares)
[![Go Reference](https://pkg.go.dev/badge/github.com/shengyanli1982/orbit-middlewares.svg)](https://pkg.go.dev/github.com/shengyanli1982/orbit-middlewares)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/shengyanli1982/orbit-middlewares)

`orbit-middlewares` is a production-oriented middleware toolkit for Go teams using the Gin web framework. It provides essential request processing capabilities including authentication, rate limiting, filtering, and observability — all with clear contracts and minimal overhead.

You can compose these middlewares in any combination to build secure, observable, and resilient API services without changing your mental model.

## Why Teams Choose Orbit Middlewares

- **One library, full request pipeline**: from authentication and authorization to rate limiting, timeout control, and request tracing.
- **Built for hot paths**: O(1) IP/key lookups via `map[string]struct{}`, sharded rate limiter storage with `sync.Map`, zero-copy conversions where possible.
- **Clear failure semantics**: explicit error responses with descriptive messages, typed error codes for upstream handling.
- **Cross-platform confidence**: CI runs `go test -v ./...` on Linux, macOS, and Windows.
- **Evidence over slogans**: the library focuses on correctness and performance without unnecessary complexity.

## Middleware Portfolio

| Middleware    | Best for             | Key capability                                              |
| ------------- | -------------------- | ----------------------------------------------------------- |
| `IPFilter`    | Access control       | IP whitelist/blacklist with O(1) lookup                     |
| `JWTAuth`     | Token authentication | HMAC signature validation with custom KeyFunc support       |
| `APIKeyAuth`  | API key validation   | Multi-key support with custom Validator function            |
| `RateLimiter` | Request throttling   | Token bucket algorithm, global or per-IP mode               |
| `RequestID`   | Request tracing      | ID propagation or generation with crypto/rand               |
| `RequestSize` | Payload protection   | Body size limit before reading                              |
| `Timeout`     | Deadline control     | Context-based timeout with proper cancellation              |
| `Compression` | Response compression | GZIP compression with configurable level and excluded paths |
| `Security`    | Security hardening   | HTTP security headers (CSP, HSTS, X-Frame-Options, etc.)    |

## Quick Start

```bash
go get github.com/shengyanli1982/orbit-middlewares
```

```go
package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit"
	"github.com/shengyanli1982/orbit-middlewares/middleware/auth"
	"github.com/shengyanli1982/orbit-middlewares/middleware/compression"
	"github.com/shengyanli1982/orbit-middlewares/middleware/ipfilter"
	"github.com/shengyanli1982/orbit-middlewares/middleware/ratelimiter"
	"github.com/shengyanli1982/orbit-middlewares/middleware/requestid"
	"github.com/shengyanli1982/orbit-middlewares/middleware/requestsize"
	"github.com/shengyanli1982/orbit-middlewares/middleware/security"
	"github.com/shengyanli1982/orbit-middlewares/middleware/timeout"
)

type DemoService struct{}

func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	g.GET("/ping", func(c *gin.Context) {
		requestID, _ := c.Get("request_id")
		c.JSON(http.StatusOK, gin.H{
			"message":    "pong",
			"request_id": requestID,
		})
	})
}

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions()
	engine := orbit.NewEngine(config, opts)

	engine.RegisterMiddleware(requestid.New(requestid.Config{}))
	engine.RegisterMiddleware(security.New(security.DefaultConfig()))
	engine.RegisterMiddleware(compression.New(compression.Config{
		MinLength: 1024,
	}))
	engine.RegisterMiddleware(ipfilter.New(ipfilter.Config{
		BlockedIPs: []string{"192.168.1.100"},
	}))
	engine.RegisterMiddleware(requestsize.New(requestsize.Config{
		MaxSize: 10 * 1024 * 1024,
	}))
	engine.RegisterMiddleware(timeout.New(timeout.Config{
		Timeout: 10 * time.Second,
	}))
	engine.RegisterMiddleware(ratelimiter.New(ratelimiter.Config{
		Mode:  ratelimiter.ModeIP,
		QPS:   100,
		Burst: 200,
	}))
	engine.RegisterMiddleware(auth.APIKeyAuth(auth.APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    []string{"admin-key", "user-key"},
	}))

	engine.RegisterService(&DemoService{})

	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}
```

## Middleware Details

### IPFilter

IP address filtering with whitelist and blacklist support.

```go
engine.RegisterMiddleware(ipfilter.New(ipfilter.Config{
    AllowedIPs: []string{"10.0.0.0/8"},
    BlockedIPs: []string{"192.168.1.100"},
}))
```

- Returns `403 Forbidden` with message `"[403] ip blocked"` or `"[403] ip not allowed"`

### JWTAuth

JWT token validation with HMAC signature verification.

```go
engine.RegisterMiddleware(auth.JWTAuth(auth.JWTAuthConfig{
    Secret: []byte("your-secret-key"),
}))
```

- Parses `Authorization: Bearer <token>` header
- Stores valid claims in `c.Set("jwt_claims", claims)`
- Returns `401 Unauthorized` on validation failure

### APIKeyAuth

API key authentication with multi-key and custom validator support.

```go
engine.RegisterMiddleware(auth.APIKeyAuth(auth.APIKeyAuthConfig{
    HeaderName: "X-API-Key",
    QueryParam: "api_key",
    APIKeys:    []string{"key1", "key2"},
    Validator: func(key string, c *gin.Context) bool {
        return validateAgainstDB(key)
    },
}))
```

- Checks query parameter first, then header
- Returns `401 Unauthorized` with reason

### RateLimiter

Token bucket rate limiter with global or per-IP modes.

```go
engine.RegisterMiddleware(ratelimiter.New(ratelimiter.Config{
    Mode:  ratelimiter.ModeIP,
    QPS:   100,
    Burst: 200,
    TTL:   5 * time.Minute,
}))
```

- `ModeGlobal`: single bucket for all requests
- `ModeIP`: per-client-IP limiting with 256 shards
- Adds `X-RateLimit-Limit` and `Retry-After` headers
- Returns `429 Too Many Requests` when exceeded

### RequestID

Request tracing ID generation and propagation.

```go
engine.RegisterMiddleware(requestid.New(requestid.Config{
    HeaderName: "X-Request-ID",
}))
```

- Reuses client-provided ID if present
- Generates 16-byte cryptographically random ID if not
- Stores ID in `c.Set("request_id", requestID)`

### RequestSize

Maximum request body size limit.

```go
engine.RegisterMiddleware(requestsize.New(requestsize.Config{
    MaxSize: 5 * 1024 * 1024,
}))
```

- Returns `413 Request Entity Too Large` if exceeded

### Timeout

Request timeout control with proper context cancellation.

```go
engine.RegisterMiddleware(timeout.New(timeout.Config{
    Timeout: 30 * time.Second,
}))
```

- Returns `504 Gateway Timeout` if timeout exceeded

### Compression

HTTP response compression using gzip.

```go
engine.RegisterMiddleware(compression.New(compression.Config{
    MinLength:       1024,
    CompressionLevel: compression.DefaultCompression,
}))
```

- Supports gzip compression with configurable level
- Adds `Content-Encoding: gzip` and `Vary: Accept-Encoding` headers
- Skips compression for error responses (4xx, 5xx)
- Excludes specified paths and file extensions

### Security

HTTP security headers for production applications.

```go
engine.RegisterMiddleware(security.New(security.DefaultConfig()))
```

Available presets:

- `DefaultConfig()`: Production-safe defaults
- `StrictConfig()`: Higher security requirements
- `LaxConfig()`: Development/testing

```go
engine.RegisterMiddleware(security.New(security.Config{
    XFrameOptions:       "DENY",
    XContentTypeOptions: "nosniff",
    HSTSMaxAge:          31536000,
    CSP:                 "default-src 'self'",
}))
```

Sets the following headers:

- `X-Frame-Options`: Clickjacking protection (DENY/SAMEORIGIN)
- `X-Content-Type-Options`: MIME-sniffing prevention (nosniff)
- `Strict-Transport-Security`: HTTPS enforcement (HSTS)
- `Content-Security-Policy`: XSS and injection protection
- `X-XSS-Protection`: Legacy XSS filter compatibility
- `Referrer-Policy`: Referrer information control
- `Permissions-Policy`: Browser feature restrictions

## Example Projects

Runnable demos:

- [`examples/requestid_example.go`](./examples/requestid_example.go)
- [`examples/ratelimiter_global_example.go`](./examples/ratelimiter_global_example.go)
- [`examples/ratelimiter_ip_example.go`](./examples/ratelimiter_ip_example.go)
- [`examples/timeout_example.go`](./examples/timeout_example.go)
- [`examples/jwt_example.go`](./examples/jwt_example.go)
- [`examples/apikey_example.go`](./examples/apikey_example.go)
- [`examples/requestsize_example.go`](./examples/requestsize_example.go)
- [`examples/ipfilter_example.go`](./examples/ipfilter_example.go)
- [`examples/compression_example.go`](./examples/compression_example.go)
- [`examples/security_example.go`](./examples/security_example.go)
- [`examples/combined_example.go`](./examples/combined_example.go)

Run any example directly:

```bash
go run ./examples/<example_file>
```

## Reliability by Design

- **Explicit error responses**: each middleware returns appropriate HTTP status codes with descriptive messages.
- **Skipper functions**: optional per-middleware skip logic for fine-grained control.
- **Clean separation**: each middleware is independent and can be used standalone or combined.
- **Proper resource management**: timeout middleware properly cancels contexts, rate limiter cleans up expired entries.

## API Reference

- GoDoc: <https://pkg.go.dev/github.com/shengyanli1982/orbit-middlewares>

## DeepWiki

- <https://deepwiki.com/shengyanli1982/orbit-middlewares>

## Related Projects

- [`orbit`](https://github.com/shengyanli1982/orbit): High-performance Go web framework built on Gin
- [`workqueue`](https://github.com/shengyanli1982/workqueue): Production-oriented queue toolkit for Go
