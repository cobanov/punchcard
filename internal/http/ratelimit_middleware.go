package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// rateLimitMiddleware applies per-IP limits to auth endpoints and per-principal
// (falling back to per-IP) limits to the rest of the API. It
// sets X-RateLimit-* headers on every /v1 response and Retry-After on 429.
func (d Deps) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cover the versioned API and the MCP transport; the MCP handler is
		// authenticated but was otherwise unlimited (each tool call fans out to
		// internal REST requests).
		if !strings.HasPrefix(r.URL.Path, "/v1") && !strings.HasPrefix(r.URL.Path, "/mcp") {
			next.ServeHTTP(w, r)
			return
		}

		ip := clientIP(r.Context())
		limiter := d.APILimiter
		key := "ip:" + ip
		if p := PrincipalFrom(r.Context()); p != nil {
			key = "user:" + p.UserID.String()
		}
		if isAuthEndpoint(r.URL.Path) {
			limiter, key = d.AuthLimiter, "auth:"+ip
		}

		dec := limiter.Allow(key)
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(dec.Limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(dec.Remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(dec.ResetAt.Unix(), 10))
		if !dec.Allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(dec.RetryAfter/time.Second)+1))
			writeProblem(w, http.StatusTooManyRequests, "rate_limited", "too many requests; slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAuthEndpoint(path string) bool {
	switch {
	case strings.HasPrefix(path, "/v1/auth/login"),
		strings.HasPrefix(path, "/v1/auth/register"),
		strings.HasPrefix(path, "/v1/auth/native"),
		strings.HasPrefix(path, "/v1/auth/oauth"),
		strings.HasPrefix(path, "/v1/auth/verify-email"),
		strings.HasPrefix(path, "/v1/auth/password-reset"):
		return true
	default:
		return false
	}
}
