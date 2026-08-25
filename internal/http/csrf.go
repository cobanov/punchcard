package http

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"

	"github.com/cobanov/punchcard/internal/config"
)

// csrfCookieName carries the CSRF token to the SPA. Unlike the session cookie it
// is readable by JS (not HttpOnly): the SPA echoes it back in the X-CSRF-Token
// header, and the server accepts the mutation only if the header matches the
// server-secret-derived token. An attacker cannot forge the header without the
// secret, and cannot read the victim's cookie cross-origin.
const csrfCookieName = "punchcard_csrf"

// deriveCSRFToken produces the token the server requires in X-CSRF-Token. It is
// bound to a server secret (APP_SECRET) so the value cannot be guessed; when no
// secret is configured a per-process random token is used (stable for the life
// of the process, which is enough for the double-submit check).
func deriveCSRFToken(cfg *config.Config) string {
	if cfg != nil && cfg.AppSecret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.AppSecret))
		mac.Write([]byte("csrf-token-v1"))
		return hex.EncodeToString(mac.Sum(nil))
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// rand.Read never fails on supported platforms; fall back to a fixed
		// non-empty value rather than an empty (always-matching) token.
		return "csrf-unavailable"
	}
	return hex.EncodeToString(b)
}

// csrfTokenValid reports whether the presented header matches the expected token
// in constant time.
func csrfTokenValid(expected, presented string) bool {
	if expected == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(presented)) == 1
}
