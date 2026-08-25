package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// MaxAgentRunBatch caps one ingest call. The queue is flushed in batches and a
// caller with a long backlog should send several requests rather than one that
// holds a transaction open for a second.
const MaxAgentRunBatch = 500

// maxAgentRunDuration is the longest interval that can plausibly be one turn.
//
// The client builds a run from a marker written when a prompt was submitted, so
// a machine that slept, a hook that never fired, or a crashed session leaves a
// marker that would otherwise turn into a twenty-hour "working" block. The
// client discards stale markers too; this is the server refusing to be the only
// thing standing between a bug and a report that reads as a lie.
const maxAgentRunDuration = 24 * time.Hour

// AgentRunInput is one reported working interval.
//
// Nothing here is verified. Unlike a commit, which punchcard fetches from
// GitHub itself, this is a client's account of what it did — stored as
// evidence, never as billable time.
type AgentRunInput struct {
	Tool         string
	ExternalID   string
	StartedAt    time.Time
	EndedAt      time.Time
	Model        string
	Cwd          string
	RepoFullName string
	ToolCalls    *int32
}

// RecordAgentRuns stores reported runs and files them against sessions.
//
// Accepted counts rows that did not exist; duplicates counts the ones already
// known. The split is the whole point of the idempotency key: a client that
// cannot tell whether its last flush landed can simply send again.
func (d *Domain) RecordAgentRuns(ctx context.Context, p *auth.Principal, runs []AgentRunInput) (accepted, duplicates int, err error) {
	if !p.CanWrite() {
		return 0, 0, ErrInsufficientScope
	}
	if len(runs) == 0 {
		return 0, 0, NewError(422, "validation_failed", "runs must not be empty")
	}
	if len(runs) > MaxAgentRunBatch {
		return 0, 0, NewError(422, "validation_failed",
			fmt.Sprintf("at most %d runs per request", MaxAgentRunBatch))
	}

	prepared := make([]db.InsertAgentRunParams, 0, len(runs))
	for i, r := range runs {
		params, verr := prepareAgentRun(p.UserID, r)
		if verr != nil {
			return 0, 0, NewError(422, "validation_failed", fmt.Sprintf("runs[%d]: %s", i, verr.Error()))
		}
		prepared = append(prepared, params)
	}

	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		for _, params := range prepared {
			if _, e := q.InsertAgentRun(ctx, params); e != nil {
				if isNoRows(e) {
					// The conflict clause returned nothing: this run is already
					// stored, and stored runs are already filed.
					duplicates++
					continue
				}
				return e
			}
			accepted++
		}
		if accepted == 0 {
			return nil
		}
		// Filing happens in the same transaction as the insert, so a run is
		// never briefly visible as unmatched work that is about to stop being
		// unmatched.
		return reconcileAgentRuns(ctx, q, p.UserID)
	})
	if err != nil {
		return 0, 0, fmt.Errorf("record agent runs: %w", err)
	}
	return accepted, duplicates, nil
}

// prepareAgentRun validates one reported run and shapes it for insertion.
func prepareAgentRun(userID uuid.UUID, r AgentRunInput) (db.InsertAgentRunParams, error) {
	tool := strings.TrimSpace(r.Tool)
	if tool == "" || len([]rune(tool)) > 64 {
		return db.InsertAgentRunParams{}, fmt.Errorf("tool must be 1–64 characters")
	}
	ext := strings.TrimSpace(r.ExternalID)
	if ext == "" || len([]rune(ext)) > 200 {
		return db.InsertAgentRunParams{}, fmt.Errorf("external_id must be 1–200 characters")
	}
	if r.StartedAt.IsZero() || r.EndedAt.IsZero() {
		return db.InsertAgentRunParams{}, fmt.Errorf("started_at and ended_at are required")
	}
	start, end := r.StartedAt.UTC(), r.EndedAt.UTC()
	if end.Before(start) {
		return db.InsertAgentRunParams{}, fmt.Errorf("ended_at must not be before started_at")
	}
	if end.Sub(start) > maxAgentRunDuration {
		return db.InsertAgentRunParams{}, fmt.Errorf("a run cannot be longer than 24 hours")
	}

	id, err := uuid.NewV7()
	if err != nil {
		return db.InsertAgentRunParams{}, fmt.Errorf("new uuid: %w", err)
	}
	return db.InsertAgentRunParams{
		ID: id, UserID: userID, Tool: tool, ExternalID: ext,
		StartedAt: start, EndedAt: end,
		Model:        truncateRunes(strings.TrimSpace(r.Model), 100),
		Cwd:          truncateRunes(strings.TrimSpace(r.Cwd), 500),
		RepoFullName: truncateRunes(strings.TrimSpace(r.RepoFullName), 200),
		ToolCalls:    r.ToolCalls,
	}, nil
}

// reconcileAgentRuns re-files every one of a user's runs against their sessions.
//
// Two statements, in this order: release the runs whose session no longer covers
// them, then attach the ones nobody holds. Running the whole user's set rather
// than a window is deliberate — a session edit can move a boundary in either
// direction, and working out which runs a given edit could possibly have
// touched is more code, more cases, and one more thing to get subtly wrong than
// two indexed statements are worth.
func reconcileAgentRuns(ctx context.Context, q *db.Queries, userID uuid.UUID) error {
	if _, err := q.DetachUncoveredAgentRuns(ctx, userID); err != nil {
		return fmt.Errorf("detach uncovered agent runs: %w", err)
	}
	if _, err := q.AttachCoveredAgentRuns(ctx, userID); err != nil {
		return fmt.Errorf("attach covered agent runs: %w", err)
	}
	return nil
}

// ReconcileAgentRuns is the exported entry point for callers outside a
// transaction of their own.
func (d *Domain) ReconcileAgentRuns(ctx context.Context, userID uuid.UUID) error {
	return d.store.WithTx(ctx, func(q *db.Queries) error {
		return reconcileAgentRuns(ctx, q, userID)
	})
}

// AgentRunsForSession lists the runs filed against a session.
func (d *Domain) AgentRunsForSession(ctx context.Context, p *auth.Principal, sessionID uuid.UUID) ([]db.AgentRun, error) {
	if _, err := d.GetSession(ctx, p, sessionID); err != nil {
		return nil, err
	}
	rows, err := d.store.ListAgentRunsForSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list agent runs: %w", err)
	}
	return rows, nil
}

// AgentRunsInWindow lists every run that started in a window.
func (d *Domain) AgentRunsInWindow(ctx context.Context, p *auth.Principal, from, to time.Time) ([]db.AgentRun, error) {
	rows, err := d.store.ListAgentRunsInWindow(ctx, db.ListAgentRunsInWindowParams{
		UserID: p.UserID, FromTs: from.UTC(), ToTs: to.UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("list agent runs in window: %w", err)
	}
	return rows, nil
}
