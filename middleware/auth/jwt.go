package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// bearerPrefix Bearer 认证方案前缀；RFC 7235 §2.1 规定方案名大小写不敏感。
const bearerPrefix = "Bearer "

// JWT 拒绝原因常量。值遵循全局 "<domain>.<cause>" 拒绝原因命名方案：domain（auth）
// 自证拒绝来源；auth 域含 token/apikey 两类主语，cause 带主语前缀区分，规则详见 README。
const (
	// ReasonMissingToken 请求缺少 token 拒绝的原因常量，作为 RejectHandler 的 reason 参数。
	ReasonMissingToken = "auth.token_missing"
	// ReasonInvalidToken token 验证失败拒绝的原因常量，作为 RejectHandler 的 reason 参数。
	ReasonInvalidToken = "auth.token_invalid"
)

// JWTAuthConfig JWT 认证配置。
type JWTAuthConfig struct {
	Skipper func(*gin.Context) bool
	Secret  []byte
	KeyFunc jwt.Keyfunc
	// RejectHandler 自定义拒绝时的响应逻辑（可选）。
	//
	// 契约：reason 为本包导出的拒绝原因常量；回调负责写入状态码与响应体；
	// 回调返回后框架无条件调用 c.Abort() 终止后续处理器，无论回调内部是否
	// 已自行 Abort。为 nil 时返回默认响应（c.String 文本格式 "[code] msg"）。
	RejectHandler func(c *gin.Context, reason string)
}

// JWTAuth 返回 JWT 认证中间件。
// 验证通过后将 claims 存入 context，键为 jwt_claims。
// 当 Secret 与 KeyFunc 均未配置时 panic：空密钥会使 HMAC 验签
// 对任何人自签的 token 放行（fail-open），无效配置必须在构造期暴露。
func JWTAuth(cfg JWTAuthConfig) gin.HandlerFunc {
	if cfg.KeyFunc == nil && len(cfg.Secret) == 0 {
		panic("auth: JWTAuthConfig requires either Secret or KeyFunc")
	}

	keyFunc := cfg.KeyFunc
	secret := cfg.Secret
	if keyFunc == nil {
		keyFunc = func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return secret, nil
		}
	}

	headerName := "Authorization"

	// reject 构造期捕获 cfg.RejectHandler，仅在拒绝路径调用，
	// 放行主路径零新增开销（统一拒绝契约）。
	reject := func(c *gin.Context, reason, defaultMsg string) {
		if cfg.RejectHandler != nil {
			cfg.RejectHandler(c, reason)
		} else {
			c.String(http.StatusUnauthorized, defaultMsg)
		}
		c.Abort()
	}

	return func(c *gin.Context) {
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		authHeader := c.GetHeader(headerName)
		if len(authHeader) <= len(bearerPrefix) {
			reject(c, ReasonMissingToken, "[401] unauthorized, reason: missing token")
			return
		}

		if strings.EqualFold(authHeader[:len(bearerPrefix)], bearerPrefix) {
			tokenStr := authHeader[len(bearerPrefix):]
			token, err := jwt.Parse(tokenStr, keyFunc)
			if err == nil && token.Valid {
				if claims, ok := token.Claims.(jwt.MapClaims); ok {
					c.Set("jwt_claims", claims)
					c.Next()
					return
				}
			}
		}
		reject(c, ReasonInvalidToken, "[401] unauthorized, reason: invalid token")
		return
	}
}
