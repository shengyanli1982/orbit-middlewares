package ipfilter

import (
	"fmt"
	"net"
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

func TestIPLimiter_AllowedCIDR(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		AllowedIPs: []string{"192.168.1.0/24"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// 在 CIDR 范围内，应该允许
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.100:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusOK, recorder.Code)

	// 不在 CIDR 范围内，应该拒绝
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "192.168.2.1:1234"
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusForbidden, recorder2.Code)
}

func TestIPLimiter_BlockedCIDR(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		BlockedIPs: []string{"10.0.0.0/8"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// 在黑名单 CIDR 范围内，应该拒绝
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "10.1.2.3:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "[403]")

	// 不在黑名单 CIDR 范围内，应该允许
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "192.168.1.1:1234"
	recorder2 := httptest.NewRecorder()
	router.ServeHTTP(recorder2, req2)
	assert.Equal(t, http.StatusOK, recorder2.Code)
}

func TestIPLimiter_MixedExactAndCIDR(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		AllowedIPs: []string{"192.168.1.0/24", "10.0.0.1"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	cases := []struct {
		remoteAddr string
		wantCode   int
	}{
		{"192.168.1.50:1234", http.StatusOK},    // 在 CIDR 内
		{"10.0.0.1:1234", http.StatusOK},        // 精确匹配
		{"10.0.0.2:1234", http.StatusForbidden}, // 不在白名单
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = tc.remoteAddr
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, tc.wantCode, rec.Code, "remoteAddr=%s", tc.remoteAddr)
	}
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

// TestNewIPSet_PanicsOnInvalidEntries AUD-10：非法 CIDR/IP 条目必须在构造时 panic（fail-fast），
// 禁止静默跳过——否则黑名单笔误（如 "10.0.0.0/33"）会被无声吞掉，安全配置 fail-open。
// panic 消息必须包含具体条目，便于定位配置错误。
func TestNewIPSet_PanicsOnInvalidEntries(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		// msgContains 可选：panic 消息除条目本身外还必须包含的文案。
		msgContains string
	}{
		{"CIDR前缀长度越界", "10.0.0.0/33", ""},
		{"CIDR掩码非数字", "10.0.0.0/abc", ""},
		{"CIDR多余段", "192.168.1.0/24/extra", ""},
		{"非法IPv4", "999.999.999.999", ""},
		{"非IP字符串", "not-an-ip", ""},
		{"空条目", "", ""},
		{"IPv6 zone 地址", "fe80::1%eth0", "zone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if !assert.NotNil(t, r, "newIPSet(%q) 应该 panic 而不是静默跳过", tt.entry) {
					return
				}
				err, ok := r.(error)
				if assert.True(t, ok, "panic 值应为 error 类型，实际为 %T", r) {
					assert.Contains(t, err.Error(), fmt.Sprintf("%q", tt.entry),
						"panic 消息应包含具体条目")
					if tt.msgContains != "" {
						assert.Contains(t, err.Error(), tt.msgContains,
							"panic 消息应包含 %q 相关文案", tt.msgContains)
					}
				}
			}()
			newIPSet([]string{tt.entry})
		})
	}
}

// TestNewIPSet_NormalizesExactIPs AUD-10：精确 IP 条目必须经 net.ParseIP 归一化后入集合，
// 与 c.ClientIP() 的规范化输出（小写、压缩形式）保持一致，否则大写 IPv6 永不匹配。
func TestNewIPSet_NormalizesExactIPs(t *testing.T) {
	s := newIPSet([]string{"ABCD::1", "2001:0DB8::0001", "192.168.1.1"})

	for _, want := range []string{"abcd::1", "2001:db8::1", "192.168.1.1"} {
		_, ok := s.exactIPs[want]
		assert.True(t, ok, "归一化条目 %q 应存在于集合中", want)
	}
	assert.Len(t, s.exactIPs, 3, "不同写法归一化后不应产生重复或遗漏")
}

// TestIPLimiter_BlockedIPv6UppercaseConfig 端到端：配置大写 IPv6 黑名单，
// 客户端以规范化小写形式（gin ClientIP 输出）到达时仍应被拦截。
func TestIPLimiter_BlockedIPv6UppercaseConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		BlockedIPs: []string{"ABCD::1"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "[abcd::1]:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "[403]")
}

