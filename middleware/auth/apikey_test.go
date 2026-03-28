package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestAPIKeyAuth_ValidKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    []string{"valid-key-1", "valid-key-2"},
	}

	router := gin.New()
	router.Use(APIKeyAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-API-Key", "valid-key-1")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestAPIKeyAuth_InvalidKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    []string{"valid-key"},
	}

	router := gin.New()
	router.Use(APIKeyAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-API-Key", "invalid-key")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "[401]")
}

func TestAPIKeyAuth_MissingKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    []string{"valid-key"},
	}

	router := gin.New()
	router.Use(APIKeyAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestAPIKeyAuth_QueryParam(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := APIKeyAuthConfig{
		QueryParam: "api_key",
		APIKeys:    []string{"query-key"},
	}

	router := gin.New()
	router.Use(APIKeyAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test?api_key=query-key", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestAPIKeyAuth_CustomValidator(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		Validator: func(key string, c *gin.Context) bool {
			return key == "custom-validated-key"
		},
	}

	router := gin.New()
	router.Use(APIKeyAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-API-Key", "custom-validated-key")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestAPIKeyAuth_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    []string{"valid-key"},
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/public"
		},
	}

	router := gin.New()
	router.Use(APIKeyAuth(cfg))
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
