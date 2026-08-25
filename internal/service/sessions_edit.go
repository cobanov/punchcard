package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/events"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// UpdateSessionInput is a partial correction to a recorded session. Every field
// is a pointer: an absent field is left alone.
type UpdateSessionInput struct {
	ProjectID *uuid.UUID
	Note      *string
	StartedAt *time.Time
	EndedAt   *time.Time
	SetEnded  bool // true when EndedAt was supplied at all, including as null
}

// UpdateSession corrects a recorded session's project, note or times.
//
// Correcting times is not a nicety — it is the answer to the one real weakness
// of a live timer, which is forgetting to stop it. A record that cannot be fixed
// is a record the user stops trusting.
func (d *Domain) UpdateSession(ctx context.Context, p *auth.Principal, sessionID uuid.UUID, in UpdateSessionInput) (db.WorkSession, error) {
	if !p.CanWrite() {
		return db.WorkSession{}, ErrInsufficientScope
	}
	current, err := d.GetSession(ctx, p, sessionID)
	if err != nil {
		return db.WorkSession{}, err
	}
	if in.ProjectID != nil {
		if err := d.authorizeProject(ctx, p, *in.ProjectID, true); err != nil {
			return db.WorkSession{}, err
		}
	}
	if in.Note != nil {
		n := strings.TrimSpace(*in.Note)
		if len([]rune(n)) > 500 {
			return db.WorkSession{}, NewError(422, "validation_failed", "note must be at most 500 characters")
		}
		in.Note = &n
	}

	// Work out the range the row will end up with and reject an inverted one
	// here, so the caller gets a reason rather than a constraint violation.
	start := current.StartedAt
	if in.StartedAt != nil {
		start = in.StartedAt.UTC()
	}
	end := current.EndedAt
	if in.SetEnded {
		if in.EndedAt == nil {
			end = nil
		} else {
			e := in.EndedAt.UTC()
			end = &e
		}
	}
	if end != nil && !end.After(start) {
		return db.WorkSession{}, NewError(422, "validation_failed", "ended_at must be after started_at")
	}
	// Reopening a session is only possible when no other timer is running,
	// because of one_open_session_per_user. Say so plainly instead of letting
	// the index answer.
	if end == nil && current.EndedAt != nil {
		if open, oerr := d.store.GetOpenWorkSession(ctx, p.UserID); oerr == nil && open.ID != sessionID {
			return db.WorkSession{}, NewError(409, "session_already_running",
				"another session is running; stop it before reopening this one")
		}
	}

	var updated db.WorkSession
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		// Re-queue BEFORE the update, not after: UpdateWorkSession returns the
		// row it wrote, so a later mark would leave the caller holding a record
		// that says "ok" while the queue says otherwise.
		if in.StartedAt != nil || in.SetEnded {
			if e := q.MarkSessionSyncPending(ctx, db.MarkSessionSyncPendingParams{ID: sessionID, UserID: p.UserID}); e != nil {
				return e
			}
		}
		ws, e := q.UpdateWorkSession(ctx, db.UpdateWorkSessionParams{
			ID: sessionID, UserID: p.UserID,
			ProjectID: in.ProjectID, Note: in.Note, StartedAt: in.StartedAt,
			SetEnded: in.SetEnded, EndedAt: in.EndedAt,
		})
		if e != nil {
			return e
		}
		updated = ws
		// Moving a boundary moves what the session holds: runs the new range
		// covers come in, runs it no longer covers go back to being unmatched.
		if in.StartedAt != nil || in.SetEnded {
			if e := reconcileAgentRuns(ctx, q, p.UserID); e != nil {
				return e
			}
		}
		return events.Write(ctx, q, events.TypeSessionUpdated, &ws.ProjectID, actorOf(p), sessionResource(ws), nil)
	})
	if err != nil {
		return db.WorkSession{}, mapNotFound(err)
	}
	return updated, nil
}

