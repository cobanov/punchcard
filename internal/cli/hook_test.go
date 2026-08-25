package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseRemoteHandlesGitsSpellings(t *testing.T) {
	cases := map[string]string{
		"git@github.com:cobanov/punchcard.git":       "cobanov/punchcard",
		"git@github.com:cobanov/punchcard":           "cobanov/punchcard",
		"https://github.com/cobanov/punchcard.git":   "cobanov/punchcard",
		"https://github.com/cobanov/punchcard":       "cobanov/punchcard",
		"ssh://git@gitlab.example.com/team/sub/proj": "sub/proj",
		"":          "",
		"not-a-url": "",
	}
	for in, want := range cases {
		if got := parseRemote(in); got != want {
			t.Errorf("parseRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

// A turn is only recorded when both ends of it were seen. The Stop hook alone
// cannot know when the turn began, and inventing a start would put fictional
// time on the calendar.
func TestATurnWithoutItsStartIsNotRecorded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	payload := `{"session_id":"abc","cwd":"/tmp","stop_hook_active":false}`
	if err := app.HookEmit("stop", "claude-code", strings.NewReader(payload)); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if runs := queueLines(t, dir); len(runs) != 0 {
		t.Fatalf("a stop with no marker produced %d run(s)", len(runs))
	}
}

// The ordinary path: a prompt, then a stop, produces exactly one line whose
// window is the turn.
func TestAStartThenStopRecordsOneTurn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	payload := `{"session_id":"abc-123","cwd":"/tmp","stop_hook_active":false}`
	if err := app.HookEmit("start", "claude-code", strings.NewReader(payload)); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := app.HookEmit("stop", "claude-code", strings.NewReader(payload)); err != nil {
		t.Fatalf("stop: %v", err)
	}

	runs := queueLines(t, dir)
	if len(runs) != 1 {
		t.Fatalf("want one queued run, got %d", len(runs))
	}
	r := runs[0]
	if r.Tool != "claude-code" || r.Cwd != "/tmp" {
		t.Fatalf("queued run = %+v", r)
	}
	if !strings.HasPrefix(r.ExternalID, "abc-123:") {
		t.Fatalf("external_id = %q, want it keyed on the session and the turn start", r.ExternalID)
	}
	start, err := time.Parse(time.RFC3339, r.StartedAt)
	if err != nil {
		t.Fatalf("started_at unparseable: %v", err)
	}
	end, err := time.Parse(time.RFC3339, r.EndedAt)
	if err != nil {
		t.Fatalf("ended_at unparseable: %v", err)
	}
	if end.Before(start) {
		t.Fatalf("turn ends before it starts: %s → %s", r.StartedAt, r.EndedAt)
	}

	// The marker is consumed, so a second Stop cannot report the turn twice.
	if err := app.HookEmit("stop", "claude-code", strings.NewReader(payload)); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if runs := queueLines(t, dir); len(runs) != 1 {
		t.Fatalf("a repeated stop queued the turn again (%d lines)", len(runs))
	}
}

// A Stop hook firing because of another Stop hook is not a turn; recording it
// would attribute the notifier's own runtime to the user's work.
func TestAStopTriggeredByAnotherStopIsIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	start := `{"session_id":"loop","cwd":"/tmp","stop_hook_active":false}`
	if err := app.HookEmit("start", "claude-code", strings.NewReader(start)); err != nil {
		t.Fatal(err)
	}
	nested := `{"session_id":"loop","cwd":"/tmp","stop_hook_active":true}`
	if err := app.HookEmit("stop", "claude-code", strings.NewReader(nested)); err != nil {
		t.Fatal(err)
	}
	if runs := queueLines(t, dir); len(runs) != 0 {
		t.Fatalf("a nested stop was recorded as a turn (%d lines)", len(runs))
	}
}

// A marker left behind by a killed process or a sleeping laptop is not a
// twenty-hour working session.
func TestAStaleMarkerIsDiscardedRatherThanBelieved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	state := filepath.Join(dir, "punchcard")
	if err := os.MkdirAll(filepath.Join(state, "markers"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	if err := os.WriteFile(markerPath(state, "ancient"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	payload := `{"session_id":"ancient","cwd":"/tmp","stop_hook_active":false}`
	if err := app.HookEmit("stop", "claude-code", strings.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if runs := queueLines(t, dir); len(runs) != 0 {
		t.Fatalf("a two-day-old marker became a run: %+v", runs)
	}
}

func queueLines(t *testing.T, stateHome string) []QueuedRun {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateHome, "punchcard", "queue.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []QueuedRun
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var r QueuedRun
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("queue line unreadable: %v", err)
		}
		out = append(out, r)
	}
	return out
}
