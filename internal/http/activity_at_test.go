package http

import (
	"net/http"
	"testing"
	"time"

	"github.com/cobanov/punchcard/internal/testutil"
)

// latestActivityTime reads the occurred_at column of this env's user's most
// recently *written* activity row, straight from Postgres — there is no read
// endpoint yet, so the table is the only place to check what was actually
// recorded. Ordered by id, not occurred_at: id is a UUIDv7 stamped at insert
// time and so is a true record of write order, whereas occurred_at is exactly
// the value these tests backdate — ordering by it would let newAPIEnv's setup
// writes (list creation, recorded at real "now") outrank a deliberately
// backdated row and hide the one the test just made.
func (e *apiEnv) latestActivityTime(t *testing.T) time.Time {
	t.Helper()
	row := testutil.QueryRow(t, e.pool, `
		SELECT occurred_at FROM activity WHERE user_id = $1
		ORDER BY id DESC LIMIT 1`, e.userID)
	var occurred time.Time
	if err := row.Scan(&occurred); err != nil {
		t.Fatalf("latest activity time: %v", err)
	}
	return occurred
}

// activityTimeFor reads the occurred_at of this env's user's activity row for
// a given subject (task title). Unlike latestActivityTime — which can only
// ever answer for the single most recent write — this can tell two rows from
// the same request apart, which a test asserting on more than one op in a
// single batch needs.
func (e *apiEnv) activityTimeFor(t *testing.T, subject string) time.Time {
	t.Helper()
	row := testutil.QueryRow(t, e.pool, `
		SELECT occurred_at FROM activity WHERE user_id = $1 AND subject = $2
		ORDER BY id DESC LIMIT 1`, e.userID, subject)
	var occurred time.Time
	if err := row.Scan(&occurred); err != nil {
		t.Fatalf("activity time for %q: %v", subject, err)
	}
	return occurred
}

// One bulk batch covers a whole offline session, and its ops did not all
// happen at the same moment: this sends one backdated create alongside one
// that omits `at`, in the SAME request, and requires the two resulting
// activity rows to land at two different times, each the right one. A
// per-request stamp — clamping the whole batch to a single time rather than
// applying `at` inside the per-op loop — would pass every other test in this
// file (each sends only one op) while still being exactly the bug this task
// exists to fix.
func TestBulkAtIsPerOp(t *testing.T) {
	env := newAPIEnv(t)
	when := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	before := time.Now().UTC().Add(-time.Minute)

	status, _ := do(t, env.session, http.MethodPost, env.base+"/v1/tasks/bulk", map[string]any{
		"operations": []map[string]any{
			{
				"op": "create", "at": when.Format(time.RFC3339),
				"task": map[string]any{"list_id": env.listID, "title": "batch: backdated"},
			},
			{
				"op":   "create",
				"task": map[string]any{"list_id": env.listID, "title": "batch: not backdated"},
			},
		},
	}, testCSRF())
	must(t, "mixed bulk batch", status, http.StatusOK)

	backdated := env.activityTimeFor(t, "batch: backdated")
	if backdated.Sub(when).Abs() > time.Second {
		t.Fatalf("backdated op: occurred_at got %v, want %v", backdated, when)
	}
	notBackdated := env.activityTimeFor(t, "batch: not backdated")
	if notBackdated.Before(before) {
		t.Fatalf("non-backdated op: occurred_at got %v, want ~now", notBackdated)
	}
	if !notBackdated.After(backdated.Add(time.Hour)) {
		t.Fatalf("expected the two ops to land at clearly different times: backdated=%v, not-backdated=%v", backdated, notBackdated)
	}
}

func TestBulkAtBackdatesTheLog(t *testing.T) {
	env := newAPIEnv(t)
	when := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)

	status, _ := do(t, env.session, http.MethodPost, env.base+"/v1/tasks/bulk", map[string]any{
		"operations": []map[string]any{{
			"op": "create", "at": when.Format(time.RFC3339),
			"task": map[string]any{"list_id": env.listID, "title": "added on the plane"},
		}},
	}, testCSRF())
	must(t, "bulk create with at", status, http.StatusOK)

	got := env.latestActivityTime(t)
	if got.Sub(when).Abs() > time.Second {
		t.Fatalf("occurred_at: got %v, want %v", got, when)
	}
}

// A client may be wrong about the time, and a log that can be backdated
// arbitrarily is not evidence of anything.
func TestBulkAtIsClamped(t *testing.T) {
	env := newAPIEnv(t)
	now := time.Now().UTC()

	for _, tc := range []struct {
		name  string
		at    time.Time
		check func(time.Time) bool
	}{
		{"far future", now.Add(72 * time.Hour), func(g time.Time) bool { return !g.After(now.Add(time.Minute)) }},
		{"far past", now.Add(-400 * 24 * time.Hour), func(g time.Time) bool { return g.After(now.Add(-31 * 24 * time.Hour)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := do(t, env.session, http.MethodPost, env.base+"/v1/tasks/bulk", map[string]any{
				"operations": []map[string]any{{
					"op": "create", "at": tc.at.Format(time.RFC3339),
					"task": map[string]any{"list_id": env.listID, "title": "clamp me " + tc.name},
				}},
			}, testCSRF())
			must(t, "bulk create", status, http.StatusOK)
			if got := env.latestActivityTime(t); !tc.check(got) {
				t.Fatalf("occurred_at not clamped: got %v (now %v)", got, now)
			}
		})
	}
}

// The field is optional because commands already queued in IndexedDB carry the
// old shape and there is no store migration — a required field would silently
// drop whatever was waiting to be sent, which is the trap the list-color
// command was given its own kind to avoid.
func TestBulkWithoutAtUsesServerTime(t *testing.T) {
	env := newAPIEnv(t)
	before := time.Now().UTC().Add(-time.Minute)

	status, _ := do(t, env.session, http.MethodPost, env.base+"/v1/tasks/bulk", map[string]any{
		"operations": []map[string]any{{
			"op":   "create",
			"task": map[string]any{"list_id": env.listID, "title": "no at field"},
		}},
	}, testCSRF())
	must(t, "bulk create without at", status, http.StatusOK)

	if got := env.latestActivityTime(t); got.Before(before) {
		t.Fatalf("occurred_at: got %v, want ~now", got)
	}
}
