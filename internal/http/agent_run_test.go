package http

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// runBody builds a one-run batch payload.
func runBody(tool, externalID string, start, end time.Time, extra map[string]any) map[string]any {
	run := map[string]any{
		"tool":        tool,
		"external_id": externalID,
		"started_at":  start.Format(time.RFC3339),
		"ended_at":    end.Format(time.RFC3339),
	}
	for k, v := range extra {
		run[k] = v
	}
	return map[string]any{"runs": []any{run}}
}

type recordResult struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
}

// Flushing the same queue twice must not double-count the work.
//
// The client cannot always tell whether its last flush landed — the reply may
// have been lost after the server committed — so its only safe move is to send
// again. That has to be free.
func TestResendingTheSameRunIsNotADuplicateRow(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "runs-idem@example.com")

	start := time.Now().UTC().Add(-3 * time.Hour)
	body := runBody("claude-code", "sess-1:1000", start, start.Add(20*time.Minute), nil)

	code, raw := do(t, c, http.MethodPost, base+"/v1/agent-runs", body, csrf)
	must(t, "first record", code, http.StatusAccepted)
	var first recordResult
	unmarshal(t, raw, &first)
	if first.Accepted != 1 || first.Duplicates != 0 {
		t.Fatalf("first send = %+v, want 1 accepted", first)
	}

	code, raw = do(t, c, http.MethodPost, base+"/v1/agent-runs", body, csrf)
	must(t, "second record", code, http.StatusAccepted)
	var second recordResult
	unmarshal(t, raw, &second)
	if second.Accepted != 0 || second.Duplicates != 1 {
		t.Fatalf("resend = %+v, want 0 accepted and 1 duplicate", second)
	}
}

// A run inside a session's range belongs to it; a run outside belongs to
// nobody and has to show up as unmatched, which is what makes a forgotten
// timer recoverable.
func TestRunsAttachInsideASessionAndStayUnmatchedOutside(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "runs-attach@example.com")
	projectID := newProject(t, c, base, csrf, "Attach")

	// A session from three hours ago to two hours ago.
	start := time.Now().UTC().Add(-3 * time.Hour)
	end := start.Add(time.Hour)
	code, raw := do(t, c, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": projectID, "started_at": start.Format(time.RFC3339)}, csrf)
	must(t, "start session", code, http.StatusCreated)
	var ws struct {
		ID string `json:"id"`
	}
	unmarshal(t, raw, &ws)
	must(t, "stop session", st(t, c, http.MethodPost, base+"/v1/sessions/"+ws.ID+"/stop",
		map[string]any{"at": end.Format(time.RFC3339)}, csrf), http.StatusOK)

	// One run inside the session, one an hour after it ended.
	inside := start.Add(10 * time.Minute)
	outside := end.Add(time.Hour)
	must(t, "record inside", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "in", inside, inside.Add(5*time.Minute),
			map[string]any{"model": "opus-5", "repo": "cobanov/punchcard", "tool_calls": 7}), csrf),
		http.StatusAccepted)
	must(t, "record outside", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "out", outside, outside.Add(5*time.Minute), nil), csrf),
		http.StatusAccepted)

	code, raw = do(t, c, http.MethodGet, base+"/v1/sessions/"+ws.ID+"/agent-runs", nil, nil)
	must(t, "session runs", code, http.StatusOK)
	var attached struct {
		AgentRuns []struct {
			Tool      string `json:"tool"`
			Model     string `json:"model"`
			Seconds   int64  `json:"seconds"`
			ToolCalls *int32 `json:"tool_calls"`
		} `json:"agent_runs"`
	}
	unmarshal(t, raw, &attached)
	if len(attached.AgentRuns) != 1 {
		t.Fatalf("want exactly the inside run attached, got %+v", attached.AgentRuns)
	}
	got := attached.AgentRuns[0]
	if got.Tool != "claude-code" || got.Model != "opus-5" || got.Seconds != 300 {
		t.Fatalf("attached run = %+v", got)
	}
	if got.ToolCalls == nil || *got.ToolCalls != 7 {
		t.Fatalf("tool_calls = %v, want 7", got.ToolCalls)
	}

	// The other one is unmatched, and the sweep reports it even though no
	// commit was ever fetched — evidence of work is evidence of work.
	code, raw = do(t, c, http.MethodGet, base+"/v1/github/unmatched", nil, nil)
	must(t, "unmatched", code, http.StatusOK)
	if !strings.Contains(string(raw), `"agent_runs"`) {
		t.Fatalf("unmatched sweep did not mention the orphan run: %s", raw)
	}
}

