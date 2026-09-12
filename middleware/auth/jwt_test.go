package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// AUD-02：Secret 与 KeyFunc 均未配置（含显式空 Secret）时，
// 空密钥会使 jwt/v5 HMAC 验签对任何人自签的 token 放行（fail-open），
// 必须在构造期 panic，与项目"无效配置构造时 panic"约定一致。
func TestJWTAuth_ZeroConfigPanics(t *testing.T) {
	gin.SetMode(gin.TestMode)

	assert.Panics(t, func() {
		JWTAuth(JWTAuthConfig{})
	}, "零值配置（Secret 与 KeyFunc 均为空）应在构造时 panic")

	assert.Panics(t, func() {
		JWTAuth(JWTAuthConfig{Secret: []byte{}})
	}, "显式空 Secret 且无 KeyFunc 应在构造时 panic")

	// 自定义 KeyFunc 是合法配置（Secret 可为空），不得被误伤
	assert.NotPanics(t, func() {
		JWTAuth(JWTAuthConfig{KeyFunc: func(token *jwt.Token) (any, error) {
			return testSecret, nil
		}})
	}, "仅提供 KeyFunc 的配置不应 panic")
}

// AUD-02 PoC 回归：攻击者用空密钥 SignedString([]byte{}) 自签任意 token，
// 在任何情况下都不得通过认证。修复后零值配置在构造期即 panic（源头杜绝），
// 本测试钉住端到端安全语义。
func TestJWTAuth_ForgedEmptyKeyTokenRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	forged := generateTestToken([]byte{}, jwt.MapClaims{
		"sub": "attacker",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	authenticated := false
	func() {
		defer func() {
			// 修复后构造期 panic 即视为拒绝，安全语义满足
			recover()
		}()

		router := gin.New()
		router.Use(JWTAuth(JWTAuthConfig{}))
		router.GET("/admin", func(c *gin.Context) {
			c.String(http.StatusOK, "ADMIN-AREA")
		})

		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		req.Header.Set("Authorization", "Bearer "+forged)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)

		authenticated = recorder.Code == http.StatusOK
	}()

	assert.False(t, authenticated, "空密钥伪造 token 不得通过认证（AUD-02 PoC）")
}

// AUD-16：RFC 7235 §2.1 规定 auth-scheme 大小写不敏感，
// "bearer "/"BEARER " 等合法变体不应被 401 拒绝。
func TestJWTAuth_BearerSchemeCaseInsensitive(t *testing.T) {
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

	schemes := []string{"Bearer ", "bearer ", "BEARER ", "BeArEr "}
	for _, scheme := range schemes {
		t.Run(strings.TrimSpace(scheme), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Authorization", scheme+token)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusOK, recorder.Code, "scheme %q 应被接受", scheme)
		})
	}
}

// TestJWTAuth_RejectHandler_CustomResponse 钉住统一拒绝契约：
// 自定义 RejectHandler 在缺 token / token 无效两条拒绝路径均生效
// （状态码+响应体），且回调收到对应的 reason 常量。
func TestJWTAuth_RejectHandler_CustomResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		authHeader string
		wantReason string
	}{
		{
			name:       "缺 token 收到 ReasonMissingToken",
			wantReason: ReasonMissingToken,
		},
		{
			name:       "无效 token 收到 ReasonInvalidToken",
			authHeader: "Bearer invalid-token",
			wantReason: ReasonInvalidToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReason string
			cfg := JWTAuthConfig{
				Secret: testSecret,
				RejectHandler: func(c *gin.Context, reason string) {
					gotReason = reason
					c.JSON(http.StatusTeapot, gin.H{"reason": reason})
				},
			}

			router := gin.New()
			router.Use(JWTAuth(cfg))
			router.GET("/test", func(c *gin.Context) {
				c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
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

// TestJWTAuth_RejectHandler_AbortEnforced 钉住统一拒绝契约：
// 回调内不调用 Abort，框架仍无条件 Abort，后续路由处理器不得执行。
func TestJWTAuth_RejectHandler_AbortEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)

	routeHits := 0
	cfg := JWTAuthConfig{
		Secret: testSecret,
		RejectHandler: func(c *gin.Context, reason string) {
			// 故意不调用 c.Abort()
			c.String(http.StatusUnauthorized, "rejected by callback: "+reason)
		},
	}

	router := gin.New()
	router.Use(JWTAuth(cfg))
	router.GET("/test", func(c *gin.Context) {
		routeHits++
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Equal(t, "rejected by callback: auth.token_missing", recorder.Body.String())
	assert.Equal(t, 0, routeHits, "拒绝后框架必须 Abort，路由处理器不得执行")
}

// TestJWTAuth_RejectHandler_NilUsesDefault 钉住统一拒绝契约：
// 未配置 RejectHandler 时，两条拒绝路径保持默认文本响应不变。
func TestJWTAuth_RejectHandler_NilUsesDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		authHeader string
		wantBody   string
	}{
		{
			name:     "缺 token 默认响应",
			wantBody: "[401] unauthorized, reason: missing token",
		},
		{
			name:       "无效 token 默认响应",
			authHeader: "Bearer invalid-token",
			wantBody:   "[401] unauthorized, reason: invalid token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(JWTAuth(JWTAuthConfig{Secret: testSecret}))
			router.GET("/test", func(c *gin.Context) {
				c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			assert.Equal(t, tt.wantBody, recorder.Body.String())
		})
	}
}
