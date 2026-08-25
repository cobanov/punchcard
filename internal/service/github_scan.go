package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/events"
	"github.com/cobanov/punchcard/internal/github"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// backoff is how long a failed scan waits before the next attempt. After the
// last step the session is marked 'error' and stops being retried, because a
// queue that never gives up is a queue that hides a real problem.
var backoff = []time.Duration{
	time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour,
}

// maxDiscoveredRepos bounds how many repositories one scan will look at.
//
// A cap rather than no cap: an account that pushed to forty repositories in a
// week is unusual, and forty branch enumerations in one pass is not what a
// background job should do without someone deciding it should.
const maxDiscoveredRepos = 25

// RescanWindow is how far back the periodic sweep re-queues finished sessions.
//
// This exists for the second GitHub trap: a commit that has not been pushed does
// not exist as far as the API is concerned, so the scan that runs when a session
// stops cannot see work written that morning and pushed that evening. Re-queuing
// the last week means the record fills in by itself once the push happens.
const RescanWindow = 7 * 24 * time.Hour

// ScanResult summarizes one pass.
type ScanResult struct {
	ReposScanned int
	ReposSkipped int
	CommitsFound int
	Attached     int
}

// ScanWindow fetches the user's commits in [from, to] and files each one under
// whichever session covers its commit time.
//
// Scanning is per WINDOW rather than per session on purpose: two sessions an
// hour apart in the same repositories would otherwise pay for the same branch
// enumeration twice.
func (d *Domain) ScanWindow(ctx context.Context, userID uuid.UUID, from, to time.Time) (ScanResult, error) {
	var res ScanResult

	client, login, err := d.githubClientFor(ctx, userID)
	if err != nil {
		return res, err
	}
	if client == nil {
		// No connection, or no token key on this deployment. Nothing to do, and
		// nothing worth reporting as a failure.
		return res, nil
	}

	// Which repositories to look in.
	//
	// Linking a repository to a project is OPTIONAL, and this is where that is
	// made true: the scanner asks GitHub which repositories the account has
	// pushed to since the window began, and looks there. A user who connects
	// GitHub and starts a timer gets their commits with no setup at all.
	//
	// Explicitly linked repositories are added on top rather than replacing the
	// discovered set. They serve a different purpose — suggesting which project
	// a stretch of unmatched commits belongs to — and a user who linked one
	// still works in others.
	type repoRef struct {
		id       *uuid.UUID
		branches []string
	}
	byName := map[string]repoRef{}

	discovered, derr := client.ReposPushedSince(ctx, from, maxDiscoveredRepos)
	if derr != nil {
		permanent, msg := classifyGitHubError(derr)
		d.markGitHubError(ctx, userID, msg)
		if permanent {
			return res, permanentGitHubError{msg: msg}
		}
		return res, derr
	}
	for _, r := range discovered {
		byName[r.FullName] = repoRef{}
	}

	linked, err := d.store.ListReposForUser(ctx, userID)
	if err != nil {
		return res, fmt.Errorf("list repos: %w", err)
	}
	for _, r := range linked {
		if existing, seen := byName[r.FullName]; seen && existing.id != nil {
			continue
		}
		var cached []string
		if r.BranchesAt != nil && time.Since(*r.BranchesAt) < github.BranchCacheTTL {
			_ = json.Unmarshal(r.Branches, &cached)
		}
		id := r.ID
		byName[r.FullName] = repoRef{id: &id, branches: cached}
	}
	if len(byName) == 0 {
		// Nothing was pushed in this window, so there is nothing to look in.
		// The scan still HAPPENED, and recording that matters: without it
		// last_scan_at stays null and every client reports "never scanned",
		// which reads as broken to a user whose integration is working fine and
		// simply had a quiet morning.
		d.touchScan(ctx, userID)
		return res, nil
	}

	for fullName, ref := range byName {
		scan, serr := github.ScanRepo(ctx, client, fullName, login, from, to, ref.branches)
		if serr != nil {
			permanent, msg := classifyGitHubError(serr)
			d.markGitHubError(ctx, userID, msg)
			if permanent {
				return res, permanentGitHubError{msg: msg}
			}
			return res, serr
		}
		if scan.Skipped {
			res.ReposSkipped++
			continue
		}
		res.ReposScanned++

		// Only a linked repository has a row to cache the branch list on. A
		// discovered one is re-listed next time, which costs one request.
		if ref.id != nil && len(scan.Branches) > 0 && len(ref.branches) == 0 {
			if payload, merr := json.Marshal(scan.Branches); merr == nil {
				if uerr := d.store.SetRepoBranches(ctx, db.SetRepoBranchesParams{
					ID: *ref.id, Branches: payload,
				}); uerr != nil {
					d.log.WarnContext(ctx, "could not cache branches", "repo", fullName, "error", uerr.Error())
				}
			}
		}

		attached, aerr := d.storeCommits(ctx, userID, fullName, scan.Commits)
		if aerr != nil {
			return res, aerr
		}
		res.CommitsFound += len(scan.Commits)
		res.Attached += attached
	}

	d.touchScan(ctx, userID)
	return res, nil
}

