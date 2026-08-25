package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/events"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
)

func validRole(r string) bool { return r == RoleOwner || r == RoleEditor || r == RoleViewer }

// ListMembers returns a list's members (any member may read).
func (d *Domain) ListMembers(ctx context.Context, p *auth.Principal, listID uuid.UUID) ([]db.ListMembersRow, error) {
	if _, err := d.authorizeList(ctx, p, listID, RoleViewer, false); err != nil {
		return nil, err
	}
	rows, err := d.store.ListMembers(ctx, db.ListMembersParams{
		ListID: listID, Lim: quotaLimit(d.cfg.MaxMembersPerList),
	})
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	return rows, nil
}

// AddMember adds an existing user to a list by id (owner only).
func (d *Domain) AddMember(ctx context.Context, p *auth.Principal, listID, userID uuid.UUID, role, ip string) error {
	if _, err := d.authorizeList(ctx, p, listID, RoleOwner, true); err != nil {
		return err
	}
	if !validRole(role) {
		return NewError(422, "validation_failed", "role must be owner|editor|viewer")
	}
	n, err := d.store.CountMembers(ctx, listID)
	if err != nil {
		return fmt.Errorf("count members: %w", err)
	}
	if int(n) >= d.cfg.MaxMembersPerList {
		return ErrQuotaExceeded
	}
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		if e := q.AddMember(ctx, db.AddMemberParams{ListID: listID, UserID: userID, Role: role}); e != nil {
			return e
		}
		return events.Write(ctx, q, events.TypeMemberAdded, &listID, actorOf(p),
			map[string]any{"list_id": listID, "user_id": userID, "role": role}, nil)
	})
	if err != nil {
		if repo.IsForeignKeyViolation(err) {
			return NewError(422, "validation_failed", "user not found")
		}
		return fmt.Errorf("add member: %w", err)
	}
	d.audit.Record(ctx, &p.UserID, audit.ActionMemberChange, ip, map[string]any{"list_id": listID, "user_id": userID, "role": role, "action": "add"})
	return nil
}

// UpdateMemberRole changes a member's role (owner only; the founding owner is
// immutable).
func (d *Domain) UpdateMemberRole(ctx context.Context, p *auth.Principal, listID, userID uuid.UUID, role, ip string) error {
	if _, err := d.authorizeList(ctx, p, listID, RoleOwner, true); err != nil {
		return err
	}
	if !validRole(role) {
		return NewError(422, "validation_failed", "role must be owner|editor|viewer")
	}
	list, err := d.store.GetListForUser(ctx, db.GetListForUserParams{ID: listID, UserID: p.UserID})
	if err != nil {
		return fmt.Errorf("get list: %w", err)
	}
	if list.List.OwnerID == userID {
		return NewError(409, "owner_immutable", "the founding owner's role cannot be changed")
	}
	n, err := d.store.UpdateMemberRole(ctx, db.UpdateMemberRoleParams{ListID: listID, UserID: userID, Role: role})
	if err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	d.audit.Record(ctx, &p.UserID, audit.ActionMemberChange, ip, map[string]any{"list_id": listID, "user_id": userID, "role": role, "action": "update"})
	return nil
}

// RemoveMember removes a member. Owners may remove anyone but the founding
// owner; any member may remove themselves.
func (d *Domain) RemoveMember(ctx context.Context, p *auth.Principal, listID, userID uuid.UUID, ip string) error {
	if !p.AllowsList(listID) {
		return ErrNotFound
	}
	if !p.CanWrite() {
		return ErrInsufficientScope
	}
	list, err := d.store.GetListForUser(ctx, db.GetListForUserParams{ID: listID, UserID: p.UserID})
	if err != nil {
		if repo.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("get list: %w", err)
	}
	if list.List.OwnerID == userID {
		return NewError(409, "owner_immutable", "the founding owner cannot be removed; transfer or delete the list")
	}
	selfRemoval := p.UserID == userID
	if !selfRemoval && list.Role != RoleOwner {
		return ErrInsufficientRole
	}
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		n, e := q.RemoveMember(ctx, db.RemoveMemberParams{ListID: listID, UserID: userID})
		if e != nil {
			return e
		}
		if n == 0 {
			return ErrNotFound
		}
		return events.Write(ctx, q, events.TypeMemberRemoved, &listID, actorOf(p),
			map[string]any{"list_id": listID, "user_id": userID}, nil)
	})
	if err != nil {
		return err
	}
	d.audit.Record(ctx, &p.UserID, audit.ActionMemberChange, ip, map[string]any{"list_id": listID, "user_id": userID, "action": "remove"})
	return nil
}
