package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/activity"
	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/email"
	"github.com/cobanov/punchcard/internal/events"
	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/service"
	"github.com/cobanov/punchcard/internal/testutil"
)

// testEnv is one registered owner over a fresh database, wired the same way
// production wires Domain (NewAuth/NewDomain over one store), so a test
// exercises the real transaction path — authorization, WithTx, events.Write —
// rather than a shortcut around it.
type testEnv struct {
	pool    *testutil.Pool
	store   *repo.Store
	authSvc *service.Auth
	domain  *service.Domain
	owner   *auth.Principal
}

// newTestEnv provisions a fresh Postgres (internal/testutil.Postgres) and one
// verified owner principal, mirroring newTestAuthDomain/registerPrincipal in
// invites_test.go. The owner's email is unique per call rather than a fixed
// literal: testutil.Postgres reuses a shared TEST_DATABASE_URL across a
// package's tests when one is set (only IsolatedPostgres gets a private
// database), and a repeated literal address would collide on the second call
// with ErrEmailTaken.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	pool := testutil.Postgres(t)
	store := repo.NewStore(pool)
	logger := observability.NewLogger("error")
	cfg := &config.Config{
		Env: config.EnvDevelopment, EmailProvider: "dev",
		MaxListsPerUser: 200, MaxTasksPerList: 100, MaxMembersPerList: 50,
	}
	auditor := audit.NewLogger(store, logger)
	sender := email.New(cfg, logger)
	a := service.NewAuth(store, sender, auditor, logger, cfg)
	d := service.NewDomain(store, auditor, sender, nil, logger, cfg)

	ctx := context.Background()
	addr := fmt.Sprintf("activity-owner-%s@example.com", uuid.NewString())
	u, _, err := a.Register(ctx, addr, "supersecret123", "1.2.3.4", "test")
	if err != nil {
		t.Fatalf("register owner: %v", err)
	}
	if err := store.MarkEmailVerified(ctx, u.ID); err != nil {
		t.Fatalf("verify owner: %v", err)
	}
	owner := &auth.Principal{
		UserID: u.ID, Email: u.Email, EmailVerified: true,
		ViaSession: true, Scope: auth.ScopeReadWrite,
	}
	return &testEnv{pool: pool, store: store, authSvc: a, domain: d, owner: owner}
}

// mustRegisterUser registers and verifies a second account — a person other
// than the owner, e.g. someone to add as a member — reusing the same Auth
// service and store newTestEnv already built for the owner.
func (e *testEnv) mustRegisterUser(ctx context.Context, t *testing.T) *auth.Principal {
	t.Helper()
	addr := fmt.Sprintf("activity-member-%s@example.com", uuid.NewString())
	u, _, err := e.authSvc.Register(ctx, addr, "supersecret123", "1.2.3.4", "test")
	if err != nil {
		t.Fatalf("register user: %v", err)
	}
	if err := e.store.MarkEmailVerified(ctx, u.ID); err != nil {
		t.Fatalf("verify user: %v", err)
	}
	return &auth.Principal{
		UserID: u.ID, Email: u.Email, EmailVerified: true,
		ViaSession: true, Scope: auth.ScopeReadWrite,
	}
}

// mustCreateList creates a list as the env's owner, failing the test on error.
func (e *testEnv) mustCreateList(ctx context.Context, t *testing.T, name string) db.List {
	t.Helper()
	l, err := e.domain.CreateList(ctx, e.owner, name, nil)
	if err != nil {
		t.Fatalf("create list %q: %v", name, err)
	}
	return l
}

// mustCreateTask creates a task as the env's owner, failing the test on error.
func (e *testEnv) mustCreateTask(ctx context.Context, t *testing.T, listID uuid.UUID, title string) db.Task {
	t.Helper()
	task, err := e.domain.CreateTask(ctx, e.owner, service.TaskInput{ListID: listID, Title: title})
	if err != nil {
		t.Fatalf("create task %q: %v", title, err)
	}
	return task
}

