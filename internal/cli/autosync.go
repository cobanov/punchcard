package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Nothing sends the queue on its own unless this file makes it happen.
//
// The hook appends a turn and returns; that is deliberate and stays true. But
// for the first version the only thing that ever emptied the queue was a human
// typing `punchcard sync`, with the README suggesting they wire up launchd
// themselves. That is homework, and homework does not get done: on the machine
// this was written, seventy-one turns sat unsent for two days and nothing
// anywhere said so. A queue nobody drains looks exactly like "I never used an
// agent", which is the same silent-zero trap the GitHub scanner already has a
// warning about.
//
// So the queue now drains from two directions, neither of which asks the user
// to install anything:
//
//   - Opportunistically, from the commands that are already authenticated and
//     already talking to the server (`stop`, `status`, `today`, `week`).
//   - From the Stop hook itself, throttled, as a DETACHED child process — the
//     hook still appends and returns without waiting on the network, and on a
//     plane the child simply fails and the queue keeps its lines.
//
// launchd, cron and systemd timers still work and are still documented. They
// are now an option for people who want one, not the only path.

// autoSyncEvery is how long the hook waits before spawning another flush.
//
// Turns arrive every few minutes during real work, and each one is a single
// line. Two minutes keeps the timeline close to live while a fast
// back-and-forth spawns one child per two minutes rather than one per turn.
const autoSyncEvery = 2 * time.Minute

// orphanAfter is how long a claimed batch may sit before it is presumed dead.
//
// A syncer that is killed between claiming the queue and sending it leaves the
// batch on disk under a name nothing else looks at. Ten minutes is far longer
// than any send and far shorter than a working day, and re-sending costs
// nothing anyway: `external_id` is the idempotency key, so the worst outcome of
// recovering a batch that was in fact delivered is a "duplicates" count.
const orphanAfter = 10 * time.Minute

func stampPath(dir string) string { return filepath.Join(dir, "last-autosync") }

// pendingRuns counts what is waiting, for the clients that want to show it.
//
// It reads rather than estimates: the number is shown to a person deciding
// whether something is wrong, and "about 70" would not help them.
func pendingRuns(dir string) int {
	runs, _ := readQueue(queuePath(dir))
	return len(runs)
}

// flushQuietly sends the queue with a client the caller already has.
//
// Failure is not reported: these calls ride along with a command the user asked
// for something else entirely, and `punchcard status` refusing to print because
// an unrelated background flush failed would be a worse bug than the one this
// fixes. Whatever does not go stays queued for the next attempt.
func (a *App) flushQuietly(c *Client) (accepted int) {
	dir, err := StateDir()
	if err != nil {
		return 0
	}
	accepted, _, _, err = syncQueue(dir, c)
	if err != nil {
		return 0
	}
	return accepted
}

// maybeAutoSync spawns a detached `punchcard sync` if one is due.
//
// The stamp is written BEFORE the child starts, not after it succeeds. A server
// that is down would otherwise leave the stamp stale and spawn a doomed child
// on every single turn, turning an outage into a fork bomb paced by the user's
// typing.
func (a *App) maybeAutoSync(dir string) {
	if os.Getenv("PUNCHCARD_NO_AUTOSYNC") != "" {
		return
	}
	if info, err := os.Stat(stampPath(dir)); err == nil {
		if time.Since(info.ModTime()) < autoSyncEvery {
			return
		}
	}
	self, err := os.Executable()
	if err != nil || self == "" {
		return
	}
	if err := os.WriteFile(stampPath(dir), []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600); err != nil {
		return
	}
	_ = syncSpawner(self)
}

// syncSpawner starts the detached flush.
//
// It is a variable so the throttle can be tested without launching anything:
// the executable a test binary would spawn is the test binary, and running it
// with a "sync" argument runs the whole suite again — recursively. TestMain in
// this package replaces it for exactly that reason.
var syncSpawner = startDetachedSync

func startDetachedSync(self string) error {
	// #nosec G204 -- self is this executable's own path, not user input.
	cmd := exec.Command(self, "sync")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Deliberately not waited on for its result. The point is that the hook
	// returns now; the child is reparented and finishes on its own, or does
	// not, and either way the queue is left consistent. The goroutine exists
	// only so the process is reaped rather than left a zombie.
	go func() { _ = cmd.Wait() }()
	return nil
}

// describePending is the one line a client shows when the queue is not empty.
func describePending(n int) string {
	if n == 1 {
		return "1 agent turn waiting to sync"
	}
	return fmt.Sprintf("%d agent turns waiting to sync", n)
}
