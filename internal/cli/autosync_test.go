package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain disarms the spawner for every test in this package.
//
// A test binary asked to run itself with a "sync" argument runs the whole suite
// again, and each of those spawns more — so this is not tidiness, it is the
// difference between a test run and a fork bomb.
func TestMain(m *testing.M) {
	syncSpawner = func(string) error { return nil }
	os.Exit(m.Run())
}

// The hook's first turn kicks off a flush; a second turn moments later does
// not. Without the throttle a fast back-and-forth would spawn a process per
// turn, which is how a helpful background flush becomes a problem of its own.
func TestAutoSyncIsThrottled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	dir := filepath.Join(home, "punchcard")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	var spawns int
	restore := syncSpawner
	syncSpawner = func(string) error { spawns++; return nil }
	t.Cleanup(func() { syncSpawner = restore })

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	app.maybeAutoSync(dir)
	app.maybeAutoSync(dir)
	app.maybeAutoSync(dir)
	if spawns != 1 {
		t.Fatalf("three turns in a row spawned %d flushes, want 1", spawns)
	}

	// Age the stamp past the window and the next turn is allowed through.
	old := time.Now().Add(-2 * autoSyncEvery)
	if err := os.Chtimes(stampPath(dir), old, old); err != nil {
		t.Fatal(err)
	}
	app.maybeAutoSync(dir)
	if spawns != 2 {
		t.Fatalf("after the window elapsed spawns = %d, want 2", spawns)
	}
}

// Recording without sending has to stay available: someone may want the turns
// on disk and the network never touched.
func TestAutoSyncCanBeTurnedOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	t.Setenv("PUNCHCARD_NO_AUTOSYNC", "1")
	dir := filepath.Join(home, "punchcard")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	var spawns int
	restore := syncSpawner
	syncSpawner = func(string) error { spawns++; return nil }
	t.Cleanup(func() { syncSpawner = restore })

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	app.maybeAutoSync(dir)
	if spawns != 0 {
		t.Fatalf("PUNCHCARD_NO_AUTOSYNC still spawned %d flush(es)", spawns)
	}
	if _, err := os.Stat(stampPath(dir)); err == nil {
		t.Fatal("a disabled autosync wrote its throttle stamp")
	}
}

// A server that is down must not turn every turn into a doomed child process.
// The stamp is written before the spawn, so a failing flush still costs one
// attempt per window and not one per turn.
func TestAFailingFlushStillRespectsTheThrottle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	dir := filepath.Join(home, "punchcard")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	var spawns int
	restore := syncSpawner
	syncSpawner = func(string) error { spawns++; return os.ErrPermission }
	t.Cleanup(func() { syncSpawner = restore })

	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	for range 5 {
		app.maybeAutoSync(dir)
	}
	if spawns != 1 {
		t.Fatalf("five turns against a broken flush spawned %d, want 1", spawns)
	}
}

// Two syncers overlapping must carry disjoint halves, not destroy each other's.
// With a single shared ".sending" name the second claim renamed a freshly
// written queue over the first claim's in-flight batch and lost it.
func TestTwoClaimsDoNotClobberEachOther(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	dir := filepath.Join(home, "punchcard")

	write := func(id string) {
		if err := appendQueue(dir, QueuedRun{
			Tool: "claude-code", ExternalID: id,
			StartedAt: "2026-08-27T10:00:00Z", EndedAt: "2026-08-27T10:05:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}

	write("first")
	firstRuns, firstBatch, _, err := claimQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstRuns) != 1 || firstRuns[0].ExternalID != "first" {
		t.Fatalf("first claim took %+v", firstRuns)
	}

	// A hook appends while the first syncer is still sending.
	write("second")
	secondRuns, secondBatch, _, err := claimQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondRuns) != 1 || secondRuns[0].ExternalID != "second" {
		t.Fatalf("second claim took %+v", secondRuns)
	}
	if firstBatch == secondBatch {
		t.Fatal("both claims used the same batch path")
	}
	// The first syncer's batch must still be on disk, untouched.
	if _, err := os.Stat(firstBatch); err != nil {
		t.Fatalf("the first batch was destroyed by the second claim: %v", err)
	}
}

// A syncer killed between claiming and sending used to take its batch with it
// silently. Old batches come back; a batch that may still be in flight does not.
func TestOrphanedBatchesComeBackOnceTheyAreOldEnough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	dir := filepath.Join(home, "punchcard")

	if err := appendQueue(dir, QueuedRun{
		Tool: "claude-code", ExternalID: "abandoned",
		StartedAt: "2026-08-27T10:00:00Z", EndedAt: "2026-08-27T10:05:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	_, batch, _, err := claimQueue(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Fresh: another syncer may still be sending it, so it is left alone.
	recoverOrphans(dir)
	if n := pendingRuns(dir); n != 0 {
		t.Fatalf("a batch claimed a moment ago was stolen back (%d pending)", n)
	}

	old := time.Now().Add(-2 * orphanAfter)
	if err := os.Chtimes(batch, old, old); err != nil {
		t.Fatal(err)
	}
	recoverOrphans(dir)
	if n := pendingRuns(dir); n != 1 {
		t.Fatalf("an abandoned batch was not recovered (%d pending)", n)
	}
	if _, err := os.Stat(batch); !os.IsNotExist(err) {
		t.Fatal("the recovered batch file was left behind to be recovered again")
	}
}

// The queue count is what a client shows a person deciding whether something is
// broken, so it counts lines rather than estimating from the file size.
func TestPendingRunsCountsWhatIsWaiting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	dir := filepath.Join(home, "punchcard")

	if n := pendingRuns(dir); n != 0 {
		t.Fatalf("an empty state dir reported %d pending", n)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := appendQueue(dir, QueuedRun{
			Tool: "claude-code", ExternalID: id,
			StartedAt: "2026-08-27T10:00:00Z", EndedAt: "2026-08-27T10:05:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n := pendingRuns(dir); n != 3 {
		t.Fatalf("pendingRuns = %d, want 3", n)
	}
	if got := describePending(1); !strings.Contains(got, "1 agent turn ") {
		t.Fatalf("describePending(1) = %q, want the singular", got)
	}
}
