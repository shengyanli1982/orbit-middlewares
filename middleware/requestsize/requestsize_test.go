package requestsize

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRequestSizeLimiter_SmallBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 100,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.POST("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	body := strings.NewReader("small body")
	req := httptest.NewRequest(http.MethodPost, "/test", body)
	req.Header.Set("Content-Type", "application/octet-stream")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestRequestSizeLimiter_LargeBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 10,
	}

	router := gin.New()
	router.Use(New(cfg))
	router.POST("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	body := strings.NewReader("this body is too large")
	req := httptest.NewRequest(http.MethodPost, "/test", body)
	req.Header.Set("Content-Type", "application/octet-stream")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "[413]")
}

func TestRequestSizeLimiter_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		MaxSize: 10,
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/skip"
		},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.POST("/skip", func(c *gin.Context) {
		c.String(http.StatusOK, "skipped")
	})
	router.POST("/normal", func(c *gin.Context) {
		c.String(http.StatusOK, "normal")
	})

	body1 := strings.NewReader("this body is too large")
	req1 := httptest.NewRequest(http.MethodPost, "/skip", body1)
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)

	body2 := strings.NewReader("this body is too large")
	req2 := httptest.NewRequest(http.MethodPost, "/normal", body2)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder2.Code)
}
