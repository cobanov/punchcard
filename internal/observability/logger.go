// Package observability wires the cross-cutting operational concerns:
// structured logging, Prometheus metrics, and per-request correlation.
// Nothing here contains business logic.
package observability

import (
	"log/slog"
	"os"
)

// NewLogger builds the process-wide structured JSON logger. level is one of
// debug|info|warn|error (already validated by config).
func NewLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}
