package requestid

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.Use(RequestID())
	router.GET("/test", func(c *gin.Context) {
		requestID := c.GetString("request_id")
		c.String(http.StatusOK, requestID)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.NotEmpty(t, recorder.Body.String())
	assert.NotEmpty(t, recorder.Header().Get("X-Request-ID"))
}

func TestRequestID_HeaderExists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.Use(RequestID())
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.NotEmpty(t, recorder.Header().Get("X-Request-ID"))
}

func TestRequestID_CustomHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.Use(RequestID(WithHeaderName("X-Custom-ID")))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.NotEmpty(t, recorder.Header().Get("X-Custom-ID"))
}

func TestRequestID_Skipper(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.Use(RequestID(WithSkipper(func(c *gin.Context) bool {
		return c.Request.URL.Path == "/skip"
	})))
	router.GET("/skip", func(c *gin.Context) {
		_, exists := c.Get("request_id")
		assert.False(t, exists)
		c.String(http.StatusOK, "skipped")
	})
	router.GET("/normal", func(c *gin.Context) {
		_, exists := c.Get("request_id")
		assert.True(t, exists)
		c.String(http.StatusOK, "normal")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/skip", nil)
	recorder1 := httptest.NewRecorder()
	router.ServeHTTP(recorder1, req1)
	assert.Equal(t, http.StatusOK, recorder1.Code)
	assert.Empty(t, recorder1.Header().Get("X-Request-ID"))

	req2 := httptest.NewRequest(http.MethodGet, "/normal", nil)
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusOK, recorder2.Code)
	assert.NotEmpty(t, recorder2.Header().Get("X-Request-ID"))
}
