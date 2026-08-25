package http

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/events"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// sseHub caps concurrent SSE connections per user.
type sseHub struct {
	mu      sync.Mutex
	perUser map[uuid.UUID]int
	max     int
}

func newSSEHub(max int) *sseHub {
	if max < 1 {
		max = 10
	}
	return &sseHub{perUser: make(map[uuid.UUID]int), max: max}
}

func (h *sseHub) acquire(u uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.perUser[u] >= h.max {
		return false
	}
	h.perUser[u]++
	return true
}

func (h *sseHub) release(u uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.perUser[u] > 0 {
		h.perUser[u]--
	}
	if h.perUser[u] == 0 {
		delete(h.perUser, u)
	}
}

// handleSSE streams events for the principal's accessible lists.
// Membership is re-checked every poll, so a revoked user stops receiving a
// list's events. Auth is via session cookie or PAT (handled by authMiddleware).
func (d Deps) handleSSE() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := PrincipalFrom(r.Context())
		if p == nil {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeProblem(w, http.StatusInternalServerError, "internal_error", "streaming unsupported")
			return
		}
		// Clear the server's read/write deadlines for this long-lived stream so
		// the 30s WriteTimeout does not drop it.
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Time{})
		_ = rc.SetReadDeadline(time.Time{})

		var filterList *uuid.UUID
		if q := r.URL.Query().Get("list_id"); q != "" {
			id, err := uuid.Parse(q)
			if err != nil {
				writeProblem(w, http.StatusUnprocessableEntity, "validation_failed", "list_id must be a uuid")
				return
			}
			filterList = &id
		}

		if !d.sseHub.acquire(p.UserID) {
			writeProblem(w, http.StatusTooManyRequests, "too_many_connections", "per-user SSE connection limit reached")
			return
		}
		defer d.sseHub.release(p.UserID)

		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()

		ctx := r.Context()
		grace := d.Config.EventGraceWindow.Seconds()
		// The stream cursor is the event seq (docs/offline-sync.md §5): strictly
		// ordered, so resumes cannot skip events that share a timestamp tick.
		// Default to the current head; Last-Event-ID resumes — either a seq
		// (new clients) or an event UUID (legacy, resolved to its seq).
		cursor, err := d.Store.MaxEventSeq(ctx, grace)
		if err != nil {
			cursor = 0
		}
		if leid := r.Header.Get("Last-Event-ID"); leid != "" {
			if seq, perr := strconv.ParseInt(leid, 10, 64); perr == nil && seq >= 0 {
				cursor = seq
			} else if id, perr := uuid.Parse(leid); perr == nil {
				if ev, gerr := d.Store.GetEvent(ctx, id); gerr == nil {
					cursor = ev.Seq
				}
			}
		}

		pollEvery := d.Config.SSEPollInterval
		if pollEvery <= 0 {
			pollEvery = time.Second
		}
		pollT := time.NewTicker(pollEvery)
		defer pollT.Stop()
		beatT := time.NewTicker(25 * time.Second)
		defer beatT.Stop()

		// The set of accessible lists rarely changes, so cache it instead of
		// re-querying (lists JOIN list_members) every poll tick. It is refreshed
		// at least every membershipTTL — which bounds how long a revoked user
		// keeps receiving a list's events — and immediately after any membership
		// change reaches this stream (member.*/list.deleted).
		const membershipTTL = 30 * time.Second
		var (
			accessibleIDs []uuid.UUID
			idsFetched    time.Time // zero => needs a refresh
		)
		refreshIDs := func() bool {
			ids, err := d.Store.ListAccessibleListIDs(ctx, p.UserID)
			if err != nil {
				return false
			}
			accessibleIDs = allowedListIDs(ids, p, filterList)
			idsFetched = time.Now()
			return true
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-beatT.C:
				_, _ = fmt.Fprint(w, ": keepalive\n\n")
				flusher.Flush()
			case <-pollT.C:
				if idsFetched.IsZero() || time.Since(idsFetched) >= membershipTTL {
					if !refreshIDs() {
						continue
					}
				}
				if len(accessibleIDs) == 0 {
					continue
				}
				evs, err := d.Store.ListEventsForListsAfterSeq(ctx, db.ListEventsForListsAfterSeqParams{
					ListIds: accessibleIDs, AfterSeq: cursor, GraceSecs: grace, Lim: 200,
				})
				if err != nil {
					continue
				}
				for _, ev := range evs {
					_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, ev.Payload)
					cursor = ev.Seq
					if ev.Type == events.TypeMemberAdded || ev.Type == events.TypeMemberRemoved || ev.Type == events.TypeListDeleted {
						idsFetched = time.Time{} // membership may have changed; refresh next tick
					}
				}
				if len(evs) > 0 {
					flusher.Flush()
				}
			}
		}
	}
}

// allowedListIDs filters accessible list ids by the token's list scope and the
// optional list_id query filter.
func allowedListIDs(ids []uuid.UUID, p *auth.Principal, filter *uuid.UUID) []uuid.UUID {
	out := ids[:0]
	for _, id := range ids {
		if filter != nil && id != *filter {
			continue
		}
		if !p.AllowsList(id) {
			continue
		}
		out = append(out, id)
	}
	return out
}
