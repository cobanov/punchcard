package service

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/events"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// The event payload IS the feed: a field missing from here is a field the other
// devices never learn about, however correct the write was. `color` is a
// pointer, so it serialises as null when unset rather than being dropped —
// a client has to be able to tell "cleared" from "not mentioned".
func listResource(l db.List) map[string]any {
	return map[string]any{
		"id": l.ID, "name": l.Name, "owner_id": l.OwnerID, "is_personal": l.IsPersonal,
		"color": l.Color, "created_at": l.CreatedAt, "updated_at": l.UpdatedAt,
	}
}

// ListWithRole pairs a list with the acting user's role on it.
type ListWithRole struct {
	List db.List
	Role string
}

// createListTx creates a list and its owner membership in one transaction.
// clientID, when non-nil, is a caller-supplied UUIDv7 (offline sync); nil
// generates one server-side.
func createListTx(ctx context.Context, q *db.Queries, name string, ownerID uuid.UUID, personal bool, clientID *uuid.UUID) (db.List, error) {
	var id uuid.UUID
	if clientID != nil {
		id = *clientID
	} else {
		var err error
		id, err = uuid.NewV7()
		if err != nil {
			return db.List{}, err
		}
	}
	list, err := q.CreateList(ctx, db.CreateListParams{ID: id, Name: name, OwnerID: ownerID, IsPersonal: personal})
	if err != nil {
		return db.List{}, err
	}
	if err := q.AddMember(ctx, db.AddMemberParams{ListID: list.ID, UserID: ownerID, Role: RoleOwner}); err != nil {
		return db.List{}, err
	}
	return list, nil
}

// CreateList creates a shared list owned by the principal. clientID, when
// non-nil, is an offline client's pre-generated UUIDv7 (docs/offline-sync.md).
func (d *Domain) CreateList(ctx context.Context, p *auth.Principal, name string, clientID *uuid.UUID) (db.List, error) {
	if !p.CanWrite() {
		return db.List{}, ErrInsufficientScope
	}
	if p.TokenScopedToLists() {
		return db.List{}, ErrInsufficientScope // a list-scoped token cannot create new lists
	}
	if clientID != nil {
		if err := validateClientID(*clientID); err != nil {
			return db.List{}, err
		}
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return db.List{}, NewError(422, "validation_failed", "list name must be 1-200 characters")
	}
	count, err := d.store.CountListsForUser(ctx, p.UserID)
	if err != nil {
		return db.List{}, fmt.Errorf("count lists: %w", err)
	}
	if int(count) >= d.cfg.MaxListsPerUser {
		return db.List{}, ErrQuotaExceeded
	}

	var list db.List
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		l, e := createListTx(ctx, q, name, p.UserID, false, clientID)
		if e != nil {
			return e
		}
		list = l
		return events.Write(ctx, q, events.TypeListCreated, &l.ID, actorOf(p), listResource(l), nil)
	})
	if err != nil {
		if repo.IsUniqueViolation(err) {
			return db.List{}, NewError(409, "id_conflict", "a list with this id already exists")
		}
		return db.List{}, fmt.Errorf("create list: %w", err)
	}
	return list, nil
}

// ListLists returns every list the principal can access, narrowed to the
// token's list scope when applicable.
func (d *Domain) ListLists(ctx context.Context, p *auth.Principal) ([]ListWithRole, error) {
	rows, err := d.store.ListListsForUser(ctx, db.ListListsForUserParams{
		UserID: p.UserID, Lim: quotaLimit(d.cfg.MaxListsPerUser),
	})
	if err != nil {
		return nil, fmt.Errorf("list lists: %w", err)
	}
	out := make([]ListWithRole, 0, len(rows))
	for _, r := range rows {
		if !p.AllowsList(r.List.ID) {
			continue
		}
		out = append(out, ListWithRole{List: r.List, Role: r.Role})
	}
	return out, nil
}

// GetList returns a list the principal can read.
func (d *Domain) GetList(ctx context.Context, p *auth.Principal, id uuid.UUID) (db.List, string, error) {
	role, err := d.authorizeList(ctx, p, id, RoleViewer, false)
	if err != nil {
		return db.List{}, "", err
	}
	row, err := d.store.GetListForUser(ctx, db.GetListForUserParams{ID: id, UserID: p.UserID})
	if err != nil {
		return db.List{}, "", fmt.Errorf("get list: %w", err)
	}
	return row.List, role, nil
}

// ListColors is the set a list's colour may take, and the only place the set is
// written down on this side of the wire. The database CHECK in 00012 mirrors it
// because the column is reachable from HTTP and from MCP, and a constraint that
// only exists in Go is a constraint an agent can talk its way around.
var ListColors = []string{"red", "amber", "green", "teal", "blue", "violet", "pink", "slate"}

func validColor(c string) bool {
	return slices.Contains(ListColors, c)
}

// UpdateList renames a list and/or sets its colour (owner only).
//
// Both arguments are optional and mean "leave this alone" when nil, so the two
// edits share one statement, one transaction and one event — a rename that also
// silently cleared the colour would be a data loss nobody asked for. Clearing
// the colour is an explicit empty string; see the query for why the two cannot
// both be NULL.
func (d *Domain) UpdateList(ctx context.Context, p *auth.Principal, id uuid.UUID, name *string, color *string) (db.List, error) {
	if _, err := d.authorizeList(ctx, p, id, RoleOwner, true); err != nil {
		return db.List{}, err
	}
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" || len(trimmed) > 200 {
			return db.List{}, NewError(422, "validation_failed", "list name must be 1-200 characters")
		}
		name = &trimmed
	}
	if color != nil && *color != "" && !validColor(*color) {
		return db.List{}, NewError(422, "validation_failed",
			"list color must be one of "+strings.Join(ListColors, ", ")+", or empty to clear it")
	}
	if name == nil && color == nil {
		return db.List{}, NewError(422, "validation_failed", "nothing to update: give a name, a color, or both")
	}
	var list db.List
	err := d.store.WithTx(ctx, func(q *db.Queries) error {
		l, e := q.UpdateList(ctx, db.UpdateListParams{ID: id, Name: name, Color: color})
		if e != nil {
			return e
		}
		list = l
		return events.Write(ctx, q, events.TypeListUpdated, &id, actorOf(p), listResource(l), nil)
	})
	if err != nil {
		return db.List{}, fmt.Errorf("update list: %w", err)
	}
	return list, nil
}

// DeleteList soft-deletes a list (owner only). The personal Inbox is undeletable.
func (d *Domain) DeleteList(ctx context.Context, p *auth.Principal, id uuid.UUID) error {
	if _, err := d.authorizeList(ctx, p, id, RoleOwner, true); err != nil {
		return err
	}
	row, err := d.store.GetListForUser(ctx, db.GetListForUserParams{ID: id, UserID: p.UserID})
	if err != nil {
		return fmt.Errorf("get list: %w", err)
	}
	if row.List.IsPersonal {
		return NewError(409, "list_undeletable", "the personal Inbox list cannot be deleted")
	}
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		if _, e := q.SoftDeleteList(ctx, id); e != nil {
			return e
		}
		return events.Write(ctx, q, events.TypeListDeleted, &id, actorOf(p), listResource(row.List), nil)
	})
	if err != nil {
		return fmt.Errorf("delete list: %w", err)
	}
	return nil
}
