package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// benchWriter 是基准专用的干净状态 ResponseWriter：Header 返回每轮由 reset 清空的
// map，Write 丢弃响应体，WriteHeader 记录状态码。复用 recorder 且从不 Reset 会让
// Body 逐轮无界增长，常驻大 buffer 抬高 GC heap goal、抑制 GC 触发，令基准跨版本
// 对比失真；本 writer 零 body 驻留，脚手架开销可忽略。先例与实测依据见 autoindex
// 基准的 benchWriter 注释。
type benchWriter struct {
	header http.Header
	status int
}

// newBenchWriter 创建预分配 header map 的 benchWriter。
func newBenchWriter() *benchWriter {
	return &benchWriter{header: make(http.Header, 8)}
}

// reset 把 writer 恢复到「本轮尚未写过任何内容」的干净状态。
func (w *benchWriter) reset() {
	clear(w.header)
	w.status = 0
}

// Header 返回可变的响应头 map（每轮由 reset 清空，不跨轮残留）。
func (w *benchWriter) Header() http.Header { return w.header }

// Write 丢弃响应体内容，仅返回成功（零 body 驻留）。
func (w *benchWriter) Write(p []byte) (int, error) { return len(p), nil }

// WriteHeader 记录状态码；与 http.ResponseWriter 一致，首次写入后不再改变。
func (w *benchWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

// resetRecorder 把 recorder 恢复到本轮干净状态：清空 Body 防逐轮累积，清空响应头
// 防跨轮残留。标准库 ResponseRecorder 无 Reset 方法，但 Body 与 HeaderMap 均为导出
// 字段，直接复位即可消除无界增长；未导出的 wroteHeader 自第 2 轮起保持 true，仅
// 跳过 recorder 自身的记录逻辑（基准不断言响应内容），不改变被测调用路径。
func resetRecorder(rw *httptest.ResponseRecorder) {
	rw.Body.Reset()
	clear(rw.Header())
}

func BenchmarkJWTAuth_ValidToken(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := JWTAuthConfig{
		Secret: []byte("test-secret"),
	}

	router := gin.New()
	router.Use(JWTAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"foo": "bar",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tokenString, _ := token.SignedString(cfg.Secret)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+tokenString)
		recorder := httptest.NewRecorder() // 每轮 Reset，防 Body 跨轮累积抬高 GC heap goal
		for pb.Next() {
			resetRecorder(recorder)
			router.ServeHTTP(recorder, req)
		}
	})
}

func BenchmarkJWTAuth_InvalidToken(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := JWTAuthConfig{
		Secret: []byte("test-secret"),
	}

	router := gin.New()
	router.Use(JWTAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer invalid.token.here")
		w := newBenchWriter() // 拒绝路径每轮干净 writer，防 Body 无界增长抬高 GC heap goal
		for pb.Next() {
			w.reset()
			router.ServeHTTP(w, req)
		}
	})
}

func BenchmarkJWTAuth_MissingHeader(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := JWTAuthConfig{
		Secret: []byte("test-secret"),
	}

	router := gin.New()
	router.Use(JWTAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		w := newBenchWriter() // 拒绝路径每轮干净 writer，防 Body 无界增长抬高 GC heap goal
		for pb.Next() {
			w.reset()
			router.ServeHTTP(w, req)
		}
	})
}

func BenchmarkJWTAuth_MemAllocation(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := JWTAuthConfig{
		Secret: []byte("test-secret"),
	}

	router := gin.New()
	router.Use(JWTAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"foo": "bar",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tokenString, _ := token.SignedString(cfg.Secret)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+tokenString)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}
}

func BenchmarkAPIKeyAuth_LinearSearch(b *testing.B) {
	gin.SetMode(gin.TestMode)

	apiKeys := make([]string, 100)
	for i := 0; i < 100; i++ {
		apiKeys[i] = fmt.Sprintf("api-key-%d", i)
	}

	cfg := APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    apiKeys,
	}

	router := gin.New()
	router.Use(APIKeyAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-API-Key", "api-key-50")
		recorder := httptest.NewRecorder() // 每轮 Reset，防 Body 跨轮累积抬高 GC heap goal
		for pb.Next() {
			resetRecorder(recorder)
			router.ServeHTTP(recorder, req)
		}
	})
}

func BenchmarkAPIKeyAuth_NoMatch(b *testing.B) {
	gin.SetMode(gin.TestMode)

	apiKeys := make([]string, 100)
	for i := 0; i < 100; i++ {
		apiKeys[i] = fmt.Sprintf("api-key-%d", i)
	}

	cfg := APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    apiKeys,
	}

	router := gin.New()
	router.Use(APIKeyAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-API-Key", "invalid-key")
		w := newBenchWriter() // 拒绝路径每轮干净 writer，防 Body 无界增长抬高 GC heap goal
		for pb.Next() {
			w.reset()
			router.ServeHTTP(w, req)
		}
	})
}

func BenchmarkAPIKeyAuth_MemAllocation(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    []string{"key1", "key2", "key3"},
	}

	router := gin.New()
	router.Use(APIKeyAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-API-Key", "key1")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}
}

// BenchmarkAPIKeyAuth_MapLookupOnly 度量 100 key 规模下 map 查找的孤立成本
// （实现已由线性扫描切换为 map，原 LinearSearchOnly 合成基准度量的是
// 已不存在的算法，予以替换）。
func BenchmarkAPIKeyAuth_MapLookupOnly(b *testing.B) {
	apiKeys := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		apiKeys[fmt.Sprintf("api-key-%d", i)] = struct{}{}
	}

	key := "api-key-50"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = apiKeys[key]
	}
}

// BenchmarkAPIKeyAuth_QueryParam 覆盖 Query 优先提取路径：
// c.Query → initQueryCache → URL.Query() 每请求解析查询串（含 map 与
// 字符串分配），是 Header 路径没有的成本，配置 QueryParam 的部署
// （webhook 等场景）每请求都会支付。
func BenchmarkAPIKeyAuth_QueryParam(b *testing.B) {
	gin.SetMode(gin.TestMode)

	cfg := APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		QueryParam: "api_key",
		APIKeys:    []string{"key1", "key2", "key3"},
	}

	router := gin.New()
	router.Use(APIKeyAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/test?api_key=key1", nil)
		recorder := httptest.NewRecorder() // 每轮 Reset，防 Body 跨轮累积抬高 GC heap goal
		for pb.Next() {
			resetRecorder(recorder)
			router.ServeHTTP(recorder, req)
		}
	})
}
