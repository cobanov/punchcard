// Package oauth implements social sign-in (Google + GitHub) via the OAuth 2.0
// authorization-code flow. It is a thin adapter: it turns a provider's
// authorization code into a verified Identity (stable provider id + verified
// email). Account linking, user creation, and session issuance stay in the
// service layer; cookie/redirect handling stays in the transport layer.
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"

	"github.com/cobanov/punchcard/internal/config"
)

// Provider names. These are also the path segments in /v1/auth/oauth/{provider}.
const (
	ProviderGoogle = "google"
	ProviderGitHub = "github"
)

// Identity is a verified social profile. EmailVerified is only ever true when
// the provider itself asserts the email is verified; the service layer refuses
// to link or create an account otherwise.
type Identity struct {
	Provider       string
	ProviderUserID string
	Email          string
	Name           string
	EmailVerified  bool

	// Username is the provider's human-facing handle, when it has one. GitHub
	// is the only provider that does, and punchcard needs it for a reason
	// nothing else does: the commit scanner filters by `author=<login>`, and a
	// numeric id matches nothing — silently, which is the failure mode this
	// whole feature is built to avoid. It is NOT an identity: GitHub logins can
	// be renamed, so account linking still keys on ProviderUserID.
	Username string

	// AccessToken and GrantedScopes are what the provider actually handed over.
	// Only the GitHub connection uses them, and only to store an encrypted
	// token for the commit scanner. Never log either field.
	AccessToken   string
	GrantedScopes string
}

// Provider wires one social-login provider: its oauth2 config plus the
// provider-specific call that turns a token into a verified Identity.
type Provider struct {
	name  string
	conf  *oauth2.Config
	fetch func(ctx context.Context, conf *oauth2.Config, tok *oauth2.Token) (Identity, error)
	// Set only for Apple, which cannot use oauth2.Config: its client secret is a
	// JWT signed per request rather than a static string. See apple.go.
	apple *appleConfig
}

// Providers holds the configured providers. Only those with both a client id
// and secret are present; the rest are simply absent (Get returns false).
type Providers struct {
	m map[string]*Provider
}

// New builds the configured providers from config. Callback URLs are derived
// from PublicBaseURL so the provider app registration is the single source of
// truth for redirect URIs.
func New(cfg *config.Config) *Providers {
	ps := &Providers{m: map[string]*Provider{}}
	callback := func(p string) string { return cfg.PublicBaseURL + "/v1/auth/oauth/" + p + "/callback" }

	if cfg.GoogleOAuthEnabled() {
		ps.m[ProviderGoogle] = &Provider{
			name: ProviderGoogle,
			conf: &oauth2.Config{
				ClientID:     cfg.GoogleClientID,
				ClientSecret: cfg.GoogleClientSecret,
				RedirectURL:  callback(ProviderGoogle),
				Scopes:       []string{"openid", "email", "profile"},
				Endpoint:     google.Endpoint,
			},
			fetch: fetchGoogle,
		}
	}
	if cfg.GitHubOAuthEnabled() {
		ps.m[ProviderGitHub] = &Provider{
			name: ProviderGitHub,
			conf: &oauth2.Config{
				ClientID:     cfg.GitHubClientID,
				ClientSecret: cfg.GitHubClientSecret,
				RedirectURL:  callback(ProviderGitHub),
				Scopes:       []string{"read:user", "user:email"},
				Endpoint:     github.Endpoint,
			},
			fetch: fetchGitHub,
		}
	}
	if cfg.AppleOAuthEnabled() {
		key, err := parseApplePrivateKey(cfg.ApplePrivateKey)
		if err != nil {
			// A malformed key is a deployment mistake, not a runtime condition.
			// Leaving the provider out means the button simply does not appear,
			// which is the same outcome as not configuring it at all — and far
			// better than a button that fails at the callback.
			log.Printf("apple sign-in disabled: %v", err)
		} else {
			ps.m[ProviderApple] = &Provider{
				name: ProviderApple,
				apple: &appleConfig{
					clientID:   cfg.AppleClientID,
					teamID:     cfg.AppleTeamID,
					keyID:      cfg.AppleKeyID,
					privateKey: key,
					redirect:   callback(ProviderApple),
				},
			}
		}
	}
	return ps
}

// UsesFormPost reports whether the provider returns its callback as a
// cross-site POST rather than a redirect with query parameters. Only Apple
// does, and only because asking for a scope obliges it to.
func (p *Provider) UsesFormPost() bool { return p.name == ProviderApple }

// Get returns the named provider, or false if it is not configured.
func (ps *Providers) Get(name string) (*Provider, bool) {
	p, ok := ps.m[name]
	return p, ok
}

// Enabled reports whether the named provider is configured.
func (ps *Providers) Enabled(name string) bool {
	_, ok := ps.m[name]
	return ok
}

