package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/testutil"
)

// testEnv is one migrated Postgres plus the domain service over it. Tests that
// need two users take two principals from the same env, which is what makes the
// isolation assertions meaningful — they share a database.
type testEnv struct {
	d    *Domain
	pool *testutil.Pool
	ctx  context.Context
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	pool := testutil.Postgres(t)
	store := repo.NewStore(pool)
	logger := observability.NewLogger("error")
	cfg := &config.Config{
		Env:                config.EnvDevelopment,
		PublicBaseURL:      "http://localhost:8080",
		EmailProvider:      "dev",
		MaxPATsPerUser:     25,
		MaxProjectsPerUser: 500,
		MaxWebhooksPerUser: 10,
	}
	auditor := audit.NewLogger(store, logger)
	return &testEnv{
		d:    NewDomain(store, auditor, noopSender{}, nil, nil, logger, cfg),
		pool: pool,
		ctx:  context.Background(),
	}
}

// newUser registers a bare account and returns a full-scope session principal
// for it. It goes straight to the store rather than through Auth.Register so a
// domain test is not also testing password hashing and email delivery.
func (e *testEnv) newUser(t *testing.T) *auth.Principal {
	t.Helper()
	id := uuid.New()
	u, err := e.d.store.CreateUser(e.ctx, db.CreateUserParams{
		ID:           id,
		Email:        fmt.Sprintf("user-%s@example.com", uuid.NewString()),
		PasswordHash: "x",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return &auth.Principal{
		UserID: u.ID, Email: u.Email, EmailVerified: true,
		ViaSession: true, Scope: auth.ScopeReadWrite,
	}
}

func (e *testEnv) mustProject(t *testing.T, p *auth.Principal, name string) db.Project {
	t.Helper()
	proj, err := e.d.CreateProject(e.ctx, p, CreateProjectInput{Name: name, Currency: "TRY", Billable: true})
	if err != nil {
		t.Fatalf("create project %q: %v", name, err)
	}
	return proj
}

// at parses a wall-clock time on a fixed reference day, in UTC. Tests read
// better as "10:00" than as a full RFC 3339 string, and every assertion in this
// package is about relative position within a day.
func at(clock string) time.Time {
	ts, err := time.Parse(time.RFC3339, "2026-03-01T"+clock+":00Z")
	if err != nil {
		panic(err)
	}
	return ts
}

func TestCreateProjectRejectsDuplicateName(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)

	if _, err := e.d.CreateProject(e.ctx, p, CreateProjectInput{Name: "capsarsiv", Currency: "TRY"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := e.d.CreateProject(e.ctx, p, CreateProjectInput{Name: "CAPSARSIV", Currency: "TRY"})
	if err == nil {
		t.Fatal("a name that differs only in case must be rejected")
	}
	var de *Error
	if !asError(err, &de) || de.Status != 409 {
		t.Fatalf("want 409 conflict, got %v", err)
	}
}

// A project belonging to someone else must be indistinguishable from one that
// does not exist. A 403 would confirm the id is real.
func TestProjectOfAnotherUserReadsAsNotFound(t *testing.T) {
	e := newTestEnv(t)
	alice, bob := e.newUser(t), e.newUser(t)
	proj := e.mustProject(t, alice, "gizli")

	_, err := e.d.GetProject(e.ctx, bob, proj.ID)
	if err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUpdateProjectClearsRateOnRequest(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	rate := int64(33333)
	proj, err := e.d.CreateProject(e.ctx, p, CreateProjectInput{
		Name: "rated", Currency: "TRY", Billable: true, HourlyRateCents: &rate,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if proj.HourlyRateCents == nil || *proj.HourlyRateCents != rate {
		t.Fatalf("rate not stored: %v", proj.HourlyRateCents)
	}

	// A nil pointer means "leave it alone", so clearing needs its own flag.
	updated, err := e.d.UpdateProject(e.ctx, p, proj.ID, UpdateProjectInput{ClearRate: true})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.HourlyRateCents != nil {
		t.Fatalf("rate should be cleared, got %v", *updated.HourlyRateCents)
	}
}

func TestCreateProjectRejectsUnknownColour(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	if _, err := e.d.CreateProject(e.ctx, p, CreateProjectInput{
		Name: "x", Currency: "TRY", Color: "#3d9aff",
	}); err == nil {
		t.Fatal("a hex value is not a palette name and must be rejected")
	}
}

func TestLinkRepoValidatesFullName(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "x")

	for _, bad := range []string{"capsarsiv", "a/b/c", "", "https://github.com/a/b", "a b/c"} {
		if _, err := e.d.LinkRepo(e.ctx, p, proj.ID, bad); err == nil {
			t.Errorf("should have been rejected: %q", bad)
		}
	}
	r, err := e.d.LinkRepo(e.ctx, p, proj.ID, "cobanov/capsarsiv")
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if r.FullName != "cobanov/capsarsiv" {
		t.Fatalf("full_name = %q", r.FullName)
	}
}

// A repository can serve two projects: client work and an internal tool may live
// in the same monorepo. Which session a commit lands in is decided by time.
func TestSameRepoCanBeLinkedToTwoProjects(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	a, b := e.mustProject(t, p, "a"), e.mustProject(t, p, "b")

	if _, err := e.d.LinkRepo(e.ctx, p, a.ID, "cobanov/x"); err != nil {
		t.Fatalf("link a: %v", err)
	}
	if _, err := e.d.LinkRepo(e.ctx, p, b.ID, "cobanov/x"); err != nil {
		t.Fatalf("link b: %v", err)
	}
}

// Linking the same repository twice is the desired end state, not an error.
func TestLinkRepoIsIdempotent(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "x")

	first, err := e.d.LinkRepo(e.ctx, p, proj.ID, "cobanov/x")
	if err != nil {
		t.Fatalf("first link: %v", err)
	}
	again, err := e.d.LinkRepo(e.ctx, p, proj.ID, "cobanov/x")
	if err != nil {
		t.Fatalf("second link: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("second link created a new row: %s != %s", again.ID, first.ID)
	}
}

// asError is errors.As without importing errors into every test file.
func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
