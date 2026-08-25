package service

import (
	"context"
	"log/slog"
	"math"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/email"
	"github.com/cobanov/punchcard/internal/events"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/webhooks"
)

// actorOf packages the acting principal for the event trail.
func actorOf(p *auth.Principal) events.Actor {
	label := "user:" + p.UserID.String()
	if p.TokenID != nil {
		label = "token:" + p.TokenID.String()
	}
	return events.Actor{Label: label, UserID: p.UserID}
}

// Domain is the service for projects, work sessions, reports and the GitHub
// link. It is the single place authorization decisions about a user's own data
// are made.
//
// punchcard has no sharing: every project has exactly one owner, so there are
// no roles and no membership table to consult. Authorization is therefore a
// single question — is this row the caller's? — answered by scoping every query
// on owner_id/user_id rather than by fetching a row and then checking it. A
// project that is not yours reads as 404, never 403: a 403 would confirm that a
// project with that id exists.
type Domain struct {
	store  *repo.Store
	audit  *audit.Logger
	email  email.Sender
	cipher *webhooks.Cipher // nil when webhook encryption is not configured
	github *GitHubCipher    // nil when GITHUB_TOKEN_KEY is not set
	log    *slog.Logger
	cfg    *config.Config
}

// NewDomain builds the domain service. cipher and ghCipher may be nil when the
// corresponding feature is not configured.
func NewDomain(store *repo.Store, auditor *audit.Logger, sender email.Sender, cipher *webhooks.Cipher, ghCipher *GitHubCipher, log *slog.Logger, cfg *config.Config) *Domain {
	return &Domain{store: store, audit: auditor, email: sender, cipher: cipher, github: ghCipher, log: log, cfg: cfg}
}

// authorizeProject enforces that the project belongs to the caller and, for
// mutations, that the token carries write scope. It returns ErrNotFound rather
// than ErrForbidden for someone else's project so existence is never leaked.
func (d *Domain) authorizeProject(ctx context.Context, p *auth.Principal, projectID uuid.UUID, write bool) error {
	if !p.AllowsProject(projectID) {
		return ErrNotFound
	}
	if write && !p.CanWrite() {
		return ErrForbidden
	}
	if _, err := d.store.GetProjectForUser(ctx, projectIDParams(projectID, p.UserID)); err != nil {
		return mapNotFound(err)
	}
	return nil
}

// Paging bounds shared by the list endpoints.
const (
	defaultLimit = 50
	maxLimit     = 200
)

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

// isNoRows reports whether err is "no rows", the shape a sqlc :one query
// returns when nothing matched.
func isNoRows(err error) bool { return repo.IsNotFound(err) }
