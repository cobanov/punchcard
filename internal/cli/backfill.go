package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backfill reconstructs past turns from transcripts already on this machine.
//
// The hook can only see turns that happen after it is installed, which on day
// one is none of them. But Claude Code has been writing a complete transcript
// of every session all along — timestamps, working directory, model, every tool
// call — and that is the same evidence the hook reports, sitting in a file. So
// the history is not lost, it just has to be read.
//
// This is what makes the first screen worth looking at: an account that signs
// up today can see the last month of its own work immediately, rather than
// waiting a month to accumulate it.

// transcriptEntry is the subset of a transcript line this needs. Claude Code
// owns this format; everything read here is optional and anything unparseable
// is skipped rather than guessed at.
type transcriptEntry struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	Timestamp   string `json:"timestamp"`
	Cwd         string `json:"cwd"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// turn is one prompt and everything the agent did in response.
type turn struct {
	sessionID string
	start     time.Time
	end       time.Time
	cwd       string
	model     string
	toolCalls int32
}

// maxTurn is the longest reconstructed turn worth believing, as a backstop
// behind the idle split below.
const maxTurn = 4 * time.Hour

// idleGap is how long a turn can go without the agent writing anything before
// the silence stops counting as work.
//
// Claude Code logs continuously while it is working, so a gap this long is not
// a long tool call — it is a machine that slept, a permission prompt nobody
// answered, or a session resumed the next morning. Measured against ninety days
// of real transcripts, splitting here takes the longest reconstructed stretch
// from about seven hours down to under four and drops the share of total time
// held by the ten longest turns from 12% to 7%. Ten minutes was tighter still,
// but it starts cutting genuine work in half: a test suite or an install can
// legitimately hold a turn open for twelve minutes with nothing logged.
const idleGap = 15 * time.Minute

// BackfillOptions are the knobs `punchcard backfill` exposes.
type BackfillOptions struct {
	Days   int
	Until  time.Time
	DryRun bool
	Root   string
}

func (a *App) Backfill(opts BackfillOptions) error {
	root := opts.Root
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot find a home directory: %w", err)
		}
		root = filepath.Join(home, ".claude", "projects")
	}
	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("no Claude Code transcripts at %s — nothing to backfill", root)
	}

	from := time.Now().AddDate(0, 0, -opts.Days)
	until := opts.Until
	if until.IsZero() {
		until = liveCaptureBegan()
	}

	files, err := transcriptFiles(root, from)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		a.println("no transcripts in that window")
		return nil
	}

	seen := map[string]bool{}
	runs := make([]QueuedRun, 0, 512)
	repos := map[string]string{}
	for _, path := range files {
		for _, t := range turnsIn(path) {
			if t.start.Before(from) || !t.end.Before(until) {
				continue
			}
			id := fmt.Sprintf("%s:%d", t.sessionID, t.start.UnixMilli())
			if seen[id] {
				continue
			}
			seen[id] = true

			repo, ok := repos[t.cwd]
			if !ok {
				repo = repoOf(t.cwd)
				repos[t.cwd] = repo
			}
			var calls *int32
			if t.toolCalls > 0 {
				n := t.toolCalls
				calls = &n
			}
			runs = append(runs, QueuedRun{
				Tool: "claude-code", ExternalID: id,
				StartedAt: t.start.Format(time.RFC3339), EndedAt: t.end.Format(time.RFC3339),
				Model: t.model, Cwd: t.cwd, Repo: repo, ToolCalls: calls,
			})
		}
	}
	if len(runs) == 0 {
		a.println("no turns found in that window")
		return nil
	}

	// Oldest first, so a partial send leaves a contiguous history rather than
	// holes.
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt < runs[j].StartedAt })

	var hours float64
	for _, r := range runs {
		s, _ := time.Parse(time.RFC3339, r.StartedAt)
		e, _ := time.Parse(time.RFC3339, r.EndedAt)
		hours += e.Sub(s).Hours()
	}

	if opts.DryRun {
		a.printf("%d turn(s) across %d transcript(s), %.1f hours — nothing sent (--dry-run)\n",
			len(runs), len(files), hours)
		return nil
	}

	c, _, err := a.client()
	if err != nil {
		return err
	}
	var accepted, duplicates int
	for start := 0; start < len(runs); start += syncBatchSize {
		end := start + syncBatchSize
		if end > len(runs) {
			end = len(runs)
		}
		ok, dup, err := c.RecordAgentRuns(runs[start:end])
		if err != nil {
			return fmt.Errorf("sent %d of %d: %w", accepted, len(runs), err)
		}
		accepted += ok
		duplicates += dup
	}

	if a.JSON {
		return a.writeJSON(map[string]any{
			"turns": len(runs), "accepted": accepted, "duplicates": duplicates, "hours": hours,
		})
	}
	a.printf("backfilled %d turn(s), %.1f hours of agent work", accepted, hours)
	if duplicates > 0 {
		a.printf(" (%d already known)", duplicates)
	}
	a.println()
	return nil
}

// liveCaptureBegan is when the hooks were installed, and therefore where
// backfill has to stop.
//
// Past that moment the hook is recording turns as they happen, and a
// transcript-derived turn would be the same work counted a second time under a
// different key. Without the stamp — hooks never installed — there is nothing
// live to collide with, so backfill runs right up to now.
func liveCaptureBegan() time.Time {
	dir, err := StateDir()
	if err != nil {
		return time.Now()
	}
	raw, err := os.ReadFile(filepath.Join(dir, "hooks-installed-at")) // #nosec G304 -- our own state file
	if err != nil {
		return time.Now()
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
	if err != nil {
		return time.Now()
	}
	return at
}

// transcriptFiles lists transcripts that could contain a turn after `from`.
// A file untouched since before the window cannot, so it is never opened —
// which is what keeps a year of history from being read to answer a question
// about last week.
func transcriptFiles(root string, from time.Time) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner of the tree is not a reason to stop
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.ModTime().Before(from) {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read transcripts: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// turnsIn reconstructs the turns in one transcript.
//
// A turn opens on a real prompt and closes on the last thing the assistant did
// before the next one. "Real" is the distinction that matters: most entries of
// type "user" are tool results being fed back in, not a person typing, and
// counting those would slice one turn into a dozen.
//
// The turn ends at the assistant's last message rather than at the next prompt,
// so a session someone walked away from is measured by how long the agent
// worked, not by how long the terminal sat open.
func turnsIn(path string) []turn {
	f, err := os.Open(path) // #nosec G304 -- a transcript path this command was pointed at
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var out []turn
	var cur *turn
	var last time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var e transcriptEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.IsSidechain {
			continue
		}
		ts, terr := time.Parse(time.RFC3339, e.Timestamp)
		if terr != nil {
			continue
		}

		switch {
		case e.Type == "user" && isRealPrompt(e.Message.Content):
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &turn{sessionID: e.SessionID, start: ts, end: ts, cwd: e.Cwd}
			last = ts
		case e.Type == "assistant" && cur != nil:
			// A long silence inside a turn is not work being done slowly, it is
			// work that stopped and started again. Close the stretch at the last
			// thing that happened and open a new one here, rather than billing
			// the gap.
			if !last.IsZero() && ts.Sub(last) > idleGap {
				out = append(out, *cur)
				cur = &turn{sessionID: e.SessionID, start: ts, end: ts, cwd: e.Cwd}
			}
			cur.end = ts
			if e.Message.Model != "" {
				cur.model = e.Message.Model
			}
			cur.toolCalls += countToolUse(e.Message.Content)
			if cur.cwd == "" {
				cur.cwd = e.Cwd
			}
			last = ts
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}

	kept := out[:0]
	for _, t := range out {
		if t.sessionID == "" || !t.end.After(t.start) || t.end.Sub(t.start) > maxTurn {
			continue
		}
		kept = append(kept, t)
	}
	return kept
}

// isRealPrompt tells a person typing from a tool result being fed back.
//
// A prompt's content is either a bare string or a list containing a text block.
// A tool result is a list of nothing but tool_result blocks — same entry type,
// entirely different event.
func isRealPrompt(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return strings.TrimSpace(asString) != ""
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "text" {
			return true
		}
	}
	return false
}

func countToolUse(raw json.RawMessage) int32 {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return 0
	}
	var n int32
	for _, b := range blocks {
		if b.Type == "tool_use" {
			n++
		}
	}
	return n
}