// Moving a session's boundary moves what it holds. A run the new range covers
// comes in; a run it no longer covers goes back to being unmatched, rather than
// staying filed under a stretch that no longer claims it.
func TestEditingASessionRefilesItsRuns(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "runs-edit@example.com")
	projectID := newProject(t, c, base, csrf, "Refile")

	start := time.Now().UTC().Add(-4 * time.Hour)
	end := start.Add(30 * time.Minute)
	code, raw := do(t, c, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": projectID, "started_at": start.Format(time.RFC3339)}, csrf)
	must(t, "start", code, http.StatusCreated)
	var ws struct {
		ID string `json:"id"`
	}
	unmarshal(t, raw, &ws)
	must(t, "stop", st(t, c, http.MethodPost, base+"/v1/sessions/"+ws.ID+"/stop",
		map[string]any{"at": end.Format(time.RFC3339)}, csrf), http.StatusOK)

	// The run sits an hour after the session ends, so it starts out unmatched.
	runStart := start.Add(90 * time.Minute)
	must(t, "record", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "late", runStart, runStart.Add(10*time.Minute), nil), csrf),
		http.StatusAccepted)
	if n := countSessionRuns(t, c, base, ws.ID); n != 0 {
		t.Fatalf("run attached before the session covered it (%d)", n)
	}

	// Extend the session past the run: it should come in.
	must(t, "extend", st(t, c, http.MethodPatch, base+"/v1/sessions/"+ws.ID,
		map[string]any{"ended_at": runStart.Add(time.Hour).Format(time.RFC3339)}, csrf), http.StatusOK)
	if n := countSessionRuns(t, c, base, ws.ID); n != 1 {
		t.Fatalf("extending the session did not pick up the run it now covers (%d)", n)
	}

	// Shrink it back: the run has to be released, not left behind.
	must(t, "shrink", st(t, c, http.MethodPatch, base+"/v1/sessions/"+ws.ID,
		map[string]any{"ended_at": end.Format(time.RFC3339)}, csrf), http.StatusOK)
	if n := countSessionRuns(t, c, base, ws.ID); n != 0 {
		t.Fatalf("shrinking the session left %d run(s) filed under a range that no longer covers them", n)
	}
}

// Runs are scoped to the account that reported them. Another user's session
// covering the same instant must not collect them.
func TestOneAccountsRunsNeverLandInAnothersSession(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL

	ownerCSRF := testCSRF()
	owner, _ := registerActor(t, base, "runs-owner@example.com")
	runStart := time.Now().UTC().Add(-2 * time.Hour)
	must(t, "record", st(t, owner, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "mine", runStart, runStart.Add(10*time.Minute), nil), ownerCSRF),
		http.StatusAccepted)

	// A second account records a session spanning the same window.
	otherCSRF := testCSRF()
	other, _ := registerActor(t, base, "runs-other@example.com")
	projectID := newProject(t, other, base, otherCSRF, "Theirs")
	code, raw := do(t, other, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": projectID, "started_at": runStart.Add(-time.Minute).Format(time.RFC3339)}, otherCSRF)
	must(t, "start", code, http.StatusCreated)
	var ws struct {
		ID string `json:"id"`
	}
	unmarshal(t, raw, &ws)
	must(t, "stop", st(t, other, http.MethodPost, base+"/v1/sessions/"+ws.ID+"/stop",
		map[string]any{"at": runStart.Add(time.Hour).Format(time.RFC3339)}, otherCSRF), http.StatusOK)

	if n := countSessionRuns(t, other, base, ws.ID); n != 0 {
		t.Fatalf("another account's session collected %d run(s)", n)
	}
}

// A run longer than a day is a marker that outlived its turn — a slept laptop,
// a hook that never fired — not a day of work. Refusing it here keeps a bug
// from becoming a report that reads as a lie.
func TestImplausibleRunsAreRefused(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "runs-bad@example.com")

	start := time.Now().UTC().Add(-30 * time.Hour)
	must(t, "25-hour run", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "toolong", start, start.Add(25*time.Hour), nil), csrf),
		http.StatusUnprocessableEntity)

	now := time.Now().UTC()
	must(t, "backwards run", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "backwards", now, now.Add(-time.Minute), nil), csrf),
		http.StatusUnprocessableEntity)
}

func countSessionRuns(t *testing.T, c *http.Client, base, sessionID string) int {
	t.Helper()
	code, raw := do(t, c, http.MethodGet, base+"/v1/sessions/"+sessionID+"/agent-runs", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("session runs = %d: %s", code, raw)
	}
	var body struct {
		AgentRuns []struct{} `json:"agent_runs"`
	}
	unmarshal(t, raw, &body)
	return len(body.AgentRuns)
}

// newProject creates a project and returns its id.
func newProject(t *testing.T, c *http.Client, base string, csrf map[string]string, name string) string {
	t.Helper()
	code, raw := do(t, c, http.MethodPost, base+"/v1/projects", map[string]any{"name": name}, csrf)
	if code != http.StatusCreated {
		t.Fatalf("create project %q = %d: %s", name, code, raw)
	}
	var p struct {
		ID string `json:"id"`
	}
	unmarshal(t, raw, &p)
	if p.ID == "" {
		t.Fatalf("project %q created with no id", name)
	}
	return p.ID
}