// mustRenameList renames a list as the env's owner, failing the test on error.
func (e *testEnv) mustRenameList(ctx context.Context, t *testing.T, listID uuid.UUID, name string) db.List {
	t.Helper()
	l, err := e.domain.UpdateList(ctx, e.owner, listID, &name, nil)
	if err != nil {
		t.Fatalf("rename list: %v", err)
	}
	return l
}

// mustMoveTask moves a task to a different list as the env's owner, failing
// the test on error.
func (e *testEnv) mustMoveTask(ctx context.Context, t *testing.T, taskID, destListID uuid.UUID) db.Task {
	t.Helper()
	task, err := e.domain.UpdateTask(ctx, e.owner, taskID, service.TaskPatch{ListID: &destListID})
	if err != nil {
		t.Fatalf("move task: %v", err)
	}
	return task
}

// mustReposition changes only a task's position as the env's owner, failing
// the test on error.
func (e *testEnv) mustReposition(ctx context.Context, t *testing.T, taskID uuid.UUID, position string) db.Task {
	t.Helper()
	task, err := e.domain.UpdateTask(ctx, e.owner, taskID, service.TaskPatch{Position: &position})
	if err != nil {
		t.Fatalf("reposition task: %v", err)
	}
	return task
}

// A created task must leave exactly one activity row, carrying the title and
// the list's name as they were at the time.
func TestCreateTaskWritesActivity(t *testing.T) {
	env := newTestEnv(t)
	ctx := activity.WithOrigin(context.Background(), activity.User)

	list := env.mustCreateList(ctx, t, "Work")
	task := env.mustCreateTask(ctx, t, list.ID, "Buy milk")

	rows := env.activityRows(t, env.owner.UserID)
	if len(rows) != 2 { // list.created + task.created
		t.Fatalf("got %d activity rows, want 2: %+v", len(rows), rows)
	}
	got := rows[0] // newest first
	if got.Action != "task.created" {
		t.Fatalf("action: got %q, want task.created", got.Action)
	}
	if got.Subject == nil || *got.Subject != "Buy milk" {
		t.Fatalf("subject: got %v, want \"Buy milk\"", got.Subject)
	}
	if got.ListName == nil || *got.ListName != "Work" {
		t.Fatalf("list_name: got %v, want \"Work\"", got.ListName)
	}
	if got.Origin != "user" {
		t.Fatalf("origin: got %q, want user", got.Origin)
	}
	_ = task
}

// The name is a snapshot. Renaming the list must not rewrite what already
// happened.
func TestActivityKeepsTheNameItWasWrittenWith(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	list := env.mustCreateList(ctx, t, "Work")
	env.mustCreateTask(ctx, t, list.ID, "Buy milk")
	env.mustRenameList(ctx, t, list.ID, "İş")

	rows := env.activityRows(t, env.owner.UserID)
	for _, r := range rows {
		if r.Action == "task.created" {
			if r.ListName == nil || *r.ListName != "Work" {
				t.Fatalf("history rewritten: got %v, want \"Work\"", r.ListName)
			}
			return
		}
	}
	t.Fatal("no task.created row")
}

