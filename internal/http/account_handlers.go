package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
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
}
