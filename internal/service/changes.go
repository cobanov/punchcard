package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// Change is one delta-feed item: the outbox event's cursor position plus its
// full envelope payload (which carries a complete resource snapshot, so
// clients apply it directly without a follow-up fetch).
type Change struct {
	Seq     int64
	Type    string
	Payload json.RawMessage
}

// ChangesPage is the result of a delta pull (docs/offline-sync.md §5).
type ChangesPage struct {
	Items   []Change
	Next    int64
	HasMore bool
	// Reset signals that Since predates event retention (or cannot be
	// verified); the client must full-resync via the REST collections.
	Reset bool
}

const (
	changesDefaultLimit = 200
	changesMaxLimit     = 500
)

// Changes returns outbox events after the given cursor for every list the
// principal can access, ordered by seq. A nil since means "head only": no
// items, just the current cursor — the client bootstraps via REST and then
// polls from that head. Membership is evaluated at read time and token list
// scopes apply, so results can never exceed the caller's current access.
func (d *Domain) Changes(ctx context.Context, p *auth.Principal, since *int64, limit int) (ChangesPage, error) {
	if limit <= 0 {
		limit = changesDefaultLimit
	}
	if limit > changesMaxLimit {
		limit = changesMaxLimit
	}
	grace := d.cfg.EventGraceWindow.Seconds()

	head, err := d.store.MaxEventSeq(ctx, grace)
	if err != nil {
		return ChangesPage{}, fmt.Errorf("max event seq: %w", err)
	}
	if since == nil {
		return ChangesPage{Next: head}, nil
	}

	// Verify the cursor is still inside retention: the feed can only prove
	// continuity for cursors >= min-1. Anything older may have had events
	// purged between the cursor and the oldest retained row (this includes
	// since=0 — "from the beginning" is unverifiable once history is gone).
	// since > head only happens after a database restore — resync too. An
	// empty table (min==0) can verify nothing beyond a zero cursor.
	minSeq, err := d.store.MinEventSeq(ctx)
	if err != nil {
		return ChangesPage{}, fmt.Errorf("min event seq: %w", err)
	}
	s := *since
	reset := s > 0
	if minSeq > 0 {
		reset = s < minSeq-1 || s > head
	}
	if reset {
		return ChangesPage{Next: head, Reset: true}, nil
	}

	ids, err := d.store.ListAccessibleListIDs(ctx, p.UserID)
	if err != nil {
		return ChangesPage{}, fmt.Errorf("accessible lists: %w", err)
	}
	scoped := ids[:0]
	for _, id := range ids {
		if p.AllowsList(id) {
			scoped = append(scoped, id)
		}
	}
	if len(scoped) == 0 {
		return ChangesPage{Next: s}, nil
	}

	rows, err := d.store.ListEventsForListsAfterSeq(ctx, db.ListEventsForListsAfterSeqParams{
		ListIds: scoped, AfterSeq: s, GraceSecs: grace, Lim: int32(limit + 1), //nolint:gosec // limit ≤ 500
	})
	if err != nil {
		return ChangesPage{}, fmt.Errorf("list events after seq: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	page := ChangesPage{Next: s, HasMore: hasMore}
	page.Items = make([]Change, 0, len(rows))
	for _, ev := range rows {
		page.Items = append(page.Items, Change{Seq: ev.Seq, Type: ev.Type, Payload: ev.Payload})
		page.Next = ev.Seq
	}
	return page, nil
}
