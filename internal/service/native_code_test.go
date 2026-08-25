package service

import (
	"testing"
	"time"
)

func TestNativeCodeStore(t *testing.T) {
	s := newNativeCodeStore()
	now := time.Now()

	code, err := s.put("tdo_secret", now)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if code == "" {
		t.Fatal("empty code")
	}

	// First redemption returns the token.
	tok, ok := s.take(code, now)
	if !ok || tok != "tdo_secret" {
		t.Fatalf("take = %q,%v; want tdo_secret,true", tok, ok)
	}

	// Single-use: a second redemption fails.
	if _, ok := s.take(code, now); ok {
		t.Fatal("code was reusable; want single-use")
	}

	// Expired codes are not redeemable.
	code2, _ := s.put("tdo_two", now)
	if _, ok := s.take(code2, now.Add(nativeCodeTTL+time.Second)); ok {
		t.Fatal("expired code was redeemable")
	}
}
