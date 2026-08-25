package webhooks

import (
	"context"
	"errors"
	"net"
	"net/url"
)

// SSRF errors.
var (
	ErrScheme  = errors.New("webhook url must use https")
	ErrHost    = errors.New("webhook url must have a host")
	ErrResolve = errors.New("webhook host could not be resolved")
	ErrBlocked = errors.New("webhook url resolves to a disallowed (private/loopback/metadata) address")
)

var blockedCIDRs = mustCIDRs(
	"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"169.254.0.0/16", "0.0.0.0/8", "100.64.0.0/10",
	"::1/128", "fc00::/7", "fe80::/10", "::/128",
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

// allowPrivateForTest disables private-range blocking. It is set ONLY by tests
// (httptest servers bind to loopback) and must never be enabled in production.
var allowPrivateForTest bool

// blockedIP reports whether an IP is in a range webhooks must never reach.
func blockedIP(ip net.IP) bool {
	if allowPrivateForTest {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	for _, b := range blockedCIDRs {
		if b.Contains(ip) {
			return true
		}
	}
	return false
}

// resolver is swappable in tests.
var resolver = net.DefaultResolver.LookupIPAddr

// ValidateURL validates a webhook URL and returns the resolved IPs to pin the
// connection to at delivery (DNS-rebinding defense). It rejects non-https
// (unless allowHTTP) and any host resolving into a private/loopback/link-local/
// metadata range. It must be called on registration AND before every delivery.
func ValidateURL(ctx context.Context, raw string, allowHTTP bool) ([]net.IP, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, ErrHost
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return nil, ErrScheme
		}
	default:
		return nil, ErrScheme
	}
	host := u.Hostname()
	if host == "" {
		return nil, ErrHost
	}
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return nil, ErrBlocked
		}
		return []net.IP{ip}, nil
	}
	addrs, err := resolver(ctx, host)
	if err != nil || len(addrs) == 0 {
		return nil, ErrResolve
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if blockedIP(a.IP) {
			return nil, ErrBlocked
		}
		ips = append(ips, a.IP)
	}
	return ips, nil
}