// TestIPLimiter_AllowedIPv6UppercaseConfig 端到端：配置大写 IPv6 白名单，
// 客户端 IPv6 地址归一化后仍应匹配白名单被放行。
func TestIPLimiter_AllowedIPv6UppercaseConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := Config{
		AllowedIPs: []string{"2001:DB8::1"},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// RemoteAddr 为大写写法，gin ClientIP() 会归一化为小写
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "[2001:0db8::0001]:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

// TestNewIPSet_NormalizationMatchesNetParseIP T3：切换 net/netip 后，精确条目的
// 归一化 key 必须与 net.ParseIP(entry).String() 逐字一致（含 4-in-6 映射地址
// "::ffff:1.2.3.4" → "1.2.3.4"），否则既有配置会静默失配。
func TestNewIPSet_NormalizationMatchesNetParseIP(t *testing.T) {
	entries := []string{
		"192.168.1.1", "10.0.0.1", "0.0.0.0", "255.255.255.255",
		"::1", "::", "ABCD::1", "2001:0DB8::0001", "2001:db8:0:0:1:0:0:1",
		"::ffff:192.168.1.1", "::ffff:0:0", "fe80::1",
	}
	for _, entry := range entries {
		want := net.ParseIP(entry)
		if !assert.NotNil(t, want, "测试前提：%q 应为合法 IP", entry) {
			continue
		}
		s := newIPSet([]string{entry})
		_, ok := s.exactIPs[want.String()]
		assert.True(t, ok, "条目 %q 的集合 key 应等于 net.ParseIP 归一化输出 %q", entry, want.String())
		assert.Len(t, s.exactIPs, 1, "条目 %q 应恰好产生一个归一化 key", entry)
	}
}

// TestNewIPSet_CIDRNormalizationParity T3：netip.ParsePrefix 原样保留 host bits
// 与 4-in-6 前缀，构造期必须归一对齐 net.ParseCIDR 旧语义，防止匹配行为回归。
func TestNewIPSet_CIDRNormalizationParity(t *testing.T) {
	// host bits 非零：旧 net.ParseCIDR("10.0.1.5/8") → network 10.0.0.0/8
	s := newIPSet([]string{"10.0.1.5/8"})
	assert.True(t, s.contains("10.9.9.9"), "host bits 条目应按掩码后网络 10.0.0.0/8 匹配")
	assert.False(t, s.contains("11.0.0.1"))

	// 4-in-6 前缀：旧实现对 v4 客户端等效 10.0.0.0/8（掩码截取末 4 字节）
	s2 := newIPSet([]string{"::ffff:10.0.0.0/104"})
	assert.True(t, s2.contains("10.1.2.3"), "4-in-6 前缀应归一为等价 IPv4 前缀并匹配 v4 客户端")
	assert.False(t, s2.contains("192.168.1.1"))
}

// TestIPSet_Contains_4in6ClientIP T3：4-in-6 形式的客户端 IP（可信代理转发头
// 可能出现）经 Unmap 后应仍匹配 v4 前缀，与旧 net.ParseIP 行为一致；
// 非法 IP 字符串不匹配任何条目。
func TestIPSet_Contains_4in6ClientIP(t *testing.T) {
	s := newIPSet([]string{"10.0.0.0/8"})
	assert.True(t, s.contains("::ffff:10.1.2.3"))
	assert.False(t, s.contains("::ffff:192.168.1.1"))
	assert.False(t, s.contains("not-an-ip"))
}

// TestIPFilter_RejectHandler_CustomResponse 钉住统一拒绝契约：
// 自定义 RejectHandler 在黑名单/白名单两条拒绝路径均生效（状态码+响应体），
// 且回调收到对应的 reason 常量。
func TestIPFilter_RejectHandler_CustomResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		cfg        Config
		remoteAddr string
		wantReason string
	}{
		{
			name:       "黑名单命中收到 ReasonBlocked",
			cfg:        Config{BlockedIPs: []string{"192.168.1.1"}},
			remoteAddr: "192.168.1.1:1234",
			wantReason: ReasonBlocked,
		},
		{
			name:       "白名单未命中收到 ReasonNotAllowed",
			cfg:        Config{AllowedIPs: []string{"10.0.0.1"}},
			remoteAddr: "192.168.1.1:1234",
			wantReason: ReasonNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReason string
			tt.cfg.RejectHandler = func(c *gin.Context, reason string) {
				gotReason = reason
				c.JSON(http.StatusTeapot, gin.H{"reason": reason})
			}

			router := gin.New()
			router.Use(New(tt.cfg))
			router.GET("/test", func(c *gin.Context) {
				c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = tt.remoteAddr
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusTeapot, recorder.Code, "回调写入的状态码应生效")
			assert.JSONEq(t, `{"reason":"`+tt.wantReason+`"}`, recorder.Body.String(),
				"回调写入的响应体应生效")
			assert.Equal(t, tt.wantReason, gotReason, "回调应收到正确 reason")
		})
	}
}

// TestIPFilter_RejectHandler_AbortEnforced 钉住统一拒绝契约：
// 回调内不调用 Abort，框架仍无条件 Abort，后续路由处理器不得执行。
func TestIPFilter_RejectHandler_AbortEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)

	routeHits := 0
	cfg := Config{
		BlockedIPs: []string{"192.168.1.1"},
		RejectHandler: func(c *gin.Context, reason string) {
			// 故意不调用 c.Abort()
			c.String(http.StatusForbidden, "rejected by callback: "+reason)
		},
	}

	router := gin.New()
	router.Use(New(cfg))
	router.GET("/test", func(c *gin.Context) {
		routeHits++
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Equal(t, "rejected by callback: ip.blocked", recorder.Body.String())
	assert.Equal(t, 0, routeHits, "拒绝后框架必须 Abort，路由处理器不得执行")
}

// TestIPFilter_RejectHandler_NilUsesDefault 钉住统一拒绝契约：
// 未配置 RejectHandler 时，两条拒绝路径保持默认文本响应不变。
func TestIPFilter_RejectHandler_NilUsesDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		cfg      Config
		wantBody string
	}{
		{
			name:     "黑名单默认响应",
			cfg:      Config{BlockedIPs: []string{"192.168.1.1"}},
			wantBody: "[403] ip blocked",
		},
		{
			name:     "白名单默认响应",
			cfg:      Config{AllowedIPs: []string{"10.0.0.1"}},
			wantBody: "[403] ip not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(New(tt.cfg))
			router.GET("/test", func(c *gin.Context) {
				c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = "192.168.1.1:1234"
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusForbidden, recorder.Code)
			assert.Equal(t, tt.wantBody, recorder.Body.String())
		})
	}
}
