package service

import (
	"sync"
	"time"

	"github.com/cobanov/punchcard/internal/auth"
)

// nativeCodeTTL bounds how long an OAuth exchange code is valid.
const nativeCodeTTL = 90 * time.Second

type nativeCodeEntry struct {
	token   string
	expires time.Time
}

// nativeCodeStore is a small in-process, single-use map from short-lived
// exchange codes to freshly-minted device tokens. It lets the OAuth deep link
// carry only an opaque code instead of the bearer token, which would otherwise
// be written to OS logs (ATD-42). The prod deployment is single-instance, so an
// in-memory store is sufficient; a lost code just means the user retries login.
type nativeCodeStore struct {
	mu sync.Mutex
	m  map[string]nativeCodeEntry
}

func newNativeCodeStore() *nativeCodeStore {
	return &nativeCodeStore{m: make(map[string]nativeCodeEntry)}
}

// put stores token under a fresh random code and returns the code.
func (s *nativeCodeStore) put(token string, now time.Time) (string, error) {
	code, err := auth.GenerateSecret(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	s.m[code] = nativeCodeEntry{token: token, expires: now.Add(nativeCodeTTL)}
	return code, nil
}

// take returns and removes the token for code (single-use); ok is false when the
// code is unknown or expired.
func (s *nativeCodeStore) take(code string, now time.Time) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[code]
	if !ok {
		return "", false
	}
	delete(s.m, code)
	if now.After(e.expires) {
		return "", false
	}
	return e.token, true
}

func (s *nativeCodeStore) pruneLocked(now time.Time) {
	for k, e := range s.m {
		if now.After(e.expires) {
			delete(s.m, k)
		}
	}
}

// StashNativeToken stores a device token behind a single-use exchange code.
func (a *Auth) StashNativeToken(token string) (string, error) {
	return a.nativeCodes.put(token, time.Now())
}

// ExchangeNativeCode redeems an exchange code for its device token (single-use).
func (a *Auth) ExchangeNativeCode(code string) (string, error) {
	tok, ok := a.nativeCodes.take(code, time.Now())
	if !ok {
		return "", ErrTokenInvalid
	}
	return tok, nil
}
