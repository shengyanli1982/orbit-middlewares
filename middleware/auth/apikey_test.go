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

// TestAPIKeyAuth_RejectHandler_CustomResponse 钉住统一拒绝契约：
// 自定义 RejectHandler 在缺 key / key 无效两条拒绝路径均生效
// （状态码+响应体），且回调收到对应的 reason 常量。
func TestAPIKeyAuth_RejectHandler_CustomResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		apiKey     string
		wantReason string
	}{
		{
			name:       "缺 key 收到 ReasonMissingAPIKey",
			wantReason: ReasonMissingAPIKey,
		},
		{
			name:       "无效 key 收到 ReasonInvalidAPIKey",
			apiKey:     "wrong-key",
			wantReason: ReasonInvalidAPIKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReason string
			cfg := APIKeyAuthConfig{
				HeaderName: "X-API-Key",
				APIKeys:    []string{"valid-key"},
				RejectHandler: func(c *gin.Context, reason string) {
					gotReason = reason
					c.JSON(http.StatusTeapot, gin.H{"reason": reason})
				},
			}

			router := gin.New()
			router.Use(APIKeyAuth(cfg))
			router.GET("/test", func(c *gin.Context) {
				c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.apiKey != "" {
				req.Header.Set("X-API-Key", tt.apiKey)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusTeapot, recorder.Code, "回调写入的状态码应生效")
			assert.JSONEq(t, `{"reason":"`+tt.wantReason+`"}`, recorder.Body.String(),
				"回调写入的响应体应生效")
			assert.Equal(t, tt.wantReason, gotReason, "回调应收到正确 reason")
		})
	}
}

// TestAPIKeyAuth_RejectHandler_AbortEnforced 钉住统一拒绝契约：
// 回调内不调用 Abort，框架仍无条件 Abort，后续路由处理器不得执行。
func TestAPIKeyAuth_RejectHandler_AbortEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)

	routeHits := 0
	cfg := APIKeyAuthConfig{
		HeaderName: "X-API-Key",
		APIKeys:    []string{"valid-key"},
		RejectHandler: func(c *gin.Context, reason string) {
			// 故意不调用 c.Abort()
			c.String(http.StatusUnauthorized, "rejected by callback: "+reason)
		},
	}

	router := gin.New()
	router.Use(APIKeyAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		routeHits++
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Equal(t, "rejected by callback: auth.apikey_missing", recorder.Body.String())
	assert.Equal(t, 0, routeHits, "拒绝后框架必须 Abort，路由处理器不得执行")
}

// TestAPIKeyAuth_RejectHandler_NilUsesDefault 钉住统一拒绝契约：
// 未配置 RejectHandler 时，两条拒绝路径保持默认文本响应不变。
func TestAPIKeyAuth_RejectHandler_NilUsesDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		apiKey   string
		wantBody string
	}{
		{
			name:     "缺 key 默认响应",
			wantBody: "[401] unauthorized, reason: missing api key",
		},
		{
			name:     "无效 key 默认响应",
			apiKey:   "wrong-key",
			wantBody: "[401] unauthorized, reason: invalid api key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(APIKeyAuth(APIKeyAuthConfig{
				HeaderName: "X-API-Key",
				APIKeys:    []string{"valid-key"},
			}))
			router.GET("/test", func(c *gin.Context) {
				c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.apiKey != "" {
				req.Header.Set("X-API-Key", tt.apiKey)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			assert.Equal(t, tt.wantBody, recorder.Body.String())
		})
	}
}

// P2-1：APIKeys 与 Validator 均未配置时，valid 恒为 false，全请求 401 锁死。
// 无效配置必须 fail-fast，在构造期 panic，与 JWTAuth 零值校验（AUD-02）及
// 项目"无效配置构造时 panic"约定一致。
func TestAPIKeyAuth_ZeroConfigPanics(t *testing.T) {
	gin.SetMode(gin.TestMode)

	assert.Panics(t, func() {
		APIKeyAuth(APIKeyAuthConfig{})
	}, "零值配置（APIKeys 与 Validator 均为空）应在构造时 panic")

	assert.Panics(t, func() {
		APIKeyAuth(APIKeyAuthConfig{HeaderName: "X-API-Key"})
	}, "仅配置 HeaderName（APIKeys 与 Validator 均为空）应在构造时 panic")

	// 仅提供 Validator 是合法配置（APIKeys 可为空），不得被误伤
	assert.NotPanics(t, func() {
		APIKeyAuth(APIKeyAuthConfig{
			Validator: func(key string, c *gin.Context) bool { return key == "k" },
		})
	}, "仅提供 Validator 的配置不应 panic")

	// 仅提供 APIKeys 是合法配置（Validator 可为空），不得被误伤
	assert.NotPanics(t, func() {
		APIKeyAuth(APIKeyAuthConfig{APIKeys: []string{"k"}})
	}, "仅提供 APIKeys 的配置不应 panic")
}
