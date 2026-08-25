package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/service"
)

// sessionCookie builds the session cookie: HttpOnly, SameSite=Lax,
// Secure in production.
func (d Deps) sessionCookie(token string, expires time.Time) http.Cookie {
	// #nosec G124 -- Secure is set from config: true in production (served over
	// HTTPS via Caddy/Cloudflare), false in dev where Secure cookies over
	// http://localhost would be dropped. HttpOnly and SameSite are always set.
	return http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   d.Config.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	}
}

// csrfCookie builds the (JS-readable) CSRF token cookie. Not HttpOnly by design:
// the SPA must read it to echo the value in the X-CSRF-Token header.
func (d Deps) csrfCookie() *http.Cookie {
	// #nosec G124 -- Secure mirrors the session cookie (config-driven); not
	// HttpOnly on purpose so the SPA can read and resubmit the token.
	return &http.Cookie{
		Name:     csrfCookieName,
		Value:    d.csrfToken,
		Path:     "/",
		HttpOnly: false,
		Secure:   d.Config.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	}
}

func (d Deps) clearedSessionCookie() http.Cookie {
	c := d.sessionCookie("", time.Unix(0, 0)) // #nosec G124 -- delegates to sessionCookie
	c.MaxAge = -1
	return c
}

type messageOutput struct {
	Body struct {
		OK      bool   `json:"ok"`
		Message string `json:"message,omitempty"`
	}
}

func msgOK(message string) *messageOutput {
	o := &messageOutput{}
	o.Body.OK = true
	o.Body.Message = message
	return o
}

type authOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      UserDTO
}

// loginOutput represents either a completed session login (User set, cookie
// issued) or a 2FA challenge (TwoFactorRequired + Challenge, no cookie yet).
type loginOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		User              *UserDTO `json:"user,omitempty"`
		TwoFactorRequired bool     `json:"two_factor_required,omitempty"`
		Challenge         string   `json:"challenge,omitempty"`
	}
}

// nativeAuthOutput is the bearer-token equivalent of loginOutput for non-web
// clients: a token+user on success, or a 2FA challenge.
type nativeAuthOutput struct {
	Body struct {
		Token             string   `json:"token,omitempty" doc:"Bearer token for the Authorization header."`
		User              *UserDTO `json:"user,omitempty"`
		TwoFactorRequired bool     `json:"two_factor_required,omitempty"`
		Challenge         string   `json:"challenge,omitempty"`
	}
}

func nativeAuth(token string, user db.User) *nativeAuthOutput {
	out := &nativeAuthOutput{}
	out.Body.Token = token
	u := userDTO(user)
	out.Body.User = &u
	return out
}

type credentialsInput struct {
	Body struct {
		Email    string `json:"email" format:"email" maxLength:"254" doc:"Account email address."`
		Password string `json:"password" minLength:"8" maxLength:"200" doc:"Account password."`
	}
}

