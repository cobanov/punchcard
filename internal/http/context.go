package http

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/cobanov/punchcard/internal/auth"
)

type contextKey int

const (
	principalKey contextKey = iota
	clientIPKey
	userAgentKey
)

func withPrincipal(ctx context.Context, p *auth.Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFrom returns the authenticated principal, or nil if anonymous.
func PrincipalFrom(ctx context.Context) *auth.Principal {
	if p, ok := ctx.Value(principalKey).(*auth.Principal); ok {
		return p
	}
	return nil
}

func withRequestMeta(ctx context.Context, ip, ua string) context.Context {
	ctx = context.WithValue(ctx, clientIPKey, ip)
	return context.WithValue(ctx, userAgentKey, ua)
}

func clientIP(ctx context.Context) string {
	if v, ok := ctx.Value(clientIPKey).(string); ok {
		return v
	}
	return ""
}

func userAgent(ctx context.Context) string {
	if v, ok := ctx.Value(userAgentKey).(string); ok {
		return v
	}
	return ""
}

// defaultTrustedProxies are honored for forwarded headers when TRUSTED_PROXIES
// is unset: loopback + RFC1918 private + CGNAT (Tailscale) + IPv6 ULA/link-local.
// These match a reverse proxy (Caddy/Cloudflare Tunnel) on a private network,
// while public sources can never spoof CF-Connecting-IP / X-Forwarded-For to
// evade the per-IP auth rate limiter.
var defaultTrustedProxies = parseCIDRs([]string{
	"127.0.0.0/8", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"100.64.0.0/10", "fc00::/7", "fe80::/10",
})

func parseCIDRs(cidrs []string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// realIP extracts the client IP. Reverse-proxy headers (CF-Connecting-IP,
// X-Forwarded-For, X-Real-IP) are honored ONLY when the direct peer
// (RemoteAddr) is a trusted proxy; otherwise the peer address is used, so an
// attacker with direct access can't forge a source IP to bypass rate limits.
func (d Deps) realIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !ipInNets(net.ParseIP(host), d.trustedProxyNets) {
		return host
	}
	if v := r.Header.Get("CF-Connecting-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i >= 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	return host
}
