package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/service"
)

// writeProblem writes an RFC 9457 problem response directly (used by middleware
// that runs outside huma's handler machinery).
func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	pe := &ProblemError{Status: status, Title: http.StatusText(status), Detail: detail, Code: code}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(pe)
}

// problem maps a service/repo error to a huma-returnable ProblemError. Unknown
// errors are logged (never leaked) and become a generic 500.
func (d Deps) problem(ctx context.Context, err error) error {
	var se *service.Error
	if errors.As(err, &se) {
		return NewProblem(se.Status, se.Code, se.Message)
	}
	if errors.Is(err, repo.ErrNoRows) {
		return NewProblem(http.StatusNotFound, "not_found", "resource not found")
	}
	d.Logger.ErrorContext(ctx, "unhandled error",
		"error", err.Error(),
		"request_id", observability.RequestIDFrom(ctx))
	return NewProblem(http.StatusInternalServerError, "internal_error", "internal server error")
}

// requirePrincipal returns the authenticated principal or a 401 problem.
func requirePrincipal(ctx context.Context) (*auth.Principal, error) {
	p := PrincipalFrom(ctx)
	if p == nil {
		return nil, NewProblem(http.StatusUnauthorized, "unauthenticated", "authentication required")
	}
	return p, nil
}
