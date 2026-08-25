package http

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/cobanov/punchcard/internal/observability"
)

// requestLogger logs one structured line per request after it completes. Health
// and metrics probes are skipped to keep logs signal-rich. The principal id is
// added in Phase 1 once authentication exists.
func requestLogger(log *slog.Logger, metricsPath string) func(http.Handler) http.Handler {
	quiet := map[string]bool{"/healthz": true, "/readyz": true, metricsPath: true}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if quiet[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			log.LogAttrs(r.Context(), slog.LevelInfo, "http_request",
				slog.String("request_id", observability.RequestIDFrom(r.Context())),
				slog.String("method", r.Method),
				slog.String("route", route),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}

// metricsMiddleware records request count and latency by route and method.
func metricsMiddleware(m *observability.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			m.HTTPRequests.WithLabelValues(route, r.Method, strconv.Itoa(ww.Status())).Inc()
			m.HTTPDuration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		})
	}
}
