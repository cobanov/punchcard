package repo_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/testutil"
)

// testStore pairs a Store with the pool testutil.Postgres handed back, so
// this file's helpers can run assertion SQL through testutil.Query exactly as
// internal/service/activity_write_test.go and internal/http's activity tests
// do. Store has no accessor for its own pool — 8871bf3 removed one as
// unreachable — so a test that needs raw SQL has to keep its own reference
// rather than reach back into Store for it.
type testStore struct {
	*repo.Store
	pool *testutil.Pool
}

// newStore provisions a fresh, fully-migrated Postgres (testutil.Postgres)
// and wraps it in a Store. A sweep runs underneath authorization and origin
// labelling entirely, so a bare Store — no service.Domain, no principal — is
// the whole harness this package's tests need, unlike newTestEnv in
// internal/service/activity_write_test.go, which builds a full Domain because
// its tests exercise that layer.
func newStore(t *testing.T) (*testStore, context.Context) {
	t.Helper()
	pool := testutil.Postgres(t)
	return &testStore{Store: repo.NewStore(pool), pool: pool}, context.Background()
}

// insertActivityAt writes one activity row at an arbitrary occurred_at,
// straight through the store rather than internal/activity.Write: Write's
// At() clamps a backdated timestamp to 30 days (the offline sync horizon), so
// it can never produce a fixture past the 400-day line this file exists to
// enforce. Each call registers its own throwaway user to satisfy
// activity.user_id's FK (00013_activity.sql) — a row none of the other sweep
// jobs have any reason to touch, so this fixture cannot be disturbed by
// running the rest of the sweep alongside it.
func insertActivityAt(t *testing.T, store *testStore, occurredAt time.Time, subject string) {
	t.Helper()
	ctx := context.Background()
	u, err := store.CreateUser(ctx, db.CreateUserParams{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("retention-%s@example.com", uuid.NewString()),
		PasswordHash: "x",
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	err = store.InsertActivity(ctx, db.InsertActivityParams{
		ID:         uuid.New(),
		UserID:     u.ID,
		Origin:     "user",
		Action:     "fixture",
		Subject:    &subject,
		Detail:     []byte("{}"),
		OccurredAt: occurredAt,
	})
	if err != nil {
		t.Fatalf("insert activity at %s: %v", occurredAt, err)
	}
}

// activitySubjects returns the subject of every row still in the table,
// oldest first. A purge test needs to name its survivors, not just count
// them — a bug that purges the wrong row can still leave the right count.
func activitySubjects(t *testing.T, store *testStore) []string {
	t.Helper()
	rows := testutil.Query(t, store.pool, `SELECT subject FROM activity ORDER BY occurred_at`)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var subject *string
		if err := rows.Scan(&subject); err != nil {
			t.Fatalf("scan subject: %v", err)
		}
		if subject != nil {
			out = append(out, *subject)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("activity subjects: %v", err)
	}
	return out
}

// Calling store.PurgeOldActivity directly would only prove the SQL cutoff
// works — it says nothing about whether the janitor's sweep ever runs it, so
// the job could be left out of sweep's job list and this test would stay
// green forever. Driving it through Janitor.Sweep (export_test.go's
// passthrough to the unexported sweep) is what makes that omission visible:
// until purge_old_activity is added to the list, sweep runs only the other
// three jobs (tasks, events, idempotency_keys — none of which touch
// activity) and leaves both fixture rows behind, so this fails for the right
// reason before the wiring exists.
func TestSweepPurgesOldActivity(t *testing.T) {
	store, ctx := newStore(t)
	insertActivityAt(t, store, time.Now().Add(-401*24*time.Hour), "ancient")
	insertActivityAt(t, store, time.Now().Add(-399*24*time.Hour), "recent enough")

	// interval is unused: Sweep runs one pass directly, so Run's ticker loop
	// never starts.
	j := repo.NewJanitor(store.Store, observability.NewLogger("error"), time.Hour)
	j.Sweep(ctx)

	if got := activitySubjects(t, store); len(got) != 1 || got[0] != "recent enough" {
		t.Fatalf("survivors: %v", got)
	}
}
