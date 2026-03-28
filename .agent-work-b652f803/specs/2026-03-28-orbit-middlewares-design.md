# orbit-middlewares 设计规格

## 概述

为 `github.com/shengyanli1982/orbit` 提供高性能、通用的 Gin 中间件库。这些中间件都是常见且使用频率很高的，长期稳定使用，因此内存和执行效率是核心考量。

## 设计原则

1. **高性能** - sync.Pool 复用、atomic 无锁操作、零拷贝配置
2. **低依赖** - 最小化外部依赖，只引入必要的生产级库
3. **一致性** - 错误返回格式与 orbit 现有风格保持一致
4. **可测试** - 完整的测试覆盖，TDD 开发方法

## 中间件清单

| # | 中间件 | 功能 | 核心依赖 |
|---|--------|------|----------|
| 1 | RequestID | 生成唯一请求ID，写入 Header | 标准库 |
| 2 | RateLimiter | Token Bucket 限流算法 | 标准库 |
| 3 | Timeout | 请求超时控制 | context |
| 4 | JWTAuth | JWT Bearer 认证 | golang-jwt/jwt/v5 |
| 5 | APIKeyAuth | API Key 认证 | 标准库 |
| 6 | RequestSizeLimiter | 请求体大小限制 | 标准库 |
| 7 | IPLimiter | IP 限流/白名单 | 标准库 |

## 目录结构

```
orbit-middlewares/
├── go.mod
├── middleware/
│   ├── requestid/          # 请求ID生成
│   ├── ratelimiter/        # Token Bucket 限流
│   ├── timeout/            # 超时控制
│   ├── auth/
│   │   ├── jwt.go          # JWT 认证
│   │   └── apikey.go       # API Key 认证
│   ├── requestsize/        # 请求大小限制
│   └── iplimiter/          # IP 限流/白名单
└── pool/                    # 共享 sync.Pool
```

## 错误返回格式（与 orbit 一致）

orbit 使用字符串格式返回错误，中间件保持一致：

```go
// 401 未授权
context.String(http.StatusUnauthorized, "[401] unauthorized, reason: invalid token")

// 429 限流
context.String(http.StatusTooManyRequests, "[429] rate limit exceeded")

// 504 超时
context.String(http.StatusGatewayTimeout, "[504] request timeout")
```

## 技术规范

### 依赖

```go
require (
    github.com/gin-gonic/gin              // HTTP 框架
    github.com/golang-jwt/jwt/v5          // JWT 认证
    github.com/stretchr/testify           // 测试断言
    go.uber.org/zap                       // 高性能日志
)
```

### 中间件接口

所有中间件遵循 Gin 标准接口：

```go
type MiddlewareFunc func(*gin.Context)

// 配置结构
type Config struct {
    Skipper func(*gin.Context) bool  // 可选跳过函数
    // ... 其他配置
}

func New(cfg Config) gin.HandlerFunc {
    return func(c *gin.Context) {
        if cfg.Skipper != nil && cfg.Skipper(c) {
            c.Next()
            return
        }
        // 处理逻辑
    }
}
```

### 测试规范（与 orbit 一致）

```go
func TestRateLimiter(t *testing.T) {
    router := gin.New()
    router.Use(RateLimiter(cfg))
    router.GET("/test", handler)
    
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    recorder := httptest.NewRecorder()
    router.ServeHTTP(recorder, req)
    
    assert.Equal(t, http.StatusOK, recorder.Code)
}
```

## 性能优化策略

1. **sync.Pool** - 预分配 buffer，复用对象减少 GC
2. **atomic** - 无锁计数器，减少锁竞争
3. **只读配置** - 启动时验证，运行时不复制
4. **strings.Builder** - 字符串拼接减少内存分配
5. **避免 []byte -> string 转换** - 减少内存分配

## 开发流程

使用 TDD 方法：
1. **RED** - 编写会失败的测试
2. **GREEN** - 最小实现使测试通过
3. **REFACTOR** - 重构优化代码

## 各中间件详细设计

### 1. RequestID

- 生成唯一 ID（UUID 或 时间戳+随机数）
- 写入 `X-Request-ID` Header
- 存储到 `c.Get("request_id")` 供后续使用

### 2. RateLimiter

- **算法**: Token Bucket
- **配置**: QPS、突发容量、Key 提取器
- **存储**: 内存（可扩展到 Redis）
- **错误**: `[429] rate limit exceeded`

### 3. Timeout

- **实现**: context.WithTimeout
- **配置**: 超时时长
- **错误**: `[504] request timeout`

### 4. JWTAuth

- **算法**: RS256/HS256
- **配置**: Key、Claims 验证器、Skipper
- **错误**: `[401] unauthorized, reason: invalid token`

### 5. APIKeyAuth

- **位置**: Header/QueryParam
- **配置**: Key 名称、验证函数
- **错误**: `[401] unauthorized, reason: invalid api key`

### 6. RequestSizeLimiter

- **配置**: 最大请求体大小
- **错误**: `[413] request body too large`

### 7. IPLimiter

- **功能**: 白名单/黑名单/限流
- **配置**: IP 列表、限流阈值
- **错误**: `[403] ip blocked` 或 `[429] ip rate limit exceeded`

## 验证标准

- [ ] 所有中间件通过测试
- [ ] benchmark 测试验证性能
- [ ] 错误格式与 orbit 一致
- [ ] 代码风格与 orbit 一致
