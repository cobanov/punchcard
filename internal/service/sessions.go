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

// validSources mirrors the CHECK on work_sessions.source. It records which
// client booked the time, which is the only way to tell a hand-entered record
// from one recovered out of GitHub after the fact.
var validSources = map[string]bool{
	"web": true, "cli": true, "extension": true, "mobile": true, "auto": true,
}

// StartSessionInput starts a timer.
//
// StopCurrent defaults to true at the transport layer: pressing start when
// something is already running should do the obvious thing. A caller that wants
// the conflict reported instead — a CLI guarding against a forgotten timer on
// another machine — sets it false and handles the 409.
type StartSessionInput struct {
	ProjectID   uuid.UUID
	Note        string
	StartedAt   *time.Time
	Source      string
	StopCurrent bool
}

// StartSession opens a session, closing whichever one was running.
//
// The close and the open happen in ONE transaction. Doing them as two
// statements would leave an instant with two open sessions, which
// one_open_session_per_user rejects — so a user who did nothing wrong would see
// a constraint violation whenever the two raced.
func (d *Domain) StartSession(ctx context.Context, p *auth.Principal, in StartSessionInput) (db.WorkSession, error) {
	if err := d.authorizeProject(ctx, p, in.ProjectID, true); err != nil {
		return db.WorkSession{}, err
	}
	note := strings.TrimSpace(in.Note)
	if len([]rune(note)) > 500 {
		return db.WorkSession{}, NewError(422, "validation_failed", "note must be at most 500 characters")
	}
	source := in.Source
	if source == "" {
		source = "web"
	}
	if !validSources[source] {
		return db.WorkSession{}, NewError(422, "validation_failed", "unknown source")
	}
	startedAt := time.Now().UTC()
	if in.StartedAt != nil {
		startedAt = in.StartedAt.UTC()
		if startedAt.After(time.Now().UTC().Add(time.Minute)) {
			return db.WorkSession{}, NewError(422, "validation_failed", "started_at cannot be in the future")
		}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return db.WorkSession{}, fmt.Errorf("new uuid: %w", err)
	}

	var started db.WorkSession
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		open, e := q.GetOpenWorkSession(ctx, p.UserID)
		switch {
		case e == nil && !in.StopCurrent:
			return NewError(409, "session_already_running",
				"a session is already running; stop it first or pass stop_current")
		case e == nil:
			// Close the running one at the new one's start so the two abut.
			//
			// The query nudges an end time that would land on its own start one
			// microsecond forward (the range CHECK forbids a zero-length
			// session), so the row that comes back may end slightly LATER than
			// the start we asked for. Reading it back and starting there is what
			// keeps the ranges from overlapping — and overlapping ranges would
			// let one commit belong to two sessions.
			stopped, se := q.StopOpenSessionForUser(ctx, db.StopOpenSessionForUserParams{
				UserID: p.UserID, At: startedAt,
			})
			if se != nil {
				return se
			}
			for _, ws := range stopped {
				if ws.EndedAt != nil && ws.EndedAt.After(startedAt) {
					startedAt = *ws.EndedAt
				}
				if e := events.Write(ctx, q, events.TypeSessionStopped, &ws.ProjectID, actorOf(p), sessionResource(ws), nil); e != nil {
					return e
				}
			}
			_ = open
		case !isNoRows(e):
			return e
		}

		ws, se := q.StartWorkSession(ctx, db.StartWorkSessionParams{
			ID: id, ProjectID: in.ProjectID, UserID: p.UserID,
			Note: note, StartedAt: startedAt, Source: source,
		})
		if se != nil {
			return se
		}
		started = ws
		return events.Write(ctx, q, events.TypeSessionStarted, &ws.ProjectID, actorOf(p), sessionResource(ws), nil)
	})
	if err != nil {
		return db.WorkSession{}, mapDomainError(err, "start session")
	}
	return started, nil
}

