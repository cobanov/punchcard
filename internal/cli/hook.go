package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The queue is the whole integration contract.
//
// punchcard does not ask agents to speak its API. It asks them to append one
// JSON object per finished turn to a file, and takes care of the rest. That is
// the entire story for Claude Code, for Codex, and for anything else that can
// run a command when it stops working: three lines of shell and a text file.
//
// The hook never touches the network. It runs on every turn, so a network call
// there would put punchcard's availability in the path of the user's editor —
// and be lost anyway on a laptop that is offline. Appending is microseconds and
// cannot fail in a way that matters.

// QueuedRun is one line of the queue.
type QueuedRun struct {
	Tool       string `json:"tool"`
	ExternalID string `json:"external_id"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at"`
	Model      string `json:"model,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Repo       string `json:"repo,omitempty"`
	ToolCalls  *int32 `json:"tool_calls,omitempty"`
}

// hookPayload is what Claude Code writes to a hook's stdin. Only these fields
// are relied on; everything else is version-specific and would be a hostage.
type hookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	StopHookActive bool   `json:"stop_hook_active"`
}

// staleMarker is how long a started-but-never-finished turn stays believable.
//
// A marker outlives its turn whenever the process is killed, the machine
// sleeps, or a session is resumed days later. Emitting one of those as a run
// would put a twenty-hour block on the calendar, so past this age the marker is
// discarded rather than believed. The server refuses the same thing
// independently; neither side trusts the other to be the only guard.
const staleMarker = 24 * time.Hour

// StateDir is where the queue and the turn markers live.
func StateDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot find a home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "punchcard"), nil
}

func queuePath(dir string) string { return filepath.Join(dir, "queue.jsonl") }
func markerPath(dir, session string) string {
	// The session id comes from a hook payload, so it is never used as a path
	// component without being flattened first.
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, session)
	return filepath.Join(dir, "markers", safe)
}

// HookEmit is the command a hook runs. `event` is "start" or "stop".
//
// The event is an argument rather than something read out of the payload: the
// field naming it is version-specific, and `hook install` writes both commands
// anyway, so there is nothing to gain by guessing what we were already told.
//
// It never returns an error to the caller's exit code for anything the user
// could not act on. A hook that fails loudly interrupts the work it was
// supposed to be quietly recording.
func (a *App) HookEmit(event, tool string, stdin io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil || len(raw) == 0 {
		return nil
	}
	var p hookPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.SessionID == "" {
		return nil
	}
	dir, err := StateDir()
	if err != nil {
		return nil
	}

	switch event {
	case "start":
		return a.markTurnStart(dir, p)
	case "stop":
		// A Stop hook that fired because of another Stop hook is not a turn.
		if p.StopHookActive {
			return nil
		}
		return a.emitTurn(dir, tool, p)
	default:
		return fmt.Errorf("unknown hook event %q — expected start or stop", event)
	}
}

func (a *App) markTurnStart(dir string, p hookPayload) error {
	path := markerPath(dir, p.SessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil
	}
	_ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
	return nil
}

func (a *App) emitTurn(dir, tool string, p hookPayload) error {
	path := markerPath(dir, p.SessionID)
	raw, err := os.ReadFile(path) // #nosec G304 -- path is built from a flattened session id under our own state dir
	if err != nil {
		// No marker means no honest window: the turn began before the hook was
		// installed, or across a restart. Skip it rather than invent a start.
		return nil
	}
	_ = os.Remove(path)

	start, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
	if err != nil {
		return nil
	}
	end := time.Now().UTC()
	if end.Sub(start) > staleMarker {
		return nil
	}

	run := QueuedRun{
		Tool:       tool,
		ExternalID: fmt.Sprintf("%s:%d", p.SessionID, start.UnixNano()),
		StartedAt:  start.Format(time.RFC3339),
		EndedAt:    end.Format(time.RFC3339),
		Cwd:        p.Cwd,
		Repo:       repoOf(p.Cwd),
	}
	run.Model, run.ToolCalls = readTranscript(p.TranscriptPath, start)
	if err := appendQueue(dir, run); err != nil {
		return err
	}
	// The turn is safely on disk before anything reaches for the network. This
	// call spawns a detached child at most once every autoSyncEvery and returns
	// immediately, so the promise above — the hook never waits on a server —
	// still holds, and so does the one about working offline.
	a.maybeAutoSync(dir)
	return nil
}

// repoOf reads the origin remote of the directory a turn ran in.
//
// An absent remote is not a failure — plenty of real work happens in folders
// git has never heard of. It just means the server has one less way to guess
// the project, and falls back to the directory name.
func repoOf(cwd string) string {
	if cwd == "" {
		return ""
	}
	// #nosec G204 -- cwd is one argv element handed straight to git, never a
	// shell string, so a hostile path can only ever be a bad directory name.
	cmd := exec.Command("git", "-C", cwd, "config", "--get", "remote.origin.url")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return parseRemote(strings.TrimSpace(string(out)))
}

// parseRemote turns any of git's URL spellings into owner/repo.
func parseRemote(url string) string {
	url = strings.TrimSuffix(url, ".git")
	switch {
	case strings.HasPrefix(url, "git@"):
		// git@github.com:owner/repo
		if i := strings.Index(url, ":"); i >= 0 {
			url = url[i+1:]
		}
	case strings.Contains(url, "://"):
		// https://github.com/owner/repo — drop scheme and host
		if i := strings.Index(url, "://"); i >= 0 {
			url = url[i+3:]
		}
		if i := strings.Index(url, "/"); i >= 0 {
			url = url[i+1:]
		}
	}
	parts := strings.Split(strings.Trim(url, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	// Keep the last two segments: self-hosted paths can be deeper than
	// owner/repo, and the tail is the part that identifies the repository.
	return strings.Join(parts[len(parts)-2:], "/")
}

// readTranscript picks the model and the tool-call count out of the turn.
//
// Both are best-effort decoration. The transcript format belongs to Claude
// Code, not to punchcard, so anything unreadable here yields empty values and
// the run is still recorded — a run with an unknown model is worth far more
// than no run at all.
func readTranscript(path string, since time.Time) (model string, toolCalls *int32) {
	if path == "" {
		return "", nil
	}
	f, err := os.Open(path) // #nosec G304 -- path comes from the hook payload and is read, never written
	if err != nil {
		return "", nil
	}
	defer func() { _ = f.Close() }()

	var count int32
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var entry struct {
			Timestamp string `json:"timestamp"`
			Message   struct {
				Model   string `json:"model"`
				Content []struct {
					Type string `json:"type"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &entry) != nil {
			continue
		}
		if entry.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil && ts.Before(since) {
				continue
			}
		}
		if entry.Message.Model != "" {
			model = entry.Message.Model
		}
		for _, c := range entry.Message.Content {
			if c.Type == "tool_use" {
				count++
			}
		}
	}
	if count > 0 {
		return model, &count
	}
	return model, nil
}

