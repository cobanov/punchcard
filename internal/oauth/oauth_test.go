package oauth

import (
	"net/url"
	"testing"

	"github.com/cobanov/punchcard/internal/config"
)

func testProviders() *Providers {
	return New(&config.Config{
		PublicBaseURL:      "https://todo.example.com",
		GoogleClientID:     "gid",
		GoogleClientSecret: "gsecret",
		GitHubClientID:     "hid",
		GitHubClientSecret: "hsecret",
	})
}

func TestEnabledReflectsConfig(t *testing.T) {
	// Only GitHub configured.
	ps := New(&config.Config{
		PublicBaseURL:      "https://todo.example.com",
		GitHubClientID:     "hid",
		GitHubClientSecret: "hsecret",
	})
	if ps.Enabled(ProviderGoogle) {
		t.Fatal("google should be disabled without credentials")
	}
	if !ps.Enabled(ProviderGitHub) {
		t.Fatal("github should be enabled")
	}
	if _, ok := ps.Get(ProviderGoogle); ok {
		t.Fatal("Get(google) should report not-configured")
	}
}

func TestAuthCodeURL(t *testing.T) {
	ps := testProviders()

	g, ok := ps.Get(ProviderGoogle)
	if !ok {
		t.Fatal("google not configured")
	}
	raw := g.AuthCodeURL("xyz-state")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := u.Query()
	if got := q.Get("client_id"); got != "gid" {
		t.Errorf("client_id = %q", got)
	}
	if got := q.Get("state"); got != "xyz-state" {
		t.Errorf("state = %q", got)
	}
	if got := q.Get("redirect_uri"); got != "https://todo.example.com/v1/auth/oauth/google/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	if got := q.Get("scope"); got != "openid email profile" {
		t.Errorf("scope = %q", got)
	}
	if got := q.Get("prompt"); got != "select_account" {
		t.Errorf("prompt = %q", got)
	}
	if u.Host != "accounts.google.com" {
		t.Errorf("host = %q, want accounts.google.com", u.Host)
	}

	h, _ := ps.Get(ProviderGitHub)
	hu, _ := url.Parse(h.AuthCodeURL("s"))
	if hu.Host != "github.com" {
		t.Errorf("github host = %q", hu.Host)
	}
	if got := hu.Query().Get("scope"); got != "read:user user:email" {
		t.Errorf("github scope = %q", got)
	}
	if got := hu.Query().Get("redirect_uri"); got != "https://todo.example.com/v1/auth/oauth/github/callback" {
		t.Errorf("github redirect_uri = %q", got)
	}
}
