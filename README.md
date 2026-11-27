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
| `AutoIndex`   | Static file serving  | Apache-style directory index + file downloads (terminal handler, mount via `NoRoute`) |

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

	// Timeout MUST be registered first: it replays the full middleware chain in an
	// isolated goroutine, so any middleware registered before it would run twice
	// per request (doubled side effects, duplicated response headers).
	engine.RegisterMiddleware(timeout.New(timeout.Config{
		Engine:  engine.GetGinEngine(), // pass the underlying *gin.Engine
		Timeout: 10 * time.Second,
	}))

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
- Panics at construction time on any malformed IP/CIDR entry (fail-fast — a typo in a blacklist entry must not be silently dropped)
- Exact IP entries are normalized at construction time (e.g. uppercase IPv6 `ABCD::1` is stored as `abcd::1`, matching gin's canonical `c.ClientIP()` output)
- **Trust boundary**: the client IP comes from gin's `c.ClientIP()`, which honors `X-Forwarded-For` / `X-Real-IP` when proxies are trusted. Call `router.SetTrustedProxies`, sit behind a proxy that sanitizes forwarding headers, or set `RemoteIPHeaders = nil` — otherwise a public-facing service can be bypassed with spoofed headers

---

### JWTAuth

Validates `Authorization: Bearer <token>` using HMAC by default.

```go
engine.RegisterMiddleware(auth.JWTAuth(auth.JWTAuthConfig{
    Secret: []byte("your-secret-key"),
}))
```

Valid claims are stored in `c.Get("jwt_claims")`. Returns `401 Unauthorized` on failure. The `Bearer` scheme prefix is matched case-insensitively (RFC 7235). Panics at construction time when both `Secret` and `KeyFunc` are empty/nil — an empty HMAC key would accept self-signed tokens (fail-open).

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
- Returns `429 Too Many Requests` with body `"[429] rate limit exceeded"` when exceeded
- Panics at construction time when `QPS <= 0` (NaN included) or `Burst <= 0` — an invalid limiter would reject every request
- **Trust boundary**: `ModeIP` keys on `c.ClientIP()` by default, which honors `X-Forwarded-For` / `X-Real-IP` when proxies are trusted. Configure `router.SetTrustedProxies` for your real topology, or supply a custom `IPExtractor` (e.g. authenticated subject ID) — otherwise a public-facing service can be bypassed with spoofed headers minting a fresh bucket per request

Custom reject response via `RejectHandler` (see [Custom Reject Responses](#custom-reject-responses) for the shared contract). `X-RateLimit-Limit` / `Retry-After` are already written when the callback runs (read or override via `c.Writer.Header()`); the callback writes the status code and body; `c.Abort()` is always called afterwards; `reason` is `ratelimiter.ReasonRateLimited`:

```go
handler, stop := ratelimiter.New(ratelimiter.Config{
    Mode:  ratelimiter.ModeIP,
    QPS:   100,
    Burst: 200,
    RejectHandler: func(c *gin.Context, reason string) {
        c.JSON(http.StatusTooManyRequests, gin.H{"error": "slow down", "reason": reason})
    },
})
```

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

Returns `413 Request Entity Too Large` with body `"[413] request entity too large"` if exceeded. Panics at construction time when `MaxSize <= 0`.

---

### Timeout

Enforces a per-request deadline. Requires the `*gin.Engine` instance to create an isolated context per request, avoiding `gin.Context` data races.

**Must be registered first.** The middleware replays the full middleware chain inside an isolated goroutine (the recursion guard only covers timeout itself), so any middleware registered before it runs twice per request — doubled side effects (e.g. two rate-limit tokens consumed) and duplicated response headers (e.g. two different `X-Request-ID` values).

```go
// register before all other middleware
engine.RegisterMiddleware(timeout.New(timeout.Config{
    Engine:  engine.GetGinEngine(), // required
    Timeout: 30 * time.Second,      // must be > 0, panics otherwise
}))
```

- Returns `504 Gateway Timeout` with body `"[504] request timeout"` when the deadline is exceeded; the middleware then waits for the handler goroutine to finish so its buffer can be reclaimed. A handler that ignores `c.Request.Context()` cancellation runs to completion — make handlers context-aware to truly bound request resources
- `c.Writer.Flush()` is a no-op — the buffered response is delivered in one shot when the handler finishes, so SSE events are not streamed incrementally; `Hijack`/`CloseNotify` are unsupported (they panic into the 500 recovery path) — exclude such handlers via `Skipper`
- Handler panics are recovered in the worker goroutine and returned as `500` with body `"[500] internal server error"`; a half-written body is discarded and rewritten — the process stays up
- Panics at construction time when `Engine` is nil or `Timeout <= 0`
- Buffered response headers replace same-named headers preset on the outer writer, so replayed middleware cannot duplicate header values

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
- `CompressionLevel` zero value means `DefaultCompression`; the `NoCompression` constant is **deprecated** (its value `0` is indistinguishable from the zero value — bypass the middleware if you need stored-only output)
- Compressed responses that outgrow the buffer are sent chunked without `Content-Length` (net/http freezes headers once the first body byte hits the wire)
- ETags set by handlers are preserved on passthrough and `304 Not Modified` responses (RFC 7232); weakening to `W/` happens only on actually-compressed responses

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

Headers set: `X-Frame-Options`, `X-Content-Type-Options`, `Strict-Transport-Security`, `Content-Security-Policy`, `X-XSS-Protection`, `Referrer-Policy`, `Permissions-Policy`. The `Default`/`Strict` presets send `X-XSS-Protection: 0` — OWASP deprecates the legacy `1; mode=block` (it introduces XSS leaks in older browsers), so the auditor is explicitly disabled.

---

### AutoIndex

Apache `mod_autoindex`-style directory listing plus static file downloads. Unlike the nine pipeline middleware above, this is a **terminal handler**: it generates the response body itself and does not call `c.Next()` on the served path (a `Skipper` hit passes through via `c.Next()`). Mount it as the `NoRoute` fallback — gin's global `Use` middleware automatically wraps the `NoRoute` chain, so pipeline middleware still applies.

```go
// orbit engine
engine.GetGinEngine().NoRoute(autoindex.New(autoindex.Config{
    Root: "./public",
}))

// bare gin
r := gin.New()
r.NoRoute(autoindex.New(autoindex.Config{
    Root:        "./public",
    IndexIgnore: []string{".git", "*.tmp"},
}))
```

- Renders an Apache-style HTML listing for directories: `Parent Directory` link, folders first, `Name` / `Last modified` / `Size` columns
- Sorting via query parameters — `?C=N|M|S` (name / mtime / size) and `?O=A|D` (ascending / descending); column headers are self-links that toggle order; ties always break by name ascending
- `IndexIgnore` hides entries from the listing by glob pattern (`path.Match` semantics; unlike Apache, `[abc]` character classes work)
- Files are served via `http.ServeContent`: `Range` → `206`, `If-Modified-Since` → `304`, `HEAD` supported
- The listing page declares `Content-Length` — it is rendered in memory in one pass, so the exact byte count is known up front (net/http only auto-fills it for bodies ≤ 2048 bytes, and a listing page is always larger); no chunked framing, matching Apache `mod_autoindex` / nginx `autoindex`. `HEAD` on a directory returns the same `Content-Length` as `GET`, with an empty body
- Only `GET` and `HEAD` are served — other methods get `405 Method Not Allowed` with `Allow: GET, HEAD` and body `"[405] method not allowed"`, matching Apache `mod_autoindex` / nginx `autoindex`
- Directory URLs without a trailing slash get a `301` redirect (query string preserved; `Location` is re-percent-encoded so directory names containing `?` / `#` / `%` / spaces stay intact)
- Traversal outside `Root` and filesystem permission errors (`EACCES`) return `403 Forbidden` with `"[403] forbidden"`; missing paths return `404`; open/read failures after a successful stat (fd exhaustion, I/O error) return `500` with `"[500] internal server error"`
- Panics at construction time when `Root` is empty / missing / not a directory, or an `IndexIgnore` pattern is malformed (fail-fast)

**Mounting & conflict avoidance:**

- **`NoRoute` only** (or a non-root prefix catch-all like `/files/*filepath`). In a `Use` chain it would stat the filesystem on every request and shadow your routes. Under orbit, do **not** pair it with root-level `StaticFS("/")` — its catch-all route conflicts with the built-in `/ping` / `/metrics` and panics
- **A prefix catch-all does not strip the prefix**: the handler maps the **full** URL path onto `Root` (`filepath.Join(Root, URL.Path)`), so a `/files/*filepath` mount resolves `/files/a.txt` to `Root/files/a.txt`, **not** `Root/a.txt` — the intuitive `Root: "./public"` will `404`. Point `Root` at a dir that contains a matching `files/` subdir, or (recommended) mount at `NoRoute` with `Root` as the real content root:

    ```go
    r.GET("/files/*filepath", autoindex.New(autoindex.Config{Root: "./public"}))  // /files/a.txt → public/files/a.txt
    r.NoRoute(autoindex.New(autoindex.Config{Root: "./public/files"}))            // /a.txt       → public/files/a.txt (1:1)
    ```

- **A prefix mount needs `HEAD` registered separately**: gin's `r.GET` does not route `HEAD`, so under a `/files/*filepath` prefix mount `HEAD` never reaches the handler — also register `r.HEAD("/files/*filepath", h)`. Under a `NoRoute` mount **every** method reaches the handler, where non-`GET`/`HEAD` methods get the `405` above — i.e. the method gate only fires under `NoRoute`
- **With Compression**: `206` (Range) and `304` responses are automatically exempted from compression — their headers and slice bytes pass through unchanged, so no exclusion is required for correctness (excluding download paths is now optional, e.g. to save CPU). The listing HTML itself is safe to compress:

    ```go
    compression.New(compression.Config{ExcludedPaths: []string{"/downloads/"}}) // optional since 206/304 are auto-exempted
    ```

- **With Timeout**: timeout buffers the whole response in memory — large downloads risk OOM and get cut off with `504`. Exclude download paths via `Skipper`, or don't mount timeout on a pure static service:

    ```go
    timeout.New(timeout.Config{Engine: r, Timeout: 10 * time.Second,
        Skipper: func(c *gin.Context) bool { return strings.HasPrefix(c.Request.URL.Path, "/downloads/") }})
    ```

- **With Security**: the default CSP `default-src 'self'` blocks the listing page's inline `<style>` — the page loses its styling but **every link and download keeps working**. Accept the plain look, or relax `style-src`
- **Write timeout**: orbit resets `WithHttpWriteTimeout(0)` to its 15s default, which would truncate large downloads. AutoIndex already clears the per-connection write deadline on the download branch — nothing to configure
- **`IndexIgnore` is not access control** — it only hides entries from the listing; hidden files are still served on direct request. Never put secrets inside `Root`

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

## Error Response Convention

All middleware emit plain-text error bodies in the form `"[code] message"` — e.g. `"[403] ip blocked"`, `"[413] request entity too large"`, `"[429] rate limit exceeded"`, `"[500] internal server error"`, `"[504] request timeout"`.

## Custom Reject Responses

Every middleware with a reject point accepts an optional `RejectHandler` in its config, sharing one contract:

```go
RejectHandler func(c *gin.Context, reason string)
```

- The callback runs instead of the default response and receives `reason` — one of the exported `Reason*` constants of that package
- The callback writes the status code and body
- `c.Abort()` is always called after the callback returns, whether or not the callback aborted itself
- A nil `RejectHandler` keeps the default plain-text response (see [Error Response Convention](#error-response-convention))

`reason` values follow a global `<domain>.<cause>` naming scheme:

- `domain` is the functional area (`auth`, `ip`, `ratelimit`, `request`) and self-identifies which middleware issued the rejection — reasons stay globally unique, so one shared handler can be safely registered across multiple middleware
- `cause` is a snake_case state description; the subject is omitted when self-evident within the domain (`ratelimit.exceeded`) and prefixed when the domain covers multiple subjects (`auth.token_missing` vs `auth.apikey_missing`)
- New middleware or new reject reasons extend the same scheme

| Middleware    | Config field                          | Reason constants                                                          | Reject point                     |
| ------------- | ------------------------------------- | ------------------------------------------------------------------------- | -------------------------------- |
| `IPFilter`    | `ipfilter.Config.RejectHandler`       | `ReasonBlocked` = `ip.blocked`, `ReasonNotAllowed` = `ip.not_allowed`     | blacklist hit / not in whitelist |
| `JWTAuth`     | `auth.JWTAuthConfig.RejectHandler`    | `ReasonMissingToken` = `auth.token_missing`, `ReasonInvalidToken` = `auth.token_invalid` | missing / invalid bearer token   |
| `APIKeyAuth`  | `auth.APIKeyAuthConfig.RejectHandler` | `ReasonMissingAPIKey` = `auth.apikey_missing`, `ReasonInvalidAPIKey` = `auth.apikey_invalid` | missing / invalid API key        |
| `RateLimiter` | `ratelimiter.Config.RejectHandler`    | `ReasonRateLimited` = `ratelimit.exceeded`                                | rate limit exceeded              |
| `RequestSize` | `requestsize.Config.RejectHandler`    | `ReasonEntityTooLarge` = `request.entity_too_large`                       | Content-Length fast reject       |
| `Timeout`     | `timeout.Config.RejectHandler`        | `ReasonTimeout` = `request.timeout`                                       | 504 request timeout              |
| `AutoIndex`   | `autoindex.Config.RejectHandler`      | `ReasonForbidden` = `autoindex.forbidden`, `ReasonPermission` = `autoindex.permission` | path traversal outside root / filesystem permission denied (EACCES) |

Example — uniform JSON reject responses for `IPFilter`:

```go
engine.RegisterMiddleware(ipfilter.New(ipfilter.Config{
    BlockedIPs: []string{"1.2.3.4"},
    RejectHandler: func(c *gin.Context, reason string) {
        c.JSON(http.StatusForbidden, gin.H{"error": "access denied", "reason": reason})
    },
}))
```

Coverage notes:

- `RateLimiter`: `X-RateLimit-Limit` / `Retry-After` are already set when the callback runs (read or override via `c.Writer.Header()`)
- `RequestSize`: covers the Content-Length fast-reject path only — on chunked transfers the 413 surfaced by `http.MaxBytesReader` is emitted by the downstream handler that reads the body
- `Timeout`: covers the 504 timeout path only — the panic-recovery 500 response is written through the worker goroutine's buffered writer and keeps the default body

## Examples

Runnable demos live in [`examples/`](./examples) — one subdirectory per example, as a separate Go module wired to the repo root via `replace`.

Available: `apikey`, `autoindex`, `combined`, `compression`, `ipfilter`, `jwt`, `ratelimiter_global`, `ratelimiter_ip`, `requestid`, `requestsize`, `security`, `timeout`.

```bash
cd examples
go build ./...     # build every example
go run ./timeout   # run one example
# or from inside an example directory:
cd timeout && go run .
```

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
