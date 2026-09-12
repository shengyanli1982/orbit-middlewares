package ipfilter

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"
)

// IP 拒绝原因常量。值遵循全局 "<domain>.<cause>" 拒绝原因命名方案：
// domain（ip）自证拒绝来源，cause 为 snake_case 状态描述，规则详见 README。
const (
	// ReasonBlocked 黑名单命中拒绝的原因常量，作为 RejectHandler 的 reason 参数。
	ReasonBlocked = "ip.blocked"
	// ReasonNotAllowed 白名单未命中拒绝的原因常量，作为 RejectHandler 的 reason 参数。
	ReasonNotAllowed = "ip.not_allowed"
)

// Config IP 过滤器配置。
//
// 安全说明（客户端 IP 信任边界）：本中间件通过 gin 的 c.ClientIP() 获取客户端 IP。
// gin 默认信任所有代理（未调用 SetTrustedProxies）且 ForwardedByClientIP 开启，
// 此时 ClientIP 会优先解析 X-Forwarded-For / X-Real-IP 等代理转发头，而这些头可被
// 客户端伪造：服务直接暴露公网时，攻击者可伪造 XFF 头绕过黑名单或冒充白名单身份。
// 部署前必须至少满足以下条件之一：
//   - 调用 router.SetTrustedProxies 显式声明可信代理地址（推荐），gin 仅解析来自
//     可信代理的转发头；
//   - 或将服务置于会覆盖/清洗转发头的前置反向代理之后；
//   - 或设置 engine.RemoteIPHeaders = nil，强制 ClientIP 仅使用 RemoteAddr（直连 IP）。
type Config struct {
	Skipper    func(*gin.Context) bool
	AllowedIPs []string
	BlockedIPs []string
	// RejectHandler 自定义拒绝时的响应逻辑（可选）。
	//
	// 契约：reason 为本包导出的拒绝原因常量；回调负责写入状态码与响应体；
	// 回调返回后框架无条件调用 c.Abort() 终止后续处理器，无论回调内部是否
	// 已自行 Abort。为 nil 时返回默认响应（c.String 文本格式 "[code] msg"）。
	RejectHandler func(c *gin.Context, reason string)
}

// ipSet 存储精确 IP（O(1) map 查找）和 CIDR 前缀列表。
// 统一使用 net/netip 值类型：运行时 ParseAddr / Prefix.Contains 均零分配，
// 替代 net.ParseIP 每请求产生的 net.IP 切片分配。
type ipSet struct {
	exactIPs map[string]struct{}
	prefixes []netip.Prefix
	hasCIDR  bool
}

// newIPSet 构建 IP 集合。条目非法（既不是合法 CIDR 也不是合法 IP）时 panic，
// 遵循 fail-fast：静默跳过会使黑名单笔误被无声吞掉，导致安全配置 fail-open。
func newIPSet(ips []string) *ipSet {
	s := &ipSet{exactIPs: make(map[string]struct{}, len(ips))}
	for _, entry := range ips {
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				panic(fmt.Errorf("ipfilter: invalid CIDR entry %q: %w", entry, err))
			}
			s.prefixes = append(s.prefixes, normalizePrefix(prefix))
		} else {
			addr, err := netip.ParseAddr(entry)
			if err != nil {
				panic(fmt.Errorf("ipfilter: invalid IP entry %q: %w", entry, err))
			}
			// netip.ParseAddr 接受 IPv6 zone（如 "fe80::1%eth0"）而 net.ParseIP
			// 拒绝；保持原有校验严格度，避免永不匹配的条目被静默接受。
			if addr.Zone() != "" {
				panic(fmt.Errorf("ipfilter: invalid IP entry %q: IPv6 zone addresses are not supported", entry))
			}
			// 归一化后入集合（如 "ABCD::1" → "abcd::1"、"::ffff:1.2.3.4" → "1.2.3.4"），
			// 与 net.ParseIP / gin c.ClientIP() 的规范化输出保持一致，否则大写
			// IPv6 条目永不匹配（一致性由 TestNewIPSet_NormalizationMatchesNetParseIP 钉住）。
			s.exactIPs[addr.Unmap().String()] = struct{}{}
		}
	}
	s.hasCIDR = len(s.prefixes) > 0
	return s
}

// normalizePrefix 对齐 net.ParseCIDR 的旧语义：
//   - 4-in-6 前缀（如 "::ffff:10.0.0.0/104"）归一为等价 IPv4 前缀（10.0.0.0/8）。
//     旧实现 networkNumberAndMask 对 v4 网络号会截取 16 字节掩码的末 4 字节，
//     等价于前缀长减 96；不归一则前缀 BitLen(128) 与 v4 客户端地址(32)不等，
//     Prefix.Contains 恒为 false，产生行为回归。
//   - 非零 host bits 掩码化（如 "10.0.1.5/8" → 10.0.0.0/8），与 net.ParseCIDR
//     返回的 network 一致。
func normalizePrefix(p netip.Prefix) netip.Prefix {
	if addr := p.Addr(); addr.Is4In6() {
		bits := p.Bits() - 96
		if bits < 0 {
			bits = 0
		}
		p = netip.PrefixFrom(addr.Unmap(), bits)
	}
	return p.Masked()
}

// contains 报告 ip 是否命中集合：先查精确 map（零分配）；未命中且配置了
// CIDR 时，用 netip.ParseAddr（值类型，零分配）解析并 Unmap（4-in-6 映射
// 地址归一为 v4）后线性 Contains；解析失败返回 false，与 net.ParseIP
// 返回 nil 的旧行为一致。
func (s *ipSet) contains(ip string) bool {
	if _, ok := s.exactIPs[ip]; ok {
		return true
	}
	if !s.hasCIDR {
		return false
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range s.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// ipFilter IP过滤器
// blockedIPs: 黑名单（精确 IP + CIDR）
// allowedIPs: 白名单（精确 IP + CIDR，若为空则不启用白名单）
// hasAllowed: 标记是否启用了白名单模式
type ipFilter struct {
	skipper    func(*gin.Context) bool
	blockedIPs *ipSet
	allowedIPs *ipSet
	hasAllowed bool
}

func New(cfg Config) gin.HandlerFunc {
	f := &ipFilter{
		skipper:    cfg.Skipper,
		blockedIPs: newIPSet(cfg.BlockedIPs),
		allowedIPs: newIPSet(cfg.AllowedIPs),
		hasAllowed: len(cfg.AllowedIPs) > 0,
	}

	// reject 构造期捕获 cfg.RejectHandler，仅在拒绝路径调用，
	// allow 主路径零新增开销（统一拒绝契约）。
	reject := func(c *gin.Context, reason, defaultMsg string) {
		if cfg.RejectHandler != nil {
			cfg.RejectHandler(c, reason)
		} else {
			c.String(http.StatusForbidden, defaultMsg)
		}
		c.Abort()
	}

	return func(c *gin.Context) {
		if f.skipper != nil && f.skipper(c) {
			c.Next()
			return
		}

		clientIP := c.ClientIP()

		if f.blockedIPs.contains(clientIP) {
			reject(c, ReasonBlocked, "[403] ip blocked")
			return
		}

		if f.hasAllowed && !f.allowedIPs.contains(clientIP) {
			reject(c, ReasonNotAllowed, "[403] ip not allowed")
			return
		}

		c.Next()
	}
}
