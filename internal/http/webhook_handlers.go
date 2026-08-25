package http

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cobanov/punchcard/internal/repo/db"
)

// WebhookDTO is the public webhook (never carries the secret).
type WebhookDTO struct {
	ID             string    `json:"id"`
	URL            string    `json:"url"`
	Events         []string  `json:"events"`
	Active         bool      `json:"active"`
	DisabledReason *string   `json:"disabled_reason,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

func webhookDTO(w db.Webhook) WebhookDTO {
	evs := []string{}
	_ = json.Unmarshal(w.Events, &evs)
	return WebhookDTO{
		ID: w.ID.String(), URL: w.Url, Events: evs,
		Active: w.Active, DisabledReason: w.DisabledReason, CreatedAt: w.CreatedAt,
	}
}

// DeliveryDTO is a delivery-log entry.
type DeliveryDTO struct {
	ID             string     `json:"id"`
	WebhookID      string     `json:"webhook_id"`
	EventID        string     `json:"event_id"`
	Attempt        int        `json:"attempt"`
	Status         string     `json:"status"`
	ResponseStatus *int       `json:"response_status,omitempty"`
	ResponseBody   *string    `json:"response_body_snippet,omitempty"`
	Error          *string    `json:"error,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

func deliveryDTO(d db.WebhookDelivery) DeliveryDTO {
	dto := DeliveryDTO{
		ID: d.ID.String(), WebhookID: d.WebhookID.String(), EventID: d.EventID.String(),
		Attempt: int(d.Attempt), Status: d.Status, ResponseBody: d.ResponseBodySnippet,
		Error: d.Error, DeliveredAt: d.DeliveredAt, NextRetryAt: d.NextRetryAt, CreatedAt: d.CreatedAt,
	}
	if d.ResponseStatus != nil {
		s := int(*d.ResponseStatus)
		dto.ResponseStatus = &s
	}
	return dto
}

type webhookBody struct {
	Body WebhookDTO
}

func (d Deps) registerWebhookRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "webhooks-list", Method: http.MethodGet, Path: "/v1/webhooks",
		Summary: "List webhooks on a list", Tags: []string{"webhooks"}, Errors: []int{401, 403, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct {
		Body struct {
			Webhooks []WebhookDTO `json:"webhooks"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		whs, err := d.Domain.ListWebhooks(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Webhooks []WebhookDTO `json:"webhooks"`
			}
		}{}
		out.Body.Webhooks = make([]WebhookDTO, 0, len(whs))
		for _, w := range whs {
			out.Body.Webhooks = append(out.Body.Webhooks, webhookDTO(w))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "webhooks-create", Method: http.MethodPost, Path: "/v1/webhooks",
		Summary: "Create a webhook", Tags: []string{"webhooks"}, DefaultStatus: http.StatusCreated,
		Errors: []int{401, 403, 404, 409, 422, 503},
	}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			URL    string   `json:"url" format:"uri"`
			Events []string `json:"events,omitempty" doc:"Event types to receive; empty = all."`
		}
	}) (*struct {
		Body struct {
			Webhook WebhookDTO `json:"webhook"`
			Secret  string     `json:"secret" doc:"Signing secret; shown only once."`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		wh, secret, err := d.Domain.CreateWebhook(ctx, p, in.Body.URL, in.Body.Events, clientIP(ctx))
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Webhook WebhookDTO `json:"webhook"`
				Secret  string     `json:"secret" doc:"Signing secret; shown only once."`
			}
		}{}
		out.Body.Webhook = webhookDTO(wh)
		out.Body.Secret = secret
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "webhooks-get", Method: http.MethodGet, Path: "/v1/webhooks/{id}",
		Summary: "Get a webhook", Tags: []string{"webhooks"}, Errors: []int{401, 403, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*webhookBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		wh, err := d.Domain.GetWebhook(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &webhookBody{Body: webhookDTO(wh)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "webhooks-update", Method: http.MethodPatch, Path: "/v1/webhooks/{id}",
		Summary: "Update a webhook (url/events/active)", Tags: []string{"webhooks"},
		Errors: []int{401, 403, 404, 422},
	}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			URL    string   `json:"url" format:"uri"`
			Events []string `json:"events,omitempty"`
			Active bool     `json:"active"`
		}
	}) (*webhookBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		wh, err := d.Domain.UpdateWebhook(ctx, p, id, in.Body.URL, in.Body.Events, in.Body.Active, clientIP(ctx))
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &webhookBody{Body: webhookDTO(wh)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "webhooks-delete", Method: http.MethodDelete, Path: "/v1/webhooks/{id}",
		Summary: "Delete a webhook", Tags: []string{"webhooks"}, DefaultStatus: http.StatusNoContent,
		Errors: []int{401, 403, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.DeleteWebhook(ctx, p, id, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "webhooks-deliveries", Method: http.MethodGet, Path: "/v1/webhooks/{id}/deliveries",
		Summary: "List delivery attempts", Tags: []string{"webhooks"}, Errors: []int{401, 403, 404},
	}, func(ctx context.Context, in *struct {
		ID    string `path:"id" format:"uuid"`
		Limit int    `query:"limit" minimum:"1" maximum:"100"`
	}) (*struct {
		Body struct {
			Deliveries []DeliveryDTO `json:"deliveries"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		deliveries, err := d.Domain.ListDeliveries(ctx, p, id, in.Limit)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Deliveries []DeliveryDTO `json:"deliveries"`
			}
		}{}
		out.Body.Deliveries = make([]DeliveryDTO, 0, len(deliveries))
		for _, dv := range deliveries {
			out.Body.Deliveries = append(out.Body.Deliveries, deliveryDTO(dv))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "webhooks-redeliver", Method: http.MethodPost,
		Path:    "/v1/webhooks/{id}/deliveries/{delivery_id}/redeliver",
		Summary: "Re-enqueue a delivery", Tags: []string{"webhooks"}, Errors: []int{401, 403, 404},
	}, func(ctx context.Context, in *struct {
		ID         string `path:"id" format:"uuid"`
		DeliveryID string `path:"delivery_id" format:"uuid"`
	}) (*messageOutput, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		did, err := parseUUID(in.DeliveryID)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.Redeliver(ctx, p, id, did, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return msgOK("redelivery enqueued"), nil
	})
}
