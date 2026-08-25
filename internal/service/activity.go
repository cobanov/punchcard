package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/repo/db"
)

const (
	activityDefaultLimit = 50
	activityMaxLimit     = 200
)

// activityMaxTime is the exclusive upper bound used for the first page's
// cursor. Deliberately not time.Now() plus a bit: occurred_at is app-supplied
// (a backdated offline batch, see docs/offline-sync.md), so a fixed buffer
// sized against the reading instance's own clock can still undercut a row
// written by an instance whose clock disagrees with it by more than that
// buffer. A row excluded from the first page this way is not recoverable by
// paging either — pagination only walks backward from the initial cursor. A
// sentinel far beyond any realistic occurred_at removes the class of bug
// instead of sizing the buffer.
var activityMaxTime = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)

// ActivityFilter is one page request. Before/BeforeID are the keyset cursor;
// zero values mean "the newest page".
type ActivityFilter struct {
	Before   *time.Time
	BeforeID uuid.UUID
	Limit    int
	Origins  []string
	Mine     bool
}

// ActivityItem is one recorded change. The text is not here on purpose: the
// client renders the sentence, so a row read from its offline cache reads the
// same as one read from the server, and better phrasing later reaches rows
// already cached.
type ActivityItem struct {
	ID         uuid.UUID
	OccurredAt time.Time
	Origin     string
	Action     string
	Subject    string
	ListID     *uuid.UUID
	ListName   string
	Detail     map[string]any
	ActorID    uuid.UUID
	ActorName  string
}

// ActivityPage is a page plus the cursor for the next one.
type ActivityPage struct {
	Items []ActivityItem
	Next  string
}

// ListActivity reads the log the caller may see.
//
// Two independent decisions, kept apart on purpose. AUTHORIZATION is the set of
// rows this principal may read at all: their own actions, plus every list they
// are a member of, intersected with a token's list scope. DISPLAY is what the
// user asked to look at. Collapsing the two is how a filter becomes a
// permission by accident.
func (d *Domain) ListActivity(ctx context.Context, p *auth.Principal, f ActivityFilter) (ActivityPage, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = activityDefaultLimit
	}
	if limit > activityMaxLimit {
		limit = activityMaxLimit
	}

	ids, err := d.store.ListAccessibleListIDs(ctx, p.UserID)
	if err != nil {
		return ActivityPage{}, fmt.Errorf("accessible lists: %w", err)
	}
	scoped := ids[:0]
	for _, id := range ids {
		if p.AllowsList(id) {
			scoped = append(scoped, id)
		}
	}

	// A list-scoped token loses the "my own actions" branch entirely. Keeping
	// it would let that token read this user's activity on lists the token was
	// deliberately kept away from.
	ownUserID := p.UserID
	if p.TokenScopedToLists() {
		ownUserID = uuid.Nil
	}

	before := activityMaxTime
	beforeID := uuid.Max
	if f.Before != nil {
		before, beforeID = *f.Before, f.BeforeID
	}

	origins := f.Origins
	if origins == nil {
		origins = []string{}
	}

	rows, err := d.store.ListActivity(ctx, db.ListActivityParams{
		OwnUserID: ownUserID, ListIds: scoped, Mine: f.Mine, Me: p.UserID,
		Origins: origins, BeforeTime: before, BeforeID: beforeID,
		Lim: int32(limit + 1), //nolint:gosec // limit ≤ 200
	})
	if err != nil {
		return ActivityPage{}, fmt.Errorf("list activity: %w", err)
	}

	// hasMore has to be read off the fetched row count before rows is
	// truncated: once it's cut down to limit, len(rows) == limit regardless of
	// whether a further page exists, and that's exactly the distinction this
	// bit exists to preserve. Gating the cursor on the post-truncation length
	// instead (len(page.Items) == limit) cannot tell "there is one more row"
	// from "there were exactly limit rows total" — the latter is a full page
	// that is also the last one, and would wrongly get a next that leads
	// nowhere.
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	page := ActivityPage{Items: make([]ActivityItem, 0, limit)}
	for _, r := range rows {
		var detail map[string]any
		_ = json.Unmarshal(r.Activity.Detail, &detail)
		name := r.DisplayName
		if name == nil || *name == "" {
			name = &r.Email
		}
		item := ActivityItem{
			ID: r.Activity.ID, OccurredAt: r.Activity.OccurredAt,
			Origin: r.Activity.Origin, Action: r.Activity.Action,
			ListID: r.Activity.ListID, Detail: detail,
			ActorID: r.Activity.UserID, ActorName: *name,
		}
		if r.Activity.Subject != nil {
			item.Subject = *r.Activity.Subject
		}
		if r.Activity.ListName != nil {
			item.ListName = *r.Activity.ListName
		}
		page.Items = append(page.Items, item)
	}
	if hasMore {
		last := page.Items[len(page.Items)-1]
		page.Next = last.OccurredAt.UTC().Format(time.RFC3339Nano) + "," + last.ID.String()
	}
	return page, nil
}

// ParseActivityCursor splits a Next value back into its two halves. An
// unparseable cursor is not an error the caller should guess at: returning the
// newest page instead would silently restart a paginating client at the top,
// forever.
func ParseActivityCursor(s string) (time.Time, uuid.UUID, error) {
	at, id, ok := strings.Cut(s, ",")
	if !ok {
		return time.Time{}, uuid.Nil, fmt.Errorf("cursor must be <time>,<id>")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("cursor time: %w", err)
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("cursor id: %w", err)
	}
	return t, u, nil
}
