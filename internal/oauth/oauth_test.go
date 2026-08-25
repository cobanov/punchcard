package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// GitHub's identity has to carry the LOGIN as well as the numeric id.
//
// The two are not interchangeable and each has one job. Account linking keys on
// the numeric id, because a login can be renamed out from under it. The commit
// scanner asks GitHub for `author=<login>`, and a numeric id there matches
// nothing — returning an empty commit list, which is indistinguishable from
// "this person did no work". That is exactly the silent failure the scanner is
// designed to avoid, and it shipped once because the login was fetched and then
// thrown away.
func TestGitHubIdentityCarriesTheLoginAndTheID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"id":29142615,"login":"cobanov","name":"Mert Cobanov"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"m@example.com","primary":true,"verified":true}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	id, err := fetchGitHubFrom(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if id.Username != "cobanov" {
		t.Fatalf("Username = %q, want the login — the scanner filters commits by it", id.Username)
	}
	if id.ProviderUserID != "29142615" {
		t.Fatalf("ProviderUserID = %q, want the numeric id — logins can be renamed", id.ProviderUserID)
	}
	if id.Email != "m@example.com" {
		t.Fatalf("Email = %q", id.Email)
	}
}