// Name is the provider identifier ("google"|"github").
func (p *Provider) Name() string { return p.name }

// AuthCodeURL builds the provider's authorization URL carrying an opaque state.
func (p *Provider) AuthCodeURL(state string) string {
	if p.apple != nil {
		return p.apple.authCodeURL(state)
	}
	opts := []oauth2.AuthCodeOption{}
	if p.name == ProviderGoogle {
		// Always show the account chooser; we don't need offline/refresh tokens.
		opts = append(opts, oauth2.AccessTypeOnline, oauth2.SetAuthURLParam("prompt", "select_account"))
	}
	return p.conf.AuthCodeURL(state, opts...)
}

// Exchange swaps an authorization code for a verified Identity.
//
// The identity carries the raw access token. Sign-in itself has no use for it —
// but punchcard's GitHub scanner does, and asking the user to authorize twice
// (once to sign in, once to read commits) for the same provider would be a
// worse experience than storing what the one authorization already granted.
// Callers that do not need it simply ignore the field; nothing persists it
// except Domain.ConnectGitHub, which encrypts it first.
func (p *Provider) Exchange(ctx context.Context, code string) (Identity, error) {
	if p.apple != nil {
		return exchangeApple(ctx, *p.apple, code)
	}
	tok, err := p.conf.Exchange(ctx, code)
	if err != nil {
		return Identity{}, fmt.Errorf("exchange code: %w", err)
	}
	id, err := p.fetch(ctx, p.conf, tok)
	if err != nil {
		return Identity{}, err
	}
	id.Provider = p.name
	id.AccessToken = tok.AccessToken
	// GitHub returns the granted scopes on the token response; they can be
	// narrower than what was asked for, because the user may decline.
	if raw, ok := tok.Extra("scope").(string); ok {
		id.GrantedScopes = raw
	}
	return id, nil
}

// AuthCodeURLWithScopes is AuthCodeURL with extra scopes merged in, used by the
// "connect GitHub for commit matching" flow to ask for `repo` on top of the
// sign-in scopes.
func (p *Provider) AuthCodeURLWithScopes(state string, extra ...string) string {
	if p.apple != nil || len(extra) == 0 {
		return p.AuthCodeURL(state)
	}
	widened := *p.conf
	widened.Scopes = append(append([]string{}, p.conf.Scopes...), extra...)
	return widened.AuthCodeURL(state)
}

// fetchGoogle reads the OIDC userinfo endpoint. Google returns email_verified
// as a real JSON boolean there, so no id_token verification library is needed.
func fetchGoogle(ctx context.Context, conf *oauth2.Config, tok *oauth2.Token) (Identity, error) {
	var g struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := getJSON(ctx, conf.Client(ctx, tok), "https://openidconnect.googleapis.com/v1/userinfo", nil, &g); err != nil {
		return Identity{}, fmt.Errorf("google userinfo: %w", err)
	}
	return Identity{
		ProviderUserID: g.Sub,
		Email:          g.Email,
		Name:           g.Name,
		EmailVerified:  g.EmailVerified,
	}, nil
}

// fetchGitHub reads the profile and the verified-email list. GitHub's profile
// email can be unverified/public, so the address is taken only from the
// primary+verified entry of /user/emails.
func fetchGitHub(ctx context.Context, conf *oauth2.Config, tok *oauth2.Token) (Identity, error) {
	return fetchGitHubFrom(ctx, conf.Client(ctx, tok), "https://api.github.com")
}

// fetchGitHubFrom is fetchGitHub with the API's address as a parameter, so the
// identity mapping can be tested against a fake rather than against GitHub.
func fetchGitHubFrom(ctx context.Context, client *http.Client, baseURL string) (Identity, error) {
	ghHeaders := map[string]string{"Accept": "application/vnd.github+json"}

	var prof struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Login string `json:"login"`
	}
	if err := getJSON(ctx, client, baseURL+"/user", ghHeaders, &prof); err != nil {
		return Identity{}, fmt.Errorf("github user: %w", err)
	}

	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, client, baseURL+"/user/emails", ghHeaders, &emails); err != nil {
		return Identity{}, fmt.Errorf("github emails: %w", err)
	}

	email := ""
	for _, e := range emails { // prefer the primary verified address
		if e.Primary && e.Verified {
			email = e.Email
			break
		}
	}
	if email == "" {
		for _, e := range emails { // fall back to any verified address
			if e.Verified {
				email = e.Email
				break
			}
		}
	}

	name := prof.Name
	if name == "" {
		name = prof.Login
	}
	return Identity{
		ProviderUserID: strconv.FormatInt(prof.ID, 10),
		Username:       prof.Login,
		Email:          email,
		Name:           name,
		EmailVerified:  email != "", // only verified addresses reach here
	}, nil
}

// getJSON performs a GET with optional headers and decodes a JSON body. The
// body is capped to blunt a hostile/oversized provider response.
func getJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
