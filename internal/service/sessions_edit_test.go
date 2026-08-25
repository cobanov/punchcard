package service

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// stoppedSession records a fixed 10:00–12:00 stretch, the shape most of the
// correction tests want.
func (e *testEnv) stoppedSession(t *testing.T, p *auth.Principal, projectID uuid.UUID) db.WorkSession {
	t.Helper()
	start, end := at("10:00"), at("12:00")
	ws, err := e.d.StartSession(e.ctx, p, StartSessionInput{
		ProjectID: projectID, Note: "iş", StartedAt: &start, StopCurrent: true,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	stopped, err := e.d.StopSession(e.ctx, p, ws.ID, end)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	return stopped
}

// seedCommit writes a commit and attaches it to a session, standing in for what
// the GitHub scanner would have done.
func (e *testEnv) seedCommit(t *testing.T, p *auth.Principal, sessionID uuid.UUID, sha string, when time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	c, err := e.d.store.UpsertCommit(e.ctx, db.UpsertCommitParams{
		ID: id, UserID: p.UserID, RepoFullName: "cobanov/x", Sha: sha,
		Message: "fixture", CommittedAt: when, Url: "",
	})
	if err != nil {
		t.Fatalf("upsert commit: %v", err)
	}
	if _, err := e.d.store.AttachCommitToSession(e.ctx, db.AttachCommitToSessionParams{
		SessionID: sessionID, CommitID: c.ID,
	}); err != nil {
		t.Fatalf("attach commit: %v", err)
	}
	return c.ID
}

func TestUpdateRejectsInvertedRange(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	ws := e.stoppedSession(t, p, e.mustProject(t, p, "p").ID)

	end := ws.StartedAt.Add(-time.Minute)
	_, err := e.d.UpdateSession(e.ctx, p, ws.ID, UpdateSessionInput{SetEnded: true, EndedAt: &end})
	var de *Error
	if !asError(err, &de) || de.Status != 422 {
		t.Fatalf("want 422, got %v", err)
	}
}

// Correcting a time re-queues the scan: a different window may cover different
// commits.
func TestMovingASessionRequeuesItsScan(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	ws := e.stoppedSession(t, p, e.mustProject(t, p, "p").ID)
	if err := e.d.store.SetSessionSyncOK(e.ctx, ws.ID); err != nil {
		t.Fatalf("mark synced: %v", err)
	}

	newEnd := at("13:00")
	updated, err := e.d.UpdateSession(e.ctx, p, ws.ID, UpdateSessionInput{SetEnded: true, EndedAt: &newEnd})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.SyncState != "pending" {
		t.Fatalf("sync_state = %q, want pending after the window moved", updated.SyncState)
	}
}

func TestSplitProducesTwoAdjacentSessions(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	ws := e.stoppedSession(t, p, e.mustProject(t, p, "p").ID)
	cut := at("11:00")

	left, right, err := e.d.SplitSession(e.ctx, p, ws.ID, cut)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if !left.EndedAt.Equal(cut) {
		t.Fatalf("left ends %s, want %s", left.EndedAt, cut)
	}
	if !right.StartedAt.Equal(cut) {
		t.Fatalf("right starts %s, want %s", right.StartedAt, cut)
	}
	if !right.EndedAt.Equal(*ws.EndedAt) {
		t.Fatalf("right ends %s, want the original %s", right.EndedAt, ws.EndedAt)
	}
}

func TestSplitOutsideRangeFails(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	ws := e.stoppedSession(t, p, e.mustProject(t, p, "p").ID)

	for _, bad := range []time.Time{at("09:00"), at("13:00"), ws.StartedAt, *ws.EndedAt} {
		if _, _, err := e.d.SplitSession(e.ctx, p, ws.ID, bad); err == nil {
			t.Errorf("split at %s should have been rejected", bad)
		}
	}
}

// The evidence has to follow the clock, or splitting a stretch of work leaves
// its commits filed under the wrong half.
func TestSplitReassignsCommitsByTime(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	ws := e.stoppedSession(t, p, e.mustProject(t, p, "p").ID)
	e.seedCommit(t, p, ws.ID, "aaaaaaa", at("10:15"))
	e.seedCommit(t, p, ws.ID, "bbbbbbb", at("11:30"))

	left, right, err := e.d.SplitSession(e.ctx, p, ws.ID, at("11:00"))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	lc, err := e.d.CommitsForSession(e.ctx, p, left.ID)
	if err != nil {
		t.Fatalf("left commits: %v", err)
	}
	rc, err := e.d.CommitsForSession(e.ctx, p, right.ID)
	if err != nil {
		t.Fatalf("right commits: %v", err)
	}
	if len(lc) != 1 || lc[0].Sha != "aaaaaaa" {
		t.Fatalf("left commits = %v", shas(lc))
	}
	if len(rc) != 1 || rc[0].Sha != "bbbbbbb" {
		t.Fatalf("right commits = %v", shas(rc))
	}
}

func TestRunningSessionCannotBeSplit(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")
	ws := e.mustStart(t, p, proj.ID, "çalışıyor")

	if _, _, err := e.d.SplitSession(e.ctx, p, ws.ID, time.Now()); err == nil {
		t.Fatal("a running session has no end to split against")
	}
}

// Deleting a session must not delete its commits: they become unmatched again
// and can be recovered into a new record.
func TestDeletingASessionReleasesItsCommits(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	ws := e.stoppedSession(t, p, e.mustProject(t, p, "p").ID)
	e.seedCommit(t, p, ws.ID, "aaaaaaa", at("10:15"))

	if err := e.d.DeleteSession(e.ctx, p, ws.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	unmatched, err := e.d.store.ListUnmatchedCommits(e.ctx, db.ListUnmatchedCommitsParams{
		UserID: p.UserID, FromTs: at("00:00"), ToTs: at("23:00"),
	})
	if err != nil {
		t.Fatalf("list unmatched: %v", err)
	}
	if len(unmatched) != 1 || unmatched[0].Sha != "aaaaaaa" {
		t.Fatalf("the commit should be unmatched again, got %v", shas(unmatched))
	}
}

// Reopening a record while another timer runs would put two sessions in flight.
func TestReopeningWhileAnotherSessionRunsConflicts(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")
	old := e.stoppedSession(t, p, proj.ID)
	e.mustStart(t, p, proj.ID, "şimdi")

	_, err := e.d.UpdateSession(e.ctx, p, old.ID, UpdateSessionInput{SetEnded: true, EndedAt: nil})
	var de *Error
	if !asError(err, &de) || de.Status != 409 {
		t.Fatalf("want 409, got %v", err)
	}
}

func shas(rows []db.Commit) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Sha)
	}
	return out
}
