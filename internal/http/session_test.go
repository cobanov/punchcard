package http

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startTimer creates a session and returns its id.
func startTimer(t *testing.T, c *http.Client, base, projectID, note string) string {
	t.Helper()
	code, body := do(t, c, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": projectID, "note": note}, testCSRF())
	if code != http.StatusCreated {
		t.Fatalf("start timer = %d: %s", code, body)
	}
	var out struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &out)
	return out.ID
}

func firstProjectID(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	_, body := do(t, c, http.MethodGet, base+"/v1/projects", nil, nil)
	var out struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
	}
	unmarshal(t, body, &out)
	if len(out.Projects) == 0 {
		t.Fatal("account has no project")
	}
	return out.Projects[0].ID
}

// The whole loop a client performs on an ordinary day.
func TestTimerRoundTrip(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "timer@example.com")
	projectID := firstProjectID(t, c, base)

	// Idle: there is no current session, and that is a 404 rather than an
	// empty record.
	must(t, "current while idle",
		st(t, c, http.MethodGet, base+"/v1/sessions/current", nil, nil), http.StatusNotFound)

	id := startTimer(t, c, base, projectID, "yorum sistemi refactor")

	code, body := do(t, c, http.MethodGet, base+"/v1/sessions/current", nil, nil)
	must(t, "current while running", code, http.StatusOK)
	var current struct {
		ID      string `json:"id"`
		Running bool   `json:"running"`
		Note    string `json:"note"`
	}
	unmarshal(t, body, &current)
	if current.ID != id || !current.Running || current.Note != "yorum sistemi refactor" {
		t.Fatalf("current session = %+v", current)
	}

	code, body = do(t, c, http.MethodPost, base+"/v1/sessions/"+id+"/stop", map[string]any{}, csrf)
	must(t, "stop", code, http.StatusOK)
	var stopped struct {
		Running   bool   `json:"running"`
		SyncState string `json:"commit_sync_state"`
	}
	unmarshal(t, body, &stopped)
	if stopped.Running {
		t.Fatal("session still running after stop")
	}
	if stopped.SyncState != "pending" {
		t.Fatalf("commit_sync_state = %q, want pending", stopped.SyncState)
	}

	must(t, "current after stop",
		st(t, c, http.MethodGet, base+"/v1/sessions/current", nil, nil), http.StatusNotFound)
}

// Starting replaces a running timer by default; a caller that would rather hear
// about the conflict asks for it.
func TestStartWithStopCurrentFalseConflicts(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "conflict@example.com")
	projectID := firstProjectID(t, c, base)

	startTimer(t, c, base, projectID, "bir")
	code, _ := do(t, c, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": projectID, "stop_current": false}, csrf)
	if code != http.StatusConflict {
		t.Fatalf("start with stop_current=false = %d, want 409", code)
	}
}

func TestSessionIsolation(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	alice, _ := registerActor(t, base, "alice-s@example.com")
	bob, _ := registerActor(t, base, "bob-s@example.com")

	id := startTimer(t, alice, base, firstProjectID(t, alice, base), "gizli")

	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"read", http.MethodGet, "/v1/sessions/" + id, nil},
		{"stop", http.MethodPost, "/v1/sessions/" + id + "/stop", map[string]any{}},
		{"update", http.MethodPatch, "/v1/sessions/" + id, map[string]any{"note": "hijacked"}},
		{"delete", http.MethodDelete, "/v1/sessions/" + id, nil},
		{"commits", http.MethodGet, "/v1/sessions/" + id + "/commits", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, rb := do(t, bob, tc.method, base+tc.path, tc.body, csrf)
			if code != http.StatusNotFound {
				t.Fatalf("%s another user's session = %d, want 404: %s", tc.name, code, rb)
			}
		})
	}
}

