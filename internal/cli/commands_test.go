package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedAPI answers a fixed script of routes and remembers what it was sent.
type scriptedAPI struct {
	*httptest.Server
	mu      sync.Mutex
	routes  map[string]any
	posts   []map[string]any
	running *map[string]any
}

func newScriptedAPI(t *testing.T) *scriptedAPI {
	t.Helper()
	s := &scriptedAPI{routes: map[string]any{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.posts = append(s.posts, body)
		}
		key := r.Method + " " + r.URL.Path
		// /v1/sessions/current is special: it is a 404 when idle, which is a
		// state rather than an error.
		if key == "GET /v1/sessions/current" {
			if s.running == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": 404, "code": "not_found", "detail": "resource not found"})
				return
			}
			_ = json.NewEncoder(w).Encode(*s.running)
			return
		}
		resp, ok := s.routes[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 404, "code": "not_found", "detail": "no route " + key})
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *scriptedAPI) route(key string, resp any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[key] = resp
}

func (s *scriptedAPI) setRunning(v map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = &v
}

// newApp wires an App against a signed-in config pointing at the fake API.
func newApp(t *testing.T, api *scriptedAPI) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SaveConfig(path, Config{BaseURL: api.URL, Token: "pk_test"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	var out, errBuf bytes.Buffer
	return &App{Out: &out, Err: &errBuf, ConfigPath: path}, &out, &errBuf
}

func TestStartResolvesProjectAndPrintsIt(t *testing.T) {
	api := newScriptedAPI(t)
	api.route("GET /v1/projects", map[string]any{"projects": []any{
		map[string]any{"id": "p1", "name": "capsarsiv", "currency": "TRY", "billable": true},
	}})
	api.route("POST /v1/sessions", map[string]any{
		"id": "s1", "project_id": "p1", "note": "yorum sistemi",
		"started_at": time.Now().Format(time.RFC3339), "running": true, "seconds": 0,
	})
	app, out, _ := newApp(t, api)

	if err := app.Start("caps", "yorum sistemi"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.Contains(out.String(), "capsarsiv") || !strings.Contains(out.String(), "yorum sistemi") {
		t.Fatalf("output does not confirm what started:\n%s", out)
	}
	// The prefix must have been resolved to an id before the request went out.
	last := api.posts[len(api.posts)-1]
	if last["project_id"] != "p1" {
		t.Fatalf("sent project_id %v, want p1", last["project_id"])
	}
}

// Stopping with nothing running is a normal outcome, not a failure: a CLI that
// exits non-zero here breaks any script that stops defensively.
func TestStopWhenIdleSaysSoWithoutFailing(t *testing.T) {
	api := newScriptedAPI(t)
	app, out, _ := newApp(t, api)

	if err := app.Stop(); err != nil {
		t.Fatalf("stop while idle should not fail: %v", err)
	}
	if !strings.Contains(out.String(), "No timer running") {
		t.Fatalf("output = %q", out)
	}
}

func TestStatusShowsElapsedTime(t *testing.T) {
	api := newScriptedAPI(t)
	api.route("GET /v1/projects", map[string]any{"projects": []any{
		map[string]any{"id": "p1", "name": "capsarsiv", "currency": "TRY", "billable": true},
	}})
	api.route("GET /v1/github/status", map[string]any{"connected": true, "login": "cobanov"})
	api.setRunning(map[string]any{
		"id": "s1", "project_id": "p1", "note": "refactor",
		"started_at": time.Now().Add(-90 * time.Minute).Format(time.RFC3339),
		"running":    true, "seconds": 5400,
	})
	app, out, _ := newApp(t, api)

	if err := app.Status(); err != nil {
		t.Fatalf("status: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "capsarsiv") || !strings.Contains(got, "refactor") {
		t.Fatalf("status does not say what is running:\n%s", got)
	}
	if !strings.Contains(got, "01:30:") {
		t.Fatalf("status does not show elapsed time:\n%s", got)
	}
}

// The scan fails in the background where nobody can see it. The CLI is where
// the user finds out, or the integration just looks broken.
func TestStatusWarnsWhenGitHubIsNotConnected(t *testing.T) {
	api := newScriptedAPI(t)
	api.route("GET /v1/github/status", map[string]any{"connected": false})
	app, _, errBuf := newApp(t, api)

	if err := app.Status(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(errBuf.String(), "not connected") {
		t.Fatalf("no warning about GitHub:\n%s", errBuf)
	}
}

func TestStatusWarnsAboutAScanFailure(t *testing.T) {
	api := newScriptedAPI(t)
	api.route("GET /v1/github/status", map[string]any{
		"connected": true, "login": "cobanov",
		"last_error": "GitHub rejected the stored token; reconnect GitHub",
	})
	app, _, errBuf := newApp(t, api)

	if err := app.Status(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(errBuf.String(), "reconnect GitHub") {
		t.Fatalf("the server's reason did not reach the user:\n%s", errBuf)
	}
}

func TestTodayListsSessionsOldestFirstWithATotal(t *testing.T) {
	api := newScriptedAPI(t)
	api.route("GET /v1/projects", map[string]any{"projects": []any{
		map[string]any{"id": "p1", "name": "capsarsiv", "currency": "TRY", "billable": true},
	}})
	start := time.Now().Add(-4 * time.Hour)
	mid := start.Add(time.Hour)
	later := time.Now().Add(-2 * time.Hour)
	laterEnd := later.Add(time.Hour)
	api.route("GET /v1/sessions", map[string]any{"sessions": []any{
		// The API returns newest first.
		map[string]any{"id": "s2", "project_id": "p1", "note": "ikinci",
			"started_at": later.Format(time.RFC3339), "ended_at": laterEnd.Format(time.RFC3339),
			"seconds": 3600, "running": false},
		map[string]any{"id": "s1", "project_id": "p1", "note": "birinci",
			"started_at": start.Format(time.RFC3339), "ended_at": mid.Format(time.RFC3339),
			"seconds": 3600, "running": false},
	}})
	api.route("GET /v1/sessions/s1/commits", map[string]any{"commits": []any{
		map[string]any{"sha": "aaaaaaa", "repo": "cobanov/capsarsiv", "message": "fix",
			"committed_at": start.Add(30 * time.Minute).Format(time.RFC3339)},
	}})
	api.route("GET /v1/sessions/s2/commits", map[string]any{"commits": []any{}})
	app, out, _ := newApp(t, api)

	if err := app.Today(1); err != nil {
		t.Fatalf("today: %v", err)
	}
	got := out.String()
	// A day reads forwards even though the API answers newest first.
	if strings.Index(got, "birinci") > strings.Index(got, "ikinci") {
		t.Fatalf("the day is printed backwards:\n%s", got)
	}
	if !strings.Contains(got, "1 commit · cobanov/capsarsiv") {
		t.Fatalf("commit line missing:\n%s", got)
	}
	if !strings.Contains(got, "total") || !strings.Contains(got, "2s") {
		t.Fatalf("total missing or wrong:\n%s", got)
	}
}

// Without a config, every command has the same answer.
func TestCommandsWithoutAConfigSayHowToSignIn(t *testing.T) {
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{},
		ConfigPath: filepath.Join(t.TempDir(), "absent.json")}

	for name, fn := range map[string]func() error{
		"status":  app.Status,
		"stop":    app.Stop,
		"today":   func() error { return app.Today(1) },
		"project": app.Projects,
	} {
		if err := fn(); err != ErrNotLoggedIn {
			t.Errorf("%s: err = %v, want ErrNotLoggedIn", name, err)
		}
	}
}

// --json is what makes the CLI usable from a script or a status bar.
func TestJSONOutputIsMachineReadable(t *testing.T) {
	api := newScriptedAPI(t)
	api.route("GET /v1/projects", map[string]any{"projects": []any{
		map[string]any{"id": "p1", "name": "capsarsiv", "currency": "TRY", "billable": true},
	}})
	app, out, _ := newApp(t, api)
	app.JSON = true

	if err := app.Projects(); err != nil {
		t.Fatalf("projects: %v", err)
	}
	var decoded []Project
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(decoded) != 1 || decoded[0].Name != "capsarsiv" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

// The rate is typed the way a person says it and stored the way money is kept.
// A CLI that made the user type 250000 for 2500/hour would be exporting the
// database's representation as the user interface.
func TestNewProjectConvertsTheRateToMinorUnits(t *testing.T) {
	api := newScriptedAPI(t)
	api.route("POST /v1/projects", map[string]any{
		"id": "p1", "name": "capsarsiv", "currency": "TRY", "billable": true,
	})
	app, _, _ := newApp(t, api)

	if err := app.NewProject("capsarsiv", "Acme", "2500", "TRY"); err != nil {
		t.Fatalf("new project: %v", err)
	}
	sent := api.posts[len(api.posts)-1]
	if sent["hourly_rate_cents"] != float64(250000) {
		t.Fatalf("hourly_rate_cents = %v, want 250000", sent["hourly_rate_cents"])
	}
	if sent["client"] != "Acme" {
		t.Fatalf("client = %v", sent["client"])
	}
}

// A fractional rate must not lose a kuruş to floating point.
func TestNewProjectRoundsAFractionalRate(t *testing.T) {
	api := newScriptedAPI(t)
	api.route("POST /v1/projects", map[string]any{"id": "p1", "name": "x", "currency": "TRY"})
	app, _, _ := newApp(t, api)

	if err := app.NewProject("x", "", "333.33", "TRY"); err != nil {
		t.Fatalf("new project: %v", err)
	}
	if got := api.posts[len(api.posts)-1]["hourly_rate_cents"]; got != float64(33333) {
		t.Fatalf("hourly_rate_cents = %v, want 33333", got)
	}
}

// No rate is not a rate of zero: a project with no rate must not be sent as
// costed at nothing.
func TestNewProjectWithoutARateSendsNone(t *testing.T) {
	api := newScriptedAPI(t)
	api.route("POST /v1/projects", map[string]any{"id": "p1", "name": "x", "currency": "TRY"})
	app, _, _ := newApp(t, api)

	if err := app.NewProject("x", "", "", ""); err != nil {
		t.Fatalf("new project: %v", err)
	}
	if _, present := api.posts[len(api.posts)-1]["hourly_rate_cents"]; present {
		t.Fatal("an unset rate must be absent, not zero")
	}
}

func TestNewProjectRejectsANonNumericRate(t *testing.T) {
	api := newScriptedAPI(t)
	app, _, _ := newApp(t, api)
	if err := app.NewProject("x", "", "bedava", ""); err == nil {
		t.Fatal("a rate that is not a number must be refused")
	}
}

// Linking resolves the project prefix like every other command.
func TestLinkRepoResolvesTheProjectPrefix(t *testing.T) {
	api := newScriptedAPI(t)
	api.route("GET /v1/projects", map[string]any{"projects": []any{
		map[string]any{"id": "p1", "name": "capsarsiv", "currency": "TRY", "billable": true},
	}})
	api.route("POST /v1/projects/p1/repos", map[string]any{
		"id": "r1", "project_id": "p1", "full_name": "cobanov/capsarsiv",
	})
	app, out, _ := newApp(t, api)

	if err := app.LinkRepo("caps", "cobanov/capsarsiv"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if !strings.Contains(out.String(), "cobanov/capsarsiv") {
		t.Fatalf("output = %q", out)
	}
	// Saying it is optional is the point — otherwise it reads as a required step.
	if !strings.Contains(out.String(), "optional") {
		t.Fatalf("linking must be presented as optional:\n%s", out)
	}
}
