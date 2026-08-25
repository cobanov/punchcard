package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/events"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/webhooks"
)

var knownEventTypes = map[string]bool{
	events.TypeTaskCreated: true, events.TypeTaskUpdated: true, events.TypeTaskCompleted: true,
	events.TypeTaskDeleted: true, events.TypeTaskRestored: true,
	events.TypeListCreated: true, events.TypeListUpdated: true, events.TypeListDeleted: true,
	events.TypeMemberAdded: true, events.TypeMemberRemoved: true,
}

func validateEventTypes(types []string) error {
	for _, t := range types {
		if !knownEventTypes[t] {
			return NewError(422, "validation_failed", "unknown event type: "+t)
		}
	}
	return nil
}

func (d *Domain) requireWebhooks(p *auth.Principal) error {
	if d.cipher == nil {
		return NewError(503, "webhooks_unconfigured", "webhook support is not configured on this instance")
	}
	if !p.EmailVerified {
		return ErrEmailNotVerified
	}
	return nil
}

// CreateWebhook registers a webhook on a list (owner only, verified email).
// The returned secret is shown exactly once.
func (d *Domain) CreateWebhook(ctx context.Context, p *auth.Principal, listID uuid.UUID, url string, eventTypes []string, ip string) (db.Webhook, string, error) {
	if _, err := d.authorizeList(ctx, p, listID, RoleOwner, true); err != nil {
		return db.Webhook{}, "", err
	}
	if err := d.requireWebhooks(p); err != nil {
		return db.Webhook{}, "", err
	}
	if _, err := webhooks.ValidateURL(ctx, url, d.cfg.WebhookAllowHTTP); err != nil {
		return db.Webhook{}, "", NewError(422, "validation_failed", err.Error())
	}
	if err := validateEventTypes(eventTypes); err != nil {
		return db.Webhook{}, "", err
	}
	n, err := d.store.CountWebhooksForList(ctx, listID)
	if err != nil {
		return db.Webhook{}, "", fmt.Errorf("count webhooks: %w", err)
	}
	if int(n) >= d.cfg.MaxWebhooksPerList {
		return db.Webhook{}, "", ErrQuotaExceeded
	}

	secret, err := auth.GenerateSecret(24)
	if err != nil {
		return db.Webhook{}, "", err
	}
	secret = "whsec_" + secret
	enc, err := d.cipher.Encrypt([]byte(secret))
	if err != nil {
		return db.Webhook{}, "", fmt.Errorf("encrypt secret: %w", err)
	}
	if eventTypes == nil {
		eventTypes = []string{}
	}
	evJSON, _ := json.Marshal(eventTypes)
	id, err := uuid.NewV7()
	if err != nil {
		return db.Webhook{}, "", err
	}
	created := p.UserID
	wh, err := d.store.CreateWebhook(ctx, db.CreateWebhookParams{
		ID: id, ListID: listID, Url: url, SecretEncrypted: enc, Events: evJSON, CreatedBy: &created,
	})
	if err != nil {
		return db.Webhook{}, "", fmt.Errorf("create webhook: %w", err)
	}
	d.audit.Record(ctx, &p.UserID, audit.ActionWebhookChange, ip, map[string]any{"webhook_id": wh.ID, "list_id": listID, "action": "create"})
	return wh, secret, nil
}

// ListWebhooks returns a list's webhooks (owner only).
func (d *Domain) ListWebhooks(ctx context.Context, p *auth.Principal, listID uuid.UUID) ([]db.Webhook, error) {
	if _, err := d.authorizeList(ctx, p, listID, RoleOwner, false); err != nil {
		return nil, err
	}
	whs, err := d.store.ListWebhooksForList(ctx, db.ListWebhooksForListParams{
		ListID: listID, Lim: quotaLimit(d.cfg.MaxWebhooksPerList),
	})
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	return whs, nil
}

