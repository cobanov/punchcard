// Package events implements the transactional outbox. Write is called inside
// the same transaction as a domain mutation so an event is persisted atomically
// with the change; a background dispatcher then fans out to webhooks and SSE.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/repo/db"
)

// Event type constants.
const (
	TypeProjectCreated  = "project.created"
	TypeProjectUpdated  = "project.updated"
	TypeProjectArchived = "project.archived"
	TypeProjectDeleted  = "project.deleted"

	TypeSessionStarted = "session.started"
	TypeSessionStopped = "session.stopped"
	TypeSessionUpdated = "session.updated"
	TypeSessionDeleted = "session.deleted"

	TypeCommitsAttached = "commits.attached"
	TypeGitHubFailed    = "github.scan_failed"
)

// Envelope is the delivered event payload: the full resource plus metadata.
type Envelope struct {
	EventID   string         `json:"event_id"`
	Type      string         `json:"type"`
	ProjectID *string        `json:"project_id,omitempty"`
	Actor     string         `json:"actor,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Resource  any            `json:"resource,omitempty"`
	Changes   map[string]any `json:"changes,omitempty"`
}

// Actor identifies who caused an event. Label is the audit-trail string
// ("user:<uuid>" or "token:<uuid>"); UserID is the account it belongs to. They
// are separate because a token's label names the token while the feed is scoped
// by account — parsing the id back out of the label would be a string contract
// where a field will do.
type Actor struct {
	Label  string
	UserID uuid.UUID
}

// Write inserts an outbox event within the given transaction's queries.
//
// Unlike helva, where events were scoped to a shared list, punchcard scopes
// them to the account: a project belongs to exactly one user, so the SSE hub
// and the webhook dispatcher both filter on user_id. project_id is carried
// alongside so a subscriber can tell which project moved without unpacking the
// payload.
func Write(ctx context.Context, q *db.Queries, eventType string, projectID *uuid.UUID, actor Actor, resource any, changes map[string]any) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	var projStr *string
	if projectID != nil {
		s := projectID.String()
		projStr = &s
	}
	env := Envelope{
		EventID: id.String(), Type: eventType, ProjectID: projStr, Actor: actor.Label,
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
	_, err = q.InsertEvent(ctx, db.InsertEventParams{
		ID:        id,
		Type:      eventType,
		UserID:    actor.UserID,
		ProjectID: projectID,
		Actor:     actorPtr,
		Payload:   payload,
	})
	return err
}
