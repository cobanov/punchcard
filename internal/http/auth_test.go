package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/email"
	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/service"
	"github.com/cobanov/punchcard/internal/testutil"
	"github.com/cobanov/punchcard/internal/webhooks"
)

// captureSender records the last email so tests can extract tokens.
//
// Sends are dispatched off the request goroutine (see service.sendAsync), so a
// handler can return before the mail is delivered here. Waiting on `sent`
// rather than reading straight after the request is what keeps that from being
// an intermittent failure.
type captureSender struct {
	mu   sync.Mutex
	last email.Message
	sent chan struct{}
}

func newCaptureSender() *captureSender {
	return &captureSender{sent: make(chan struct{}, 16)}
}

func (c *captureSender) Send(_ context.Context, m email.Message) error {
	c.mu.Lock()
	c.last = m
	c.mu.Unlock()
	select {
	case c.sent <- struct{}{}:
	default: // never block the sender on a test that is not watching
	}
	return nil
}

// token waits for a send to land, then returns the token from it. It fails the
// test rather than returning "" on timeout, so a genuinely missing email reads
// as a missing email instead of as a malformed token further down.
func (c *captureSender) token(t *testing.T) string {
	t.Helper()
	select {
	case <-c.sent:
	case <-time.After(5 * time.Second):
		t.Fatal("no email was sent within 5s")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := regexp.MustCompile(`token=([^\s]+)`).FindStringSubmatch(c.last.Text)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

func newAuthTestServer(t *testing.T) (*httptest.Server, *captureSender) {
	t.Helper()
	pool := testutil.Postgres(t)
	store := repo.NewStore(pool)
	logger := observability.NewLogger("error")
	sender := newCaptureSender()
	cfg := testConfig()
	auditor := audit.NewLogger(store, logger)
	authSvc := service.NewAuth(store, sender, auditor, logger, cfg)
	whKey, _ := webhooks.GenerateKey()
	cipher, _ := webhooks.NewCipher(whKey)
	ghCipher, err := service.NewGitHubCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatalf("github cipher: %v", err)
	}
	domainSvc := service.NewDomain(store, auditor, sender, cipher, ghCipher, logger, cfg)

	migrator, merr := repo.NewMigrator(pool)
	if merr != nil {
		t.Fatalf("migrator: %v", merr)
	}
	t.Cleanup(func() { _ = migrator.Close() })

	router, _ := BuildRouter(Deps{
		Config: cfg, Logger: logger, Metrics: observability.NewMetrics(),
		Pool: pool, Migrator: migrator, Store: store, Auth: authSvc, Domain: domainSvc,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, sender
}

// do performs a JSON request and returns the status and body bytes, closing the
// response body internally.
func do(t *testing.T, client *http.Client, method, url string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, b
}

func TestAuthFlow(t *testing.T) {
	srv, sender := newAuthTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	csrf := testCSRF()

	creds := map[string]string{"email": "alice@example.com", "password": "supersecret123"}

	var me struct {
		ID, Email     string
		EmailVerified bool `json:"email_verified"`
	}

	// Register -> 201 + session cookie.
	if st, _ := do(t, client, http.MethodPost, srv.URL+"/v1/auth/register", creds, nil); st != http.StatusCreated {
		t.Fatalf("register status = %d, want 201", st)
	}

	// /me with the session cookie.
	st, body := do(t, client, http.MethodGet, srv.URL+"/v1/me", nil, nil)
	if st != http.StatusOK {
		t.Fatalf("me = %d, want 200", st)
	}
	unmarshal(t, body, &me)
	if me.Email != "alice@example.com" {
		t.Fatalf("me.email = %q", me.Email)
	}
	if me.EmailVerified {
		t.Fatal("email should be unverified right after register")
	}

	// CSRF: cookie-authenticated POST without the header -> 403.
	if st, _ := do(t, client, http.MethodPost, srv.URL+"/v1/tokens",
		map[string]any{"name": "x", "scope": "read_write"}, nil); st != http.StatusForbidden {
		t.Fatalf("token create without CSRF = %d, want 403", st)
	}
	// CSRF: a present-but-wrong token value is rejected (value is bound to the
	// server secret, not merely required to exist).
	if st, _ := do(t, client, http.MethodPost, srv.URL+"/v1/tokens",
		map[string]any{"name": "x", "scope": "read_write"},
		map[string]string{csrfHeader: "not-the-real-token"}); st != http.StatusForbidden {
		t.Fatalf("token create with bogus CSRF = %d, want 403", st)
	}

	// Create a read_write PAT (with CSRF header).
	st, body = do(t, client, http.MethodPost, srv.URL+"/v1/tokens",
		map[string]any{"name": "ci", "scope": "read_write"}, csrf)
	if st != http.StatusCreated {
		t.Fatalf("token create = %d, want 201", st)
	}
	var tok struct {
		Secret string `json:"secret"`
	}
	unmarshal(t, body, &tok)
	if len(tok.Secret) < 4 || tok.Secret[:4] != "tdo_" {
		t.Fatalf("bad secret %q", tok.Secret)
	}

	// Use the PAT as a Bearer token on a cookie-less client.
	bearer := &http.Client{}
	if st, _ := do(t, bearer, http.MethodGet, srv.URL+"/v1/me",
		nil, map[string]string{"Authorization": "Bearer " + tok.Secret}); st != http.StatusOK {
		t.Fatalf("me via PAT = %d, want 200", st)
	}

	// Invalid bearer -> 401.
	if st, _ := do(t, bearer, http.MethodGet, srv.URL+"/v1/me",
		nil, map[string]string{"Authorization": "Bearer tdo_nope"}); st != http.StatusUnauthorized {
		t.Fatalf("bad PAT = %d, want 401", st)
	}

	// Verify email using the token from the (captured) email.
	vtok := sender.token(t)
	if vtok == "" {
		t.Fatal("no verification token captured")
	}
	if st, _ := do(t, client, http.MethodPost, srv.URL+"/v1/auth/verify-email",
		map[string]string{"token": vtok}, csrf); st != http.StatusOK {
		t.Fatalf("verify-email = %d, want 200", st)
	}
	_, body = do(t, client, http.MethodGet, srv.URL+"/v1/me", nil, nil)
	unmarshal(t, body, &me)
	if !me.EmailVerified {
		t.Fatal("email should be verified after verify-email")
	}

	// Duplicate registration on a fresh client -> 409.
	fresh := &http.Client{}
	if st, _ := do(t, fresh, http.MethodPost, srv.URL+"/v1/auth/register", creds, nil); st != http.StatusConflict {
		t.Fatalf("duplicate register = %d, want 409", st)
	}

	// Logout, then /me is unauthorized.
	if st, _ := do(t, client, http.MethodPost, srv.URL+"/v1/auth/logout", nil, csrf); st != http.StatusOK {
		t.Fatalf("logout = %d, want 200", st)
	}
	if st, _ := do(t, client, http.MethodGet, srv.URL+"/v1/me", nil, nil); st != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d, want 401", st)
	}

	// Log back in.
	if st, _ := do(t, client, http.MethodPost, srv.URL+"/v1/auth/login", creds, nil); st != http.StatusOK {
		t.Fatalf("login = %d, want 200", st)
	}

	// Wrong password (valid length) -> 401.
	if st, _ := do(t, fresh, http.MethodPost, srv.URL+"/v1/auth/login",
		map[string]string{"email": "alice@example.com", "password": "wrongpassword"}, nil); st != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", st)
	}
}

// TestNativeLoginToken covers the non-web (desktop/mobile/extension) auth path:
// email+password returns a bearer device token that authenticates API calls with
// no cookie, and the OAuth start guards its native redirect target.
func TestNativeLoginToken(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	creds := map[string]string{"email": "dev@example.com", "password": "supersecret123"}
	if st, _ := do(t, client, http.MethodPost, srv.URL+"/v1/auth/register", creds, nil); st != http.StatusCreated {
		t.Fatalf("register = %d, want 201", st)
	}

	// Native login from a cookieless client (the canonical non-web case) returns
	// a bearer token — no session cookie or CSRF header involved.
	st, body := do(t, &http.Client{}, http.MethodPost, srv.URL+"/v1/auth/native/login",
		map[string]string{"email": "dev@example.com", "password": "supersecret123", "client_name": "Desktop"}, nil)
	if st != http.StatusOK {
		t.Fatalf("native login = %d, want 200", st)
	}
	var out struct {
		Token string                 `json:"token"`
		User  struct{ Email string } `json:"user"`
	}
	unmarshal(t, body, &out)
	if !strings.HasPrefix(out.Token, "tdo_") {
		t.Fatalf("token = %q, want tdo_ prefix", out.Token)
	}

	// The token authenticates a fresh client (no cookie jar) via Bearer.
	fresh := &http.Client{}
	bearer := map[string]string{"Authorization": "Bearer " + out.Token}
	if st, _ := do(t, fresh, http.MethodGet, srv.URL+"/v1/webhooks", nil, bearer); st != http.StatusOK {
		t.Fatalf("bearer /v1/webhooks = %d, want 200", st)
	}

	// Logging out with the device token revokes it server-side (ATD-44): the same
	// token must stop authenticating afterwards.
	if st, _ := do(t, fresh, http.MethodPost, srv.URL+"/v1/auth/logout", nil, bearer); st != http.StatusOK {
		t.Fatalf("bearer logout = %d, want 200", st)
	}
	if st, _ := do(t, fresh, http.MethodGet, srv.URL+"/v1/webhooks", nil, bearer); st != http.StatusUnauthorized {
		t.Fatalf("bearer /v1/webhooks after logout = %d, want 401", st)
	}

	// Wrong password → 401.
	if st, _ := do(t, &http.Client{}, http.MethodPost, srv.URL+"/v1/auth/native/login",
		map[string]string{"email": "dev@example.com", "password": "nope-nope-nope"}, nil); st != http.StatusUnauthorized {
		t.Fatalf("bad native login = %d, want 401", st)
	}

	// OAuth start rejects a non-allowlisted redirect target (no rt stored in the
	// state cookie → the callback would fall back to the web cookie flow, so no
	// token is ever delivered to an attacker origin).
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/auth/oauth/google?redirect_to=https://evil.example/steal", nil)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("oauth start: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// OAuth is unconfigured in tests → start redirects to the SPA auth-error, not
	// to a provider; either way the disallowed target must never appear.
	if loc := resp.Header.Get("Location"); strings.Contains(loc, "evil.example") {
		t.Fatalf("disallowed redirect_to leaked into Location: %q", loc)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "punchcard_oauth_state" && strings.Contains(c.Value, "evil.example") {
			t.Fatalf("disallowed redirect_to stored in state cookie: %q", c.Value)
		}
	}
}

func unmarshal(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal %s: %v", string(b), err)
	}
}