func TestReportsAndCSVExport(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "report@example.com")
	projectID := firstProjectID(t, c, base)

	// A project with a rate, and an hour booked against it.
	must(t, "set rate", st(t, c, http.MethodPatch, base+"/v1/projects/"+projectID,
		map[string]any{"hourly_rate_cents": 250000}, csrf), http.StatusOK)

	start := time.Now().UTC().Add(-2 * time.Hour)
	end := start.Add(time.Hour)
	code, body := do(t, c, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": projectID, "started_at": start.Format(time.RFC3339)}, csrf)
	must(t, "start backdated", code, http.StatusCreated)
	var ws struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &ws)
	must(t, "stop", st(t, c, http.MethodPost, base+"/v1/sessions/"+ws.ID+"/stop",
		map[string]any{"at": end.Format(time.RFC3339)}, csrf), http.StatusOK)

	code, body = do(t, c, http.MethodGet, base+"/v1/reports/summary", nil, nil)
	must(t, "summary", code, http.StatusOK)
	var summary struct {
		Projects []struct {
			Seconds     int64  `json:"seconds"`
			AmountCents *int64 `json:"amount_cents"`
		} `json:"projects"`
	}
	unmarshal(t, body, &summary)
	if len(summary.Projects) != 1 {
		t.Fatalf("want one project total, got %+v", summary.Projects)
	}
	if summary.Projects[0].Seconds != 3600 {
		t.Fatalf("seconds = %d, want 3600", summary.Projects[0].Seconds)
	}
	if summary.Projects[0].AmountCents == nil || *summary.Projects[0].AmountCents != 250000 {
		t.Fatalf("amount = %v, want 250000", summary.Projects[0].AmountCents)
	}

	code, csv := do(t, c, http.MethodGet, base+"/v1/reports/export.csv", nil, nil)
	must(t, "csv", code, http.StatusOK)
	if !strings.HasPrefix(string(csv), "session_id,project,") {
		t.Fatalf("unexpected CSV header: %.60s", csv)
	}
}

// --- SSE ---------------------------------------------------------------

// streamEvents reads an SSE stream, sending "connected" once and each event's
// type thereafter, until ctx is cancelled or the stream closes.
func streamEvents(ctx context.Context, client *http.Client, url string, out chan<- string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == ": connected":
			trySend(ctx, out, "connected")
		case strings.HasPrefix(line, "event: "):
			trySend(ctx, out, strings.TrimPrefix(line, "event: "))
		}
	}
}

func trySend(ctx context.Context, out chan<- string, v string) {
	select {
	case out <- v:
	case <-ctx.Done():
	}
}

func waitFor(ch <-chan string, want string, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case v := <-ch:
			if v == want {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// A stream carries the account's own events and nobody else's. This is the
// isolation guarantee that replaced helva's membership-based one.
func TestSSECarriesOnlyTheAccountsOwnEvents(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	alice, _ := registerActor(t, base, "sse-alice@example.com")
	bob, _ := registerActor(t, base, "sse-bob@example.com")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bobEvents := make(chan string, 32)
	go streamEvents(ctx, bob, base+"/v1/events/stream", bobEvents)
	if !waitFor(bobEvents, "connected", 3*time.Second) {
		t.Fatal("bob's SSE did not connect")
	}

	aliceEvents := make(chan string, 32)
	go streamEvents(ctx, alice, base+"/v1/events/stream", aliceEvents)
	if !waitFor(aliceEvents, "connected", 3*time.Second) {
		t.Fatal("alice's SSE did not connect")
	}

	startTimer(t, alice, base, firstProjectID(t, alice, base), "alice çalışıyor")

	if !waitFor(aliceEvents, "session.started", 4*time.Second) {
		t.Fatal("alice did not receive her own session.started")
	}
	if waitFor(bobEvents, "session.started", 2*time.Second) {
		t.Fatal("bob received alice's event")
	}
}

// An empty range still answers with a list.
//
// The summary used to drop the key entirely when nothing matched, so a quiet
// week and a grouping that was never requested looked identical on the wire.
// The analytics screen compares a range against the one before it, and the
// first user whose previous week was empty got a crash instead of a zero.
func TestEmptyRangeStillReturnsAList(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	c, _ := registerActor(t, base, "quiet@example.com")

	// A window that closed before the account existed: nothing can be in it.
	to := time.Now().UTC().Add(-48 * time.Hour)
	from := to.Add(-24 * time.Hour)
	q := "?from=" + from.Format(time.RFC3339) + "&to=" + to.Format(time.RFC3339)

	code, body := do(t, c, http.MethodGet, base+"/v1/reports/summary"+q+"&group_by=project", nil, nil)
	must(t, "empty project summary", code, http.StatusOK)
	if !strings.Contains(string(body), `"projects":[]`) {
		t.Fatalf("empty project summary omitted the list: %s", body)
	}

	code, body = do(t, c, http.MethodGet, base+"/v1/reports/summary"+q+"&group_by=day", nil, nil)
	must(t, "empty day summary", code, http.StatusOK)
	if !strings.Contains(string(body), `"days":[]`) {
		t.Fatalf("empty day summary omitted the list: %s", body)
	}
}
