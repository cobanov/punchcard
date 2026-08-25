package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// GitHubCipher seals and opens GitHub access tokens with AES-256-GCM.
//
// The key comes from GITHUB_TOKEN_KEY and the service refuses to start without
// it when GitHub is configured at all. There is deliberately no plaintext
// fallback: a fallback would mean the difference between "the token is
// encrypted" and "the token is in every database backup in cleartext" is one
// unset environment variable that nothing complains about.
type GitHubCipher struct {
	aead cipher.AEAD
}

// ErrGitHubKeyMissing is returned when the encryption key is absent or unusable.
var ErrGitHubKeyMissing = errors.New("GITHUB_TOKEN_KEY must be 32 bytes, base64-encoded")

// NewGitHubCipher builds a cipher from a base64-encoded 32-byte key. It returns
// (nil, nil) for an empty key so a deployment with no GitHub integration can
// start; callers must treat a nil cipher as "GitHub is not configured".
func NewGitHubCipher(b64Key string) (*GitHubCipher, error) {
	if b64Key == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil || len(key) != 32 {
		return nil, ErrGitHubKeyMissing
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("github cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("github gcm: %w", err)
	}
	return &GitHubCipher{aead: aead}, nil
}

// Seal encrypts a token. The nonce is fresh for every call and is prefixed to
// the ciphertext, so the same token sealed twice produces different bytes.
func (c *GitHubCipher) Seal(token string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("github nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, []byte(token), nil), nil
}

// Open decrypts a token sealed by Seal.
func (c *GitHubCipher) Open(sealed []byte) (string, error) {
	n := c.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("github token: ciphertext too short")
	}
	plain, err := c.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", fmt.Errorf("github token: %w", err)
	}
	return string(plain), nil
}
