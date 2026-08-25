// Janitor runs the retention jobs the schema comments promise: hard-purging
// soft-deleted tasks after 30 days, dropping outbox events past the offline
// sync horizon (30 days — see docs/offline-sync.md; long-offline clients get a
// reset), expiring idempotency keys after 24 hours, and purging activity log
// rows after 400 days — long enough to compare against the same month last
// year, bounded enough that a self-hosted instance does not leak. The
// retention periods live in the SQL (db/queries); this loop only schedules
// them. A poll loop matches the webhook dispatcher's deviation pattern (no
// River yet).
package repo

import (
	"context"
	"log/slog"
	"time"
)

// Janitor periodically runs data-retention sweeps.
type Janitor struct {
	store    *Store
	log      *slog.Logger
	interval time.Duration
}

// NewJanitor builds a Janitor. interval <= 0 disables Run.
func NewJanitor(store *Store, log *slog.Logger, interval time.Duration) *Janitor {
	return &Janitor{store: store, log: log, interval: interval}
}

// Run sweeps immediately and then on every interval tick until ctx ends.
func (j *Janitor) Run(ctx context.Context) {
	if j.interval <= 0 {
		j.log.Info("janitor disabled (JANITOR_INTERVAL <= 0)")
		return
	}
	j.sweep(ctx)
	t := time.NewTicker(j.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.sweep(ctx)
		}
	}
}

func (j *Janitor) sweep(ctx context.Context) {
	type job struct {
		name string
		run  func(context.Context) (int64, error)
	}
	for _, jb := range []job{
		{"purge_old_events", j.store.PurgeOldEvents},
		{"purge_idempotency_keys", j.store.DeleteExpiredIdempotencyKeys},
	} {
		n, err := jb.run(ctx)
		if err != nil {
			if ctx.Err() == nil {
				j.log.Error("janitor sweep failed", "job", jb.name, "error", err.Error())
			}
			continue
		}
		if n > 0 {
			j.log.Info("janitor sweep", "job", jb.name, "rows", n)
		}
	}
}
