package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/email"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
)

const inviteTTL = 7 * 24 * time.Hour

// CreateInvite invites an email to a list (owner only, verified email required).
func (d *Domain) CreateInvite(ctx context.Context, p *auth.Principal, listID uuid.UUID, emailAddr, role, ip string) (db.Invite, error) {
	if _, err := d.authorizeList(ctx, p, listID, RoleOwner, true); err != nil {
		return db.Invite{}, err
	}
	if !p.EmailVerified {
		return db.Invite{}, ErrEmailNotVerified
	}
	addr, err := normalizeEmail(emailAddr)
	if err != nil {
		return db.Invite{}, err
	}
	if !validRole(role) {
		return db.Invite{}, NewError(422, "validation_failed", "role must be owner|editor|viewer")
	}
	n, err := d.store.CountPendingInvitesByInviter(ctx, &p.UserID)
	if err != nil {
		return db.Invite{}, fmt.Errorf("count invites: %w", err)
	}
	if int(n) >= d.cfg.MaxInvitesPerUser {
		return db.Invite{}, ErrQuotaExceeded
	}
	token, err := auth.GenerateSecret(32)
	if err != nil {
		return db.Invite{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return db.Invite{}, err
	}
	inv, err := d.store.CreateInvite(ctx, db.CreateInviteParams{
		ID: id, ListID: listID, Email: addr, Role: role,
		TokenHash: auth.HashToken(token), InvitedBy: &p.UserID, ExpiresAt: time.Now().UTC().Add(inviteTTL),
	})
	if err != nil {
		return db.Invite{}, fmt.Errorf("create invite: %w", err)
	}
	sendAsync(ctx, d.email, d.log, email.Message{
		To:      addr,
		Subject: "You've been invited to a list on punchcard",
		Text: fmt.Sprintf("You've been invited to collaborate on a list.\n\nAccept at:\n%s/invite?token=%s\n\n"+
			"If you don't have an account, sign up with this email and the invite will appear under your pending invites.\n\nThis invite expires in 7 days.",
			d.cfg.PublicBaseURL, token),
	})
	d.audit.Record(ctx, &p.UserID, audit.ActionInviteChange, ip, map[string]any{"list_id": listID, "email": addr, "role": role, "action": "create"})
	return inv, nil
}

// ListInvites returns pending invites for a list (owner only).
func (d *Domain) ListInvites(ctx context.Context, p *auth.Principal, listID uuid.UUID) ([]db.Invite, error) {
	if _, err := d.authorizeList(ctx, p, listID, RoleOwner, false); err != nil {
		return nil, err
	}
	invites, err := d.store.ListInvitesForList(ctx, listID)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	return invites, nil
}

// CancelInvite deletes a pending invite (owner only).
func (d *Domain) CancelInvite(ctx context.Context, p *auth.Principal, listID, inviteID uuid.UUID, ip string) error {
	if _, err := d.authorizeList(ctx, p, listID, RoleOwner, true); err != nil {
		return err
	}
	n, err := d.store.DeleteInvite(ctx, db.DeleteInviteParams{ID: inviteID, ListID: listID})
	if err != nil {
		return fmt.Errorf("delete invite: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	d.audit.Record(ctx, &p.UserID, audit.ActionInviteChange, ip, map[string]any{"list_id": listID, "invite_id": inviteID, "action": "cancel"})
	return nil
}

// ListMyInvites returns invites pending for the principal's email.
func (d *Domain) ListMyInvites(ctx context.Context, p *auth.Principal) ([]db.ListPendingInvitesForEmailRow, error) {
	rows, err := d.store.ListPendingInvitesForEmail(ctx, p.Email)
	if err != nil {
		return nil, fmt.Errorf("list my invites: %w", err)
	}
	return rows, nil
}

// AcceptInvite adds the principal to the invited list.
//
// Accepting writes a membership row, so it needs a read_write credential like
// any other mutation — routing the write through /v1/me rather than a list
// endpoint does not exempt it from the token's scope.
func (d *Domain) AcceptInvite(ctx context.Context, p *auth.Principal, inviteID uuid.UUID, ip string) error {
	if !p.CanWrite() {
		return ErrInsufficientScope
	}
	// Match CreateInvite: a verified email is required to join a list.
	if !p.EmailVerified {
		return ErrEmailNotVerified
	}
	inv, err := d.getOwnInvite(ctx, p, inviteID)
	if err != nil {
		return err
	}
	// A list-scoped token must not be able to widen its own reach by joining a
	// list outside that scope. In practice this rejects every list-scoped token:
	// the invite is by definition to a list the user is not yet a member of, so
	// it can never already be in the token's allow-list. Accepting is a
	// session's job.
	if !p.AllowsList(inv.ListID) {
		return ErrNotFound
	}
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		if e := q.AddMember(ctx, db.AddMemberParams{ListID: inv.ListID, UserID: p.UserID, Role: inv.Role}); e != nil {
			return e
		}
		n, e := q.MarkInviteAccepted(ctx, inv.ID)
		if e != nil {
			return e
		}
		if n == 0 {
			return NewError(409, "invite_used", "invite already accepted")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("accept invite: %w", err)
	}
	d.audit.Record(ctx, &p.UserID, audit.ActionInviteChange, ip, map[string]any{"list_id": inv.ListID, "invite_id": inviteID, "action": "accept"})
	return nil
}

// DeclineInvite removes a pending invite addressed to the principal.
//
// Declining destroys a row, so it carries the same scope requirements as
// accepting (see AcceptInvite) — a read-scope token must not be able to burn an
// invite the account has not seen yet.
func (d *Domain) DeclineInvite(ctx context.Context, p *auth.Principal, inviteID uuid.UUID, ip string) error {
	if !p.CanWrite() {
		return ErrInsufficientScope
	}
	inv, err := d.getOwnInvite(ctx, p, inviteID)
	if err != nil {
		return err
	}
	if !p.AllowsList(inv.ListID) {
		return ErrNotFound
	}
	if _, err := d.store.DeleteInvite(ctx, db.DeleteInviteParams{ID: inv.ID, ListID: inv.ListID}); err != nil {
		return fmt.Errorf("decline invite: %w", err)
	}
	return nil
}

func (d *Domain) getOwnInvite(ctx context.Context, p *auth.Principal, inviteID uuid.UUID) (db.Invite, error) {
	inv, err := d.store.GetInvite(ctx, inviteID)
	if err != nil {
		if repo.IsNotFound(err) {
			return db.Invite{}, ErrNotFound
		}
		return db.Invite{}, fmt.Errorf("get invite: %w", err)
	}
	if !strings.EqualFold(inv.Email, p.Email) {
		return db.Invite{}, ErrNotFound // not addressed to this user; hide existence
	}
	if inv.AcceptedAt != nil {
		return db.Invite{}, NewError(409, "invite_used", "invite already accepted")
	}
	if inv.ExpiresAt.Before(time.Now()) {
		return db.Invite{}, NewError(409, "invite_expired", "invite has expired")
	}
	return inv, nil
}
