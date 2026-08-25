package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/oauth"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// LoginOAuth resolves the account behind a verified social identity and issues
// a session. Matching order mirrors bucketmark's proven flow: stable provider
// id first, then verified email (link), then create. Only provider-verified
// emails are trusted for matching/creation — otherwise an attacker who controls
// an unverified address at a provider could take over an existing account.
func (a *Auth) LoginOAuth(ctx context.Context, id oauth.Identity, ip, userAgent string) (db.User, SessionIssue, error) {
	if !id.EmailVerified || strings.TrimSpace(id.Email) == "" || id.ProviderUserID == "" {
		return db.User{}, SessionIssue{}, ErrOAuthUnverified
	}
	email := strings.ToLower(strings.TrimSpace(id.Email))

	user, err := a.resolveOAuthUser(ctx, id, email, ip)
	if err != nil {
		return db.User{}, SessionIssue{}, err
	}

	a.audit.Record(ctx, &user.ID, audit.ActionLoginSuccess, ip, map[string]any{"provider": id.Provider})
	sess, err := a.issueSession(ctx, user.ID, ip, userAgent)
	if err != nil {
		return db.User{}, SessionIssue{}, err
	}
	return user, sess, nil
}

// LoginOAuthNative resolves the account behind a verified social identity and
// returns a first-party device token (bearer) instead of a session cookie. Used
// by non-web clients whose OAuth round trip lands back on a native deep link.
func (a *Auth) LoginOAuthNative(ctx context.Context, id oauth.Identity, clientName, ip string) (db.User, string, error) {
	if !id.EmailVerified || strings.TrimSpace(id.Email) == "" || id.ProviderUserID == "" {
		return db.User{}, "", ErrOAuthUnverified
	}
	email := strings.ToLower(strings.TrimSpace(id.Email))
	user, err := a.resolveOAuthUser(ctx, id, email, ip)
	if err != nil {
		return db.User{}, "", err
	}
	a.audit.Record(ctx, &user.ID, audit.ActionLoginSuccess, ip, map[string]any{"provider": id.Provider, "native": true})
	token, err := a.MintDeviceToken(ctx, user.ID, clientName, ip)
	if err != nil {
		return db.User{}, "", err
	}
	return user, token, nil
}

func (a *Auth) resolveOAuthUser(ctx context.Context, id oauth.Identity, email, ip string) (db.User, error) {
	// 1) By stable provider id — the account was created/linked before.
	if u, err := a.getByProviderID(ctx, id.Provider, id.ProviderUserID); err == nil {
		return u, nil
	} else if !repo.IsNotFound(err) {
		return db.User{}, fmt.Errorf("lookup provider id: %w", err)
	}

	// 2) By verified email — link this provider to the existing account.
	if u, err := a.store.GetUserByEmail(ctx, email); err == nil {
		if err := a.linkProviderID(ctx, id.Provider, u.ID, id.ProviderUserID); err != nil {
			return db.User{}, fmt.Errorf("link provider id: %w", err)
		}
		return a.store.GetUserByID(ctx, u.ID)
	} else if !repo.IsNotFound(err) {
		return db.User{}, fmt.Errorf("lookup email: %w", err)
	}

	// 3) Create a fresh OAuth-only account (+ default Inbox list) atomically.
	return a.createOAuthUser(ctx, id, email, ip)
}

func (a *Auth) getByProviderID(ctx context.Context, provider, providerUserID string) (db.User, error) {
	switch provider {
	case oauth.ProviderGoogle:
		return a.store.GetUserByGoogleSub(ctx, &providerUserID)
	case oauth.ProviderGitHub:
		return a.store.GetUserByGithubID(ctx, &providerUserID)
	case oauth.ProviderApple:
		return a.store.GetUserByAppleSub(ctx, &providerUserID)
	default:
		return db.User{}, fmt.Errorf("unknown provider %q", provider)
	}
}

func (a *Auth) linkProviderID(ctx context.Context, provider string, userID uuid.UUID, providerUserID string) error {
	switch provider {
	case oauth.ProviderGoogle:
		return a.store.LinkGoogleSub(ctx, db.LinkGoogleSubParams{ID: userID, GoogleSub: &providerUserID})
	case oauth.ProviderGitHub:
		return a.store.LinkGithubID(ctx, db.LinkGithubIDParams{ID: userID, GithubID: &providerUserID})
	case oauth.ProviderApple:
		return a.store.LinkAppleSub(ctx, db.LinkAppleSubParams{ID: userID, AppleSub: &providerUserID})
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}
}

func (a *Auth) createOAuthUser(ctx context.Context, id oauth.Identity, email, ip string) (db.User, error) {
	uid, err := uuid.NewV7()
	if err != nil {
		return db.User{}, fmt.Errorf("new uuid: %w", err)
	}
	// Unusable password: a random secret we immediately discard. The account can
	// only ever be reached through OAuth (or a password-reset the user requests).
	secret, err := auth.GenerateSecret(32)
	if err != nil {
		return db.User{}, fmt.Errorf("generate secret: %w", err)
	}
	hash, err := auth.HashPassword(secret)
	if err != nil {
		return db.User{}, fmt.Errorf("hash password: %w", err)
	}

	var gsub, ghid, asub *string
	switch id.Provider {
	case oauth.ProviderGoogle:
		gsub = &id.ProviderUserID
	case oauth.ProviderGitHub:
		ghid = &id.ProviderUserID
	case oauth.ProviderApple:
		asub = &id.ProviderUserID
	}

	var user db.User
	err = a.store.WithTx(ctx, func(q *db.Queries) error {
		u, e := q.CreateOAuthUser(ctx, db.CreateOAuthUserParams{
			ID: uid, Email: email, PasswordHash: hash, GoogleSub: gsub, GithubID: ghid, AppleSub: asub,
		})
		if e != nil {
			return e
		}
		user = u
		_, e = createDefaultProjectTx(ctx, q, u.ID)
		return e
	})
	if err != nil {
		if repo.IsUniqueViolation(err) {
			// Concurrent first login for the same identity: the other request won
			// the insert, so read it back instead of failing.
			if u, e := a.getByProviderID(ctx, id.Provider, id.ProviderUserID); e == nil {
				return u, nil
			}
			return db.User{}, ErrEmailTaken
		}
		return db.User{}, fmt.Errorf("create oauth user: %w", err)
	}
	a.audit.Record(ctx, &user.ID, audit.ActionRegister, ip, map[string]any{"email": user.Email, "provider": id.Provider})
	return user, nil
}
