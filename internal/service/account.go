package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// exportPageSize bounds how many tasks are held in memory at once while
// streaming the GDPR export, so the endpoint cannot be used to exhaust the heap
// regardless of how many tasks the account has.
const exportPageSize = 1000

// SharedListError is returned when account deletion is blocked by owned shared
// lists. It lists the blocking lists so the caller can act.
type SharedListError struct {
	Lists []db.ListSharedListsOwnedByRow
}

func (e *SharedListError) Error() string {
	return "account has shared lists that must be handled first"
}

// DeleteAccount hard-deletes the user and all solely-owned data.
// It is blocked while the user owns shared lists, which must be transferred or
// deleted first. Session-only.
func (d *Domain) DeleteAccount(ctx context.Context, p *auth.Principal, ip string) error {
	if !p.FirstParty() {
		return ErrSessionOnly
	}
	shared, err := d.store.ListSharedListsOwnedBy(ctx, p.UserID)
	if err != nil {
		return fmt.Errorf("check shared lists: %w", err)
	}
	if len(shared) > 0 {
		return &SharedListError{Lists: shared}
	}
	// Record the audit entry before deletion (its user_id becomes NULL on cascade).
	d.audit.Record(ctx, &p.UserID, audit.ActionAccountDelete, ip, nil)
	if err := d.store.HardDeleteUser(ctx, p.UserID); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	return nil
}

// AuthorizeExport reports whether the principal may run the GDPR export.
//
// Session-only, for the same reason DeleteAccount and token management are: the
// export reaches every list the account can see — soft-deleted tasks included —
// and lists the metadata of the account's other PATs. It is account-plane data,
// so no API token may read it, scoped or not. Without this an unprivileged PAT
// (read scope, narrowed to one list) could dump the whole account.
//
// It is separate from ExportDataStream because the export streams: the handler
// has to settle authorization before the response headers go out, since an
// error raised inside the stream body can only truncate a 200.
func (d *Domain) AuthorizeExport(p *auth.Principal) error {
	if !p.FirstParty() {
		return ErrSessionOnly
	}
	return nil
}

// ExportDataStream writes all of the user's data to w as a single JSON object
// (GDPR). Tasks are fetched page-by-page and encoded incrementally so the whole
// dataset is never materialized in memory — an authenticated caller cannot use
// this endpoint to exhaust the heap regardless of task count.
//
// Re-checks AuthorizeExport so the policy holds for any caller, not just the
// one transport that pre-checks it.
func (d *Domain) ExportDataStream(ctx context.Context, p *auth.Principal, w io.Writer) error {
	if err := d.AuthorizeExport(p); err != nil {
		return err
	}
	user, err := d.store.GetUserByID(ctx, p.UserID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	lists, err := d.store.ListListsForUser(ctx, db.ListListsForUserParams{
		UserID: p.UserID, Lim: quotaLimit(d.cfg.MaxListsPerUser),
	})
	if err != nil {
		return fmt.Errorf("list lists: %w", err)
	}
	tokens, err := d.store.ListTokensByUser(ctx, p.UserID)
	if err != nil {
		return fmt.Errorf("list tokens: %w", err)
	}

	bw := bufio.NewWriter(w)
	put := func(v any) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		_, e = bw.Write(b)
		return e
	}
	raw := func(s string) error { _, e := bw.WriteString(s); return e }

	if err := raw(`{"user":`); err != nil {
		return err
	}
	if err := put(map[string]any{
		"id": user.ID, "email": user.Email,
		"email_verified_at": user.EmailVerifiedAt, "created_at": user.CreatedAt,
	}); err != nil {
		return err
	}

	if err := raw(`,"lists":[`); err != nil {
		return err
	}
	for i, l := range lists {
		if i > 0 {
			if err := raw(","); err != nil {
				return err
			}
		}
		if err := raw(`{"list":`); err != nil {
			return err
		}
		if err := put(l.List); err != nil {
			return err
		}
		if err := raw(`,"role":`); err != nil {
			return err
		}
		if err := put(l.Role); err != nil {
			return err
		}
		if err := raw(`,"tasks":[`); err != nil {
			return err
		}
		if err := d.streamListTasks(ctx, p.UserID, l.List.ID, bw, put, raw); err != nil {
			return err
		}
		if err := raw(`]}`); err != nil {
			return err
		}
	}
	if err := raw(`],"tokens":`); err != nil {
		return err
	}

	// Strip token hashes from the export.
	tokenMeta := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		tokenMeta = append(tokenMeta, map[string]any{
			"id": t.ID, "name": t.Name, "prefix": t.TokenPrefix, "scope": t.Scope,
			"created_at": t.CreatedAt, "expires_at": t.ExpiresAt, "last_used_at": t.LastUsedAt,
		})
	}
	if err := put(tokenMeta); err != nil {
		return err
	}
	if err := raw(`}`); err != nil {
		return err
	}
	return bw.Flush()
}

// streamListTasks pages through one list's tasks (including soft-deleted) and
// writes them as comma-separated JSON values, holding at most one page.
func (d *Domain) streamListTasks(ctx context.Context, userID, listID uuid.UUID, bw *bufio.Writer, put func(any) error, raw func(string) error) error {
	var (
		cursorPos string
		cursorID  uuid.UUID
		cursorSet bool
		first     = true
	)
	lid := listID
	for {
		rows, err := d.store.ListTasksByPosition(ctx, db.ListTasksByPositionParams{
			UserID: userID, ListID: &lid, IncludeDeleted: true, Lim: exportPageSize,
			CursorSet: cursorSet, CursorPosition: cursorPos, CursorID: cursorID,
		})
		if err != nil {
			return fmt.Errorf("export tasks: %w", err)
		}
		for _, r := range rows {
			if !first {
				if err := raw(","); err != nil {
					return err
				}
			}
			if err := put(r.Task); err != nil {
				return err
			}
			first = false
			cursorPos, cursorID, cursorSet = r.Task.Position, r.Task.ID, true
		}
		if len(rows) < exportPageSize {
			return nil
		}
	}
}
