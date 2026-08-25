package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- config ---------------------------------------------------------------

// The config file holds a bearer token. A token readable by every process on a
// shared machine is a token that did not need stealing.
func TestConfigIsWrittenPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := SaveConfig(path, Config{BaseURL: "https://example.test", Token: "pk_secret"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %04o, want 0600", perm)
	}

	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Token != "pk_secret" || got.BaseURL != "https://example.test" {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

// A missing config is "not signed in", not a crash.
func TestLoadMissingConfigReportsSignedOut(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "nope.json"))
	if err != ErrNotLoggedIn {
		t.Fatalf("err = %v, want ErrNotLoggedIn", err)
	}
}

// --- project resolution ---------------------------------------------------

// Typing the whole project name every time is the friction a CLI exists to
// remove, so a prefix is enough — as long as it is unambiguous.
func TestResolveProjectMatchesOnPrefix(t *testing.T) {
	projects := []Project{
		{ID: "1", Name: "capsarsiv"},
		{ID: "2", Name: "punchcard"},
		{ID: "3", Name: "helva"},
	}
	got, err := resolveProject(projects, "caps")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != "1" {
		t.Fatalf("matched %+v, want capsarsiv", got)
	}
}

func TestResolveProjectIsCaseInsensitive(t *testing.T) {
	projects := []Project{{ID: "1", Name: "capsarsiv"}}
	if _, err := resolveProject(projects, "CAPS"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
}

// An exact name wins even when it is a prefix of another project's name.
// Without this, having "punchcard" and "punchcard-cli" would make the shorter
// one unreachable.
func TestExactNameBeatsAmbiguity(t *testing.T) {
	projects := []Project{
		{ID: "1", Name: "punchcard"},
		{ID: "2", Name: "punchcard-cli"},
	}
	got, err := resolveProject(projects, "punchcard")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != "1" {
		t.Fatalf("matched %+v, want the exact name", got)
	}
}

// A genuinely ambiguous prefix must say which projects it matched, or the user
// has to go look them up to find out what went wrong.
func TestAmbiguousPrefixNamesTheCandidates(t *testing.T) {
	projects := []Project{
		{ID: "1", Name: "punchcard"},
		{ID: "2", Name: "punchbowl"},
	}
	_, err := resolveProject(projects, "punch")
	if err == nil {
		t.Fatal("an ambiguous prefix must fail")
	}
	if !strings.Contains(err.Error(), "punchcard") || !strings.Contains(err.Error(), "punchbowl") {
		t.Fatalf("error does not name the candidates: %v", err)
	}
}

func TestUnknownProjectFails(t *testing.T) {
	if _, err := resolveProject([]Project{{ID: "1", Name: "capsarsiv"}}, "zzz"); err == nil {
		t.Fatal("an unknown project must fail")
	}
}

// --- duration formatting --------------------------------------------------

// The clock is the thing the user actually reads. Rounding an unfinished hour
// down to "1s" would make a running timer look stuck.
func TestFormatDuration(t *testing.T) {
	for _, tc := range []struct {
		seconds int64
		want    string
	}{
		{0, "00:00:00"},
		{59, "00:00:59"},
		{60, "00:01:00"},
		{3599, "00:59:59"},
		{3600, "01:00:00"},
		{6127, "01:42:07"},
		{360000, "100:00:00"},
	} {
		if got := formatDuration(tc.seconds); got != tc.want {
			t.Errorf("formatDuration(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

// Totals read better as "6s 12d" than as a wall clock: nobody bills in seconds.
func TestFormatTotal(t *testing.T) {
	for _, tc := range []struct {
		seconds int64
		want    string
	}{
		{0, "0d"},
		{540, "9d"},
		{3600, "1s"},
		{6127, "1s 42d"},
		{22320, "6s 12d"},
	} {
		if got := formatTotal(tc.seconds); got != tc.want {
			t.Errorf("formatTotal(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

// --- client ---------------------------------------------------------------

// fakeAPI is a stand-in for punchcard, recording what the CLI sent.
type fakeAPI struct {
	*httptest.Server
	lastAuth string
	lastBody map[string]any
	lastPath string
}

func newFakeAPI(t *testing.T, routes map[string]any) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.Header.Get("Authorization")
		f.lastPath = r.URL.Path
		if r.Body != nil {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastBody = body
		}
		resp, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 404, "code": "not_found", "detail": "no route matches " + r.URL.Path,
			})
			return
		}
		if code, isCode := resp.(int); isCode {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": code, "code": "boom", "detail": "fake failure",
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(f.Close)
	return f
}

// Every request carries the bearer token; a CLI that silently drops it would
// look like a permissions problem on the server.
func TestClientSendsBearerToken(t *testing.T) {
	api := newFakeAPI(t, map[string]any{
		"GET /v1/projects": map[string]any{"projects": []any{}},
	})
	c := New(api.URL, "pk_secret")

	if _, err := c.Projects(false); err != nil {
		t.Fatalf("projects: %v", err)
	}
	if api.lastAuth != "Bearer pk_secret" {
		t.Fatalf("Authorization = %q", api.lastAuth)
	}
}

func TestStartSendsProjectAndNote(t *testing.T) {
	api := newFakeAPI(t, map[string]any{
		"POST /v1/sessions": map[string]any{
			"id": "s1", "project_id": "p1", "note": "refactor",
			"started_at": time.Now().Format(time.RFC3339), "running": true, "seconds": 0,
		},
	})
	c := New(api.URL, "t")

	ws, err := c.Start("p1", "refactor")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if ws.ID != "s1" {
		t.Fatalf("session = %+v", ws)
	}
	if api.lastBody["project_id"] != "p1" || api.lastBody["note"] != "refactor" {
		t.Fatalf("sent %+v", api.lastBody)
	}
}

// The server reports "no timer running" as a 404. That is a normal state for a
// CLI, not an error to print a stack trace about.
func TestCurrentReportsIdleDistinctly(t *testing.T) {
	api := newFakeAPI(t, map[string]any{})
	c := New(api.URL, "t")

	_, err := c.Current()
	if err != ErrNoRunningSession {
		t.Fatalf("err = %v, want ErrNoRunningSession", err)
	}
}

// A server error must reach the user as the server's own words, not as a
// generic failure — "GitHub rejected the stored token" is actionable, "request
// failed" is not.
func TestServerProblemDetailSurfacesToTheUser(t *testing.T) {
	api := newFakeAPI(t, map[string]any{"GET /v1/projects": http.StatusInternalServerError})
	c := New(api.URL, "t")

	_, err := c.Projects(false)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "fake failure") {
		t.Fatalf("error lost the server's detail: %v", err)
	}
}

// An expired or revoked token has one fix — log in again — and the CLI should
// say so rather than reporting "unauthorized".
func TestUnauthorizedTellsTheUserToLogInAgain(t *testing.T) {
	api := newFakeAPI(t, map[string]any{"GET /v1/projects": http.StatusUnauthorized})
	c := New(api.URL, "t")

	_, err := c.Projects(false)
	if err != ErrNotLoggedIn {
		t.Fatalf("err = %v, want ErrNotLoggedIn", err)
	}
}

// --- login ----------------------------------------------------------------

// The browser hands the code back to a loopback listener; the CLI trades it for
// a token. Nothing but the code ever travels through the browser.
func TestLoginExchangesTheCallbackCode(t *testing.T) {
	api := newFakeAPI(t, map[string]any{
		"POST /v1/auth/native/exchange": map[string]any{"token": "pk_from_exchange"},
	})

	var opened string
	token, err := runLogin(api.URL, func(url string) error {
		opened = url
		// Stand in for the browser: the redirect lands on the loopback
		// listener with the one-time code.
		go func() {
			redirect := extractRedirectTo(url)
			_, _ = http.Get(redirect + "?code=one-time-code") //nolint:gosec,bodyclose // loopback, test
		}()
		return nil
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if token != "pk_from_exchange" {
		t.Fatalf("token = %q", token)
	}
	if !strings.Contains(opened, "/v1/auth/oauth/github") {
		t.Fatalf("opened the wrong URL: %s", opened)
	}
	if !strings.Contains(opened, "scope=repo") {
		t.Fatalf("login must ask for the repo scope, or commit matching stays off: %s", opened)
	}
	if api.lastBody["code"] != "one-time-code" {
		t.Fatalf("exchanged %+v", api.lastBody)
	}
}

// extractRedirectTo pulls the loopback URL back out of the authorization URL.
func extractRedirectTo(authURL string) string {
	const key = "redirect_to="
	i := strings.Index(authURL, key)
	if i < 0 {
		return ""
	}
	rest := authURL[i+len(key):]
	if j := strings.IndexByte(rest, '&'); j >= 0 {
		rest = rest[:j]
	}
	unescaped, err := urlUnescape(rest)
	if err != nil {
		return ""
	}
	return unescaped
}
