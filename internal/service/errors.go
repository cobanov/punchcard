package service

import "net/http"

// Error is a domain error carrying an HTTP status and a stable, machine-readable
// code. The transport layer maps it directly to an RFC 9457 problem response.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// NewError builds a domain Error.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Common domain errors. Handlers can also construct ad-hoc ones with NewError.
var (
	ErrEmailTaken         = &Error{http.StatusConflict, "email_taken", "an account with that email already exists"}
	ErrInvalidCredentials = &Error{http.StatusUnauthorized, "invalid_credentials", "invalid email or password"}
	ErrUnauthenticated    = &Error{http.StatusUnauthorized, "unauthenticated", "authentication required"}
	ErrTokenInvalid       = &Error{http.StatusBadRequest, "token_invalid", "the token is invalid or has expired"}
	ErrTwoFactorRequired  = &Error{http.StatusUnauthorized, "two_factor_required", "a two-factor code is required"}
	ErrTwoFactorChallenge = &Error{http.StatusUnauthorized, "two_factor_challenge_invalid", "the two-factor challenge is invalid or expired"}
	ErrNotFound           = &Error{http.StatusNotFound, "not_found", "resource not found"}
	ErrForbidden          = &Error{http.StatusForbidden, "forbidden", "you do not have permission to perform this action"}
	ErrEmailNotVerified   = &Error{http.StatusForbidden, "email_not_verified", "email verification is required for this action"}
	ErrInsufficientScope  = &Error{http.StatusForbidden, "insufficient_scope", "this token's scope does not permit this action"}
	ErrInsufficientRole   = &Error{http.StatusForbidden, "insufficient_role", "your role on this list does not permit this action"}
	ErrQuotaExceeded      = &Error{http.StatusConflict, "quota_exceeded", "resource quota exceeded"}
	// The code stays "session_required" because it is published in the OpenAPI
	// document and clients match on it; the message no longer says "session"
	// because a first-party device token qualifies too. See Principal.FirstParty.
	ErrSessionOnly     = &Error{http.StatusForbidden, "session_required", "this action requires signing in to punchcard itself, not an API token"}
	ErrOAuthUnverified = &Error{http.StatusForbidden, "oauth_unverified", "the identity provider did not return a verified email address"}
)
