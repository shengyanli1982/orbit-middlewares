package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func BenchmarkJWTAuth_ValidToken(b *testing.B) {
	gin.SetMode(gin.TestMode)

	_, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		b.Fatal(err)
	}

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
		recorder := httptest.NewRecorder()
		for pb.Next() {
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
		recorder := httptest.NewRecorder()
		for pb.Next() {
			router.ServeHTTP(recorder, req)
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
		recorder := httptest.NewRecorder()
		for pb.Next() {
			router.ServeHTTP(recorder, req)
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
		recorder := httptest.NewRecorder()
		for pb.Next() {
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
		recorder := httptest.NewRecorder()
		for pb.Next() {
			router.ServeHTTP(recorder, req)
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
		recorder := httptest.NewRecorder()
		for pb.Next() {
			router.ServeHTTP(recorder, req)
		}
	})
}
