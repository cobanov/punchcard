package http

import (
	"crypto/subtle"
	"net/http"
)

// requireBearer gates a handler behind a static bearer token (constant-time
// compared). Used to protect /metrics when METRICS_TOKEN is configured.
func requireBearer(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets baseline headers on every response. Caddy
// adds these at the edge too; setting them here means they hold regardless of
// the fronting proxy — including HSTS in production, so a deploy without Caddy
// still forces HTTPS.
func (d Deps) securityHeaders(next http.Handler) http.Handler {
	prod := d.Config != nil && d.Config.IsProduction()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if prod {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// maxBodyBytes caps request body size (1 MB limit).
func maxBodyBytes(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// corsMiddleware applies CORS only for exactly-configured origins. With no
// origins configured, no CORS headers are emitted (same-origin only).
func (d Deps) corsMiddleware(next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(d.Config.CORSAllowedOrigins))
	for _, o := range d.Config.CORSAllowedOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, "+csrfHeader+", Idempotency-Key")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
