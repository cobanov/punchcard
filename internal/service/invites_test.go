package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/testutil"
)

func newTestAuthDomain(t *testing.T) (*Auth, *Domain, *repo.Store) {
	t.Helper()
	pool := testutil.Postgres(t)
	store := repo.NewStore(pool)
	logger := observability.NewLogger("error")
	cfg := &config.Config{
		Env:               config.EnvDevelopment,
		PublicBaseURL:     "http://localhost:8080",
		EmailProvider:     "dev",
		MaxPATsPerUser:    25,
		MaxListsPerUser:   200,
		MaxMembersPerList: 50,
		MaxInvitesPerUser: 100,
	}
	auditor := audit.NewLogger(store, logger)
	a := NewAuth(store, noopSender{}, auditor, logger, cfg)
	d := NewDomain(store, auditor, noopSender{}, nil, logger, cfg)
	return a, d, store
}

// registerPrincipal registers a user and returns a read_write session principal,
// optionally marking the account's email verified.
func registerPrincipal(t *testing.T, a *Auth, store *repo.Store, emailAddr string, verify bool) *auth.Principal {
	t.Helper()
	ctx := context.Background()
	u, _, err := a.Register(ctx, emailAddr, "supersecret123", "1.2.3.4", "test")
	if err != nil {
		t.Fatalf("register %s: %v", emailAddr, err)
	}
	if verify {
		if err := store.MarkEmailVerified(ctx, u.ID); err != nil {
			t.Fatalf("verify %s: %v", emailAddr, err)
		}
	}
	return &auth.Principal{
		UserID: u.ID, Email: u.Email, EmailVerified: verify,
		ViaSession: true, Scope: auth.ScopeReadWrite,
	}
}

// TestAcceptInviteRequiresVerifiedEmail locks in the policy parity between
// CreateInvite and AcceptInvite (ATD-32): joining a list requires a verified
// email, just like sending an invite does.
func TestAcceptInviteRequiresVerifiedEmail(t *testing.T) {
	a, d, store := newTestAuthDomain(t)
	ctx := context.Background()

	owner := registerPrincipal(t, a, store, "owner-inv@example.com", true)
	invitee := registerPrincipal(t, a, store, "invitee-inv@example.com", false)

	list, err := d.CreateList(ctx, owner, "Team", nil)
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	inv, err := d.CreateInvite(ctx, owner, list.ID, invitee.Email, "editor", "1.2.3.4")
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	// Unverified invitee cannot accept.
	if err := d.AcceptInvite(ctx, invitee, inv.ID, "1.2.3.4"); !errors.Is(err, ErrEmailNotVerified) {
		t.Fatalf("AcceptInvite (unverified) = %v, want ErrEmailNotVerified", err)
	}

	// After verifying, the invitee can accept and becomes an editor member.
	if err := store.MarkEmailVerified(ctx, invitee.UserID); err != nil {
		t.Fatalf("verify invitee: %v", err)
	}
	invitee.EmailVerified = true
	if err := d.AcceptInvite(ctx, invitee, inv.ID, "1.2.3.4"); err != nil {
		t.Fatalf("AcceptInvite (verified): %v", err)
	}
	role, err := store.GetListMembership(ctx, db.GetListMembershipParams{ListID: list.ID, UserID: invitee.UserID})
	if err != nil {
		t.Fatalf("membership after accept: %v", err)
	}
	if role != "editor" {
		t.Fatalf("member role = %q, want editor", role)
	}
}

// tokenPrincipal derives a PAT principal from a session principal: same user,
// but authenticated by token, with the given scope and list restriction.
func tokenPrincipal(p *auth.Principal, scope string, lists []uuid.UUID) *auth.Principal {
	id := uuid.New()
	tok := *p
	tok.ViaSession = false
	tok.SessionID = nil
	tok.TokenID = &id
	tok.Scope = scope
	tok.ScopedListIDs = lists
	return &tok
}

// TestInviteResponseEnforcesTokenScope covers the token plane on accept/decline.
// Both write — accepting inserts a membership row, declining deletes the invite
// — but neither consulted the principal's scope, so a read-only token could do
// both. A list-scoped token is refused too: the invite is by definition to a
// list outside the token's allow-list, and accepting it would widen the token's
// own reach.
func TestInviteResponseEnforcesTokenScope(t *testing.T) {
	a, d, store := newTestAuthDomain(t)
	ctx := context.Background()
	const ip = "1.2.3.4"

	owner := registerPrincipal(t, a, store, "owner-tokscope@example.com", true)
	invitee := registerPrincipal(t, a, store, "invitee-tokscope@example.com", true)

	list, err := d.CreateList(ctx, owner, "Team", nil)
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	inv, err := d.CreateInvite(ctx, owner, list.ID, invitee.Email, "editor", ip)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	readOnly := tokenPrincipal(invitee, auth.ScopeRead, nil)
	if err := d.AcceptInvite(ctx, readOnly, inv.ID, ip); !errors.Is(err, ErrInsufficientScope) {
		t.Fatalf("AcceptInvite (read scope) = %v, want ErrInsufficientScope", err)
	}
	if err := d.DeclineInvite(ctx, readOnly, inv.ID, ip); !errors.Is(err, ErrInsufficientScope) {
		t.Fatalf("DeclineInvite (read scope) = %v, want ErrInsufficientScope", err)
	}

	// read_write, but narrowed to a list of the invitee's own.
	own, err := d.CreateList(ctx, invitee, "Mine", nil)
	if err != nil {
		t.Fatalf("create own list: %v", err)
	}
	narrowed := tokenPrincipal(invitee, auth.ScopeReadWrite, []uuid.UUID{own.ID})
	if err := d.AcceptInvite(ctx, narrowed, inv.ID, ip); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AcceptInvite (list-scoped) = %v, want ErrNotFound", err)
	}

	// Every rejection left the invite pending and granted no membership.
	if _, err := store.GetListMembership(ctx, db.GetListMembershipParams{
		ListID: list.ID, UserID: invitee.UserID,
	}); !repo.IsNotFound(err) {
		t.Fatalf("membership after rejected accepts = %v, want none", err)
	}
	if err := d.AcceptInvite(ctx, invitee, inv.ID, ip); err != nil {
		t.Fatalf("AcceptInvite (session): %v", err)
	}
	role, err := store.GetListMembership(ctx, db.GetListMembershipParams{
		ListID: list.ID, UserID: invitee.UserID,
	})
	if err != nil {
		t.Fatalf("membership after accept: %v", err)
	}
	if role != "editor" {
		t.Fatalf("member role = %q, want editor", role)
	}
}
