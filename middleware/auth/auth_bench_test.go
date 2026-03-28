package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/pprof"
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

	f, err := os.Create("jwt_valid_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

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

	f, err := os.Create("jwt_invalid_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

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

	f, err := os.Create("jwt_missing_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

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

	f, err := os.Create("jwt_mem.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+tokenString)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}

	runtime.GC()
	pprof.WriteHeapProfile(f)
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

	f, err := os.Create("apikey_linear_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

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

	f, err := os.Create("apikey_nomatch_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

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

	f, err := os.Create("apikey_mem.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-API-Key", "key1")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
	}

	runtime.GC()
	pprof.WriteHeapProfile(f)
}

func BenchmarkAPIKeyAuth_LinearSearchOnly(b *testing.B) {
	apiKeys := make([]string, 100)
	for i := 0; i < 100; i++ {
		apiKeys[i] = fmt.Sprintf("api-key-%d", i)
	}

	key := "api-key-50"

	f, err := os.Create("apikey_searchonly_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, k := range apiKeys {
			if k == key {
				break
			}
		}
	}
}
