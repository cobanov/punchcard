package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// handleHealthz is liveness: the process is up and can serve. It intentionally
// does not touch the database.
func handleHealthz() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
	}
}

// handleReadyz is readiness: the process can serve real traffic, i.e. the DB is
// reachable and migrations are current. Failure reasons are kept generic in the
// response (details are logged, never leaked) and return 503.
func handleReadyz(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		checks := map[string]string{}

		if d.Pool == nil {
			checks["database"] = "unavailable"
			d.Logger.LogAttrs(ctx, slog.LevelError, "readyz failed", slog.String("check", "database"), slog.String("reason", "pool not configured"))
			writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable", Checks: checks})
			return
		}
		if err := d.Pool.Ping(ctx); err != nil {
			checks["database"] = "unavailable"
			d.Logger.LogAttrs(ctx, slog.LevelError, "readyz failed", slog.String("check", "database"), slog.String("error", err.Error()))
			writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable", Checks: checks})
			return
		}
		checks["database"] = "ok"

		if d.Migrator != nil {
			pending, err := d.Migrator.HasPending(ctx)
			switch {
			case err != nil:
				checks["migrations"] = "unknown"
				d.Logger.LogAttrs(ctx, slog.LevelError, "readyz failed", slog.String("check", "migrations"), slog.String("error", err.Error()))
				writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable", Checks: checks})
				return
			case pending:
				checks["migrations"] = "pending"
				writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable", Checks: checks})
				return
			default:
				checks["migrations"] = "ok"
			}
		}

		writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Checks: checks})
	}
}