// touchScan records that a scan completed, clearing any previous error.
func (d *Domain) touchScan(ctx context.Context, userID uuid.UUID) {
	if err := d.store.TouchGitHubScan(ctx, userID); err != nil {
		d.log.WarnContext(ctx, "could not record scan time", "error", err.Error())
	}
}

// permanentGitHubError marks a failure the backoff must not retry.
type permanentGitHubError struct{ msg string }

func (e permanentGitHubError) Error() string { return e.msg }

// storeCommits upserts commits and attaches each to the session covering it.
func (d *Domain) storeCommits(ctx context.Context, userID uuid.UUID, fullName string, commits []github.Commit) (int, error) {
	attached := 0
	for _, cm := range commits {
		id, err := uuid.NewV7()
		if err != nil {
			return attached, err
		}
		row, err := d.store.UpsertCommit(ctx, db.UpsertCommitParams{
			ID: id, UserID: userID, RepoFullName: fullName, Sha: cm.SHA,
			Message: cm.Message, CommittedAt: cm.CommittedAt, Url: cm.URL,
		})
		if err != nil {
			return attached, fmt.Errorf("upsert commit: %w", err)
		}

		ws, err := d.store.SessionCovering(ctx, db.SessionCoveringParams{UserID: userID, At: cm.CommittedAt})
		if err != nil {
			if isNoRows(err) {
				// No session covers this instant. The commit stays in the cache
				// unattached, which is exactly what the unmatched-commit feed
				// reads — this is how a forgotten timer gets recovered.
				continue
			}
			return attached, fmt.Errorf("find covering session: %w", err)
		}
		n, err := d.store.AttachCommitToSession(ctx, db.AttachCommitToSessionParams{
			SessionID: ws.ID, CommitID: row.ID,
		})
		if err != nil {
			return attached, fmt.Errorf("attach commit: %w", err)
		}
		attached += int(n)
	}
	return attached, nil
}

// RunPendingScans is the janitor's entry point: it claims due sessions, groups
// them per user into one window each, and scans.
func (d *Domain) RunPendingScans(ctx context.Context, now time.Time, limit int32) error {
	pending, err := d.store.ClaimPendingSyncSessions(ctx, db.ClaimPendingSyncSessionsParams{
		Now: now.UTC(), Lim: limit,
	})
	if err != nil {
		return fmt.Errorf("claim pending scans: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}

	// One window per user covering all their due sessions: the repositories and
	// branches are the same either way, so a single pass costs one enumeration
	// instead of one per session.
	type window struct {
		from, to time.Time
		sessions []db.WorkSession
	}
	byUser := map[uuid.UUID]*window{}
	for _, ws := range pending {
		w, ok := byUser[ws.UserID]
		if !ok {
			byUser[ws.UserID] = &window{from: ws.StartedAt, to: *ws.EndedAt, sessions: []db.WorkSession{ws}}
			continue
		}
		if ws.StartedAt.Before(w.from) {
			w.from = ws.StartedAt
		}
		if ws.EndedAt.After(w.to) {
			w.to = *ws.EndedAt
		}
		w.sessions = append(w.sessions, ws)
	}

	for userID, w := range byUser {
		_, serr := d.ScanWindow(ctx, userID, w.from, w.to)
		d.recordScanOutcome(ctx, userID, w.sessions, serr, now)
	}
	return nil
}

// recordScanOutcome moves each session's sync state on according to how the
// scan went.
func (d *Domain) recordScanOutcome(ctx context.Context, userID uuid.UUID, sessions []db.WorkSession, scanErr error, now time.Time) {
	if scanErr == nil {
		for _, ws := range sessions {
			if err := d.store.SetSessionSyncOK(ctx, ws.ID); err != nil {
				d.log.WarnContext(ctx, "could not mark scan ok", "session", ws.ID, "error", err.Error())
			}
			d.emitCommitsAttached(ctx, ws)
		}
		return
	}

	msg := scanErr.Error()
	if _, permanent := scanErr.(permanentGitHubError); permanent {
		// The token is gone. Retrying every session on a schedule would just
		// hammer GitHub with a credential it already rejected, so the queue is
		// parked and the reason is on the connection for the user to see.
		if _, err := d.store.SetSessionSyncSkipped(ctx, db.SetSessionSyncSkippedParams{
			UserID: userID, Err: &msg,
		}); err != nil {
			d.log.WarnContext(ctx, "could not park scans", "error", err.Error())
		}
		return
	}

	for _, ws := range sessions {
		attempt := int(ws.SyncAttempts)
		if attempt >= len(backoff) {
			if err := d.store.SetSessionSyncFailed(ctx, db.SetSessionSyncFailedParams{ID: ws.ID, Err: &msg}); err != nil {
				d.log.WarnContext(ctx, "could not mark scan failed", "session", ws.ID, "error", err.Error())
			}
			continue
		}
		next := now.Add(backoff[attempt])
		if err := d.store.SetSessionSyncRetry(ctx, db.SetSessionSyncRetryParams{
			ID: ws.ID, NextAt: &next, Err: &msg,
		}); err != nil {
			d.log.WarnContext(ctx, "could not schedule scan retry", "session", ws.ID, "error", err.Error())
		}
	}
}

// emitCommitsAttached announces a session's commits, but only when it has some:
// an event saying "zero commits attached" is noise on every stream.
func (d *Domain) emitCommitsAttached(ctx context.Context, ws db.WorkSession) {
	commits, err := d.store.ListCommitsForSession(ctx, ws.ID)
	if err != nil || len(commits) == 0 {
		return
	}
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		return events.Write(ctx, q, events.TypeCommitsAttached, &ws.ProjectID,
			events.Actor{Label: "system", UserID: ws.UserID},
			map[string]any{"session_id": ws.ID, "count": len(commits)}, nil)
	})
	if err != nil {
		d.log.WarnContext(ctx, "could not emit commits.attached", "session", ws.ID, "error", err.Error())
	}
}