// StopSession closes a running session. Stopping an already-stopped session is
// not an error and does not move its end time: a client that retries a stop it
// is unsure landed must not silently extend the record.
func (d *Domain) StopSession(ctx context.Context, p *auth.Principal, sessionID uuid.UUID, at time.Time) (db.WorkSession, error) {
	if !p.CanWrite() {
		return db.WorkSession{}, ErrInsufficientScope
	}
	existing, err := d.store.GetWorkSession(ctx, db.GetWorkSessionParams{ID: sessionID, UserID: p.UserID})
	if err != nil {
		return db.WorkSession{}, mapNotFound(err)
	}
	if existing.EndedAt != nil {
		return existing, nil
	}
	if at.IsZero() {
		at = time.Now()
	}

	var stopped db.WorkSession
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		ws, e := q.StopWorkSession(ctx, db.StopWorkSessionParams{
			ID: sessionID, UserID: p.UserID, At: at.UTC(),
		})
		if e != nil {
			return e
		}
		stopped = ws
		// A session only files runs once it has an end: until now this stretch
		// had no closed range for a run to fall inside.
		if e := reconcileAgentRuns(ctx, q, p.UserID); e != nil {
			return e
		}
		return events.Write(ctx, q, events.TypeSessionStopped, &ws.ProjectID, actorOf(p), sessionResource(ws), nil)
	})
	if err != nil {
		return db.WorkSession{}, mapNotFound(err)
	}
	return stopped, nil
}

// CurrentSession returns the running session, or ErrNotFound when the timer is
// not running.
func (d *Domain) CurrentSession(ctx context.Context, p *auth.Principal) (db.WorkSession, error) {
	ws, err := d.store.GetOpenWorkSession(ctx, p.UserID)
	if err != nil {
		return db.WorkSession{}, mapNotFound(err)
	}
	if !p.AllowsProject(ws.ProjectID) {
		return db.WorkSession{}, ErrNotFound
	}
	return ws, nil
}

// GetSession returns one of the caller's sessions.
func (d *Domain) GetSession(ctx context.Context, p *auth.Principal, sessionID uuid.UUID) (db.WorkSession, error) {
	ws, err := d.store.GetWorkSession(ctx, db.GetWorkSessionParams{ID: sessionID, UserID: p.UserID})
	if err != nil {
		return db.WorkSession{}, mapNotFound(err)
	}
	if !p.AllowsProject(ws.ProjectID) {
		return db.WorkSession{}, ErrNotFound
	}
	return ws, nil
}

// ListSessions returns sessions overlapping [from, to). A running session is
// included: it overlaps every window that has begun.
func (d *Domain) ListSessions(ctx context.Context, p *auth.Principal, from, to time.Time, projectID *uuid.UUID) ([]db.WorkSession, error) {
	if projectID != nil && !p.AllowsProject(*projectID) {
		return nil, ErrNotFound
	}
	rows, err := d.store.ListWorkSessions(ctx, db.ListWorkSessionsParams{
		UserID: p.UserID, FromTs: from.UTC(), ToTs: to.UTC(), ProjectID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	if !p.TokenScopedToProjects() {
		return rows, nil
	}
	out := rows[:0]
	for _, r := range rows {
		if p.AllowsProject(r.ProjectID) {
			out = append(out, r)
		}
	}
	return out, nil
}

// CommitsForSession returns the commits attributed to a session.
func (d *Domain) CommitsForSession(ctx context.Context, p *auth.Principal, sessionID uuid.UUID) ([]db.Commit, error) {
	if _, err := d.GetSession(ctx, p, sessionID); err != nil {
		return nil, err
	}
	rows, err := d.store.ListCommitsForSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list commits: %w", err)
	}
	return rows, nil
}

func sessionResource(ws db.WorkSession) map[string]any {
	return map[string]any{
		"id": ws.ID, "project_id": ws.ProjectID, "note": ws.Note,
		"started_at": ws.StartedAt, "ended_at": ws.EndedAt, "source": ws.Source,
	}
}

// mapDomainError lets a *Error raised inside a transaction pass through
// unchanged; anything else is wrapped with context.
func mapDomainError(err error, what string) error {
	if de, ok := err.(*Error); ok {
		return de
	}
	return fmt.Errorf("%s: %w", what, err)
}
