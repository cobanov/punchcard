package service

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
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

// EvidenceCluster is a stretch of work with no session covering it.
//
// The evidence is of two kinds and they are not equal. A commit is something
// punchcard fetched from GitHub itself; an agent run is something a local hook
// reported and nothing can check. They cluster together because they answer the
// same question — when was work happening? — but readers should keep them
// visibly apart.
type EvidenceCluster struct {
	From  time.Time
	To    time.Time
	Repos []string
	// Dirs holds the working directories of runs that had no git remote. It is
	// a separate field rather than more entries in Repos because "owner/repo"
	// and "a folder called notes" are different things, and a client that
	// renders one as the other is telling the user something untrue.
	Dirs               []string
	Commits            []db.Commit
	Runs               []db.AgentRun
	SuggestedProjectID *uuid.UUID
	SuggestedNote      string
}

// evidenceItem is one piece of evidence on the timeline.
//
// A commit happens at an instant; a run occupies a span. Clustering needs both
// to be comparable, so a commit is simply a span of zero length.
type evidenceItem struct {
	at     time.Time
	end    time.Time
	commit *db.Commit
	run    *db.AgentRun
}

// UnmatchedClusters groups evidence that belongs to no session.
//
// This is the feature that makes a live timer survivable. Forgetting to press
// start is the one failure mode a timer cannot prevent, and idle detection only
// guesses at it — but the commits and the agent runs are already there,
// timestamped, and they say exactly when work happened. So instead of guessing,
// punchcard shows the user the evidence and offers to write the record.
func (d *Domain) UnmatchedClusters(ctx context.Context, p *auth.Principal, from, to time.Time) ([]EvidenceCluster, error) {
	commits, err := d.store.ListUnmatchedCommits(ctx, db.ListUnmatchedCommitsParams{
		UserID: p.UserID, FromTs: from.UTC(), ToTs: to.UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("list unmatched commits: %w", err)
	}
	runs, err := d.store.ListUnmatchedAgentRuns(ctx, db.ListUnmatchedAgentRunsParams{
		UserID: p.UserID, FromTs: from.UTC(), ToTs: to.UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("list unmatched agent runs: %w", err)
	}
	if len(commits) == 0 && len(runs) == 0 {
		return nil, nil
	}

	items := make([]evidenceItem, 0, len(commits)+len(runs))
	for i := range commits {
		c := &commits[i]
		items = append(items, evidenceItem{at: c.CommittedAt, end: c.CommittedAt, commit: c})
	}
	for i := range runs {
		r := &runs[i]
		items = append(items, evidenceItem{at: r.StartedAt, end: r.EndedAt, run: r})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })

	clusters := make([]EvidenceCluster, 0, 4)
	current := EvidenceCluster{}
	// The gap is measured from the furthest point the cluster has reached, not
	// from the last item's start. A two-hour agent run followed by a commit ten
	// minutes later is one stretch of work; comparing against the run's start
	// would call it two.
	reach := items[0].at
	for _, it := range items {
		if !current.empty() && it.at.Sub(reach) > clusterGap {
			clusters = append(clusters, current)
			current = EvidenceCluster{}
			reach = it.at
		}
		current.add(it)
		if it.end.After(reach) {
			reach = it.end
		}
	}
	clusters = append(clusters, current)

	for i := range clusters {
		d.describeCluster(ctx, p, &clusters[i])
	}
	return clusters, nil
}

func (cl *EvidenceCluster) empty() bool { return len(cl.Commits) == 0 && len(cl.Runs) == 0 }

func (cl *EvidenceCluster) add(it evidenceItem) {
	if it.commit != nil {
		cl.Commits = append(cl.Commits, *it.commit)
		return
	}
	cl.Runs = append(cl.Runs, *it.run)
}

// describeCluster fills in the window, the repositories and the suggestion.
func (d *Domain) describeCluster(ctx context.Context, p *auth.Principal, cl *EvidenceCluster) {
	// A run knows when work started; a commit only knows when a piece of it
	// finished, which is why commits get a lead-in and runs do not. Where both
	// are present the earlier answer wins — evidence, not arithmetic.
	var from, to time.Time
	for _, c := range cl.Commits {
		if start := c.CommittedAt.Add(-clusterLeadIn); from.IsZero() || start.Before(from) {
			from = start
		}
		if to.IsZero() || c.CommittedAt.After(to) {
			to = c.CommittedAt
		}
	}
	for _, r := range cl.Runs {
		if from.IsZero() || r.StartedAt.Before(from) {
			from = r.StartedAt
		}
		if to.IsZero() || r.EndedAt.After(to) {
			to = r.EndedAt
		}
	}
	cl.From, cl.To = from, to

	seenRepo := map[string]bool{}
	addRepo := func(name string) {
		if name == "" || seenRepo[name] {
			return
		}
		seenRepo[name] = true
		cl.Repos = append(cl.Repos, name)
	}
	notes := make([]string, 0, len(cl.Commits))
	for _, c := range cl.Commits {
		addRepo(c.RepoFullName)
		if subject := firstLine(c.Message); subject != "" && len(notes) < 3 {
			notes = append(notes, subject)
		}
	}
	// Repository names reduced to their last segment, so a directory that is
	// simply that repository's own folder is recognised as the same place. The
	// same project arrives spelled both ways depending on whether the directory
	// a run happened in had a git remote, and reporting it as two would invent a
	// distinction the user does not have.
	seenBase := map[string]bool{}
	for _, name := range cl.Repos {
		seenBase[strings.ToLower(path.Base(name))] = true
	}
	for _, r := range cl.Runs {
		addRepo(r.RepoFullName)
		if r.RepoFullName != "" {
			seenBase[strings.ToLower(path.Base(r.RepoFullName))] = true
			continue
		}
		// Only when there is no repository: a directory is the weaker answer and
		// should not compete with one the git remote already gave.
		if r.Cwd == "" {
			continue
		}
		base := path.Base(filepath.ToSlash(r.Cwd))
		if base == "" || base == "." || base == "/" || seenBase[strings.ToLower(base)] {
			continue
		}
		seenBase[strings.ToLower(base)] = true
		cl.Dirs = append(cl.Dirs, base)
	}
	// The note comes from commit subjects and nothing else. Prompts are not
	// captured — deliberately — so a run has no text to contribute, and
	// inventing one from the tool name would put words in the user's record.
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
	// StopSession already reconciled; this covers the case where the recovered
	// window swallowed runs that a neighbouring session used to hold.
	if err := d.ReconcileAgentRuns(ctx, p.UserID); err != nil {
		return stopped, err
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
