package ipfilter

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Config IP 过滤器配置。
type Config struct {
	Skipper    func(*gin.Context) bool
	AllowedIPs []string
	BlockedIPs []string
}

// ipSet 存储精确 IP（O(1) 查找）和 CIDR 网段列表。
type ipSet struct {
	exactIPs map[string]struct{}
	cidrNets []*net.IPNet
}

func newIPSet(ips []string) *ipSet {
	s := &ipSet{exactIPs: make(map[string]struct{}, len(ips))}
	for _, ip := range ips {
		if strings.Contains(ip, "/") {
			_, ipNet, err := net.ParseCIDR(ip)
			if err == nil {
				s.cidrNets = append(s.cidrNets, ipNet)
			}
		} else {
			s.exactIPs[ip] = struct{}{}
		}
	}
	return s
}

func (s *ipSet) contains(ip string) bool {
	if _, ok := s.exactIPs[ip]; ok {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidr := range s.cidrNets {
		if cidr.Contains(parsed) {
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

	return func(c *gin.Context) {
		if f.skipper != nil && f.skipper(c) {
			c.Next()
			return
		}

		clientIP := c.ClientIP()

		if f.blockedIPs.contains(clientIP) {
			c.String(http.StatusForbidden, "[403] ip blocked")
			c.Abort()
			return
		}

		if f.hasAllowed {
			if !f.allowedIPs.contains(clientIP) {
				c.String(http.StatusForbidden, "[403] ip not allowed")
				c.Abort()
				return
			}
		}

		c.Next()
	}
}