// RequeueRecentSessions re-queues finished sessions from the last RescanWindow.
// See the constant: this is the answer to commits pushed hours after they were
// written.
func (d *Domain) RequeueRecentSessions(ctx context.Context, now time.Time) (int64, error) {
	n, err := d.store.MarkSessionsSyncPendingSince(ctx, now.Add(-RescanWindow).UTC())
	if err != nil {
		return 0, fmt.Errorf("requeue recent sessions: %w", err)
	}
	return n, nil
}

// RescanSession puts one session back in the queue by hand.
func (d *Domain) RescanSession(ctx context.Context, p *auth.Principal, sessionID uuid.UUID) error {
	if !p.CanWrite() {
		return ErrInsufficientScope
	}
	ws, err := d.GetSession(ctx, p, sessionID)
	if err != nil {
		return err
	}
	if ws.EndedAt == nil {
		return NewError(422, "validation_failed", "a running session has no window to scan yet")
	}
	if err := d.store.MarkSessionSyncPending(ctx, db.MarkSessionSyncPendingParams{
		ID: sessionID, UserID: p.UserID,
	}); err != nil {
		return fmt.Errorf("requeue session: %w", err)
	}
	return nil
}

// ConnectionScanWindow is how far back an account-level scan looks.
//
// Seven days, matching the window the janitor re-queues sessions over: a commit
// pushed later than that has missed its session anyway, and asking GitHub for a
// month of history on every account would spend the rate limit on work nobody
// is waiting for.
const ConnectionScanWindow = 7 * 24 * time.Hour

// connectionScanEvery is how stale a connection has to be before it is scanned
// again on its own account. Sessions still drive prompt scanning; this is the
// floor under accounts that are not recording any.
const connectionScanEvery = time.Hour

// RunConnectionScans fetches commits for accounts, rather than for sessions.
//
// The session queue cannot help someone who has not recorded a session, and
// that is precisely a new account: it connects GitHub, has nothing queued, and
// so is never scanned. The result is an empty screen on the one day the
// product most needs to show what it is for — the commits are all there on
// GitHub, and punchcard simply never asked.
//
// So accounts get claimed too. Never-scanned ones come first, which makes this
// the onboarding path as well as the maintenance one.
func (d *Domain) RunConnectionScans(ctx context.Context, now time.Time, limit int32) error {
	users, err := d.store.ClaimStaleGitHubConnections(ctx, db.ClaimStaleGitHubConnectionsParams{
		Before: now.Add(-connectionScanEvery).UTC(), Lim: limit,
	})
	if err != nil {
		return fmt.Errorf("claim stale github connections: %w", err)
	}
	for _, userID := range users {
		if ctx.Err() != nil {
			return nil
		}
		// ScanWindow records its own outcome on the connection, including the
		// error, so a rate limit or a revoked token surfaces in the UI rather
		// than only in the log.
		if _, err := d.ScanWindow(ctx, userID, now.Add(-ConnectionScanWindow), now); err != nil {
			d.log.WarnContext(ctx, "account commit scan failed", "user_id", userID, "error", err.Error())
		}
	}
	return nil
}
