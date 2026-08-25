package http

import (
	"net/http"
	"net/http/cookiejar"
	"testing"
)

// The account plane — delete, export, token and session management, password,
// 2FA, profile — is closed to automation tokens (0.4.6) because the export
// alone reaches every list, every soft-deleted task and the metadata of the
// account's other tokens.
//
// That rule was written as "must be a session", and it locked out the phone:
// the WKWebView calls the API cross-origin and iOS drops the cookie, so the
// native shells sign in with a device token. The whole Settings screen was dead
// there, account deletion included — which Apple requires to be reachable in
// the app under Guideline 5.1.1(v). Found by opening the built app in the
// simulator; nothing in the suite said a word.
//
// So these two tests are a pair and must stay one: the device token opens the
// account plane, and a user-issued PAT still does not. Loosen one without the
// other and either the phone breaks again or 0.4.6 quietly reopens.

func nativeToken(t *testing.T, base, email, password string) string {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	if st, _ := do(t, &http.Client{Jar: jar}, http.MethodPost, base+"/v1/auth/register",
		map[string]string{"email": email, "password": password}, nil); st != http.StatusCreated {
		t.Fatalf("register = %d, want 201", st)
	}
	// A cookieless client, which is the whole point: this is how a phone signs in.
	st, body := do(t, &http.Client{}, http.MethodPost, base+"/v1/auth/native/login",
		map[string]string{"email": email, "password": password, "client_name": "iPhone"}, nil)
	if st != http.StatusOK {
		t.Fatalf("native login = %d, want 200", st)
	}
	var out struct {
		Token string `json:"token"`
	}
	unmarshal(t, body, &out)
	if out.Token == "" {
		t.Fatal("native login returned no token")
	}
	return out.Token
}

func TestDeviceTokenReachesTheAccountPlane(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL

	token := nativeToken(t, base, "device-plane@example.com", "supersecret123")
	bearer := map[string]string{"Authorization": "Bearer " + token}
	c := &http.Client{}

	// One case per surface of the Settings screen that was dead on the phone.
	// A device token carries no ambient authority, so no CSRF header is involved.
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   any
		want   int
	}{
		{"list sessions", http.MethodGet, "/v1/me/sessions", nil, http.StatusOK},
		{"list tokens", http.MethodGet, "/v1/tokens", nil, http.StatusOK},
		{"export", http.MethodGet, "/v1/me/export", nil, http.StatusOK},
		{"update profile", http.MethodPatch, "/v1/me", map[string]string{"display_name": "Mert"}, http.StatusOK},
		{"start 2fa setup", http.MethodPost, "/v1/me/2fa/setup", nil, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, body := do(t, c, tc.method, base+tc.path, tc.body, bearer)
			if st != tc.want {
				t.Fatalf("%s %s = %d, want %d: %s", tc.method, tc.path, st, tc.want, body)
			}
		})
	}

	// Account deletion last: it ends the account, so nothing can follow it.
	// This is the one Apple requires to work from inside the app.
	if st, body := do(t, c, http.MethodDelete, base+"/v1/me", nil, bearer); st != http.StatusOK {
		t.Fatalf("delete account via device token = %d, want 200: %s", st, body)
	}
}

func TestUserIssuedPATStillCannotReachTheAccountPlane(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	session, _ := registerActor(t, base, "pat-plane@example.com")

	// The widest token the API will mint. If even this is refused, a narrower
	// one certainly is.
	status, body := do(t, session, http.MethodPost, base+"/v1/tokens",
		map[string]any{"name": "wide", "scope": "read_write"}, csrf)
	must(t, "create token", status, http.StatusCreated)
	var token struct {
		Secret string `json:"secret"`
	}
	unmarshal(t, body, &token)

	bearer := map[string]string{"Authorization": "Bearer " + token.Secret}
	c := &http.Client{}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"list sessions", http.MethodGet, "/v1/me/sessions", nil},
		{"list tokens", http.MethodGet, "/v1/tokens", nil},
		{"export", http.MethodGet, "/v1/me/export", nil},
		{"update profile", http.MethodPatch, "/v1/me", map[string]string{"display_name": "nope"}},
		{"start 2fa setup", http.MethodPost, "/v1/me/2fa/setup", nil},
		{"delete account", http.MethodDelete, "/v1/me", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, body := do(t, c, tc.method, base+tc.path, tc.body, bearer)
			if st != http.StatusForbidden {
				t.Fatalf("%s %s via PAT = %d, want 403: %s", tc.method, tc.path, st, body)
			}
		})
	}

	// And the token is still a working token for the data plane, so the refusals
	// above are about the plane rather than a broken credential.
	if st, _ := do(t, c, http.MethodGet, base+"/v1/lists", nil, bearer); st != http.StatusOK {
		t.Fatalf("PAT /v1/lists = %d, want 200", st)
	}
}
