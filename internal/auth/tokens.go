package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// PATPrefix is the scheme prefix for personal access tokens.
const PATPrefix = "tdo_"

// GenerateSecret returns a URL-safe random string carrying n bytes of entropy.
func GenerateSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the SHA-256 of a bearer token/secret. All bearer secrets
// (session tokens, PATs, email tokens) are stored only as this hash.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// NewPAT mints a personal access token. It returns the full secret (shown to
// the user exactly once) and a short non-secret prefix stored for display.
func NewPAT() (full, prefix string, err error) {
	r, err := GenerateSecret(32)
	if err != nil {
		return "", "", err
	}
	full = PATPrefix + r
	n := 12
	if len(full) < n {
		n = len(full)
	}
	return full, full[:n], nil
}