// getOwnedWebhook loads a webhook and verifies the principal owns its list.
func (d *Domain) getOwnedWebhook(ctx context.Context, p *auth.Principal, webhookID uuid.UUID, write bool) (db.Webhook, error) {
	wh, err := d.store.GetWebhook(ctx, webhookID)
	if err != nil {
		if repo.IsNotFound(err) {
			return db.Webhook{}, ErrNotFound
		}
		return db.Webhook{}, fmt.Errorf("get webhook: %w", err)
	}
	if _, err := d.authorizeList(ctx, p, wh.ListID, RoleOwner, write); err != nil {
		return db.Webhook{}, ErrNotFound // hide existence from non-owners
	}
	return wh, nil
}

// GetWebhook returns a single webhook (owner only).
func (d *Domain) GetWebhook(ctx context.Context, p *auth.Principal, webhookID uuid.UUID) (db.Webhook, error) {
	return d.getOwnedWebhook(ctx, p, webhookID, false)
}

// UpdateWebhook updates url/events/active (owner only).
func (d *Domain) UpdateWebhook(ctx context.Context, p *auth.Principal, webhookID uuid.UUID, url string, eventTypes []string, active bool, ip string) (db.Webhook, error) {
	wh, err := d.getOwnedWebhook(ctx, p, webhookID, true)
	if err != nil {
		return db.Webhook{}, err
	}
	if _, err := webhooks.ValidateURL(ctx, url, d.cfg.WebhookAllowHTTP); err != nil {
		return db.Webhook{}, NewError(422, "validation_failed", err.Error())
	}
	if err := validateEventTypes(eventTypes); err != nil {
		return db.Webhook{}, err
	}
	if eventTypes == nil {
		eventTypes = []string{}
	}
	evJSON, _ := json.Marshal(eventTypes)
	updated, err := d.store.UpdateWebhook(ctx, db.UpdateWebhookParams{
		ID: wh.ID, Url: url, Events: evJSON, Active: active,
	})
	if err != nil {
		return db.Webhook{}, fmt.Errorf("update webhook: %w", err)
	}
	d.audit.Record(ctx, &p.UserID, audit.ActionWebhookChange, ip, map[string]any{"webhook_id": wh.ID, "action": "update"})
	return updated, nil
}

// DeleteWebhook removes a webhook (owner only).
func (d *Domain) DeleteWebhook(ctx context.Context, p *auth.Principal, webhookID uuid.UUID, ip string) error {
	wh, err := d.getOwnedWebhook(ctx, p, webhookID, true)
	if err != nil {
		return err
	}
	if _, err := d.store.DeleteWebhook(ctx, db.DeleteWebhookParams{ID: wh.ID, ListID: wh.ListID}); err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	d.audit.Record(ctx, &p.UserID, audit.ActionWebhookChange, ip, map[string]any{"webhook_id": wh.ID, "action": "delete"})
	return nil
}

// ListDeliveries returns recent delivery attempts for a webhook (owner only).
func (d *Domain) ListDeliveries(ctx context.Context, p *auth.Principal, webhookID uuid.UUID, limit int) ([]db.WebhookDelivery, error) {
	wh, err := d.getOwnedWebhook(ctx, p, webhookID, false)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxLimit {
		limit = defaultLimit
	}
	deliveries, err := d.store.ListDeliveriesForWebhook(ctx, db.ListDeliveriesForWebhookParams{WebhookID: wh.ID, Limit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	return deliveries, nil
}

// Redeliver enqueues a fresh delivery attempt for a past delivery's event.
func (d *Domain) Redeliver(ctx context.Context, p *auth.Principal, webhookID, deliveryID uuid.UUID, ip string) error {
	wh, err := d.getOwnedWebhook(ctx, p, webhookID, true)
	if err != nil {
		return err
	}
	del, err := d.store.GetDelivery(ctx, deliveryID)
	if err != nil {
		if repo.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("get delivery: %w", err)
	}
	if del.WebhookID != wh.ID {
		return ErrNotFound
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if _, err := d.store.CreateDelivery(ctx, db.CreateDeliveryParams{ID: id, WebhookID: wh.ID, EventID: del.EventID}); err != nil {
		return fmt.Errorf("create redelivery: %w", err)
	}
	return nil
}
