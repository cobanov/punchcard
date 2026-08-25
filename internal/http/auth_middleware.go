package http

import (
	"net/http"
	"strings"
)

const (
	sessionCookieName = "punchcard_session"
	csrfHeader        = "X-CSRF-Token"
	bearerPrefix      = "Bearer "
)

// authMiddleware resolves the request principal from a Bearer PAT or a session
// cookie, and stashes client IP + user-agent in the context. It also enforces
// CSRF for cookie-authenticated mutations: browsers cannot set the
// custom X-CSRF-Token header cross-origin (CORS is locked), and cookies are
// SameSite=Lax, so requiring the header defends state-changing requests. PAT
// requests carry no ambient authority and are exempt.
func (d Deps) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1") && !strings.HasPrefix(r.URL.Path, "/mcp") {
			next.ServeHTTP(w, r)
			return
		}

		ctx := withRequestMeta(r.Context(), d.realIP(r), r.UserAgent())

		if ah := r.Header.Get("Authorization"); ah != "" {
			if !strings.HasPrefix(ah, bearerPrefix) {
				writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Authorization must use the Bearer scheme")
				return
			}
			p, err := d.Auth.AuthenticatePAT(ctx, strings.TrimSpace(ah[len(bearerPrefix):]))
			if err != nil {
				writeProblem(w, http.StatusUnauthorized, "unauthenticated", "invalid or expired token")
				return
			}
			ctx = withPrincipal(ctx, p)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
			if p, err := d.Auth.AuthenticateSession(ctx, c.Value); err == nil {
				// Hand the SPA a fresh CSRF token if it doesn't already hold the
				// current one. The token is bound to a server secret, so a
				// forged request can never present the right value.
				if cc, cerr := r.Cookie(csrfCookieName); cerr != nil || cc.Value != d.csrfToken {
					http.SetCookie(w, d.csrfCookie())
				}
				if isUnsafeMethod(r.Method) && !csrfTokenValid(d.csrfToken, r.Header.Get(csrfHeader)) {
					writeProblem(w, http.StatusForbidden, "csrf_required",
						"cookie-authenticated mutations require a valid "+csrfHeader+" header")
					return
				}
				ctx = withPrincipal(ctx, p)
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// originOf labels a request for the activity log. It answers "what kind of
// caller is this", never "what may it do" — nothing reads an origin to make a
// decision.
//
// The MCP header is self-declared, and has to be: internal/mcp calls the public
// REST API over HTTP with a PAT precisely so it can never bypass validation,
// idempotency or authorization, which also means the server cannot tell it from
// any other program holding that token. It is honoured only for a PAT and only
// for the value "mcp"; nothing can claim "agent", which the chat handler sets
// in-process. Worst case a token holder mislabels rows in their own log.

func isUnsafeMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
