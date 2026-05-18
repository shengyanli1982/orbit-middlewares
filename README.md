# Orbit Middlewares

[![Go Report Card](https://goreportcard.com/badge/github.com/shengyanli1982/orbit-middlewares)](https://goreportcard.com/report/github.com/shengyanli1982/orbit-middlewares)
[![Build Status](https://github.com/shengyanli1982/orbit-middlewares/actions/workflows/test.yaml/badge.svg)](https://github.com/shengyanli1982/orbit-middlewares)
[![Go Reference](https://pkg.go.dev/badge/github.com/shengyanli1982/orbit-middlewares.svg)](https://pkg.go.dev/github.com/shengyanli1982/orbit-middlewares)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/shengyanli1982/orbit-middlewares)

A production-ready middleware toolkit for Go services built on [Gin](https://github.com/gin-gonic/gin). Covers the full request pipeline — authentication, rate limiting, observability, security hardening, and more.

## Middleware Portfolio

| Middleware    | Purpose              | Key behavior                                           |
| ------------- | -------------------- | ------------------------------------------------------ |
| `IPFilter`    | Access control       | IP whitelist/blacklist with CIDR support               |
| `JWTAuth`     | Token authentication | HMAC signature validation, custom KeyFunc support      |
| `APIKeyAuth`  | API key validation   | Header/query lookup, custom Validator function         |
| `RateLimiter` | Request throttling   | Token bucket, global or per-IP mode with 256 shards    |
| `RequestID`   | Request tracing      | Propagates or generates a crypto-random ID per request |
| `RequestSize` | Payload protection   | Limits body size, blocks chunked transfer bypass       |
| `Timeout`     | Deadline control     | Per-request timeout with goroutine-safe implementation |
| `Compression` | Response compression | GZIP with configurable level and path exclusions       |
| `Security`    | Security headers     | CSP, HSTS, X-Frame-Options, and more                   |

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
	"github.com/shengyanli1982/orbit-middlewares/middleware/ratelimiter"
	"github.com/shengyanli1982/orbit-middlewares/middleware/requestid"
	"github.com/shengyanli1982/orbit-middlewares/middleware/security"
	"github.com/shengyanli1982/orbit-middlewares/middleware/timeout"
)

func main() {
	config := orbit.NewConfig()
	opts := orbit.NewOptions()
	engine := orbit.NewEngine(config, opts)

	engine.RegisterMiddleware(requestid.New(requestid.DefaultConfig()))
	engine.RegisterMiddleware(security.New(security.DefaultConfig()))
	engine.RegisterMiddleware(compression.New(compression.DefaultConfig()))

	rateLimitHandler, stop := ratelimiter.New(ratelimiter.Config{
		Mode:  ratelimiter.ModeIP,
		QPS:   100,
		Burst: 200,
	})
	defer stop()
	engine.RegisterMiddleware(rateLimitHandler)

	engine.RegisterMiddleware(timeout.New(timeout.Config{
		Engine:  engine.GetGinEngine(), // pass the underlying *gin.Engine
		Timeout: 10 * time.Second,
	}))

	engine.RegisterMiddleware(auth.APIKeyAuth(auth.APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    []string{"key-prod-1"},
	}))

	engine.RegisterService(&DemoService{})
	engine.Run()
	time.Sleep(30 * time.Second)
	engine.Stop()
}

type DemoService struct{}

func (s *DemoService) RegisterGroup(g *gin.RouterGroup) {
	g.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message":    "pong",
			"request_id": c.GetString("request_id"),
		})
	})
}
```

## Middleware Reference

### IPFilter

Blocks or allows requests by client IP. Blacklist takes priority over whitelist. Supports exact IPs and CIDR ranges.

```go
engine.RegisterMiddleware(ipfilter.New(ipfilter.Config{
    AllowedIPs: []string{"10.0.0.0/8", "192.168.1.50"},
    BlockedIPs: []string{"1.2.3.4"},
}))
```

- Returns `403 Forbidden` with `"[403] ip blocked"` or `"[403] ip not allowed"`
- When only `BlockedIPs` is set, all other IPs are allowed
- When only `AllowedIPs` is set, all other IPs are blocked

---

### JWTAuth

Validates `Authorization: Bearer <token>` using HMAC by default.

```go
engine.RegisterMiddleware(auth.JWTAuth(auth.JWTAuthConfig{
    Secret: []byte("your-secret-key"),
}))
```

Valid claims are stored in `c.Get("jwt_claims")`. Returns `401 Unauthorized` on failure.

Custom key function (e.g. RS256):

```go
engine.RegisterMiddleware(auth.JWTAuth(auth.JWTAuthConfig{
    KeyFunc: func(token *jwt.Token) (interface{}, error) {
        return rsaPublicKey, nil
    },
}))
```

---

### APIKeyAuth

Checks the API key from a header or query parameter.

```go
engine.RegisterMiddleware(auth.APIKeyAuth(auth.APIKeyAuthConfig{
    HeaderName: "X-API-Key",
    QueryParam: "api_key",
    APIKeys:    []string{"key-prod-1", "key-prod-2"},
}))
```

Custom validator (e.g. database lookup):

```go
engine.RegisterMiddleware(auth.APIKeyAuth(auth.APIKeyAuthConfig{
    HeaderName: "X-API-Key",
    Validator: func(key string, c *gin.Context) bool {
        return db.IsValidKey(key)
    },
}))
```

Returns `401 Unauthorized` on failure.

---

### RateLimiter

Token bucket limiter. `New` returns `(handler, stop)` — call `stop()` when the server shuts down to release the background cleanup goroutine.

```go
handler, stop := ratelimiter.New(ratelimiter.Config{
    Mode:  ratelimiter.ModeIP,   // or ratelimiter.ModeGlobal
    QPS:   100,
    Burst: 200,
    TTL:   5 * time.Minute,      // how long to keep idle per-IP state
})
defer stop()
engine.RegisterMiddleware(handler)
```

- `ModeGlobal`: one shared bucket for all requests
- `ModeIP`: per-client-IP bucket, 256 shards, IPv4 and IPv6 supported
- Adds `X-RateLimit-Limit` and `Retry-After` response headers
- Returns `429 Too Many Requests` when exceeded

---

### RequestID

Reuses the client-provided ID from the request header, or generates a new 16-byte crypto-random ID.

```go
// default: header name "X-Request-ID"
engine.RegisterMiddleware(requestid.New(requestid.DefaultConfig()))

// custom header name
engine.RegisterMiddleware(requestid.New(requestid.Config{
    HeaderName: "X-Trace-ID",
}))
```

The ID is stored in `c.Set("request_id", id)` and written back to the response header. Read it downstream with `c.GetString("request_id")`.

---

### RequestSize

Limits the maximum request body size. Uses `http.MaxBytesReader` to enforce the limit on actual reads, preventing chunked transfer bypass.

```go
engine.RegisterMiddleware(requestsize.New(requestsize.Config{
    MaxSize: 10 * 1024 * 1024, // 10 MB
}))
```

Returns `413 Request Entity Too Large` if exceeded. `MaxSize` must be > 0.

---

### Timeout

Enforces a per-request deadline. Requires the `*gin.Engine` instance to create an isolated context per request, avoiding `gin.Context` data races.

```go
engine.RegisterMiddleware(timeout.New(timeout.Config{
    Engine:  engine.GetGinEngine(), // required
    Timeout: 30 * time.Second,
}))
```

Returns `504 Gateway Timeout` if the deadline is exceeded. The background goroutine is always waited on before returning, preventing goroutine leaks.

---

### Compression

GZIP-compresses responses above a minimum size threshold.

```go
// defaults: MinLength=1024, Level=DefaultCompression
engine.RegisterMiddleware(compression.New(compression.DefaultConfig()))

// custom config
engine.RegisterMiddleware(compression.New(compression.Config{
    MinLength:        2048,
    CompressionLevel: compression.BestSpeed,
    ExcludedPaths:    []string{"/metrics"},
    ExcludedExts:     []string{".jpg", ".png", ".gif"},
}))
```

- Skips compression for error responses (4xx/5xx)
- Adds `Content-Encoding: gzip` and `Vary: Accept-Encoding`
- Reuses `gzip.Writer` via `sync.Pool`

---

### Security

Sets HTTP security headers. Three built-in presets:

```go
engine.RegisterMiddleware(security.New(security.DefaultConfig())) // production
engine.RegisterMiddleware(security.New(security.StrictConfig()))  // high security
engine.RegisterMiddleware(security.New(security.LaxConfig()))     // development
```

Custom config:

```go
engine.RegisterMiddleware(security.New(security.Config{
    XFrameOptions:       "DENY",
    XContentTypeOptions: "nosniff",
    HSTSMaxAge:          31536000,
    CSP:                 "default-src 'self'",
    ReferrerPolicy:      "strict-origin-when-cross-origin",
}))
```

Headers set: `X-Frame-Options`, `X-Content-Type-Options`, `Strict-Transport-Security`, `Content-Security-Policy`, `X-XSS-Protection`, `Referrer-Policy`, `Permissions-Policy`.

---

## Skipper

Every middleware accepts an optional `Skipper` function to bypass processing for specific requests:

```go
engine.RegisterMiddleware(auth.JWTAuth(auth.JWTAuthConfig{
    Secret: []byte("secret"),
    Skipper: func(c *gin.Context) bool {
        return c.Request.URL.Path == "/health"
    },
}))
```

## Examples

Runnable demos in [`examples/`](./examples):

- [`combined_example.go`](./examples/combined_example.go)
- [`jwt_example.go`](./examples/jwt_example.go)
- [`apikey_example.go`](./examples/apikey_example.go)
- [`ratelimiter_ip_example.go`](./examples/ratelimiter_ip_example.go)
- [`ratelimiter_global_example.go`](./examples/ratelimiter_global_example.go)
- [`ipfilter_example.go`](./examples/ipfilter_example.go)
- [`requestid_example.go`](./examples/requestid_example.go)
- [`requestsize_example.go`](./examples/requestsize_example.go)
- [`timeout_example.go`](./examples/timeout_example.go)
- [`compression_example.go`](./examples/compression_example.go)
- [`security_example.go`](./examples/security_example.go)

## Testing

```bash
go test ./...
go test -race ./...
go test -bench=. -benchmem ./middleware/...
```

## Related Projects

- [`orbit`](https://github.com/shengyanli1982/orbit): High-performance Go web framework built on Gin
- [`workqueue`](https://github.com/shengyanli1982/workqueue): Production-oriented queue toolkit for Go

## API Reference

- GoDoc: <https://pkg.go.dev/github.com/shengyanli1982/orbit-middlewares>
- DeepWiki: <https://deepwiki.com/shengyanli1982/orbit-middlewares>

## License

[MIT](./LICENSE)
