package ipfilter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestIPLimiter_AllowedIP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		AllowedIPs: []string{"192.168.1.1"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestIPLimiter_BlockedIP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		BlockedIPs: []string{"192.168.1.1"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "[403]")
}

func TestIPLimiter_NoRestrictions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestIPLimiter_BlockedFirst(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		AllowedIPs: []string{"192.168.1.1"},
		BlockedIPs: []string{"192.168.1.1"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestIPLimiter_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		BlockedIPs: []string{"192.168.1.1"},
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
	req1.RemoteAddr = "192.168.1.1:1234"
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/normal", nil)
	req2.RemoteAddr = "192.168.1.1:1234"
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusForbidden, recorder2.Code)
}
