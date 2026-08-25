package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Installing must not cost the user a hook they already had.
//
// This machine's settings carry a Stop hook that sends a phone notification.
// Replacing the Stop array instead of appending to it would silence that, and
// the failure would be invisible: nothing errors, notifications simply stop.
func TestInstallKeepsHooksItDidNotWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	existing := `{
	  "model": "opus",
	  "hooks": {
	    "Stop": [
	      {"hooks": [{"type": "command", "command": "$HOME/bin/claude-task-notify.sh"}]}
	    ]
	  }
	}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := app.HookInstall(); err != nil {
		t.Fatalf("install: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "claude-task-notify.sh") {
		t.Fatalf("install removed the user's own Stop hook:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"model": "opus"`) {
		t.Fatalf("install dropped unrelated settings:\n%s", raw)
	}
	if n := countPunchcardHooks(t, raw, "Stop"); n != 1 {
		t.Fatalf("Stop has %d punchcard hooks, want 1", n)
	}
	if n := countPunchcardHooks(t, raw, "UserPromptSubmit"); n != 1 {
		t.Fatalf("UserPromptSubmit has %d punchcard hooks, want 1", n)
	}
}

// Running install twice is a thing people do. It must be idempotent, or every
// turn ends up recorded as many times as the command was run.
func TestInstallTwiceDoesNotStack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	for i := 0; i < 3; i++ {
		if err := app.HookInstall(); err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countPunchcardHooks(t, raw, "Stop"); n != 1 {
		t.Fatalf("three installs left %d Stop hooks, want 1", n)
	}
}

// A settings file we cannot parse is a settings file we must not rewrite.
func TestInstallRefusesToRewriteUnparsableSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	broken := `{"hooks": {,}}`
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := app.HookInstall(); err == nil {
		t.Fatal("install accepted an unparsable settings file")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != broken {
		t.Fatalf("install modified a file it could not parse:\n%s", raw)
	}
}

func countPunchcardHooks(t *testing.T, raw []byte, event string) int {
	t.Helper()
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("settings unreadable after install: %v\n%s", err, raw)
	}
	n := 0
	for _, group := range settings.Hooks[event] {
		for _, h := range group.Hooks {
			if strings.Contains(h.Command, hookMarker) {
				n++
			}
		}
	}
	return n
}
