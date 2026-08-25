package service

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/repo/db"
)

func (e *testEnv) mustStart(t *testing.T, p *auth.Principal, projectID uuid.UUID, note string) db.WorkSession {
	t.Helper()
	ws, err := e.d.StartSession(e.ctx, p, StartSessionInput{
		ProjectID: projectID, Note: note, StopCurrent: true,
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	return ws
}

// Pressing start while something is running should do the obvious thing: close
// the old timer and open the new one.
func TestStartingASecondSessionStopsTheFirst(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")

	first := e.mustStart(t, p, proj.ID, "bir")
	second := e.mustStart(t, p, proj.ID, "iki")

	reloaded, err := e.d.GetSession(e.ctx, p, first.ID)
	if err != nil {
		t.Fatalf("reload first: %v", err)
	}
	if reloaded.EndedAt == nil {
		t.Fatal("the first session should have been closed")
	}
	if second.EndedAt != nil {
		t.Fatal("the second session should be running")
	}
	cur, err := e.d.CurrentSession(e.ctx, p)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if cur.ID != second.ID {
		t.Fatalf("current = %s, want %s", cur.ID, second.ID)
	}
}

// The two sessions must abut, not overlap: commit attribution assumes at most
// one session covers any instant.
func TestReplacedSessionEndsWhereTheNextBegins(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")

	first := e.mustStart(t, p, proj.ID, "bir")
	second := e.mustStart(t, p, proj.ID, "iki")

	reloaded, _ := e.d.GetSession(e.ctx, p, first.ID)
	if !reloaded.EndedAt.Equal(second.StartedAt) {
		t.Fatalf("first ends %s but second starts %s — ranges must abut",
			reloaded.EndedAt, second.StartedAt)
	}
}

// A caller that wants the conflict reported rather than resolved gets a 409.
func TestSecondSessionWithoutStopCurrentConflicts(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")
	e.mustStart(t, p, proj.ID, "bir")

	_, err := e.d.StartSession(e.ctx, p, StartSessionInput{ProjectID: proj.ID, StopCurrent: false})
	var de *Error
	if !asError(err, &de) || de.Status != 409 {
		t.Fatalf("want 409, got %v", err)
	}
}

// The one-open-session rule is a database constraint, not a service check.
// If it is ever moved into Go, two clients racing can both win and every report
// touching those days becomes wrong with no error anywhere.
func TestOpenSessionUniquenessIsEnforcedByTheDatabase(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")

	const insert = `INSERT INTO work_sessions (id, project_id, user_id, started_at)
	                VALUES ($1, $2, $3, now())`
	if _, err := e.pool.Exec(e.ctx, insert, uuid.New(), proj.ID, p.UserID); err != nil {
		t.Fatalf("first open session: %v", err)
	}
	_, err := e.pool.Exec(e.ctx, insert, uuid.New(), proj.ID, p.UserID)
	if err == nil {
		t.Fatal("the database must reject a second open session")
	}
	if got := err.Error(); !contains(got, "one_open_session_per_user") {
		t.Fatalf("rejected for the wrong reason: %v", got)
	}
}

// Retrying a stop must not extend the record.
func TestStopIsIdempotent(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")
	ws := e.mustStart(t, p, proj.ID, "bir")

	stopped, err := e.d.StopSession(e.ctx, p, ws.ID, time.Now())
	if err != nil {
		t.Fatalf("first stop: %v", err)
	}
	again, err := e.d.StopSession(e.ctx, p, ws.ID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if !again.EndedAt.Equal(*stopped.EndedAt) {
		t.Fatalf("end time moved on the second stop: %s -> %s", stopped.EndedAt, again.EndedAt)
	}
}

// Stopping schedules the GitHub scan. Without this the commits never arrive and
// the failure is silent — the record simply shows none.
func TestStoppingQueuesACommitScan(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")
	ws := e.mustStart(t, p, proj.ID, "bir")

	stopped, err := e.d.StopSession(e.ctx, p, ws.ID, time.Now())
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.SyncState != "pending" {
		t.Fatalf("sync_state = %q, want pending", stopped.SyncState)
	}
	// NULL means "due now". It used to be now(), which compared the database's
	// clock against the Go process's — a few milliseconds of drift and the scan
	// waited a whole tick, or in a test, never ran at all.
	if stopped.SyncNextAt != nil {
		t.Fatalf("sync_next_at = %v, want NULL so the session is due immediately", stopped.SyncNextAt)
	}
}

// The regression that made three scan tests flaky: a session queued at stop
// time must be claimable on the very next pass, whatever the two clocks think
// of each other.
func TestASessionQueuedAtStopIsImmediatelyClaimable(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")
	ws := e.mustStart(t, p, proj.ID, "bir")
	if _, err := e.d.StopSession(e.ctx, p, ws.ID, time.Now()); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Claim with a `now` deliberately BEHIND the database's clock, which is the
	// skew that broke it.
	claimed, err := e.d.store.ClaimPendingSyncSessions(e.ctx, db.ClaimPendingSyncSessionsParams{
		Now: time.Now().Add(-time.Hour), Lim: 10,
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	for _, c := range claimed {
		if c.ID == ws.ID {
			return
		}
	}
	t.Fatalf("the session was not claimable; claimed %d rows", len(claimed))
}

// A zero-length session is not a record of anything, and the range CHECK would
// reject it. Stopping at or before the start is nudged forward instead of
// failing, because the user's intent — end this — is unambiguous.
func TestStoppingAtTheStartInstantStillProducesAValidRange(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")
	ws := e.mustStart(t, p, proj.ID, "bir")

	stopped, err := e.d.StopSession(e.ctx, p, ws.ID, ws.StartedAt)
	if err != nil {
		t.Fatalf("stop at start: %v", err)
	}
	if !stopped.EndedAt.After(stopped.StartedAt) {
		t.Fatalf("ended_at %s must be after started_at %s", stopped.EndedAt, stopped.StartedAt)
	}
}

// Another account's session is invisible, not forbidden.
func TestSessionOfAnotherUserReadsAsNotFound(t *testing.T) {
	e := newTestEnv(t)
	alice, bob := e.newUser(t), e.newUser(t)
	proj := e.mustProject(t, alice, "gizli")
	ws := e.mustStart(t, alice, proj.ID, "bir")

	if _, err := e.d.GetSession(e.ctx, bob, ws.ID); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// Timers on two accounts do not interfere: the rule is one open session per
// user, not one per server.
func TestTwoUsersCanRunTimersAtOnce(t *testing.T) {
	e := newTestEnv(t)
	alice, bob := e.newUser(t), e.newUser(t)
	pa, pb := e.mustProject(t, alice, "a"), e.mustProject(t, bob, "b")

	e.mustStart(t, alice, pa.ID, "alice")
	e.mustStart(t, bob, pb.ID, "bob")

	ca, err := e.d.CurrentSession(e.ctx, alice)
	if err != nil {
		t.Fatalf("alice current: %v", err)
	}
	cb, err := e.d.CurrentSession(e.ctx, bob)
	if err != nil {
		t.Fatalf("bob current: %v", err)
	}
	if ca.ID == cb.ID {
		t.Fatal("the two accounts share a session")
	}
}

// CurrentSession reports absence, not an empty record.
func TestCurrentSessionIsNotFoundWhenIdle(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	if _, err := e.d.CurrentSession(e.ctx, p); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
