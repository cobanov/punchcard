package http

import (
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// TestTwoFactorFlow covers TOTP enrollment, the login challenge, recovery-code
// login, and disabling 2FA (ATD-18).
func TestTwoFactorFlow(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	creds := map[string]string{"email": "tfa@example.com", "password": "supersecret123"}

	if st, _ := do(t, client, http.MethodPost, base+"/v1/auth/register", creds, nil); st != http.StatusCreated {
		t.Fatalf("register = %d", st)
	}
	csrf := testCSRF()

	// Begin enrollment → secret.
	st, body := do(t, client, http.MethodPost, base+"/v1/me/2fa/setup", nil, csrf)
	if st != http.StatusOK {
		t.Fatalf("2fa setup = %d", st)
	}
	var setup struct{ Secret, URI string }
	unmarshal(t, body, &setup)
	if setup.Secret == "" || setup.URI == "" {
		t.Fatal("empty secret/uri")
	}

	code := func() string { c, _ := totp.GenerateCode(setup.Secret, time.Now()); return c }

	// Enable with the first code → recovery codes.
	st, body = do(t, client, http.MethodPost, base+"/v1/me/2fa/enable", map[string]string{"code": code()}, csrf)
	if st != http.StatusOK {
		t.Fatalf("2fa enable = %d, body=%s", st, body)
	}
	var enabled struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	unmarshal(t, body, &enabled)
	if len(enabled.RecoveryCodes) < 5 {
		t.Fatalf("got %d recovery codes", len(enabled.RecoveryCodes))
	}

	// /me reports 2FA enabled.
	_, body = do(t, client, http.MethodGet, base+"/v1/me", nil, nil)
	var me struct {
		TwoFactorEnabled bool `json:"two_factor_enabled"`
	}
	unmarshal(t, body, &me)
	if !me.TwoFactorEnabled {
		t.Fatal("two_factor_enabled should be true")
	}

	// Log out, then a password login returns a 2FA challenge, not a session.
	do(t, client, http.MethodPost, base+"/v1/auth/logout", nil, csrf)
	fresh, _ := cookiejar.New(nil)
	c2 := &http.Client{Jar: fresh}
	st, body = do(t, c2, http.MethodPost, base+"/v1/auth/login", creds, nil)
	if st != http.StatusOK {
		t.Fatalf("login = %d", st)
	}
	var chal struct {
		TwoFactorRequired bool                 `json:"two_factor_required"`
		Challenge         string               `json:"challenge"`
		User              *struct{ ID string } `json:"user"`
	}
	unmarshal(t, body, &chal)
	if !chal.TwoFactorRequired || chal.Challenge == "" || chal.User != nil {
		t.Fatalf("expected 2fa challenge, got %s", body)
	}
	// Before completing 2FA the session is not established.
	if st, _ := do(t, c2, http.MethodGet, base+"/v1/me", nil, nil); st != http.StatusUnauthorized {
		t.Fatalf("me before 2fa = %d, want 401", st)
	}

	// Wrong code is rejected.
	if st, _ := do(t, c2, http.MethodPost, base+"/v1/auth/2fa",
		map[string]string{"challenge": chal.Challenge, "code": "000000"}, nil); st == http.StatusOK {
		t.Fatal("2fa completed with a wrong code")
	}

	// A fresh challenge + correct code completes login.
	_, body = do(t, c2, http.MethodPost, base+"/v1/auth/login", creds, nil)
	unmarshal(t, body, &chal)
	if st, _ := do(t, c2, http.MethodPost, base+"/v1/auth/2fa",
		map[string]string{"challenge": chal.Challenge, "code": code()}, nil); st != http.StatusOK {
		t.Fatalf("2fa complete = %d", st)
	}
	if st, _ := do(t, c2, http.MethodGet, base+"/v1/me", nil, nil); st != http.StatusOK {
		t.Fatalf("me after 2fa = %d, want 200", st)
	}

	// Recovery-code login works (single-use).
	rc := enabled.RecoveryCodes[0]
	rec, _ := cookiejar.New(nil)
	c3 := &http.Client{Jar: rec}
	_, body = do(t, c3, http.MethodPost, base+"/v1/auth/login", creds, nil)
	unmarshal(t, body, &chal)
	if st, _ := do(t, c3, http.MethodPost, base+"/v1/auth/2fa",
		map[string]string{"challenge": chal.Challenge, "code": rc}, nil); st != http.StatusOK {
		t.Fatalf("recovery-code 2fa = %d", st)
	}
	// The same recovery code cannot be reused.
	_, body = do(t, c3, http.MethodPost, base+"/v1/auth/login", creds, nil)
	unmarshal(t, body, &chal)
	if st, _ := do(t, c3, http.MethodPost, base+"/v1/auth/2fa",
		map[string]string{"challenge": chal.Challenge, "code": rc}, nil); st == http.StatusOK {
		t.Fatal("recovery code was reusable")
	}

	// Disable 2FA (needs the current logged-in session c2 + csrf).
	if st, _ := do(t, c2, http.MethodPost, base+"/v1/me/2fa/disable", map[string]string{"code": code()}, csrf); st != http.StatusOK {
		t.Fatalf("2fa disable = %d", st)
	}
}
