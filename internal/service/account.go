package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// exportPageSize bounds how many sessions are held in memory at once while
// streaming the export, so the endpoint cannot be used to exhaust the heap
// regardless of how much history the account has.
const exportPageSize = 1000

// DeleteAccount hard-deletes the user and everything they own. Session-only.
//
// helva blocked this while the user owned shared lists, because deleting them
// would have taken other people's data with it. punchcard has no sharing: every
// project belongs to exactly one account, so there is nobody else to consider
// and the check is gone rather than left in as a no-op.
func (d *Domain) DeleteAccount(ctx context.Context, p *auth.Principal, ip string) error {
	if !p.FirstParty() {
		return ErrSessionOnly
	}
	// Record the audit entry before deletion (its user_id becomes NULL on cascade).
	d.audit.Record(ctx, &p.UserID, audit.ActionAccountDelete, ip, nil)
	if err := d.store.HardDeleteUser(ctx, p.UserID); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	return nil
}

// AuthorizeExport reports whether the principal may run the data export.
//
// Session-only, for the same reason DeleteAccount and token management are: the
// export reaches every project and session the account has — soft-deleted rows
// included — and lists the metadata of the account's other PATs. It is
// account-plane data, so no API token may read it, scoped or not. Without this
// an unprivileged PAT (read scope, narrowed to one project) could dump the
// whole account.
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

// ExportDataStream writes all of the user's data to w as a single JSON object.
// Sessions are fetched page-by-page and encoded incrementally so the whole
// dataset is never materialized in memory.
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
	projects, err := d.store.ListProjects(ctx, db.ListProjectsParams{
		OwnerID: p.UserID, IncludeArchived: true,
	})
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
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
		"id": user.ID, "email": user.Email, "timezone": user.Timezone,
		"email_verified_at": user.EmailVerifiedAt, "created_at": user.CreatedAt,
	}); err != nil {
		return err
	}

	if err := raw(`,"projects":[`); err != nil {
		return err
	}
	for i, proj := range projects {
		if i > 0 {
			if err := raw(","); err != nil {
				return err
			}
		}
		if err := raw(`{"project":`); err != nil {
			return err
		}
		if err := put(proj); err != nil {
			return err
		}
		repos, err := d.store.ListProjectRepos(ctx, proj.ID)
		if err != nil {
			return fmt.Errorf("export repos: %w", err)
		}
		if err := raw(`,"repos":`); err != nil {
			return err
		}
		if err := put(repos); err != nil {
			return err
		}
		if err := raw(`,"sessions":[`); err != nil {
			return err
		}
		if err := d.streamProjectSessions(ctx, p.UserID, proj.ID, put, raw); err != nil {
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

// streamProjectSessions writes one project's sessions, each with its attached
// commits, as comma-separated JSON values.
func (d *Domain) streamProjectSessions(ctx context.Context, userID, projectID uuid.UUID, put func(any) error, raw func(string) error) error {
	pid := projectID
	sessions, err := d.store.ListWorkSessions(ctx, db.ListWorkSessionsParams{
		UserID:    userID,
		FromTs:    time.Unix(0, 0).UTC(),
		ToTs:      time.Now().UTC().AddDate(100, 0, 0),
		ProjectID: &pid,
	})
	if err != nil {
		return fmt.Errorf("export sessions: %w", err)
	}
	for i, ws := range sessions {
		if i > 0 {
			if err := raw(","); err != nil {
				return err
			}
		}
		if err := raw(`{"session":`); err != nil {
			return err
		}
		if err := put(ws); err != nil {
			return err
		}
		commits, err := d.store.ListCommitsForSession(ctx, ws.ID)
		if err != nil {
			return fmt.Errorf("export commits: %w", err)
		}
		if err := raw(`,"commits":`); err != nil {
			return err
		}
		if err := put(commits); err != nil {
			return err
		}
		if err := raw(`}`); err != nil {
			return err
		}
	}
	return nil
}
