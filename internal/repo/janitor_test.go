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

// testStore pairs a Store with the pool testutil.Postgres handed back, so this
// file's helpers can run assertion SQL. Store has no accessor for its own pool,
// so a test that needs raw SQL keeps its own reference.
type testStore struct {
	*repo.Store
	pool *testutil.Pool
}

// newStore provisions a fresh, fully-migrated Postgres and wraps it in a Store.
// A sweep runs underneath authorization entirely, so a bare Store — no Domain,
// no principal — is the whole harness these tests need.
func newStore(t *testing.T) (*testStore, context.Context) {
	t.Helper()
	pool := testutil.Postgres(t)
	return &testStore{Store: repo.NewStore(pool), pool: pool}, context.Background()
}

// insertEventAt writes one outbox event at an arbitrary created_at. The row is
// inserted through raw SQL rather than InsertEvent because created_at defaults
// to now() and the retention line is the whole point of the fixture. Each call
// registers its own throwaway user to satisfy events.user_id's foreign key.
func insertEventAt(t *testing.T, store *testStore, createdAt time.Time, eventType string) {
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
	testutil.Exec(t, store.pool,
		`INSERT INTO events (id, type, user_id, payload, created_at, processed_at)
		 VALUES ($1, $2, $3, '{}'::jsonb, $4, now())`,
		uuid.New(), eventType, u.ID, createdAt)
}

// eventTypes returns the type of every row still in the table, oldest first. A
// purge test needs to name its survivors, not just count them — a bug that
// purges the wrong row can still leave the right count.
func eventTypes(t *testing.T, store *testStore) []string {
	t.Helper()
	rows := testutil.Query(t, store.pool, `SELECT type FROM events ORDER BY created_at`)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatalf("scan type: %v", err)
		}
		out = append(out, typ)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("event types: %v", err)
	}
	return out
}

// Calling store.PurgeOldEvents directly would only prove the SQL cutoff works —
// it says nothing about whether the janitor's sweep ever runs it, so the job
// could be dropped from sweep's list and this test would stay green forever.
// Driving it through Janitor.Sweep (export_test.go's passthrough to the
// unexported sweep) is what makes that omission visible.
func TestSweepPurgesOldEvents(t *testing.T) {
	store, ctx := newStore(t)
	insertEventAt(t, store, time.Now().Add(-31*24*time.Hour), "ancient")
	insertEventAt(t, store, time.Now().Add(-29*24*time.Hour), "recent enough")

	// interval is unused: Sweep runs one pass directly, so Run's ticker loop
	// never starts.
	j := repo.NewJanitor(store.Store, observability.NewLogger("error"), time.Hour)
	j.Sweep(ctx)

	if got := eventTypes(t, store); len(got) != 1 || got[0] != "recent enough" {
		t.Fatalf("survivors: %v", got)
	}
}
