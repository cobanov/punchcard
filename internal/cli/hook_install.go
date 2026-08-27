package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hookMarker identifies punchcard's own entries in a settings file it does not
// own, so a reinstall replaces the previous version instead of stacking a
// second copy.
//
// It is a sentinel appended to the command rather than anything derived from
// the command itself: the command embeds the executable's path, which is
// whatever the binary happens to be called on this machine, so matching on
// "punchcard" would quietly fail to recognise our own hook installed from
// punchcard-cli and duplicate it on every run.
const hookMarker = "#punchcard-agent-runs"

// HookInstall wires the two Claude Code hooks that record turns.
//
// The settings file belongs to the user, not to punchcard. It already contains
// hooks that matter — on this machine, a Stop hook that sends a phone
// notification — and losing one of those to make room for ours would be a far
// worse bug than not recording a turn. So the file is read, the punchcard
// entries are replaced in place, and everything else is written back untouched.
func (a *App) HookInstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot find a home directory: %w", err)
	}
	path := filepath.Join(home, ".claude", "settings.json")

	settings := map[string]any{}
	raw, err := os.ReadFile(path) // #nosec G304 -- the user's own Claude Code settings
	switch {
	case err == nil:
		if e := json.Unmarshal(raw, &settings); e != nil {
			// Never rewrite a file we could not parse: a settings file with a
			// stray comma is a file the user still wants.
			return fmt.Errorf("%s is not valid JSON, so it was left alone: %w", path, e)
		}
	case os.IsNotExist(err):
		// A fresh install; the directory may not exist either. 0700 because it
		// is the user's own configuration and nobody else's business.
		if e := os.MkdirAll(filepath.Dir(path), 0o700); e != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), e)
		}
	default:
		return fmt.Errorf("read %s: %w", path, err)
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	self, err := os.Executable()
	if err != nil || self == "" {
		self = "punchcard"
	}
	// The command swallows its own failures: a hook that exits non-zero
	// interrupts the turn it was supposed to be quietly recording.
	install := func(event, arg string) {
		cmd := fmt.Sprintf("%q hook emit %s --tool claude-code >/dev/null 2>&1; exit 0 %s", self, arg, hookMarker)
		hooks[event] = append(withoutPunchcard(hooks[event]), map[string]any{
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": cmd,
				"timeout": 10,
			}},
		})
	}
	install("UserPromptSubmit", "start")
	install("Stop", "stop")
	settings["hooks"] = hooks

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	// Stamp when live capture began. Backfill reads this and stops there, so
	// the two mechanisms meet exactly instead of overlapping: everything before
	// the stamp comes from transcripts, everything after from the hook, and no
	// turn is counted twice.
	if dir, derr := StateDir(); derr == nil {
		if os.MkdirAll(dir, 0o700) == nil {
			_ = os.WriteFile(filepath.Join(dir, "hooks-installed-at"),
				[]byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
		}
	}

	a.printf("hooks installed in %s\n", path)
	a.println("Turns are appended to a local queue and sent a couple of minutes")
	a.println("later, in the background — and whenever you run stop, status,")
	a.println("today or week. There is nothing else to install.")
	a.println()
	a.printf("  %s sync                 send everything now\n", self)
	a.println("  PUNCHCARD_NO_AUTOSYNC=1   record only; never send by itself")
	return nil
}

// withoutPunchcard returns an event's hook groups with punchcard's own removed,
// so installing twice replaces rather than duplicates — and every other hook
// the user configured survives verbatim.
func withoutPunchcard(existing any) []any {
	groups, ok := existing.([]any)
	if !ok {
		return nil
	}
	kept := make([]any, 0, len(groups))
	for _, g := range groups {
		if groupIsPunchcard(g) {
			continue
		}
		kept = append(kept, g)
	}
	return kept
}

func groupIsPunchcard(group any) bool {
	m, ok := group.(map[string]any)
	if !ok {
		return false
	}
	entries, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if cmd, ok := em["command"].(string); ok && strings.Contains(cmd, hookMarker) {
			return true
		}
	}
	return false
}
