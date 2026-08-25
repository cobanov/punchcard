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

// clusterGap is how long a silence has to be before two commits are treated as
// separate stretches of work. Thirty minutes is long enough to cover reading,
// thinking and a coffee, and short enough that morning and afternoon do not
// merge into one suggested record.
const clusterGap = 30 * time.Minute

// clusterLeadIn is how far before its first commit a suggested session starts.
// Nobody's first commit is the first thing they did.
const clusterLeadIn = 15 * time.Minute

// CommitCluster is a stretch of work with no session covering it.
type CommitCluster struct {
	From               time.Time
	To                 time.Time
	Repos              []string
	Commits            []db.Commit
	SuggestedProjectID *uuid.UUID
	SuggestedNote      string
}

// UnmatchedClusters groups commits that belong to no session.
//
// This is the feature that makes a live timer survivable. Forgetting to press
// start is the one failure mode a timer cannot prevent, and idle detection only
// guesses at it — but the commits are already there, timestamped, and they say
// exactly when work happened. So instead of guessing, punchcard shows the user
// the evidence and offers to write the record.
func (d *Domain) UnmatchedClusters(ctx context.Context, p *auth.Principal, from, to time.Time) ([]CommitCluster, error) {
	rows, err := d.store.ListUnmatchedCommits(ctx, db.ListUnmatchedCommitsParams{
		UserID: p.UserID, FromTs: from.UTC(), ToTs: to.UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("list unmatched commits: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	clusters := make([]CommitCluster, 0, 4)
	current := CommitCluster{Commits: []db.Commit{rows[0]}}
	for _, c := range rows[1:] {
		last := current.Commits[len(current.Commits)-1]
		if c.CommittedAt.Sub(last.CommittedAt) > clusterGap {
			clusters = append(clusters, current)
			current = CommitCluster{Commits: []db.Commit{c}}
			continue
		}
		current.Commits = append(current.Commits, c)
	}
	clusters = append(clusters, current)

	for i := range clusters {
		d.describeCluster(ctx, p, &clusters[i])
	}
	return clusters, nil
}

// describeCluster fills in the window, the repositories and the suggestion.
func (d *Domain) describeCluster(ctx context.Context, p *auth.Principal, cl *CommitCluster) {
	first := cl.Commits[0]
	last := cl.Commits[len(cl.Commits)-1]
	cl.From = first.CommittedAt.Add(-clusterLeadIn)
	cl.To = last.CommittedAt

	seenRepo := map[string]bool{}
	notes := make([]string, 0, len(cl.Commits))
	for _, c := range cl.Commits {
		if !seenRepo[c.RepoFullName] {
			seenRepo[c.RepoFullName] = true
			cl.Repos = append(cl.Repos, c.RepoFullName)
		}
		if subject := firstLine(c.Message); subject != "" && len(notes) < 3 {
			notes = append(notes, subject)
		}
	}
	cl.SuggestedNote = truncateRunes(strings.Join(notes, "; "), 500)

	// Suggest a project only when the answer is unambiguous: one repository,
	// linked to exactly one project. Guessing between two would put time on the
	// wrong client's invoice.
	if len(cl.Repos) != 1 {
		return
	}
	ids, err := d.store.ProjectsForRepo(ctx, db.ProjectsForRepoParams{
		OwnerID: p.UserID, FullName: cl.Repos[0],
	})
	if err != nil || len(ids) != 1 {
		return
	}
	id := ids[0]
	cl.SuggestedProjectID = &id
}

// ClusterToSessionInput turns a cluster into a recorded session.
type ClusterToSessionInput struct {
	ProjectID uuid.UUID
	From      time.Time
	To        time.Time
	Note      string
}

// SessionFromCluster records a session over a cluster's window and attaches the
// commits inside it.
//
// Source is 'auto' so the record carries how it came to exist: a report that
// cannot tell a timed stretch from a reconstructed one is a report that quietly
// overstates its own precision.
func (d *Domain) SessionFromCluster(ctx context.Context, p *auth.Principal, in ClusterToSessionInput) (db.WorkSession, error) {
	if !in.To.After(in.From) {
		return db.WorkSession{}, NewError(422, "validation_failed", "to must be after from")
	}
	// A window that overlaps an existing session would break the one-session-
	// per-instant rule commit attribution rests on.
	if covering, err := d.store.SessionCovering(ctx, db.SessionCoveringParams{
		UserID: p.UserID, At: in.From,
	}); err == nil {
		return db.WorkSession{}, NewError(409, "overlaps_existing_session",
			fmt.Sprintf("that window overlaps session %s", covering.ID))
	} else if !isNoRows(err) {
		return db.WorkSession{}, fmt.Errorf("check overlap: %w", err)
	}

	started := in.From
	ws, err := d.StartSession(ctx, p, StartSessionInput{
		ProjectID: in.ProjectID, Note: in.Note, StartedAt: &started,
		Source: "auto", StopCurrent: true,
	})
	if err != nil {
		return db.WorkSession{}, err
	}
	stopped, err := d.StopSession(ctx, p, ws.ID, in.To)
	if err != nil {
		return db.WorkSession{}, err
	}

	// Attach what is already cached rather than waiting for the janitor: the
	// user asked for this record because of those commits, and a record that
	// shows up empty for a minute reads as a failure.
	commits, err := d.store.ListCommitsInWindow(ctx, db.ListCommitsInWindowParams{
		UserID: p.UserID, FromTs: stopped.StartedAt, ToTs: *stopped.EndedAt,
	})
	if err != nil {
		return stopped, fmt.Errorf("list commits in window: %w", err)
	}
	for _, c := range commits {
		if _, err := d.store.AttachCommitToSession(ctx, db.AttachCommitToSessionParams{
			SessionID: stopped.ID, CommitID: c.ID,
		}); err != nil {
			return stopped, fmt.Errorf("attach commit: %w", err)
		}
	}
	return stopped, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
