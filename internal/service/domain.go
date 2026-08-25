package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/email"
	"github.com/cobanov/punchcard/internal/events"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/webhooks"
)

// Roles.
const (
	RoleOwner  = "owner"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// actorOf packages the acting principal for the event and activity trails.
func actorOf(p *auth.Principal) events.Actor {
	label := "user:" + p.UserID.String()
	if p.TokenID != nil {
		label = "token:" + p.TokenID.String()
	}
	return events.Actor{Label: label, UserID: p.UserID}
}

func roleRank(r string) int {
	switch r {
	case RoleViewer:
		return 1
	case RoleEditor:
		return 2
	case RoleOwner:
		return 3
	default:
		return 0
	}
}

// Domain is the service for lists, memberships, invites, and tasks. It is the
// single place list/task authorization decisions are made.
type Domain struct {
	store  *repo.Store
	audit  *audit.Logger
	email  email.Sender
	cipher *webhooks.Cipher // nil when webhook encryption is not configured
	log    *slog.Logger
	cfg    *config.Config
}

// NewDomain builds the domain service. cipher may be nil (webhooks disabled).
func NewDomain(store *repo.Store, auditor *audit.Logger, sender email.Sender, cipher *webhooks.Cipher, log *slog.Logger, cfg *config.Config) *Domain {
	return &Domain{store: store, audit: auditor, email: sender, cipher: cipher, log: log, cfg: cfg}
}

// authorizeList resolves the acting principal's role on a list and enforces the
// minimum required role and (for mutations) write scope. It returns ErrNotFound
// for non-members and out-of-token-scope lists so existence is never leaked.
func (d *Domain) authorizeList(ctx context.Context, p *auth.Principal, listID uuid.UUID, minRole string, write bool) (string, error) {
	if !p.AllowsList(listID) {
		return "", ErrNotFound
	}
	if write && !p.CanWrite() {
		return "", ErrInsufficientScope
	}
	role, err := d.store.GetListMembership(ctx, db.GetListMembershipParams{ListID: listID, UserID: p.UserID})
	if err != nil {
		if repo.IsNotFound(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("get membership: %w", err)
	}
	if roleRank(role) < roleRank(minRole) {
		return "", ErrInsufficientRole
	}
	return role, nil
}

// authorizeTask loads a task scoped by membership and enforces role + scope.
// includeDeleted controls whether a soft-deleted task is considered present.
func (d *Domain) authorizeTask(ctx context.Context, p *auth.Principal, taskID uuid.UUID, minRole string, write, includeDeleted bool) (db.Task, string, error) {
	row, err := d.store.GetTaskForUser(ctx, db.GetTaskForUserParams{ID: taskID, UserID: p.UserID})
	if err != nil {
		if repo.IsNotFound(err) {
			return db.Task{}, "", ErrNotFound
		}
		return db.Task{}, "", fmt.Errorf("get task: %w", err)
	}
	if row.Task.DeletedAt != nil && !includeDeleted {
		return db.Task{}, "", ErrNotFound
	}
	if !p.AllowsList(row.Task.ListID) {
		return db.Task{}, "", ErrNotFound
	}
	if write && !p.CanWrite() {
		return db.Task{}, "", ErrInsufficientScope
	}
	if roleRank(row.Role) < roleRank(minRole) {
		return db.Task{}, "", ErrInsufficientRole
	}
	return row.Task, row.Role, nil
}

// quotaLimit converts a config quota (a small non-negative int) to the int32 a
// sqlc LIMIT parameter expects, clamping defensively so the conversion can never
// overflow (gosec G115).
func quotaLimit(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(n)
	}
}
