// Package events implements the transactional outbox. WriteEvent is
// called inside the same transaction as a domain mutation so an event is
// persisted atomically with the change; a background dispatcher then fans out
// to webhooks and (Phase 5) SSE.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/activity"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// Event type constants.
const (
	TypeTaskCreated   = "task.created"
	TypeTaskUpdated   = "task.updated"
	TypeTaskCompleted = "task.completed"
	TypeTaskDeleted   = "task.deleted"
	TypeTaskRestored  = "task.restored"
	TypeListCreated   = "list.created"
	TypeListUpdated   = "list.updated"
	TypeListDeleted   = "list.deleted"
	TypeMemberAdded   = "member.added"
	TypeMemberRemoved = "member.removed"
)

// Envelope is the delivered event payload: the full resource plus metadata.
type Envelope struct {
	EventID   string         `json:"event_id"`
	Type      string         `json:"type"`
	ListID    *string        `json:"list_id,omitempty"`
	Actor     string         `json:"actor,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Resource  any            `json:"resource,omitempty"`
	Changes   map[string]any `json:"changes,omitempty"`
}

// Actor identifies who caused an event. Label is the audit-trail string
// ("user:<uuid>" or "token:<uuid>"); UserID is the account it belongs to. They
// are separate because a token's label names the token, and the activity log
// groups by account — parsing the id back out of the label would be a string
// contract where a field will do.
type Actor struct {
	Label  string
	UserID uuid.UUID
}

// Write inserts an outbox event within the given transaction's queries, then
// the activity row that turns it into a sentence. The two share this
// transaction rather than a second call site so they can never disagree about
// what happened — see internal/activity's package doc.
func Write(ctx context.Context, q *db.Queries, eventType string, listID *uuid.UUID, actor Actor, resource any, changes map[string]any) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	var listStr *string
	if listID != nil {
		s := listID.String()
		listStr = &s
	}
	env := Envelope{
		EventID: id.String(), Type: eventType, ListID: listStr, Actor: actor.Label,
		Timestamp: time.Now().UTC(), Resource: resource, Changes: changes,
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	var actorPtr *string
	if actor.Label != "" {
		actorPtr = &actor.Label
	}
	if _, err = q.InsertEvent(ctx, db.InsertEventParams{
		ID: id, Type: eventType, ListID: listID, Actor: actorPtr, Payload: payload,
	}); err != nil {
		return err
	}
	return activity.Write(ctx, q, actor.UserID, activityFields(ctx, q, eventType, listID, resource))
}

// activityFields turns an event into the sentence's raw material. Task events
// carry a list id but not a list name, so the name is looked up; list events
// already carry it in the resource, and looking it up again would fail for
// list.deleted, whose row is gone inside this very transaction.
func activityFields(ctx context.Context, q *db.Queries, eventType string, listID *uuid.UUID, resource any) activity.Fields {
	f := activity.Fields{Action: eventType, ListID: listID}
	res, _ := resource.(map[string]any)
	switch {
	case strings.HasPrefix(eventType, "task."):
		if v, ok := res["title"].(string); ok {
			f.Subject = v
		}
		if listID != nil {
			if name, err := q.GetListName(ctx, *listID); err == nil {
				f.ListName = name
			}
		}
	case strings.HasPrefix(eventType, "list."):
		if v, ok := res["name"].(string); ok {
			f.ListName = v
			f.Subject = v
		}
	case strings.HasPrefix(eventType, "member."):
		// A member resource is {list_id, user_id[, role]} (members.go) — no
		// name of anything, unlike task/list resources. The list name still
		// comes from a lookup, exactly like the task case above.
		if listID != nil {
			if name, err := q.GetListName(ctx, *listID); err == nil {
				f.ListName = name
			}
		}
		// The sentence template reads {who}; Subject keeps the row
		// self-describing without a join. resource is the pre-marshal value
		// events.Write was called with, so user_id is still a uuid.UUID here,
		// not a string. A lookup failure (the member was hard-deleted, or
		// GetUserByID's own soft-delete filter excludes them) must not fail
		// the membership change itself — the field is simply left empty and
		// the renderer degrades.
		if uid, ok := res["user_id"].(uuid.UUID); ok {
			if u, err := q.GetUserByID(ctx, uid); err == nil {
				who := u.Email
				if u.DisplayName != nil && *u.DisplayName != "" {
					who = *u.DisplayName
				}
				f.Subject = who
				f.Detail = map[string]any{"who": who}
			}
		}
	}
	return f
}