func (d Deps) registerAuthRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "auth-register",
		Method:        http.MethodPost,
		Path:          "/v1/auth/register",
		Summary:       "Register a new account",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusCreated,
		Errors:        []int{409, 422, 429},
	}, func(ctx context.Context, in *credentialsInput) (*authOutput, error) {
		user, sess, err := d.Auth.Register(ctx, in.Body.Email, in.Body.Password, clientIP(ctx), userAgent(ctx))
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &authOutput{SetCookie: d.sessionCookie(sess.Token, sess.ExpiresAt), Body: userDTO(user)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "auth-login",
		Method:      http.MethodPost,
		Path:        "/v1/auth/login",
		Summary:     "Log in and start a session",
		Tags:        []string{"auth"},
		Errors:      []int{401, 422, 429},
	}, func(ctx context.Context, in *credentialsInput) (*loginOutput, error) {
		user, sess, challenge, err := d.Auth.Login(ctx, in.Body.Email, in.Body.Password, clientIP(ctx), userAgent(ctx))
		if err != nil {
			if errors.Is(err, service.ErrTwoFactorRequired) {
				out := &loginOutput{}
				out.Body.TwoFactorRequired = true
				out.Body.Challenge = challenge
				return out, nil
			}
			return nil, d.problem(ctx, err)
		}
		out := &loginOutput{SetCookie: d.sessionCookie(sess.Token, sess.ExpiresAt)}
		u := userDTO(user)
		out.Body.User = &u
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "auth-2fa",
		Method:      http.MethodPost,
		Path:        "/v1/auth/2fa",
		Summary:     "Complete a cookie login with a two-factor code",
		Tags:        []string{"auth"},
		Errors:      []int{401, 422, 429},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Challenge string `json:"challenge" minLength:"1" maxLength:"200"`
			Code      string `json:"code" minLength:"1" maxLength:"20" doc:"TOTP code or a recovery code."`
		}
	}) (*loginOutput, error) {
		user, sess, err := d.Auth.Complete2FAWeb(ctx, in.Body.Challenge, in.Body.Code, clientIP(ctx), userAgent(ctx))
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &loginOutput{SetCookie: d.sessionCookie(sess.Token, sess.ExpiresAt)}
		u := userDTO(user)
		out.Body.User = &u
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "auth-native-login",
		Method:      http.MethodPost,
		Path:        "/v1/auth/native/login",
		Summary:     "Log in and receive a bearer token (non-web clients)",
		Description: "For desktop/mobile/extension clients that cannot use cookies. Returns a long-lived read/write bearer token to send as Authorization: Bearer.",
		Tags:        []string{"auth"},
		Errors:      []int{401, 422, 429},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Email      string `json:"email" format:"email" maxLength:"254" doc:"Account email address."`
			Password   string `json:"password" minLength:"8" maxLength:"200" doc:"Account password."`
			ClientName string `json:"client_name,omitempty" maxLength:"40" doc:"Human-readable client name shown in token management (e.g. 'Desktop')."`
		}
	}) (*nativeAuthOutput, error) {
		user, token, challenge, err := d.Auth.LoginNative(ctx, in.Body.Email, in.Body.Password, in.Body.ClientName, clientIP(ctx), userAgent(ctx))
		if err != nil {
			if errors.Is(err, service.ErrTwoFactorRequired) {
				out := &nativeAuthOutput{}
				out.Body.TwoFactorRequired = true
				out.Body.Challenge = challenge
				return out, nil
			}
			return nil, d.problem(ctx, err)
		}
		return nativeAuth(token, user), nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "auth-native-exchange",
		Method:      http.MethodPost,
		Path:        "/v1/auth/native/exchange",
		Summary:     "Exchange a one-time OAuth code for a bearer token (non-web clients)",
		Description: "Desktop/mobile OAuth returns a single-use code via deep link (never the token itself); exchange it here for the device bearer token.",
		Tags:        []string{"auth"},
		Errors:      []int{401, 422, 429},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Code string `json:"code" minLength:"1" maxLength:"200" doc:"The single-use code from the OAuth deep link."`
		}
	}) (*nativeAuthOutput, error) {
		token, err := d.Auth.ExchangeNativeCode(in.Body.Code)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		p, err := d.Auth.AuthenticatePAT(ctx, token)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		user, err := d.Auth.GetUser(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return nativeAuth(token, user), nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "auth-native-2fa",
		Method:      http.MethodPost,
		Path:        "/v1/auth/native/2fa",
		Summary:     "Complete a bearer login with a two-factor code (non-web clients)",
		Tags:        []string{"auth"},
		Errors:      []int{401, 422, 429},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Challenge string `json:"challenge" minLength:"1" maxLength:"200"`
			Code      string `json:"code" minLength:"1" maxLength:"20" doc:"TOTP code or a recovery code."`
		}
	}) (*nativeAuthOutput, error) {
		user, token, err := d.Auth.Complete2FANative(ctx, in.Body.Challenge, in.Body.Code, clientIP(ctx))
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return nativeAuth(token, user), nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "auth-logout",
		Method:      http.MethodPost,
		Path:        "/v1/auth/logout",
		Summary:     "Log out (revoke the current session)",
		Tags:        []string{"auth"},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		SetCookie http.Cookie `header:"Set-Cookie"`
		Body      struct {
			OK bool `json:"ok"`
		}
	}, error) {
		if err := d.Auth.Logout(ctx, PrincipalFrom(ctx), clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
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
		OperationID: "auth-verify-email",
		Method:      http.MethodPost,
		Path:        "/v1/auth/verify-email",
		Summary:     "Verify an email address with a token",
		Tags:        []string{"auth"},
		Errors:      []int{400, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Token string `json:"token" minLength:"1" doc:"The verification token from the email."`
		}
	}) (*messageOutput, error) {
		if err := d.Auth.VerifyEmail(ctx, in.Body.Token, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return msgOK("email verified"), nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "auth-password-reset-request",
		Method:        http.MethodPost,
		Path:          "/v1/auth/password-reset/request",
		Summary:       "Request a password reset email",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusAccepted,
		Errors:        []int{422, 429},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Email string `json:"email" format:"email" doc:"Account email address."`
		}
	}) (*messageOutput, error) {
		d.Auth.RequestPasswordReset(ctx, in.Body.Email, clientIP(ctx))
		return msgOK("if that account exists, a reset email has been sent"), nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "auth-password-reset-confirm",
		Method:      http.MethodPost,
		Path:        "/v1/auth/password-reset/confirm",
		Summary:     "Set a new password using a reset token",
		Tags:        []string{"auth"},
		Errors:      []int{400, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Token    string `json:"token" minLength:"1"`
			Password string `json:"password" minLength:"8" maxLength:"200"`
		}
	}) (*messageOutput, error) {
		if err := d.Auth.ConfirmPasswordReset(ctx, in.Body.Token, in.Body.Password, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return msgOK("password updated; please log in again"), nil
	})
}
