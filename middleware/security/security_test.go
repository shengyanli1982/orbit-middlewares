package security

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestSecurityHeaders_DefaultConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(New(DefaultConfig()))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "DENY", recorder.Header().Get("X-Frame-Options"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "max-age=31536000; includeSubDomains", recorder.Header().Get("Strict-Transport-Security"))
	assert.Equal(t, "default-src 'self'", recorder.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "1; mode=block", recorder.Header().Get("X-XSS-Protection"))
	assert.Equal(t, "strict-origin-when-cross-origin", recorder.Header().Get("Referrer-Policy"))
	assert.Equal(t, "geolocation=(), microphone=(), camera=()", recorder.Header().Get("Permissions-Policy"))
}

func TestSecurityHeaders_StrictConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(New(StrictConfig()))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "DENY", recorder.Header().Get("X-Frame-Options"))
	assert.Equal(t, "max-age=63072000; includeSubDomains; preload", recorder.Header().Get("Strict-Transport-Security"))
	assert.Equal(t, "default-src 'none'; script-src 'none'; object-src 'none'", recorder.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "no-referrer", recorder.Header().Get("Referrer-Policy"))
	assert.Contains(t, recorder.Header().Get("Permissions-Policy"), "payment=()")
}

func TestSecurityHeaders_LaxConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(New(LaxConfig()))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "SAMEORIGIN", recorder.Header().Get("X-Frame-Options"))
	assert.Equal(t, "default-src 'self' 'unsafe-inline' 'unsafe-eval'", recorder.Header().Get("Content-Security-Policy"))
	assert.NotEqual(t, "", recorder.Header().Get("X-Content-Type-Options"))
}

func TestSecurityHeaders_CustomConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		XFrameOptions:       "SAMEORIGIN",
		XContentTypeOptions: "nosniff",
		HSTSMaxAge:          86400,
		CSP:                 "default-src 'self'; script-src 'self' https://cdn.example.com",
		XSSProtection:       "0",
		ReferrerPolicy:      "no-referrer",
		PermissionsPolicy:   "geolocation=(self)",
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "SAMEORIGIN", recorder.Header().Get("X-Frame-Options"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "max-age=86400", recorder.Header().Get("Strict-Transport-Security"))
	assert.Equal(t, "default-src 'self'; script-src 'self' https://cdn.example.com", recorder.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "0", recorder.Header().Get("X-XSS-Protection"))
	assert.Equal(t, "no-referrer", recorder.Header().Get("Referrer-Policy"))
	assert.Equal(t, "geolocation=(self)", recorder.Header().Get("Permissions-Policy"))
}

func TestSecurityHeaders_HSTSOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		maxAge     int64
		includeSub bool
		preload    bool
		expected   string
	}{
		{"Basic", 31536000, false, false, "max-age=31536000"},
		{"WithSubDomains", 31536000, true, false, "max-age=31536000; includeSubDomains"},
		{"WithPreload", 31536000, false, true, "max-age=31536000; preload"},
		{"Full", 63072000, true, true, "max-age=63072000; includeSubDomains; preload"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				HSTSMaxAge:            tt.maxAge,
				HSTSIncludeSubDomains: tt.includeSub,
				HSTSPreload:           tt.preload,
			}

			router := gin.New()
			router.Use(New(cfg))
			router.GET("/test", func(c *gin.Context) {
				c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			assert.Equal(t, tt.expected, recorder.Header().Get("Strict-Transport-Security"))
		})
	}
}

func TestSecurityHeaders_NoHSTS(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		XFrameOptions:       "DENY",
		XContentTypeOptions: "nosniff",
		HSTSMaxAge:          0,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "DENY", recorder.Header().Get("X-Frame-Options"))
	assert.Equal(t, "", recorder.Header().Get("Strict-Transport-Security"))
}

func TestSecurityHeaders_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		XFrameOptions: "DENY",
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/skip"
		},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/skip", func(c *gin.Context) {
		c.String(http.StatusOK, "skipped")
	})
	router.GET("/normal", func(c *gin.Context) {
		c.String(http.StatusOK, "normal")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/skip", nil)
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)
	assert.Equal(t, "", recorder1.Header().Get("X-Frame-Options"))

	req2 := httptest.NewRequest(http.MethodGet, "/normal", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusOK, recorder2.Code)
	assert.Equal(t, "DENY", recorder2.Header().Get("X-Frame-Options"))
}

func TestSecurityHeaders_EmptyConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(New(Config{}))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "", recorder.Header().Get("X-Frame-Options"))
	assert.Equal(t, "", recorder.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "", recorder.Header().Get("Strict-Transport-Security"))
	assert.Equal(t, "default-src 'self'", recorder.Header().Get("Content-Security-Policy"))
}

func TestSecurityHeaders_PartialConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		XFrameOptions: "DENY",
		CSP:           "default-src 'self'",
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "DENY", recorder.Header().Get("X-Frame-Options"))
	assert.Equal(t, "", recorder.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "", recorder.Header().Get("Strict-Transport-Security"))
	assert.Equal(t, "default-src 'self'", recorder.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "", recorder.Header().Get("X-XSS-Protection"))
}
