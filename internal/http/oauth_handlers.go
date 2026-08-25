package http

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/oauth"
	"github.com/cobanov/punchcard/internal/service"
)

// oauthStateCookieName carries the anti-CSRF state through the provider round
// trip. SameSite=Lax lets it survive the top-level GET redirect back from the
// provider while still blocking cross-site POST forgery.
const oauthStateCookieName = "punchcard_oauth_state"

// oauthStateTTL bounds how long a started sign-in may sit before the callback.
const oauthStateTTL = 10 * time.Minute

// registerOAuthRoutes wires the social-login endpoints as plain handlers (they
// are 302 redirects and a tiny JSON probe, not part of the JSON API surface).
func (d Deps) registerOAuthRoutes(r chi.Router) {
	r.Get("/v1/auth/providers", d.handleOAuthProviders())
	r.Get("/v1/auth/oauth/{provider}", d.handleOAuthStart())
	r.Get("/v1/auth/oauth/{provider}/callback", d.handleOAuthCallback())
	// Apple returns the callback as a cross-site form POST, because asking for a
	// scope obliges `response_mode=form_post`. Same handler — it reads the code
	// and state from whichever side of the request carries them.
	r.Post("/v1/auth/oauth/{provider}/callback", d.handleOAuthCallback())
}

// handleOAuthProviders tells the SPA which social buttons to render.
func (d Deps) handleOAuthProviders() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]bool{
			oauth.ProviderGoogle: d.OAuth.Enabled(oauth.ProviderGoogle),
			oauth.ProviderGitHub: d.OAuth.Enabled(oauth.ProviderGitHub),
			oauth.ProviderApple:  d.OAuth.Enabled(oauth.ProviderApple),
		})
	}
}

// handleOAuthStart mints a state, stores it (plus an optional native redirect
// target + client name) in a short-lived cookie, and redirects to the
// provider's consent screen. A native client passes ?redirect_to=punchcard://…
// so the callback returns a bearer token via deep link instead of a cookie.
func (d Deps) handleOAuthStart() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := d.OAuth.Get(chi.URLParam(r, "provider"))
		if !ok {
			d.redirectAuthError(w, r, "unavailable")
			return
		}
		nonce, err := auth.GenerateSecret(32)
		if err != nil {
			d.redirectAuthError(w, r, p.Name())
			return
		}
		rt := sanitizeNativeRedirect(r.URL.Query().Get("redirect_to"))
		val := nonce
		if rt != "" {
			client := sanitizeClientName(r.URL.Query().Get("client"))
			val = nonce + "|" + base64.RawURLEncoding.EncodeToString([]byte(rt)) +
				"|" + base64.RawURLEncoding.EncodeToString([]byte(client))
		}
		http.SetCookie(w, d.oauthStateCookie(val, p.UsesFormPost()))
		// `?scope=repo` on the GitHub provider is the "connect for commit
		// matching" flow: the same authorization that signs the user in also
		// grants the scanner read access, so nobody is asked to authorize the
		// same provider twice. Anything else is ignored — the set of scopes this
		// service will ever ask for is closed, so nothing user-supplied reaches
		// the URL.
		var extra []string
		if p.Name() == oauth.ProviderGitHub && r.URL.Query().Get("scope") == service.GitHubScope {
			extra = append(extra, service.GitHubScope)
		}
		// #nosec G710 -- the destination is a provider authorization endpoint from
		// a statically-configured oauth2.Endpoint (accounts.google.com /
		// github.com). The user only selects among configured providers; the target
		// host is never user-controlled.
		http.Redirect(w, r, p.AuthCodeURLWithScopes(nonce, extra...), http.StatusFound)
	}
}