// A member resource carries ids, not names (members.go), so the row's
// subject and detail["who"] — what the sentence template actually reads —
// have to come from a lookup inside activityFields rather than the resource
// itself. This is that lookup's only coverage: without it, subject and
// detail silently stay empty and nothing else notices.
func TestAddMemberWritesReadableActivity(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	list := env.mustCreateList(ctx, t, "Work")
	invitee := env.mustRegisterUser(ctx, t) // fresh account: no display_name, so this also exercises the email fallback

	if err := env.domain.AddMember(ctx, env.owner, list.ID, invitee.UserID, "editor", "1.2.3.4"); err != nil {
		t.Fatalf("add member: %v", err)
	}

	rows := env.activityByAction(t, env.owner.UserID, "member.added")
	if len(rows) != 1 {
		t.Fatalf("got %d member.added rows, want 1: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.Subject == nil || *got.Subject != invitee.Email {
		t.Fatalf("subject: got %v, want %q", got.Subject, invitee.Email)
	}
	if got.ListName == nil || *got.ListName != "Work" {
		t.Fatalf("list_name: got %v, want \"Work\"", got.ListName)
	}
	if who, _ := got.Detail["who"].(string); who != invitee.Email {
		t.Fatalf("detail[who]: got %v, want %q", got.Detail["who"], invitee.Email)
	}
}

// events.Write inserts the event and the activity row in the same
// transaction so the two can never disagree — that is the reason the
// activity write lives inside events.Write rather than beside it (see the
// package doc on internal/activity). An earlier version of this test tried to
// prove that by rejecting an over-long task title, but CreateTask validates
// the title (tasks.go) before it ever opens a transaction, so that path never
// reached WithTx at all — the test passed for the wrong reason, and would
// have kept passing even if the activity write moved outside the shared
// transaction entirely.
//
// This version forces the failure *after* InsertEvent has already succeeded
// inside the transaction, by giving events.Write an actor whose user id names
// nobody: the event row has no FK to users and inserts cleanly; the activity
// row does (activity.user_id NOT NULL REFERENCES users, 00013_activity.sql)
// and is rejected; the closure returns that error, and WithTx must roll back
// both. Asserting only "no activity row" would still pass if a future change
// decoupled the two writes into separate transactions, since the event would
// then survive on its own — asserting the event count too is what actually
// catches that regression.
func TestActivityFailureRollsBackTheEvent(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	ghost := uuid.New() // syntactically valid; present in no table
	const eventType = events.TypeListCreated
	before := env.eventCount(t, eventType)

	err := env.store.WithTx(ctx, func(q *db.Queries) error {
		return events.Write(ctx, q, eventType, nil,
			events.Actor{Label: "user:" + ghost.String(), UserID: ghost},
			map[string]any{"name": "Ghost list"}, nil)
	})
	if err == nil {
		t.Fatal("expected the activity insert's FK violation to fail the transaction")
	}

	if rows := env.activityRows(t, ghost); len(rows) != 0 {
		t.Fatalf("activity row survived a rolled-back transaction: %+v", rows)
	}
	if after := env.eventCount(t, eventType); after != before {
		t.Fatalf("event row survived a rolled-back transaction: %d -> %d", before, after)
	}
}

type activityRow struct {
	Action   string
	Origin   string
	Subject  *string
	ListName *string
	Detail   map[string]any
}

// activityRows returns one user's log, newest first — the same order and the
// same tiebreak the read endpoint uses, so a test that passes here is testing
// what a person will see.
func (e *testEnv) activityRows(t *testing.T, userID uuid.UUID) []activityRow {
	t.Helper()
	rows := testutil.Query(t, e.pool, `
		SELECT action, origin, subject, list_name, detail
		FROM activity WHERE user_id = $1
		ORDER BY occurred_at DESC, id DESC`, userID)
	defer rows.Close()
	var out []activityRow
	for rows.Next() {
		var r activityRow
		var raw []byte
		if err := rows.Scan(&r.Action, &r.Origin, &r.Subject, &r.ListName, &raw); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &r.Detail)
		out = append(out, r)
	}
	return out
}

