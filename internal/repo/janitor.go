// Janitor runs the data-retention sweeps: dropping outbox events after 30 days
// and expiring idempotency keys after 24 hours. The retention periods live in
// the SQL (db/queries); this loop only schedules them.
//
// The GitHub commit scanner is NOT here, even though it is also a periodic job.
// It has to call the service layer, and internal/repo sits below it — see
// internal/service/scanner.go.
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