// SplitSession cuts a recorded session in two at `at`, keeping both halves.
//
// Commits move with the clock: each attached commit ends up in whichever half
// its commit time falls in. Splitting a stretch of work and finding the evidence
// still in the wrong half would defeat the point of attaching it.
func (d *Domain) SplitSession(ctx context.Context, p *auth.Principal, sessionID uuid.UUID, at time.Time) (left, right db.WorkSession, err error) {
	if !p.CanWrite() {
		return db.WorkSession{}, db.WorkSession{}, ErrInsufficientScope
	}
	current, err := d.GetSession(ctx, p, sessionID)
	if err != nil {
		return db.WorkSession{}, db.WorkSession{}, err
	}
	if current.EndedAt == nil {
		return db.WorkSession{}, db.WorkSession{}, NewError(422, "validation_failed",
			"a running session cannot be split; stop it first")
	}
	at = at.UTC()
	if !at.After(current.StartedAt) || !at.Before(*current.EndedAt) {
		return db.WorkSession{}, db.WorkSession{}, NewError(422, "validation_failed",
			"the split point must fall strictly inside the session")
	}

	newID, err := uuid.NewV7()
	if err != nil {
		return db.WorkSession{}, db.WorkSession{}, fmt.Errorf("new uuid: %w", err)
	}
	originalEnd := *current.EndedAt

	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		// Shorten the original first: the second half cannot exist alongside a
		// session that still covers its range.
		l, e := q.UpdateWorkSession(ctx, db.UpdateWorkSessionParams{
			ID: sessionID, UserID: p.UserID, SetEnded: true, EndedAt: &at,
		})
		if e != nil {
			return e
		}
		left = l

		// The second half is created open and then closed, because
		// StartWorkSession is the only insert and it deliberately has no
		// ended_at parameter: a session is always born running.
		if _, e := q.StartWorkSession(ctx, db.StartWorkSessionParams{
			ID: newID, ProjectID: current.ProjectID, UserID: p.UserID,
			Note: current.Note, StartedAt: at, Source: current.Source,
		}); e != nil {
			return e
		}
		r, e := q.UpdateWorkSession(ctx, db.UpdateWorkSessionParams{
			ID: newID, UserID: p.UserID, SetEnded: true, EndedAt: &originalEnd,
		})
		if e != nil {
			return e
		}
		right = r

		// Re-file the commits by time.
		commits, e := q.ListCommitsForSession(ctx, sessionID)
		if e != nil {
			return e
		}
		for _, c := range commits {
			if c.CommittedAt.Before(at) {
				continue
			}
			if _, e := q.DetachCommit(ctx, db.DetachCommitParams{SessionID: sessionID, CommitID: c.ID}); e != nil {
				return e
			}
			if _, e := q.AttachCommitToSession(ctx, db.AttachCommitToSessionParams{SessionID: newID, CommitID: c.ID}); e != nil {
				return e
			}
		}

		// Runs re-file by the clock, like the commits above — except the
		// statement pair works it out from the ranges rather than being told
		// which half each one belongs in.
		if e := reconcileAgentRuns(ctx, q, p.UserID); e != nil {
			return e
		}

		if e := events.Write(ctx, q, events.TypeSessionUpdated, &left.ProjectID, actorOf(p), sessionResource(left), nil); e != nil {
			return e
		}
		return events.Write(ctx, q, events.TypeSessionStarted, &right.ProjectID, actorOf(p), sessionResource(right), nil)
	})
	if err != nil {
		return db.WorkSession{}, db.WorkSession{}, mapDomainError(err, "split session")
	}
	return left, right, nil
}

// DeleteSession soft-deletes a session. Its commits and agent runs are not
// deleted — they simply become unmatched again and reappear as a suggestion.
func (d *Domain) DeleteSession(ctx context.Context, p *auth.Principal, sessionID uuid.UUID) error {
	if !p.CanWrite() {
		return ErrInsufficientScope
	}
	ws, err := d.GetSession(ctx, p, sessionID)
	if err != nil {
		return err
	}
	return d.store.WithTx(ctx, func(q *db.Queries) error {
		n, e := q.SoftDeleteWorkSession(ctx, db.SoftDeleteWorkSessionParams{ID: sessionID, UserID: p.UserID})
		if e != nil {
			return e
		}
		if n == 0 {
			return ErrNotFound
		}
		// Detach rather than cascade: a soft-deleted session still holds the
		// row, so its commits would stay attached to something invisible and
		// never resurface as unmatched.
		if _, e := q.DetachCommitsFromSession(ctx, sessionID); e != nil {
			return e
		}
		if _, e := q.DetachAgentRunsFromSession(ctx, sessionID); e != nil {
			return e
		}
		return events.Write(ctx, q, events.TypeSessionDeleted, &ws.ProjectID, actorOf(p),
			map[string]any{"id": sessionID}, nil)
	})
}
