package service

import (
	"context"
	"log/slog"
	"time"
)

// scanBatch is how many due sessions one pass claims. Small enough that a
// backlog does not hold a transaction open for long, large enough that a normal
// day's work drains in a single tick.
const scanBatch = 50

// connectionBatch is how many accounts one pass scans. Smaller than the session
// batch because each one is a full enumeration of somebody's repositories.
const connectionBatch = 10

// Scanner is the background loop behind the commit matching: it drains the scan
// queue, and periodically puts the last week back on it.
//
// It lives here rather than alongside the retention janitor in internal/repo
// because it needs the service layer, and internal/repo sits below it.
type Scanner struct {
	domain   *Domain
	log      *slog.Logger
	interval time.Duration // how often the queue is drained
	requeue  time.Duration // how often the last RescanWindow is re-queued
}

// NewScanner builds the loop. interval <= 0 disables Run.
func NewScanner(d *Domain, log *slog.Logger, interval, requeue time.Duration) *Scanner {
	return &Scanner{domain: d, log: log, interval: interval, requeue: requeue}
}

// Run drains the queue on every interval tick and re-queues recent sessions on
// every requeue tick, until ctx ends.
//
// The two cadences are different on purpose. Draining is cheap and should be
// prompt — a user who stops a timer wants the commits within a minute. Requeuing
// is what catches commits pushed hours after they were written, so it only has
// to happen often enough that an evening push shows up the same evening.
func (s *Scanner) Run(ctx context.Context) {
	if s.interval <= 0 {
		s.log.Info("commit scanner disabled (SCAN_INTERVAL <= 0)")
		return
	}
	drain := time.NewTicker(s.interval)
	defer drain.Stop()

	requeue := time.NewTicker(s.requeue)
	defer requeue.Stop()

	s.drainOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-drain.C:
			s.drainOnce(ctx)
		case <-requeue.C:
			n, err := s.domain.RequeueRecentSessions(ctx, time.Now())
			if err != nil {
				if ctx.Err() == nil {
					s.log.Error("could not re-queue recent sessions", "error", err.Error())
				}
				continue
			}
			if n > 0 {
				s.log.Info("re-queued recent sessions for a commit scan", "sessions", n)
			}
		}
	}
}

func (s *Scanner) drainOnce(ctx context.Context) {
	if err := s.domain.RunPendingScans(ctx, time.Now(), scanBatch); err != nil && ctx.Err() == nil {
		s.log.Error("commit scan pass failed", "error", err.Error())
	}
	// Accounts, not just sessions. A connection that has never been scanned is
	// claimed first, so a new account sees its own week of work within a tick
	// of connecting rather than never.
	if err := s.domain.RunConnectionScans(ctx, time.Now(), connectionBatch); err != nil && ctx.Err() == nil {
		s.log.Error("account commit scan pass failed", "error", err.Error())
	}
}
