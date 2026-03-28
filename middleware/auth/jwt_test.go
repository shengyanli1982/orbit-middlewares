package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
)

var testSecret = []byte("test-secret-key")

func generateTestToken(secret []byte, claims jwt.MapClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString(secret)
	return tokenString
}

func TestJWTAuth_ValidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := JWTAuthConfig{
		Secret: testSecret,
	}

	router := gin.New()
	router.Use(JWTAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	token := generateTestToken(testSecret, jwt.MapClaims{
		"sub": "user123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestJWTAuth_MissingToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := JWTAuthConfig{
		Secret: testSecret,
	}

	router := gin.New()
	router.Use(JWTAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "[401]")
}

func TestJWTAuth_InvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := JWTAuthConfig{
		Secret: testSecret,
	}

	router := gin.New()
	router.Use(JWTAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "[401]")
}

func TestJWTAuth_ExpiredToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := JWTAuthConfig{
		Secret: testSecret,
	}

	router := gin.New()
	router.Use(JWTAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	token := generateTestToken(testSecret, jwt.MapClaims{
		"sub": "user123",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestJWTAuth_WrongSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := JWTAuthConfig{
		Secret: testSecret,
	}

	router := gin.New()
	router.Use(JWTAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	token := generateTestToken([]byte("wrong-secret"), jwt.MapClaims{
		"sub": "user123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestJWTAuth_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := JWTAuthConfig{
		Secret: testSecret,
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/public"
		},
	}

	router := gin.New()
	router.Use(JWTAuth(cfg))
	router.GET("/public", func(c *gin.Context) {
		c.String(http.StatusOK, "public")
	})
	router.GET("/protected", func(c *gin.Context) {
		c.String(http.StatusOK, "protected")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/public", nil)
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/protected", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusUnauthorized, recorder2.Code)
}
