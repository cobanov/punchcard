package http

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// ProblemError is the single error shape returned by the API. It follows
// RFC 9457 (`application/problem+json`) and adds a stable, machine-readable
// `code` field that clients and agents can branch on without
// parsing human text. It replaces huma's default error model process-wide via
// the init hook below.
type ProblemError struct {
	Type     string              `json:"type,omitempty" doc:"URI reference identifying the problem type."`
	Title    string              `json:"title" doc:"Short, human-readable summary of the problem type."`
	Status   int                 `json:"status" doc:"HTTP status code."`
	Detail   string              `json:"detail,omitempty" doc:"Human-readable explanation specific to this occurrence."`
	Code     string              `json:"code" doc:"Stable, machine-readable error code, e.g. validation_failed."`
	Instance string              `json:"instance,omitempty" doc:"URI reference identifying this specific occurrence."`
	Errors   []*huma.ErrorDetail `json:"errors,omitempty" doc:"Field-level validation errors, when applicable."`
}

// Error implements the error interface.
func (e *ProblemError) Error() string { return e.Detail }

// GetStatus implements huma.StatusError so huma writes the right HTTP status.
func (e *ProblemError) GetStatus() int { return e.Status }

// ContentType makes huma serialize this body as application/problem+json.
func (e *ProblemError) ContentType(ct string) string {
	if ct == "application/json" || ct == "" {
		return "application/problem+json"
	}
	return ct
}

// NewProblem constructs a ProblemError with an explicit stable code. Handlers
// and the service layer use this for domain-specific codes such as
// insufficient_role or idempotency_conflict (added in later phases).
func NewProblem(status int, code, detail string) *ProblemError {
	return &ProblemError{
		Status: status,
		Title:  http.StatusText(status),
		Detail: detail,
		Code:   code,
	}
}

// codeForStatus maps an HTTP status to the default stable error code. Handlers
// may override with NewProblem when a more specific code applies.
func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusUnprocessableEntity:
		return "validation_failed"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	default:
		if status >= 500 {
			return "internal_error"
		}
		return "error"
	}
}

// init installs ProblemError as huma's error model for the whole process, so
// every validation error and huma.Error* helper produces RFC 9457 output with a
// stable code.
func init() {
	huma.NewError = func(status int, message string, errs ...error) huma.StatusError {
		details := make([]*huma.ErrorDetail, 0, len(errs))
		for _, err := range errs {
			if err == nil {
				continue
			}
			if d, ok := err.(huma.ErrorDetailer); ok {
				details = append(details, d.ErrorDetail())
				continue
			}
			details = append(details, &huma.ErrorDetail{Message: err.Error()})
		}
		return &ProblemError{
			Status: status,
			Title:  http.StatusText(status),
			Detail: message,
			Code:   codeForStatus(status),
			Errors: details,
		}
	}
}
