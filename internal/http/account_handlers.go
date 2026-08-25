package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cobanov/punchcard/internal/service"
)

func (d Deps) registerAccountRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "me-export", Method: http.MethodGet, Path: "/v1/me/export",
		Summary: "Export all account data as JSON (GDPR)", Tags: []string{"me"}, Errors: []int{401, 403},
	}, func(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		// Settle authorization before the stream opens: once the body callback
		// runs the status line is already committed, so a rejection there could
		// only truncate a 200.
		if aerr := d.Domain.AuthorizeExport(p); aerr != nil {
			return nil, d.problem(ctx, aerr)
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			hctx.SetHeader("Content-Type", "application/json")
			hctx.SetHeader("Content-Disposition", `attachment; filename="punchcard-export.json"`)
			if err := d.Domain.ExportDataStream(hctx.Context(), p, hctx.BodyWriter()); err != nil {
				d.Logger.ErrorContext(hctx.Context(), "GDPR export stream failed", "error", err)
			}
		}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "me-delete", Method: http.MethodDelete, Path: "/v1/me",
		Summary: "Delete the account and all solely-owned data", Tags: []string{"me"},
		Errors: []int{401, 403, 409},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		SetCookie http.Cookie `header:"Set-Cookie"`
		Body      struct {
			OK bool `json:"ok"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if derr := d.Domain.DeleteAccount(ctx, p, clientIP(ctx)); derr != nil {
			var sle *service.SharedListError
			if errors.As(derr, &sle) {
				pe := NewProblem(http.StatusConflict, "shared_lists_owned",
					"transfer or delete the shared lists you own before deleting your account")
				return nil, pe
			}
			return nil, d.problem(ctx, derr)
		}
		out := &struct {
			SetCookie http.Cookie `header:"Set-Cookie"`
			Body      struct {
				OK bool `json:"ok"`
			}
		}{SetCookie: d.clearedSessionCookie()}
		out.Body.OK = true
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "me-invites-list", Method: http.MethodGet, Path: "/v1/me/invites",
		Summary: "List invites pending for my email", Tags: []string{"invites"}, Errors: []int{401},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Invites []MyInviteDTO `json:"invites"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := d.Domain.ListMyInvites(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Invites []MyInviteDTO `json:"invites"`
			}
		}{}
		out.Body.Invites = make([]MyInviteDTO, 0, len(rows))
		for _, r := range rows {
			out.Body.Invites = append(out.Body.Invites, MyInviteDTO{
				InviteDTO: inviteDTO(r.Invite), ListName: r.ListName,
			})
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "me-invites-accept", Method: http.MethodPost, Path: "/v1/me/invites/{id}/accept",
		Summary: "Accept a pending invite", Tags: []string{"invites"}, Errors: []int{401, 403, 404, 409},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*messageOutput, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.AcceptInvite(ctx, p, id, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return msgOK("invite accepted"), nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "me-invites-decline", Method: http.MethodPost, Path: "/v1/me/invites/{id}/decline",
		Summary: "Decline a pending invite", Tags: []string{"invites"}, Errors: []int{401, 403, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*messageOutput, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.DeclineInvite(ctx, p, id, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return msgOK("invite declined"), nil
	})
}

// MyInviteDTO is a pending invite enriched with the list name for the invitee.
type MyInviteDTO struct {
	InviteDTO
	ListName string `json:"list_name"`
}
