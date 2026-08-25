package http

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/service"
)

func parseUUID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, NewProblem(http.StatusNotFound, "not_found", "resource not found")
	}
	return id, nil
}

type userBody struct {
	Body UserDTO
}

func (d Deps) registerMeRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "me-get", Method: http.MethodGet, Path: "/v1/me",
		Summary: "Get the current account", Tags: []string{"me"}, Errors: []int{401},
	}, func(ctx context.Context, _ *struct{}) (*userBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		u, err := d.Auth.GetUser(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &userBody{Body: userDTO(u)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "me-update", Method: http.MethodPatch, Path: "/v1/me",
		Summary: "Update the current account (email / password)", Tags: []string{"me"},
		Errors: []int{401, 403, 409, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Email           *string `json:"email,omitempty" format:"email" doc:"New email (re-triggers verification)."`
			DisplayName     *string `json:"display_name,omitempty" maxLength:"100" doc:"Display name shown in the UI."`
			AvatarURL       *string `json:"avatar_url,omitempty" maxLength:"700000" doc:"Profile photo as a data:image/... URL, or empty string to clear."`
			CurrentPassword *string `json:"current_password,omitempty" doc:"Required when changing password."`
			NewPassword     *string `json:"new_password,omitempty" minLength:"8" maxLength:"200"`
		}
	}) (*userBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if in.Body.DisplayName != nil {
			if _, err := d.Auth.UpdateProfile(ctx, p, *in.Body.DisplayName, clientIP(ctx)); err != nil {
				return nil, d.problem(ctx, err)
			}
		}
		if in.Body.AvatarURL != nil {
			if _, err := d.Auth.UpdateAvatar(ctx, p, *in.Body.AvatarURL, clientIP(ctx)); err != nil {
				return nil, d.problem(ctx, err)
			}
		}
		if in.Body.NewPassword != nil {
			if in.Body.CurrentPassword == nil {
				return nil, NewProblem(422, "validation_failed", "current_password is required to change the password")
			}
			if err := d.Auth.ChangePassword(ctx, p, *in.Body.CurrentPassword, *in.Body.NewPassword, clientIP(ctx)); err != nil {
				return nil, d.problem(ctx, err)
			}
		}
		if in.Body.Email != nil {
			if _, err := d.Auth.UpdateEmail(ctx, p, *in.Body.Email, clientIP(ctx)); err != nil {
				return nil, d.problem(ctx, err)
			}
		}
		u, err := d.Auth.GetUser(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &userBody{Body: userDTO(u)}, nil
	})

	// --- two-factor auth (TOTP) ---
	huma.Register(api, huma.Operation{
		OperationID: "me-2fa-setup", Method: http.MethodPost, Path: "/v1/me/2fa/setup",
		Summary: "Begin two-factor enrollment (returns a secret + otpauth URI)", Tags: []string{"me"},
		Errors: []int{401, 403, 409},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Secret string `json:"secret" doc:"Base32 secret to enter manually."`
			URI    string `json:"uri" doc:"otpauth:// provisioning URI for a QR code."`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		setup, err := d.Auth.SetupTOTP(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Secret string `json:"secret" doc:"Base32 secret to enter manually."`
				URI    string `json:"uri" doc:"otpauth:// provisioning URI for a QR code."`
			}
		}{}
		out.Body.Secret = setup.Secret
		out.Body.URI = setup.URI
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "me-2fa-enable", Method: http.MethodPost, Path: "/v1/me/2fa/enable",
		Summary: "Enable 2FA after verifying the first code (returns recovery codes)", Tags: []string{"me"},
		Errors: []int{401, 403, 409, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Code string `json:"code" minLength:"1" maxLength:"20"`
		}
	}) (*struct {
		Body struct {
			RecoveryCodes []string `json:"recovery_codes" doc:"Single-use recovery codes; shown only once."`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		codes, err := d.Auth.EnableTOTP(ctx, p, in.Body.Code, clientIP(ctx))
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				RecoveryCodes []string `json:"recovery_codes" doc:"Single-use recovery codes; shown only once."`
			}
		}{}
		out.Body.RecoveryCodes = codes
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "me-2fa-disable", Method: http.MethodPost, Path: "/v1/me/2fa/disable",
		Summary: "Disable 2FA (requires a current code or recovery code)", Tags: []string{"me"},
		Errors: []int{401, 403, 409, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Code string `json:"code" minLength:"1" maxLength:"20"`
		}
	}) (*messageOutput, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if err := d.Auth.DisableTOTP(ctx, p, in.Body.Code, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return msgOK("two-factor auth disabled"), nil
	})

	// --- sessions ---
	huma.Register(api, huma.Operation{
		OperationID: "me-sessions-list", Method: http.MethodGet, Path: "/v1/me/sessions",
		Summary: "List active sessions", Tags: []string{"me"}, Errors: []int{401, 403},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Sessions []AuthSessionDTO `json:"sessions"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		sessions, err := d.Auth.ListSessions(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Sessions []AuthSessionDTO `json:"sessions"`
			}
		}{}
		out.Body.Sessions = make([]AuthSessionDTO, 0, len(sessions))
		for _, s := range sessions {
			out.Body.Sessions = append(out.Body.Sessions, authSessionDTO(s, p.SessionID))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "me-sessions-revoke", Method: http.MethodDelete, Path: "/v1/me/sessions/{id}",
		Summary: "Revoke a session", Tags: []string{"me"}, DefaultStatus: http.StatusNoContent,
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
		if err := d.Auth.RevokeSession(ctx, p, id, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})

	// --- tokens ---
	huma.Register(api, huma.Operation{
		OperationID: "tokens-list", Method: http.MethodGet, Path: "/v1/tokens",
		Summary: "List personal access tokens", Tags: []string{"tokens"}, Errors: []int{401, 403},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Tokens []TokenDTO `json:"tokens"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		toks, err := d.Auth.ListTokens(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Tokens []TokenDTO `json:"tokens"`
			}
		}{}
		out.Body.Tokens = make([]TokenDTO, 0, len(toks))
		for _, t := range toks {
			out.Body.Tokens = append(out.Body.Tokens, tokenDTO(t))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "tokens-create", Method: http.MethodPost, Path: "/v1/tokens",
		Summary: "Create a personal access token", Tags: []string{"tokens"},
		DefaultStatus: http.StatusCreated, Errors: []int{401, 403, 409, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Name             string     `json:"name" minLength:"1" maxLength:"100"`
			Scope            string     `json:"scope" enum:"read,read_write"`
			ScopedProjectIDs []string   `json:"scoped_project_ids,omitempty" doc:"Restrict to these project ids; omit for all."`
			ExpiresAt        *time.Time `json:"expires_at,omitempty" doc:"Optional expiry, RFC 3339 UTC."`
		}
	}) (*struct {
		Body struct {
			Token  TokenDTO `json:"token"`
			Secret string   `json:"secret" doc:"The full token; shown only once."`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		listIDs := make([]uuid.UUID, 0, len(in.Body.ScopedProjectIDs))
		for _, s := range in.Body.ScopedProjectIDs {
			id, perr := uuid.Parse(s)
			if perr != nil {
				return nil, NewProblem(422, "validation_failed", "scoped_project_ids must be valid uuids")
			}
			listIDs = append(listIDs, id)
		}
		tok, secret, err := d.Auth.CreateToken(ctx, p, service.CreateTokenInput{
			Name: in.Body.Name, Scope: in.Body.Scope, ScopedProjectIDs: listIDs, ExpiresAt: in.Body.ExpiresAt,
		}, clientIP(ctx))
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Token  TokenDTO `json:"token"`
				Secret string   `json:"secret" doc:"The full token; shown only once."`
			}
		}{}
		out.Body.Token = tokenDTO(tok)
		out.Body.Secret = secret
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "tokens-revoke", Method: http.MethodDelete, Path: "/v1/tokens/{id}",
		Summary: "Revoke a personal access token", Tags: []string{"tokens"},
		DefaultStatus: http.StatusNoContent, Errors: []int{401, 403, 404},
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
		if err := d.Auth.RevokeToken(ctx, p, id, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})
}
