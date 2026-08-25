package service

import (
	"context"
	"testing"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/email"
	"github.com/cobanov/punchcard/internal/oauth"
	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/testutil"
)

type noopSender struct{}

func (noopSender) Send(context.Context, email.Message) error { return nil }

func newTestAuth(t *testing.T) *Auth {
	t.Helper()
	pool := testutil.Postgres(t)
	store := repo.NewStore(pool)
	logger := observability.NewLogger("error")
	cfg := &config.Config{
		Env:            config.EnvDevelopment,
		PublicBaseURL:  "http://localhost:8080",
		EmailProvider:  "dev",
		MaxPATsPerUser: 25,
	}
	auditor := audit.NewLogger(store, logger)
	return NewAuth(store, noopSender{}, auditor, logger, cfg)
}

func TestLoginOAuth_CreateThenMatch(t *testing.T) {
	a := newTestAuth(t)
	ctx := context.Background()
	id := oauth.Identity{
		Provider: oauth.ProviderGoogle, ProviderUserID: "google-123",
		Email: "New.User@Example.com", Name: "New User", EmailVerified: true,
	}

	user, sess, err := a.LoginOAuth(ctx, id, "1.2.3.4", "test-agent")
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if sess.Token == "" {
		t.Fatal("expected a session token")
	}
	if user.Email != "new.user@example.com" {
		t.Errorf("email not normalized: %q", user.Email)
	}
	if user.GoogleSub == nil || *user.GoogleSub != "google-123" {
		t.Errorf("google_sub not set: %v", user.GoogleSub)
	}
	if user.EmailVerifiedAt == nil {
		t.Error("provider-verified email should be marked verified")
	}
	// A default project should have been created: without one the account
	// cannot start a timer at all.
	projects, err := a.store.ListProjects(ctx, db.ListProjectsParams{OwnerID: user.ID, IncludeArchived: true})
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 || projects[0].Name != "General" {
		t.Fatalf("expected one default project named General, got %+v", projects)
	}

	// Second login with the same identity must reuse the account, not duplicate.
	user2, _, err := a.LoginOAuth(ctx, id, "1.2.3.4", "test-agent")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if user2.ID != user.ID {
		t.Fatalf("second login created a new user: %s != %s", user2.ID, user.ID)
	}
}

func TestLoginOAuth_LinksExistingEmail(t *testing.T) {
	a := newTestAuth(t)
	ctx := context.Background()

	// Existing password account.
	existing, _, err := a.Register(ctx, "linkme@example.com", "supersecret123", "1.1.1.1", "ua")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// GitHub login for the same (verified) email links, not creates.
	id := oauth.Identity{
		Provider: oauth.ProviderGitHub, ProviderUserID: "gh-999",
		Email: "linkme@example.com", Name: "Linker", EmailVerified: true,
	}
	linked, _, err := a.LoginOAuth(ctx, id, "1.1.1.1", "ua")
	if err != nil {
		t.Fatalf("oauth login: %v", err)
	}
	if linked.ID != existing.ID {
		t.Fatalf("linked to a different account: %s != %s", linked.ID, existing.ID)
	}
	if linked.GithubID == nil || *linked.GithubID != "gh-999" {
		t.Errorf("github_id not linked: %v", linked.GithubID)
	}
}

func TestLoginOAuth_RejectsUnverified(t *testing.T) {
	a := newTestAuth(t)
	ctx := context.Background()
	id := oauth.Identity{
		Provider: oauth.ProviderGoogle, ProviderUserID: "g-unverified",
		Email: "spoof@example.com", Name: "Spoof", EmailVerified: false,
	}
	if _, _, err := a.LoginOAuth(ctx, id, "9.9.9.9", "ua"); err != ErrOAuthUnverified {
		t.Fatalf("expected ErrOAuthUnverified, got %v", err)
	}
}
