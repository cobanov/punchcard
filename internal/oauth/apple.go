package oauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sign in with Apple differs from the other two providers in three ways, and
// each of them costs code here rather than configuration:
//
//  1. **The client secret is not a secret you are given.** It is a JWT you sign
//     yourself, per request, with a P-256 key from the developer portal. Apple
//     caps its lifetime at six months; we mint a short-lived one per exchange,
//     so there is nothing to rotate and nothing long-lived to leak.
//
//  2. **Apple POSTs the callback**, because asking for a scope forces
//     `response_mode=form_post`. That reaches the transport layer: the callback
//     route accepts POST for Apple, and the state cookie has to be SameSite=None
//     or the browser will not send it on a cross-site POST at all.
//
//  3. **The name arrives exactly once**, in the first authorization's form body,
//     never from the token endpoint. We do not depend on it: the account is keyed
//     on `sub`, and punchcard asks for a display name in Settings anyway.
const (
	ProviderApple = "apple"

	appleAuthURL = "https://appleid.apple.com/auth/authorize"
	// #nosec G101 -- a published OAuth endpoint, flagged only because the
	// constant's name contains "token". There is no secret here; the actual
	// credential is signed per request by clientSecret below.
	appleTokenURL = "https://appleid.apple.com/auth/token"
	appleIssuer   = "https://appleid.apple.com"

	// Apple's ceiling is six months. A minted-per-exchange secret has no reason
	// to outlive the request it authenticates.
	appleSecretTTL = 5 * time.Minute
)

// appleConfig is everything the portal gives you. clientID is the **Services
// ID**, not the app's bundle identifier — the bundle id is used by the native
// SDK flow, and this is the web flow that both the browser and the Tauri shell
// go through.
type appleConfig struct {
	clientID   string
	teamID     string
	keyID      string
	privateKey *ecdsa.PrivateKey
	redirect   string
}

// parseApplePrivateKey reads the PKCS#8 PEM that the portal downloads as a .p8.
func parseApplePrivateKey(pemText string) (*ecdsa.PrivateKey, error) {
	// Config often arrives with literal \n from an env file or a compose value.
	block, _ := pem.Decode([]byte(strings.ReplaceAll(pemText, `\n`, "\n")))
	if block == nil {
		return nil, fmt.Errorf("apple: private key is not PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apple: parse private key: %w", err)
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("apple: private key is %T, want ECDSA P-256", key)
	}
	return ec, nil
}

// clientSecret builds the ES256 JWT Apple accepts in place of a static secret.
//
// Written against crypto/ecdsa rather than pulling in a JWT library: this is the
// only JWT punchcard signs, the format is fully specified, and the alternative is a
// dependency in the one package where an unreviewed one would hurt most.
func (c appleConfig) clientSecret(now time.Time) (string, error) {
	header := map[string]string{"alg": "ES256", "kid": c.keyID, "typ": "JWT"}
	claims := map[string]any{
		"iss": c.teamID,
		"iat": now.Unix(),
		"exp": now.Add(appleSecretTTL).Unix(),
		"aud": appleIssuer,
		"sub": c.clientID,
	}
	seg := func(v any) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	h, err := seg(header)
	if err != nil {
		return "", err
	}
	p, err := seg(claims)
	if err != nil {
		return "", err
	}
	signing := h + "." + p
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, c.privateKey, sum[:])
	if err != nil {
		return "", fmt.Errorf("apple: sign client secret: %w", err)
	}
	// JWS wants each half fixed-width and left-padded — not ASN.1, and not the
	// variable-length big-endian that Bytes() returns.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// appleAuthCodeURL asks for the scopes and, because it asks, must also ask for
// form_post: Apple rejects a scoped request that expects a query redirect.
func (c appleConfig) authCodeURL(state string) string {
	q := url.Values{
		"client_id":     {c.clientID},
		"redirect_uri":  {c.redirect},
		"response_type": {"code"},
		"scope":         {"name email"},
		"response_mode": {"form_post"},
		"state":         {state},
	}
	return appleAuthURL + "?" + q.Encode()
}

// exchangeApple swaps the code for an identity.
//
// The id_token's signature is deliberately not verified, for the same reason
// fetchGoogle trusts the userinfo response: this token is the body of Apple's
// TLS-authenticated reply to a POST we made ourselves, carrying a secret only we
// can sign. There is no untrusted hop for a signature to protect. (OIDC Core
// §3.1.3.7 says as much for the code flow.) The claims are still checked —
// issuer, audience, expiry — because a mismatch there means we are talking to
// the wrong app, not that someone forged a token.
func exchangeApple(ctx context.Context, c appleConfig, code string) (Identity, error) {
	secret, err := c.clientSecret(time.Now())
	if err != nil {
		return Identity{}, err
	}
	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {secret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {c.redirect},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, appleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("apple: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("apple: token endpoint returned %s", resp.Status)
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return Identity{}, fmt.Errorf("apple: decode token response: %w", err)
	}
	if tok.IDToken == "" {
		return Identity{}, fmt.Errorf("apple: token response carried no id_token")
	}
	return appleIdentity(tok.IDToken, c.clientID, time.Now())
}

// appleIdentity reads and checks the claims of an id_token.
func appleIdentity(idToken, clientID string, now time.Time) (Identity, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return Identity{}, fmt.Errorf("apple: id_token is not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, fmt.Errorf("apple: decode id_token claims: %w", err)
	}
	var c struct {
		Iss   string `json:"iss"`
		Aud   string `json:"aud"`
		Sub   string `json:"sub"`
		Exp   int64  `json:"exp"`
		Email string `json:"email"`
		// Apple sends these as strings, not booleans, and not always at all.
		EmailVerified any `json:"email_verified"`
		IsPrivate     any `json:"is_private_email"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return Identity{}, fmt.Errorf("apple: parse id_token claims: %w", err)
	}
	if c.Iss != appleIssuer {
		return Identity{}, fmt.Errorf("apple: id_token issuer %q", c.Iss)
	}
	if c.Aud != clientID {
		return Identity{}, fmt.Errorf("apple: id_token audience %q, want the configured Services ID", c.Aud)
	}
	if c.Exp != 0 && now.After(time.Unix(c.Exp, 0)) {
		return Identity{}, fmt.Errorf("apple: id_token expired")
	}
	if c.Sub == "" {
		return Identity{}, fmt.Errorf("apple: id_token carried no subject")
	}
	if c.Email == "" {
		// Only possible if the scope was refused. Without an email there is no
		// account to create or link, so this is an error rather than a partial.
		return Identity{}, fmt.Errorf("apple: id_token carried no email")
	}
	return Identity{
		Provider:       ProviderApple,
		ProviderUserID: c.Sub,
		Email:          c.Email,
		// Apple only ever returns addresses it controls: either the real one it
		// has verified, or a relay it operates. Both are deliverable and both are
		// as verified as an email gets.
		EmailVerified: truthy(c.EmailVerified),
	}, nil
}

// truthy reads Apple's habit of sending JSON booleans as quoted strings.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	default:
		// Absent. Apple omits email_verified for relay addresses, which it issues
		// and delivers itself — treating that as unverified would refuse exactly
		// the users who chose the more private option.
		return true
	}
}
