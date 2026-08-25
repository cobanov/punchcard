package service

import (
	"context"
	"testing"
	"time"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/testutil"
)

// TestChangesGraceWindowAndReset covers the two delta-feed correctness guards
// (docs/offline-sync.md §5): the freshness grace window that keeps a
// forward-only cursor from permanently skipping events whose transactions
// commit out of seq order, and the retention-horizon reset that forces a full
// resync when the cursor predates the oldest retained event.
func TestChangesGraceWindowAndReset(t *testing.T) {
	// Isolated: this test deletes events rows, which would race concurrently
	// running package tests on a shared TEST_DATABASE_URL.
	pool := testutil.IsolatedPostgres(t)
	store := repo.NewStore(pool)
	logger := observability.NewLogger("error")
	const grace = 1200 * time.Millisecond
	cfg := &config.Config{
		Env: config.EnvDevelopment, EmailProvider: "dev",
		MaxListsPerUser: 200, MaxTasksPerList: 100,
		EventGraceWindow: grace,
	}
	auditor := audit.NewLogger(store, logger)
	a := NewAuth(store, noopSender{}, auditor, logger, cfg)
	d := NewDomain(store, auditor, noopSender{}, nil, logger, cfg)
	ctx := context.Background()

	p := registerPrincipal(t, a, store, "changes-grace@example.com", true)

	head, err := d.Changes(ctx, p, nil, 0)
	if err != nil {
		t.Fatalf("head pull: %v", err)
	}
	since := head.Next

	l, err := d.CreateList(ctx, p, "Sync", nil)
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	if _, err := d.CreateTask(ctx, p, TaskInput{ListID: l.ID, Title: "t"}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Fresh events are hidden while inside the grace window.
	page, err := d.Changes(ctx, p, &since, 0)
	if err != nil {
		t.Fatalf("pull inside grace: %v", err)
	}
	if len(page.Items) != 0 || page.Reset {
		t.Fatalf("inside grace window: got %d items (reset=%v), want 0", len(page.Items), page.Reset)
	}

	time.Sleep(grace + 300*time.Millisecond)
	page, err = d.Changes(ctx, p, &since, 0)
	if err != nil {
		t.Fatalf("pull after grace: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("after grace window: got %d items, want 2 (list.created, task.created)", len(page.Items))
	}
	if page.Items[0].Type != "list.created" || page.Items[1].Type != "task.created" {
		t.Fatalf("unexpected event order: %s, %s", page.Items[0].Type, page.Items[1].Type)
	}
	if page.Next != page.Items[1].Seq {
		t.Fatalf("next = %d, want last seq %d", page.Next, page.Items[1].Seq)
	}
	seq0, seq1 := page.Items[0].Seq, page.Items[1].Seq

	// Purge the first event: a cursor older than min-1 cannot prove
	// continuity → reset. A cursor exactly at min-1 is still contiguous.
	testutil.Exec(t, pool, "DELETE FROM events WHERE seq <= $1", seq0)
	stale := seq0 - 1
	page, err = d.Changes(ctx, p, &stale, 0)
	if err != nil {
		t.Fatalf("pull behind horizon: %v", err)
	}
	if !page.Reset {
		t.Fatal("cursor behind retention horizon: want reset=true")
	}
	atEdge := seq0 // == min-1 since seq1 = seq0+1 here
	page, err = d.Changes(ctx, p, &atEdge, 0)
	if err != nil {
		t.Fatalf("pull at horizon edge: %v", err)
	}
	if page.Reset || len(page.Items) != 1 || page.Items[0].Seq != seq1 {
		t.Fatalf("at horizon edge: reset=%v items=%d, want the remaining event", page.Reset, len(page.Items))
	}

	// Empty events table with a non-zero cursor: continuity unprovable → reset.
	testutil.Exec(t, pool, "DELETE FROM events")
	page, err = d.Changes(ctx, p, &seq1, 0)
	if err != nil {
		t.Fatalf("pull on empty table: %v", err)
	}
	if !page.Reset {
		t.Fatal("non-zero cursor on empty events table: want reset=true")
	}
}