// handleOAuthCallback verifies state, exchanges the code, resolves the account,
// and either sets the session cookie + lands on the SPA (web) or mints a bearer
// device token and redirects to the native deep link (non-web clients).
func (d Deps) handleOAuthCallback() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := chi.URLParam(r, "provider")
		p, ok := d.OAuth.Get(provider)
		if !ok {
			d.redirectAuthError(w, r, "unavailable")
			return
		}
		// The state cookie is single-use regardless of outcome.
		defer http.SetCookie(w, d.clearedOAuthStateCookie())

		// Apple POSTs the callback as form data; everyone else redirects with a
		// query string. ParseForm folds both into r.Form, so the checks below do
		// not have to care which arrived.
		if err := r.ParseForm(); err != nil {
			d.redirectAuthError(w, r, provider)
			return
		}
		q := r.Form
		cookie, cerr := r.Cookie(oauthStateCookieName)
		nonce, rt, client := parseStateCookie(cookie, cerr)
		if nonce == "" || q.Get("state") == "" ||
			subtle.ConstantTimeCompare([]byte(nonce), []byte(q.Get("state"))) != 1 {
			d.redirectAuthError(w, r, provider) // rt not yet trusted → web error
			return
		}
		if q.Get("error") != "" || q.Get("code") == "" {
			d.oauthError(w, r, rt, provider)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		id, err := p.Exchange(ctx, q.Get("code"))
		if err != nil {
			d.Logger.WarnContext(ctx, "oauth exchange failed", "provider", provider, "error", err)
			d.oauthError(w, r, rt, provider)
			return
		}

		if rt != "" { // native client: return a bearer token via the deep link
			user, token, terr := d.Auth.LoginOAuthNative(ctx, id, client, clientIP(r.Context()))
			if terr != nil {
				d.Logger.WarnContext(ctx, "oauth native login failed", "provider", provider, "error", terr)
				d.oauthError(w, r, rt, provider)
				return
			}
			d.Logger.InfoContext(ctx, "oauth login (native)", "provider", provider, "user_id", user.ID)
			// Deliver a single-use exchange code, not the bearer token — the deep
			// link (and thus OS logs) never sees the token itself (ATD-42).
			code, cerr := d.Auth.StashNativeToken(token)
			if cerr != nil {
				d.Logger.WarnContext(ctx, "oauth native code stash failed", "provider", provider, "error", cerr)
				d.oauthError(w, r, rt, provider)
				return
			}
			// #nosec G710 -- rt is allowlisted to punchcard:// or localhost by sanitizeNativeRedirect.
			http.Redirect(w, r, appendQuery(rt, "code", code), http.StatusFound)
			return
		}

		user, sess, err := d.Auth.LoginOAuth(ctx, id, clientIP(r.Context()), userAgent(r.Context()))
		if err != nil {
			d.Logger.WarnContext(ctx, "oauth login failed", "provider", provider, "error", err)
			d.redirectAuthError(w, r, provider)
			return
		}
		http.SetCookie(w, ptr(d.sessionCookie(sess.Token, sess.ExpiresAt)))
		d.Logger.InfoContext(ctx, "oauth login", "provider", provider, "user_id", user.ID)
		d.connectGitHubIfGranted(ctx, user.ID, id)
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// redirectAuthError sends the browser back to the SPA with a machine-readable
// marker the login screen turns into a friendly message. The marker is clamped
// to a closed set so nothing user-controlled is reflected into the redirect.
func (d Deps) redirectAuthError(w http.ResponseWriter, r *http.Request, provider string) {
	http.SetCookie(w, d.clearedOAuthStateCookie())
	http.Redirect(w, r, "/?auth_error="+authErrorMarker(provider), http.StatusFound)
}

// oauthError routes a failure to the native deep link (?error=…) when the flow
// began from a native client, or to the SPA otherwise.
func (d Deps) oauthError(w http.ResponseWriter, r *http.Request, rt, provider string) {
	http.SetCookie(w, d.clearedOAuthStateCookie())
	if rt != "" {
		// #nosec G710 -- rt is allowlisted to punchcard:// or localhost by sanitizeNativeRedirect.
		http.Redirect(w, r, appendQuery(rt, "error", authErrorMarker(provider)), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/?auth_error="+authErrorMarker(provider), http.StatusFound)
}

// authErrorMarker maps a provider to a constant marker so nothing request-derived
// is ever reflected into a redirect.
func authErrorMarker(provider string) string {
	switch provider {
	case oauth.ProviderGoogle:
		return oauth.ProviderGoogle
	case oauth.ProviderGitHub:
		return oauth.ProviderGitHub
	default:
		return "unavailable"
	}
}

// sanitizeNativeRedirect returns rt only if it targets an allowed native scheme
// (punchcard://, or the legacy ) or a loopback dev URL; anything else
// becomes "" (which falls
// back to the web cookie flow). This is the open-redirect / token-exfiltration
// guard for the deep-link token delivery.
func sanitizeNativeRedirect(rt string) string {
	if rt == "" {
		return ""
	}
	u, err := url.Parse(rt)
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "punchcard":
		return rt
	case "http", "https":
		if h := u.Hostname(); h == "localhost" || h == "127.0.0.1" {
			return rt
		}
	}
	return ""
}

func sanitizeClientName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// parseStateCookie splits the state cookie into its nonce and optional native
// redirect target + client name, re-validating the redirect against the
// allowlist (defence in depth against a tampered cookie).
func parseStateCookie(c *http.Cookie, err error) (nonce, redirectTo, client string) {
	if err != nil || c == nil || c.Value == "" {
		return "", "", ""
	}
	parts := strings.Split(c.Value, "|")
	nonce = parts[0]
	if len(parts) >= 2 {
		if b, e := base64.RawURLEncoding.DecodeString(parts[1]); e == nil {
			redirectTo = sanitizeNativeRedirect(string(b))
		}
	}
	if len(parts) >= 3 {
		if b, e := base64.RawURLEncoding.DecodeString(parts[2]); e == nil {
			client = string(b)
		}
	}
	return nonce, redirectTo, client
}

func appendQuery(base, key, val string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + key + "=" + url.QueryEscape(val)
}

func (d Deps) oauthStateCookie(state string, crossSitePost bool) *http.Cookie {
	// Lax is right for a provider that redirects back with a GET, and wrong for
	// one that POSTs: a cross-site POST does not carry a Lax cookie at all, so
	// the state check would fail every time and Apple sign-in would never
	// complete. None requires Secure, which is why Apple can only work over
	// HTTPS — it also insists on an https return URL, so nothing is lost.
	same := http.SameSiteLaxMode
	secure := d.Config.SecureCookies()
	if crossSitePost {
		same = http.SameSiteNoneMode
		secure = true
	}
	// #nosec G124 -- Secure is derived from config (true in production over HTTPS,
	// false on http://localhost where Secure cookies are dropped).
	return &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    state,
		Path:     "/v1/auth/oauth",
		MaxAge:   int(oauthStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: same,
	}
}

func (d Deps) clearedOAuthStateCookie() *http.Cookie {
	// false: clearing only needs the name and path to match, and Lax is the
	// weaker of the two attribute sets, so this expires the Apple cookie too.
	c := d.oauthStateCookie("", false) // #nosec G124 -- delegates to oauthStateCookie (HttpOnly/SameSite always set; Secure from config)
	c.MaxAge = -1
	return c
}

func ptr[T any](v T) *T { return &v }

// connectGitHubIfGranted stores the GitHub connection when the sign-in the user
// just completed carried the commit-reading scope.
//
// Failure here is deliberately not fatal to the login: the user asked to sign
// in, and refusing them a session because the optional integration could not be
// saved would be the wrong trade. The reason is logged and the connection stays
// absent, which the status endpoint reports honestly.
func (d Deps) connectGitHubIfGranted(ctx context.Context, userID uuid.UUID, id oauth.Identity) {
	if id.Provider != oauth.ProviderGitHub || id.AccessToken == "" {
		return
	}
	if !slices.Contains(strings.Split(id.GrantedScopes, ","), service.GitHubScope) {
		return
	}
	p := &auth.Principal{UserID: userID, ViaSession: true, Scope: auth.ScopeReadWrite}
	if err := d.Domain.ConnectGitHub(ctx, p, id.ProviderUserID, id.AccessToken, id.GrantedScopes); err != nil {
		d.Logger.WarnContext(ctx, "could not store github connection", "error", err)
	}
}