// appendQueue adds one line. O_APPEND makes a line-sized write atomic between
// concurrent hooks; `sync` never truncates this file in place, it renames it,
// so there is no window where an append can be lost.
func appendQueue(dir string, run QueuedRun) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	line, err := json.Marshal(run)
	if err != nil {
		return nil
	}
	f, err := os.OpenFile(queuePath(dir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
	return nil
}

// Sync flushes the queue to the server and says what happened.
//
// This is the explicit command. The same core runs from `punchcard stop`,
// `status`, `today` and `week`, and from a detached child the Stop hook starts
// on a throttle — see autosync.go for why the queue is no longer allowed to sit
// there waiting for someone to remember it.
func (a *App) Sync() error {
	dir, err := StateDir()
	if err != nil {
		return err
	}
	c, _, err := a.client()
	if err != nil {
		return err
	}
	accepted, duplicates, bad, err := syncQueue(dir, c)
	if err != nil {
		return err
	}
	if a.JSON {
		return a.writeJSON(map[string]int{
			"accepted": accepted, "duplicates": duplicates, "unreadable": bad,
		})
	}
	if accepted == 0 && duplicates == 0 && bad == 0 {
		a.println("nothing to sync")
		return nil
	}
	a.printf("synced %d run(s)", accepted)
	if duplicates > 0 {
		a.printf(", %d already known", duplicates)
	}
	if bad > 0 {
		a.printf(", %d unreadable line(s) dropped", bad)
	}
	a.println()
	return nil
}

// syncQueue is the flush itself, with a client the caller already holds.
//
// The queue file is claimed by renaming it, not truncated after reading: a hook
// appending during the flush writes to a fresh file that this run never had,
// which is what makes "record a turn" and "send the backlog" safe to happen at
// the same moment. Anything the server did not take is appended back.
func syncQueue(dir string, c *Client) (accepted, duplicates, unreadable int, err error) {
	recoverOrphans(dir)

	runs, batchPath, unreadable, err := claimQueue(dir)
	if err != nil {
		return 0, 0, 0, err
	}
	if len(runs) == 0 {
		return 0, 0, unreadable, nil
	}

	for start := 0; start < len(runs); start += syncBatchSize {
		end := start + syncBatchSize
		if end > len(runs) {
			end = len(runs)
		}
		ok, dup, serr := c.RecordAgentRuns(runs[start:end])
		if serr != nil {
			// Put back everything from this batch on, so a server that is down
			// or a token that expired costs nothing but a later retry.
			_ = writeQueueLines(dir, runs[start:])
			_ = os.Remove(batchPath)
			return accepted, duplicates, unreadable, serr
		}
		accepted += ok
		duplicates += dup
	}
	_ = os.Remove(batchPath)
	return accepted, duplicates, unreadable, nil
}

// claimQueue moves the queue aside under a name nothing else can be holding.
//
// The batch name carries this process's pid and the moment it claimed, because
// there are now several syncers: the command, four opportunistic callers and a
// child the hook spawns. With one shared ".sending" name, a second syncer that
// arrived after a hook had recreated the queue would rename the new file over
// the first syncer's in-flight batch and destroy it. Unique names make an
// overlap harmless — the two syncers simply carry disjoint halves.
func claimQueue(dir string) (runs []QueuedRun, batchPath string, unreadable int, err error) {
	batchPath = fmt.Sprintf("%s.sending.%d-%d", queuePath(dir), os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(queuePath(dir), batchPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", 0, nil
		}
		return nil, "", 0, fmt.Errorf("claim queue: %w", err)
	}
	runs, unreadable = readQueue(batchPath)
	if len(runs) == 0 {
		_ = os.Remove(batchPath)
		return nil, "", unreadable, nil
	}
	return runs, batchPath, unreadable, nil
}

// recoverOrphans puts back batches whose syncer died before sending them.
//
// Claiming and sending are two steps, and a process killed between them used to
// take its batch with it silently. Only batches older than orphanAfter are
// touched, so a flush in progress is never stolen; and because external_id is
// the idempotency key, recovering one that did in fact arrive costs a
// "duplicates" count and nothing else.
func recoverOrphans(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := filepath.Base(queuePath(dir)) + ".sending."
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || time.Since(info.ModTime()) < orphanAfter {
			continue
		}
		_ = restoreQueue(dir, filepath.Join(dir, e.Name()))
	}
}

// syncBatchSize stays under the server's per-request cap.
const syncBatchSize = 200

func readQueue(path string) (runs []QueuedRun, unreadable int) {
	f, err := os.Open(path) // #nosec G304 -- our own queue file
	if err != nil {
		return nil, 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r QueuedRun
		if json.Unmarshal([]byte(line), &r) != nil || r.Tool == "" || r.ExternalID == "" {
			// A corrupt line would block the queue forever if it were kept.
			unreadable++
			continue
		}
		runs = append(runs, r)
	}
	return runs, unreadable
}

// restoreQueue puts an unsent batch back at the FRONT of the queue, preserving
// order against anything a hook appended while we were away.
func restoreQueue(dir, batchPath string) error {
	runs, _ := readQueue(batchPath)
	_ = os.Remove(batchPath)
	return writeQueueLines(dir, runs)
}

func writeQueueLines(dir string, runs []QueuedRun) error {
	if len(runs) == 0 {
		return nil
	}
	existing, _ := readQueue(queuePath(dir))
	all := append(runs, existing...)
	var buf strings.Builder
	for _, r := range all {
		line, err := json.Marshal(r)
		if err != nil {
			continue
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return os.WriteFile(queuePath(dir), []byte(buf.String()), 0o600)
}
