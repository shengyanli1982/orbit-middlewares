package timeout

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestTimeout_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Timeout: 100 * time.Millisecond,
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
	assert.Equal(t, "ok", recorder.Body.String())
}

func TestTimeout_Exceeded(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Timeout: 50 * time.Millisecond,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		time.Sleep(100 * time.Millisecond)
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusGatewayTimeout, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "[504]")
}

func TestTimeout_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		Timeout: 50 * time.Millisecond,
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/skip"
		},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/skip", func(c *gin.Context) {
		time.Sleep(100 * time.Millisecond)
		c.String(http.StatusOK, "skipped")
	})
	router.GET("/normal", func(c *gin.Context) {
		c.String(http.StatusOK, "normal")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/skip", nil)
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/normal", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusOK, recorder2.Code)
}
