package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// line builds one transcript entry.
func line(t *testing.T, kind, session, ts, cwd string, content any, model string) string {
	t.Helper()
	e := map[string]any{"type": kind, "sessionId": session, "timestamp": ts, "cwd": cwd}
	if kind == "user" || kind == "assistant" {
		m := map[string]any{"content": content}
		if model != "" {
			m["model"] = model
		}
		e["message"] = m
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func writeTranscript(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Most entries of type "user" are tool results being fed back in, not somebody
// typing. Counting those as prompts would slice one turn into a dozen and
// report a morning's work as a hundred separate stretches.
func TestToolResultsAreNotPrompts(t *testing.T) {
	dir := t.TempDir()
	body := line(t, "user", "s1", "2026-08-01T10:00:00Z", "/w", "please refactor this", "") +
		line(t, "assistant", "s1", "2026-08-01T10:00:05Z", "/w",
			[]any{map[string]any{"type": "tool_use"}}, "claude-opus-5") +
		line(t, "user", "s1", "2026-08-01T10:00:06Z", "/w",
			[]any{map[string]any{"type": "tool_result"}}, "") +
		line(t, "assistant", "s1", "2026-08-01T10:04:00Z", "/w",
			[]any{map[string]any{"type": "text"}}, "claude-opus-5")
	p := writeTranscript(t, dir, "s1.jsonl", body)

	turns := turnsIn(p)
	if len(turns) != 1 {
		t.Fatalf("want one turn, got %d: %+v", len(turns), turns)
	}
	got := turns[0]
	if got.end.Sub(got.start) != 4*time.Minute {
		t.Fatalf("turn ran %s, want 4m", got.end.Sub(got.start))
	}
	if got.model != "claude-opus-5" || got.toolCalls != 1 || got.cwd != "/w" {
		t.Fatalf("turn = %+v", got)
	}
}

// A turn ends at the agent's last message, not at the next prompt. A session
// somebody walked away from is measured by how long the agent worked, not by
// how long the terminal sat open.
func TestATurnEndsWhenTheAgentStops(t *testing.T) {
	dir := t.TempDir()
	body := line(t, "user", "s1", "2026-08-01T10:00:00Z", "/w", "first", "") +
		line(t, "assistant", "s1", "2026-08-01T10:02:00Z", "/w", []any{}, "m") +
		// Three hours later the person comes back and types again.
		line(t, "user", "s1", "2026-08-01T13:00:00Z", "/w", "second", "") +
		line(t, "assistant", "s1", "2026-08-01T13:01:00Z", "/w", []any{}, "m")
	p := writeTranscript(t, dir, "s1.jsonl", body)

	turns := turnsIn(p)
	if len(turns) != 2 {
		t.Fatalf("want two turns, got %d", len(turns))
	}
	if d := turns[0].end.Sub(turns[0].start); d != 2*time.Minute {
		t.Fatalf("first turn ran %s, want 2m — the idle wait was billed", d)
	}
}

// A silence inside one turn is work that stopped and started again — a sleeping
// laptop, an unanswered permission prompt — not one long stretch of work.
func TestALongSilenceInsideATurnSplitsIt(t *testing.T) {
	dir := t.TempDir()
	body := line(t, "user", "s1", "2026-08-01T10:00:00Z", "/w", "go", "") +
		line(t, "assistant", "s1", "2026-08-01T10:05:00Z", "/w", []any{}, "m") +
		// Ninety minutes of nothing, then it picks up again.
		line(t, "assistant", "s1", "2026-08-01T11:35:00Z", "/w", []any{}, "m") +
		line(t, "assistant", "s1", "2026-08-01T11:40:00Z", "/w", []any{}, "m")
	p := writeTranscript(t, dir, "s1.jsonl", body)

	turns := turnsIn(p)
	if len(turns) != 2 {
		t.Fatalf("want the silence to split the turn, got %d turn(s)", len(turns))
	}
	total := turns[0].end.Sub(turns[0].start) + turns[1].end.Sub(turns[1].start)
	if total != 10*time.Minute {
		t.Fatalf("counted %s of work, want 10m — the gap leaked in", total)
	}
}

// Subagents run inside a turn. Their entries are the same turn's work, not
// turns of their own.
func TestSidechainEntriesAreNotSeparateTurns(t *testing.T) {
	dir := t.TempDir()
	body := line(t, "user", "s1", "2026-08-01T10:00:00Z", "/w", "go", "") +
		`{"type":"user","sessionId":"s1","timestamp":"2026-08-01T10:01:00Z","isSidechain":true,"message":{"content":"sub"}}` + "\n" +
		line(t, "assistant", "s1", "2026-08-01T10:03:00Z", "/w", []any{}, "m")
	p := writeTranscript(t, dir, "s1.jsonl", body)

	if turns := turnsIn(p); len(turns) != 1 {
		t.Fatalf("a subagent prompt started its own turn (%d turns)", len(turns))
	}
}

// Backfill stops where live capture began, so a turn is never counted once from
// the transcript and again from the hook.
func TestBackfillStopsWhereTheHookTookOver(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := t.TempDir()

	// Written now, so it lands inside any --days window.
	now := time.Now().UTC()
	early := now.Add(-2 * time.Hour)
	late := now.Add(-10 * time.Minute)
	body := line(t, "user", "s1", early.Format(time.RFC3339), "/w", "before hooks", "") +
		line(t, "assistant", "s1", early.Add(time.Minute).Format(time.RFC3339), "/w", []any{}, "m") +
		line(t, "user", "s1", late.Format(time.RFC3339), "/w", "after hooks", "") +
		line(t, "assistant", "s1", late.Add(time.Minute).Format(time.RFC3339), "/w", []any{}, "m")
	writeTranscript(t, dir, "s1.jsonl", body)

	// Hooks were installed an hour ago: the first turn predates them, the
	// second is already being recorded live.
	if err := os.MkdirAll(filepath.Join(state, "punchcard"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "punchcard", "hooks-installed-at"),
		[]byte(now.Add(-time.Hour).Format(time.RFC3339Nano)), 0o600); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	app := &App{Out: out, Err: &bytes.Buffer{}}
	if err := app.Backfill(BackfillOptions{Days: 7, DryRun: true, Root: dir}); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := out.String(); !bytes.Contains([]byte(got), []byte("1 turn(s)")) {
		t.Fatalf("want only the pre-hook turn, got: %s", got)
	}
}
