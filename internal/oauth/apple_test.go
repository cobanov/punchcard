package oauth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The Apple flow cannot be exercised end to end without a real developer
// account, so the parts that must be right the first time are tested directly:
// the client-secret JWT Apple will reject if a single byte is off, and the
// claim checks that decide whether a token is ours.

func testKeyPEM(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), key
}

func TestParseApplePrivateKey(t *testing.T) {
	pemText, want := testKeyPEM(t)

	got, err := parseApplePrivateKey(pemText)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Compared by re-encoding rather than by reaching into D: big.Int is not the
	// right lens on a cryptographic value, and staticcheck says so.
	gotDER, err := x509.MarshalPKCS8PrivateKey(got)
	if err != nil {
		t.Fatal(err)
	}
	wantDER, err := x509.MarshalPKCS8PrivateKey(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotDER, wantDER) {
		t.Fatal("parsed a different key")
	}

	// Config files and compose values routinely deliver the PEM with literal
	// backslash-n instead of newlines; a key that only parses when hand-pasted
	// is a key that fails on the server.
	if _, err := parseApplePrivateKey(strings.ReplaceAll(pemText, "\n", `\n`)); err != nil {
		t.Fatalf("escaped-newline PEM should parse: %v", err)
	}

	if _, err := parseApplePrivateKey("not a pem"); err == nil {
		t.Fatal("garbage should not parse")
	}
}

func TestClientSecretIsAValidES256JWT(t *testing.T) {
	pemText, key := testKeyPEM(t)
	priv, err := parseApplePrivateKey(pemText)
	if err != nil {
		t.Fatal(err)
	}
	c := appleConfig{clientID: "run.cobanov.punchcard.signin", teamID: "6U58AKY6F8", keyID: "ABC123DEFG", privateKey: priv}

	now := time.Unix(1_760_000_000, 0)
	tokenStr, err := c.clientSecret(now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT segments, got %d", len(parts))
	}

	var hdr map[string]string
	decode(t, parts[0], &hdr)
	if hdr["alg"] != "ES256" || hdr["kid"] != "ABC123DEFG" {
		t.Fatalf("header = %v", hdr)
	}

	var claims map[string]any
	decode(t, parts[1], &claims)
	if claims["iss"] != "6U58AKY6F8" {
		t.Errorf("iss = %v, want the team id", claims["iss"])
	}
	if claims["sub"] != "run.cobanov.punchcard.signin" {
		t.Errorf("sub = %v, want the Services ID", claims["sub"])
	}
	if claims["aud"] != appleIssuer {
		t.Errorf("aud = %v", claims["aud"])
	}
	exp, iat := claims["exp"].(float64), claims["iat"].(float64)
	if exp <= iat {
		t.Errorf("exp %v must be after iat %v", exp, iat)
	}
	// Apple rejects anything over six months. Ours is minutes, but the ceiling
	// is what breaks silently months later if this is ever widened.
	if time.Duration(exp-iat)*time.Second > 180*24*time.Hour {
		t.Errorf("client secret lifetime exceeds Apple's six-month ceiling")
	}

	// The signature must be the raw 64-byte r||s pair JWS specifies, not the
	// ASN.1 DER that ecdsa.Sign's sibling API returns — a mistake Apple reports
	// only as an opaque invalid_client.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes, want 64 (r||s)", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, sum[:], r, s) {
		t.Fatal("signature does not verify against the key it was signed with")
	}
}

func TestAuthCodeURLAsksForFormPost(t *testing.T) {
	c := appleConfig{clientID: "svc", redirect: "https://todo.cobanov.run/v1/auth/oauth/apple/callback"}
	u, err := url.Parse(c.authCodeURL("state-123"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	// Apple rejects a scoped request that expects a query redirect, so these two
	// are a pair: change one and sign-in breaks.
	if q.Get("scope") == "" {
		t.Error("no scope requested")
	}
	if q.Get("response_mode") != "form_post" {
		t.Errorf("response_mode = %q, want form_post alongside a scope", q.Get("response_mode"))
	}
	if q.Get("state") != "state-123" {
		t.Errorf("state = %q", q.Get("state"))
	}
	if q.Get("redirect_uri") != c.redirect {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestAppleIdentityChecksClaims(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	mint := func(claims map[string]any) string {
		b, _ := json.Marshal(claims)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	base := func() map[string]any {
		return map[string]any{
			"iss": appleIssuer, "aud": "svc", "sub": "001234.abc",
			"exp": now.Add(time.Hour).Unix(), "email": "someone@privaterelay.appleid.com",
		}
	}

	id, err := appleIdentity(mint(base()), "svc", now)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if id.ProviderUserID != "001234.abc" || id.Provider != ProviderApple {
		t.Fatalf("identity = %+v", id)
	}
	// A relay address carries no email_verified claim. Apple issues and delivers
	// those itself, so refusing them would turn away exactly the people who chose
	// the more private option.
	if !id.EmailVerified {
		t.Error("a relay address should count as verified")
	}

	for name, mutate := range map[string]func(map[string]any){
		"wrong issuer":   func(c map[string]any) { c["iss"] = "https://evil.example" },
		"wrong audience": func(c map[string]any) { c["aud"] = "someone-elses-service-id" },
		"expired":        func(c map[string]any) { c["exp"] = now.Add(-time.Hour).Unix() },
		"no subject":     func(c map[string]any) { delete(c, "sub") },
		"no email":       func(c map[string]any) { delete(c, "email") },
	} {
		t.Run(name, func(t *testing.T) {
			c := base()
			mutate(c)
			if _, err := appleIdentity(mint(c), "svc", now); err == nil {
				t.Fatalf("%s should be rejected", name)
			}
		})
	}

	if _, err := appleIdentity("not-a-jwt", "svc", now); err == nil {
		t.Error("a non-JWT should be rejected")
	}
}

func TestTruthyReadsApplesQuotedBooleans(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want bool
	}{
		{true, true}, {false, false},
		{"true", true}, {"false", false}, // Apple sends these quoted
		{nil, true}, // absent: a relay address
	} {
		if got := truthy(tc.in); got != tc.want {
			t.Errorf("truthy(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func decode(t *testing.T, seg string, into any) {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatal(err)
	}
}
