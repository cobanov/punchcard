package observability

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type ctxKey int

const requestIDKey ctxKey = iota

// RequestIDHeader is echoed on every response so clients and logs can
// correlate a single request.
const RequestIDHeader = "X-Request-ID"

// RequestIDFrom returns the request id stored in ctx, or "" if absent.
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// withRequestID returns a copy of ctx carrying the given request id.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID is middleware that assigns a request id (honouring an inbound
// X-Request-ID when present and well-formed) and echoes it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = uuid.NewString()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(withRequestID(r.Context(), id)))
	})
}

// validRequestID accepts only a non-empty, bounded token to avoid header
// injection or unbounded log fields from untrusted inbound values.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c <= 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}