// activityByAction is also what the cross-list move test in Task 3 will use
// to check each side of the deleted+created pair.
func (e *testEnv) activityByAction(t *testing.T, userID uuid.UUID, action string) []activityRow {
	t.Helper()
	var out []activityRow
	for _, r := range e.activityRows(t, userID) {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

// eventCount is also the other half of the move test in Task 3: the log
// collapsing a pair must not have changed what the sync feed emits.
func (e *testEnv) eventCount(t *testing.T, eventType string) int {
	t.Helper()
	var n int
	if err := testutil.QueryRow(t, e.pool, `SELECT count(*) FROM events WHERE type = $1`, eventType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A cross-list move is two events and one sentence. The sync feed keeps
// emitting task.deleted + task.created, because a member who can see only one
// side needs a coherent story from their side. A person reading a log does not.
func TestMoveWritesOneSentenceAndStillEmitsTwoEvents(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	from := env.mustCreateList(ctx, t, "Inbox")
	to := env.mustCreateList(ctx, t, "Work")
	task := env.mustCreateTask(ctx, t, from.ID, "Rapor")

	env.mustMoveTask(ctx, t, task.ID, to.ID)

	moved := env.activityByAction(t, env.owner.UserID, "task.moved")
	if len(moved) != 1 {
		t.Fatalf("got %d task.moved rows, want 1", len(moved))
	}
	if got := moved[0].Detail["from_list"]; got != "Inbox" {
		t.Fatalf("from_list: got %v, want Inbox", got)
	}
	if got := moved[0].Detail["to_list"]; got != "Work" {
		t.Fatalf("to_list: got %v, want Work", got)
	}
	if n := len(env.activityByAction(t, env.owner.UserID, "task.deleted")); n != 0 {
		t.Fatalf("the move leaked %d task.deleted rows into the log", n)
	}

	// The sync feed is untouched: both events must still be there, or a member
	// of only one list stops converging.
	if n := env.eventCount(t, "task.deleted"); n != 1 {
		t.Fatalf("task.deleted events: got %d, want 1", n)
	}
	if n := env.eventCount(t, "task.created"); n != 2 { // the create + the move
		t.Fatalf("task.created events: got %d, want 2", n)
	}
}

// The simple move above only ever puts two events.Write calls under the
// replaced ctx (the task's own delete+create), so a "written" flag that forgot
// to latch would still look right by accident — two calls in, one row out,
// same as if it deduped nothing and a second write just never happened to
// come. A moved task with a subtask puts four calls under the same ctx (the
// task's and the child's delete+create), which is what actually proves the
// flag is doing something: broken, this leaves 4 task.moved rows instead of 1.
func TestMoveWithChildrenStillWritesOneSentence(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	from := env.mustCreateList(ctx, t, "Inbox")
	to := env.mustCreateList(ctx, t, "Work")
	parent := env.mustCreateTask(ctx, t, from.ID, "Rapor")
	if _, err := env.domain.CreateTask(ctx, env.owner, service.TaskInput{
		ListID: from.ID, Title: "Ek", ParentID: &parent.ID,
	}); err != nil {
		t.Fatalf("create subtask: %v", err)
	}

	env.mustMoveTask(ctx, t, parent.ID, to.ID)

	moved := env.activityByAction(t, env.owner.UserID, "task.moved")
	if len(moved) != 1 {
		t.Fatalf("got %d task.moved rows, want 1", len(moved))
	}
	if got := moved[0].Detail["from_list"]; got != "Inbox" {
		t.Fatalf("from_list: got %v, want Inbox", got)
	}
	if got := moved[0].Detail["to_list"]; got != "Work" {
		t.Fatalf("to_list: got %v, want Work", got)
	}
	if n := len(env.activityByAction(t, env.owner.UserID, "task.deleted")); n != 0 {
		t.Fatalf("the move leaked %d task.deleted rows into the log", n)
	}
	// The parent's and child's own creations (before the move) are the only
	// task.created activity rows: the move's own create events are absorbed
	// into the single task.moved row above, not added to this count.
	if n := len(env.activityByAction(t, env.owner.UserID, "task.created")); n != 2 {
		t.Fatalf("got %d task.created rows, want 2 (the pre-move parent and child creates)", n)
	}

	// The sync feed carries all four: the child must travel with its parent.
	if n := env.eventCount(t, "task.deleted"); n != 2 {
		t.Fatalf("task.deleted events: got %d, want 2 (parent + child)", n)
	}
	if n := env.eventCount(t, "task.created"); n != 4 { // parent + child creates, then the same pair again on the move
		t.Fatalf("task.created events: got %d, want 4", n)
	}
}

// A drag-reorder is a real mutation and not an event in anyone's day.
func TestPositionOnlyUpdateWritesNoActivity(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	list := env.mustCreateList(ctx, t, "Work")
	a := env.mustCreateTask(ctx, t, list.ID, "A")
	env.mustCreateTask(ctx, t, list.ID, "B")
	before := len(env.activityRows(t, env.owner.UserID))

	env.mustReposition(ctx, t, a.ID, "0m")

	if after := len(env.activityRows(t, env.owner.UserID)); after != before {
		t.Fatalf("a reorder wrote %d rows", after-before)
	}
	// The event still fires — other devices must learn the new order.
	if n := env.eventCount(t, "task.updated"); n != 1 {
		t.Fatalf("task.updated events: got %d, want 1", n)
	}
}
