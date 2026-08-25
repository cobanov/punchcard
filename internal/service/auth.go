package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/email"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/webhooks"
)

// Session lifetimes: sliding 30d, absolute cap 90d.
const (
	sessionSliding  = 30 * 24 * time.Hour
	sessionAbsolute = 90 * 24 * time.Hour
	verifyTokenTTL  = 24 * time.Hour
	resetTokenTTL   = 1 * time.Hour

	minPasswordLen = 8
	maxPasswordLen = 200
)

// Auth is the identity/authentication service. It owns password,
// session, PAT, and email-token logic and is the single place authentication
// and account-level authorization decisions are made.
type Auth struct {
	store       *repo.Store
	email       email.Sender
	audit       *audit.Logger
	log         *slog.Logger
	cfg         *config.Config
	nativeCodes *nativeCodeStore
	twoFactor   *twoFactorStore
	totpCipher  *webhooks.Cipher
}

// NewAuth builds the Auth service.
func NewAuth(store *repo.Store, sender email.Sender, auditor *audit.Logger, log *slog.Logger, cfg *config.Config) *Auth {
	// Derive an AES key for encrypting TOTP secrets from APP_SECRET so 2FA works
	// without requiring the separate webhook key. base64(sha256(secret)) = 32 bytes.
	secret := ""
	if cfg != nil {
		secret = cfg.AppSecret
	}
	sum := sha256.Sum256([]byte("totp:" + secret))
	cipher, _ := webhooks.NewCipher(base64.StdEncoding.EncodeToString(sum[:]))
	return &Auth{
		store: store, email: sender, audit: auditor, log: log, cfg: cfg,
		nativeCodes: newNativeCodeStore(), twoFactor: newTwoFactorStore(), totpCipher: cipher,
	}
}

// SessionIssue is a freshly minted session (the raw token is returned once, to
// be set as a cookie by the transport layer).
type SessionIssue struct {
	Token     string
	ExpiresAt time.Time
}

// Register creates a user, sends a verification email, and logs them in.
func (a *Auth) Register(ctx context.Context, emailAddr, password, ip, userAgent string) (db.User, SessionIssue, error) {
	addr, err := normalizeEmail(emailAddr)
	if err != nil {
		return db.User{}, SessionIssue{}, err
	}
	if err := validatePassword(password); err != nil {
		return db.User{}, SessionIssue{}, err
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return db.User{}, SessionIssue{}, fmt.Errorf("hash password: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return db.User{}, SessionIssue{}, fmt.Errorf("new uuid: %w", err)
	}
	// Create the account and its default project atomically.
	var user db.User
	err = a.store.WithTx(ctx, func(q *db.Queries) error {
		u, e := q.CreateUser(ctx, db.CreateUserParams{ID: id, Email: addr, PasswordHash: hash})
		if e != nil {
			return e
		}
		user = u
		_, e = createDefaultProjectTx(ctx, q, u.ID)
		return e
	})
	if err != nil {
		if repo.IsUniqueViolation(err) {
			return db.User{}, SessionIssue{}, ErrEmailTaken
		}
		return db.User{}, SessionIssue{}, fmt.Errorf("create user: %w", err)
	}

	a.sendVerificationEmail(ctx, user)
	a.audit.Record(ctx, &user.ID, audit.ActionRegister, ip, map[string]any{"email": user.Email})

	sess, err := a.issueSession(ctx, user.ID, ip, userAgent)
	if err != nil {
		return db.User{}, SessionIssue{}, err
	}
	return user, sess, nil
}

// Login verifies credentials and issues a session.
// Login verifies credentials and issues a session. If the account has 2FA
// enabled it returns a non-empty challenge string with ErrTwoFactorRequired
// instead of a session; the caller then completes login via Complete2FAWeb.
func (a *Auth) Login(ctx context.Context, emailAddr, password, ip, userAgent string) (db.User, SessionIssue, string, error) {
	addr, err := normalizeEmail(emailAddr)
	if err != nil {
		return db.User{}, SessionIssue{}, "", ErrInvalidCredentials
	}
	user, err := a.store.GetUserByEmail(ctx, addr)
	if err != nil {
		if repo.IsNotFound(err) {
			// Still spend time hashing to blunt user-enumeration timing.
			_, _ = auth.HashPassword(password)
			a.audit.Record(ctx, nil, audit.ActionLoginFailure, ip, map[string]any{"email": addr})
			return db.User{}, SessionIssue{}, "", ErrInvalidCredentials
		}
		return db.User{}, SessionIssue{}, "", fmt.Errorf("get user: %w", err)
	}

	ok, err := auth.VerifyPassword(password, user.PasswordHash)
	if err != nil || !ok {
		a.audit.Record(ctx, &user.ID, audit.ActionLoginFailure, ip, nil)
		return db.User{}, SessionIssue{}, "", ErrInvalidCredentials
	}

	// 2FA gate: password is correct but a TOTP code is still owed. Issue a
	// short-lived challenge instead of a session.
	if user.TotpEnabled {
		challenge, cerr := a.issue2FAChallenge(user.ID, false, "")
		if cerr != nil {
			return db.User{}, SessionIssue{}, "", cerr
		}
		return user, SessionIssue{}, challenge, ErrTwoFactorRequired
	}

	a.audit.Record(ctx, &user.ID, audit.ActionLoginSuccess, ip, nil)
	sess, err := a.issueSession(ctx, user.ID, ip, userAgent)
	if err != nil {
		return db.User{}, SessionIssue{}, "", err
	}
	return user, sess, "", nil
}

// LoginNative verifies credentials and returns a first-party device token
// (bearer), instead of a browser session cookie. Used by non-web clients
// (desktop/mobile/extension) that authenticate with email + password.
// The returned challenge is non-empty (with ErrTwoFactorRequired) when the
// account has 2FA enabled; the caller completes login via Complete2FANative.
func (a *Auth) LoginNative(ctx context.Context, emailAddr, password, clientName, ip, userAgent string) (db.User, string, string, error) {
	addr, err := normalizeEmail(emailAddr)
	if err != nil {
		return db.User{}, "", "", ErrInvalidCredentials
	}
	user, err := a.store.GetUserByEmail(ctx, addr)
	if err != nil {
		if repo.IsNotFound(err) {
			_, _ = auth.HashPassword(password) // blunt user-enumeration timing
			a.audit.Record(ctx, nil, audit.ActionLoginFailure, ip, map[string]any{"email": addr})
			return db.User{}, "", "", ErrInvalidCredentials
		}
		return db.User{}, "", "", fmt.Errorf("get user: %w", err)
	}
	ok, err := auth.VerifyPassword(password, user.PasswordHash)
	if err != nil || !ok {
		a.audit.Record(ctx, &user.ID, audit.ActionLoginFailure, ip, nil)
		return db.User{}, "", "", ErrInvalidCredentials
	}
	if user.TotpEnabled {
		challenge, cerr := a.issue2FAChallenge(user.ID, true, clientName)
		if cerr != nil {
			return db.User{}, "", "", cerr
		}
		return user, "", challenge, ErrTwoFactorRequired
	}
	a.audit.Record(ctx, &user.ID, audit.ActionLoginSuccess, ip, map[string]any{"native": true})
	token, err := a.MintDeviceToken(ctx, user.ID, clientName, ip)
	if err != nil {
		return db.User{}, "", "", err
	}
	return user, token, "", nil
}

// MintDeviceToken issues a long-lived read/write PAT for a first-party client
// that has just proven identity through the interactive native sign-in flow
// (OAuth or password). Unlike user-managed PATs (CreateToken), it does not
// require a session and is not counted against the per-user PAT quota, because
// it is minted by the auth flow itself, not by a caller wielding a token.
func (a *Auth) MintDeviceToken(ctx context.Context, userID uuid.UUID, clientName, ip string) (string, error) {
	full, prefix, err := auth.NewPAT()
	if err != nil {
		return "", fmt.Errorf("mint token: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("new uuid: %w", err)
	}
	name := strings.TrimSpace(clientName)
	if name == "" {
		name = "Device"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	// Enforce the per-user token quota. Since device tokens are minted by the
	// login flow (not a caller who could self-limit), prune the oldest active
	// tokens to make room rather than rejecting the sign-in.
	if count, err := a.store.CountActiveTokens(ctx, userID); err == nil && int(count) >= a.cfg.MaxPATsPerUser {
		if toks, err := a.store.ListTokensByUser(ctx, userID); err == nil {
			sort.Slice(toks, func(i, j int) bool { return toks[i].CreatedAt.Before(toks[j].CreatedAt) })
			for i := 0; i <= int(count)-a.cfg.MaxPATsPerUser && i < len(toks); i++ {
				_, _ = a.store.RevokeAPIToken(ctx, db.RevokeAPITokenParams{ID: toks[i].ID, UserID: userID})
			}
		}
	}
	if _, err := a.store.CreateAPIToken(ctx, db.CreateAPITokenParams{
		ID: id, UserID: userID, Name: name,
		TokenHash: auth.HashToken(full), TokenPrefix: prefix,
		Scope: auth.ScopeReadWrite, ScopedProjectIds: nil, ExpiresAt: nil,
		Kind: auth.TokenKindDevice,
	}); err != nil {
		return "", fmt.Errorf("create device token: %w", err)
	}
	a.audit.Record(ctx, &userID, audit.ActionTokenCreate, ip, map[string]any{"kind": "device", "name": name})
	return full, nil
}

// Logout revokes the current session.
func (a *Auth) Logout(ctx context.Context, p *auth.Principal, ip string) error {
	if p == nil {
		return nil
	}
	switch {
	case p.SessionID != nil:
		if _, err := a.store.RevokeAuthSession(ctx, db.RevokeAuthSessionParams{ID: *p.SessionID, UserID: p.UserID}); err != nil {
			return fmt.Errorf("revoke session: %w", err)
		}
	case p.TokenID != nil:
		// Desktop/native clients authenticate with a device PAT; logging out must
		// revoke it server-side so a copied token can't outlive the session.
		if _, err := a.store.RevokeAPIToken(ctx, db.RevokeAPITokenParams{ID: *p.TokenID, UserID: p.UserID}); err != nil {
			return fmt.Errorf("revoke token: %w", err)
		}
	default:
		return nil
	}
	a.audit.Record(ctx, &p.UserID, audit.ActionLogout, ip, nil)
	return nil
}

// VerifyEmail consumes a verification token and marks the email verified.
func (a *Auth) VerifyEmail(ctx context.Context, token, ip string) error {
	et, err := a.store.GetValidEmailToken(ctx, db.GetValidEmailTokenParams{
		TokenHash: auth.HashToken(token), Kind: "verify_email",
	})
	if err != nil {
		if repo.IsNotFound(err) {
			return ErrTokenInvalid
		}
		return fmt.Errorf("get email token: %w", err)
	}
	err = a.store.WithTx(ctx, func(q *db.Queries) error {
		n, err := q.MarkEmailTokenUsed(ctx, et.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrTokenInvalid
		}
		return q.MarkEmailVerified(ctx, et.UserID)
	})
	if err != nil {
		return err
	}
	a.audit.Record(ctx, &et.UserID, audit.ActionEmailVerified, ip, nil)
	return nil
}

// RequestPasswordReset always reports success, to avoid leaking which emails
// have accounts. If the account exists, a reset email is sent.
func (a *Auth) RequestPasswordReset(ctx context.Context, emailAddr, ip string) {
	addr, err := normalizeEmail(emailAddr)
	if err != nil {
		return
	}
	user, err := a.store.GetUserByEmail(ctx, addr)
	if err != nil {
		return
	}
	token, err := a.newEmailToken(ctx, user.ID, "password_reset", resetTokenTTL)
	if err != nil {
		a.log.WarnContext(ctx, "password reset token creation failed", "error", err)
		return
	}
	sendAsync(ctx, a.email, a.log, email.Message{
		To:      user.Email,
		Subject: "Reset your punchcard password",
		Text:    a.resetEmailBody(token),
	})
}

// ConfirmPasswordReset sets a new password, revokes all sessions, and consumes
// the token.
func (a *Auth) ConfirmPasswordReset(ctx context.Context, token, newPassword, ip string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	et, err := a.store.GetValidEmailToken(ctx, db.GetValidEmailTokenParams{
		TokenHash: auth.HashToken(token), Kind: "password_reset",
	})
	if err != nil {
		if repo.IsNotFound(err) {
			return ErrTokenInvalid
		}
		return fmt.Errorf("get reset token: %w", err)
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	err = a.store.WithTx(ctx, func(q *db.Queries) error {
		n, err := q.MarkEmailTokenUsed(ctx, et.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrTokenInvalid
		}
		if err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: et.UserID, PasswordHash: hash}); err != nil {
			return err
		}
		return q.RevokeAllUserAuthSessions(ctx, et.UserID)
	})
	if err != nil {
		return err
	}
	a.audit.Record(ctx, &et.UserID, audit.ActionPasswordReset, ip, nil)
	return nil
}

// --- Personal access tokens (session-only management) ---

// CreateTokenInput describes a new PAT.
type CreateTokenInput struct {
	Name          string
	Scope         string
	ScopedProjectIDs []uuid.UUID
	ExpiresAt     *time.Time
}

// CreateToken mints a PAT. Returns the token row and the full secret (shown
// once). Management of tokens requires an interactive session.
func (a *Auth) CreateToken(ctx context.Context, p *auth.Principal, in CreateTokenInput, ip string) (db.ApiToken, string, error) {
	if !p.FirstParty() {
		return db.ApiToken{}, "", ErrSessionOnly
	}
	if in.Scope != auth.ScopeRead && in.Scope != auth.ScopeReadWrite {
		return db.ApiToken{}, "", NewError(422, "validation_failed", "scope must be 'read' or 'read_write'")
	}
	if strings.TrimSpace(in.Name) == "" {
		return db.ApiToken{}, "", NewError(422, "validation_failed", "token name is required")
	}
	count, err := a.store.CountActiveTokens(ctx, p.UserID)
	if err != nil {
		return db.ApiToken{}, "", fmt.Errorf("count tokens: %w", err)
	}
	if int(count) >= a.cfg.MaxPATsPerUser {
		return db.ApiToken{}, "", ErrQuotaExceeded
	}

	full, prefix, err := auth.NewPAT()
	if err != nil {
		return db.ApiToken{}, "", fmt.Errorf("mint token: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return db.ApiToken{}, "", fmt.Errorf("new uuid: %w", err)
	}
	tok, err := a.store.CreateAPIToken(ctx, db.CreateAPITokenParams{
		ID: id, UserID: p.UserID, Name: strings.TrimSpace(in.Name),
		TokenHash: auth.HashToken(full), TokenPrefix: prefix,
		Scope: in.Scope, ScopedProjectIds: in.ScopedProjectIDs, ExpiresAt: in.ExpiresAt,
		// A token the user issued for a program. It never reaches the account
		// plane, however wide its scope — see Principal.FirstParty.
		Kind: auth.TokenKindPAT,
	})
	if err != nil {
		return db.ApiToken{}, "", fmt.Errorf("create token: %w", err)
	}
	a.audit.Record(ctx, &p.UserID, audit.ActionTokenCreate, ip, map[string]any{"token_id": tok.ID, "scope": tok.Scope})
	return tok, full, nil
}

// ListTokens returns the user's active PATs (session-only).
func (a *Auth) ListTokens(ctx context.Context, p *auth.Principal) ([]db.ApiToken, error) {
	if !p.FirstParty() {
		return nil, ErrSessionOnly
	}
	toks, err := a.store.ListTokensByUser(ctx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	return toks, nil
}

// RevokeToken revokes one of the user's PATs (session-only).
func (a *Auth) RevokeToken(ctx context.Context, p *auth.Principal, id uuid.UUID, ip string) error {
	if !p.FirstParty() {
		return ErrSessionOnly
	}
	n, err := a.store.RevokeAPIToken(ctx, db.RevokeAPITokenParams{ID: id, UserID: p.UserID})
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	a.audit.Record(ctx, &p.UserID, audit.ActionTokenRevoke, ip, map[string]any{"token_id": id})
	return nil
}

// --- Sessions (session-only management) ---

// ListSessions lists the user's active sessions.
func (a *Auth) ListSessions(ctx context.Context, p *auth.Principal) ([]db.AuthSession, error) {
	if !p.FirstParty() {
		return nil, ErrSessionOnly
	}
	sessions, err := a.store.ListAuthSessionsByUser(ctx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return sessions, nil
}

// RevokeSession revokes one of the user's sessions.
func (a *Auth) RevokeSession(ctx context.Context, p *auth.Principal, id uuid.UUID, ip string) error {
	if !p.FirstParty() {
		return ErrSessionOnly
	}
	n, err := a.store.RevokeAuthSession(ctx, db.RevokeAuthSessionParams{ID: id, UserID: p.UserID})
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	a.audit.Record(ctx, &p.UserID, audit.ActionSessionRevoke, ip, map[string]any{"session_id": id})
	return nil
}

// GetUser fetches the account for /me.
func (a *Auth) GetUser(ctx context.Context, p *auth.Principal) (db.User, error) {
	user, err := a.store.GetUserByID(ctx, p.UserID)
	if err != nil {
		if repo.IsNotFound(err) {
			return db.User{}, ErrNotFound
		}
		return db.User{}, fmt.Errorf("get user: %w", err)
	}
	return user, nil
}

// ChangePassword verifies the current password, sets a new one, and revokes all
// sessions. Session-only.
func (a *Auth) ChangePassword(ctx context.Context, p *auth.Principal, current, newPassword, ip string) error {
	if !p.FirstParty() {
		return ErrSessionOnly
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	user, err := a.store.GetUserByID(ctx, p.UserID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	ok, err := auth.VerifyPassword(current, user.PasswordHash)
	if err != nil || !ok {
		return ErrInvalidCredentials
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	keep := uuid.Nil
	if p.SessionID != nil {
		keep = *p.SessionID
	}
	err = a.store.WithTx(ctx, func(q *db.Queries) error {
		if err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: p.UserID, PasswordHash: hash}); err != nil {
			return err
		}
		// Revoke every other session; the current one stays valid.
		return q.RevokeOtherUserAuthSessions(ctx, db.RevokeOtherUserAuthSessionsParams{UserID: p.UserID, ID: keep})
	})
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	a.audit.Record(ctx, &p.UserID, audit.ActionPasswordChange, ip, nil)
	return nil
}

// UpdateProfile sets the account display name. Session-only.
func (a *Auth) UpdateProfile(ctx context.Context, p *auth.Principal, displayName, ip string) (db.User, error) {
	if !p.FirstParty() {
		return db.User{}, ErrSessionOnly
	}
	name := strings.TrimSpace(displayName)
	if len(name) > 100 {
		return db.User{}, NewError(422, "validation_failed", "display name must be at most 100 characters")
	}
	var dn *string
	if name != "" {
		dn = &name
	}
	u, err := a.store.UpdateUserProfile(ctx, db.UpdateUserProfileParams{ID: p.UserID, DisplayName: dn})
	if err != nil {
		return db.User{}, fmt.Errorf("update profile: %w", err)
	}
	a.audit.Record(ctx, &p.UserID, "profile.change", ip, nil)
	return u, nil
}

// maxAvatarLen caps the stored data: URL. ~700 KB of base64 ≈ ~512 KB of image
// bytes — comfortable for a downscaled thumbnail, small enough to keep inline.
const maxAvatarLen = 700_000

// UpdateAvatar sets (or clears, when data is empty) the account profile photo.
// The photo is a self-contained data:image/... URL; no object store is involved.
// Session-only, matching the other profile mutations.
func (a *Auth) UpdateAvatar(ctx context.Context, p *auth.Principal, data, ip string) (db.User, error) {
	if !p.FirstParty() {
		return db.User{}, ErrSessionOnly
	}
	var avatar *string
	if s := strings.TrimSpace(data); s != "" {
		if len(s) > maxAvatarLen {
			return db.User{}, NewError(422, "validation_failed", "avatar image is too large (max ~512KB)")
		}
		if !strings.HasPrefix(s, "data:image/png;base64,") &&
			!strings.HasPrefix(s, "data:image/jpeg;base64,") &&
			!strings.HasPrefix(s, "data:image/webp;base64,") {
			return db.User{}, NewError(422, "validation_failed", "avatar must be a base64 PNG, JPEG, or WebP data URL")
		}
		avatar = &s
	}
	u, err := a.store.UpdateUserAvatar(ctx, db.UpdateUserAvatarParams{ID: p.UserID, AvatarUrl: avatar})
	if err != nil {
		return db.User{}, fmt.Errorf("update avatar: %w", err)
	}
	a.audit.Record(ctx, &p.UserID, "profile.change", ip, nil)
	return u, nil
}

// UpdateEmail changes the account email, resets verification, and sends a new
// verification email. Session-only.
func (a *Auth) UpdateEmail(ctx context.Context, p *auth.Principal, newEmail, ip string) (db.User, error) {
	if !p.FirstParty() {
		return db.User{}, ErrSessionOnly
	}
	addr, err := normalizeEmail(newEmail)
	if err != nil {
		return db.User{}, err
	}
	user, err := a.store.UpdateUserEmail(ctx, db.UpdateUserEmailParams{ID: p.UserID, Email: addr})
	if err != nil {
		if repo.IsUniqueViolation(err) {
			return db.User{}, ErrEmailTaken
		}
		return db.User{}, fmt.Errorf("update email: %w", err)
	}
	a.sendVerificationEmail(ctx, user)
	a.audit.Record(ctx, &p.UserID, "email.change", ip, map[string]any{"email": addr})
	return user, nil
}

// --- authentication helpers used by transport middleware ---

// AuthenticateSession resolves a session cookie token into a Principal and
// renews the sliding expiry (capped at the absolute expiry).
func (a *Auth) AuthenticateSession(ctx context.Context, token string) (*auth.Principal, error) {
	row, err := a.store.GetAuthSessionByHash(ctx, auth.HashToken(token))
	if err != nil {
		if repo.IsNotFound(err) {
			return nil, ErrUnauthenticated
		}
		return nil, fmt.Errorf("get session: %w", err)
	}
	newExp := time.Now().UTC().Add(sessionSliding)
	if newExp.After(row.AuthSession.AbsoluteExpiresAt) {
		newExp = row.AuthSession.AbsoluteExpiresAt
	}
	_ = a.store.TouchAuthSession(ctx, db.TouchAuthSessionParams{ID: row.AuthSession.ID, ExpiresAt: newExp})

	sid := row.AuthSession.ID
	return &auth.Principal{
		UserID:        row.User.ID,
		Email:         row.User.Email,
		EmailVerified: row.User.EmailVerifiedAt != nil,
		ViaSession:    true,
		SessionID:     &sid,
		Scope:         auth.ScopeReadWrite,
	}, nil
}

// AuthenticatePAT resolves a bearer PAT into a Principal.
func (a *Auth) AuthenticatePAT(ctx context.Context, token string) (*auth.Principal, error) {
	row, err := a.store.GetAPITokenByHash(ctx, auth.HashToken(token))
	if err != nil {
		if repo.IsNotFound(err) {
			return nil, ErrUnauthenticated
		}
		return nil, fmt.Errorf("get api token: %w", err)
	}
	_ = a.store.TouchAPIToken(ctx, row.ApiToken.ID)

	tid := row.ApiToken.ID
	return &auth.Principal{
		UserID:        row.User.ID,
		Email:         row.User.Email,
		EmailVerified: row.User.EmailVerifiedAt != nil,
		// A device token is punchcard's own client signing in, not an automation
		// the user handed a key to. See Principal.FirstParty.
		ViaDevice:     row.ApiToken.Kind == auth.TokenKindDevice,
		TokenID:       &tid,
		Scope:         row.ApiToken.Scope,
		ScopedProjectIDs: row.ApiToken.ScopedProjectIds,
	}, nil
}

// --- internal helpers ---

func (a *Auth) issueSession(ctx context.Context, userID uuid.UUID, ip, userAgent string) (SessionIssue, error) {
	token, err := auth.GenerateSecret(32)
	if err != nil {
		return SessionIssue{}, fmt.Errorf("generate session token: %w", err)
	}
	now := time.Now().UTC()
	id, err := uuid.NewV7()
	if err != nil {
		return SessionIssue{}, fmt.Errorf("new uuid: %w", err)
	}
	var ipp, uap *string
	if ip != "" {
		ipp = &ip
	}
	if userAgent != "" {
		uap = &userAgent
	}
	if _, err := a.store.CreateAuthSession(ctx, db.CreateAuthSessionParams{
		ID: id, UserID: userID, TokenHash: auth.HashToken(token),
		ExpiresAt: now.Add(sessionSliding), AbsoluteExpiresAt: now.Add(sessionAbsolute),
		Ip: ipp, UserAgent: uap,
	}); err != nil {
		return SessionIssue{}, fmt.Errorf("create session: %w", err)
	}
	return SessionIssue{Token: token, ExpiresAt: now.Add(sessionSliding)}, nil
}

func (a *Auth) newEmailToken(ctx context.Context, userID uuid.UUID, kind string, ttl time.Duration) (string, error) {
	token, err := auth.GenerateSecret(32)
	if err != nil {
		return "", err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	// One active token per (user, kind): clear old ones first.
	if err := a.store.DeleteUserEmailTokens(ctx, db.DeleteUserEmailTokensParams{UserID: userID, Kind: kind}); err != nil {
		return "", err
	}
	if _, err := a.store.CreateEmailToken(ctx, db.CreateEmailTokenParams{
		ID: id, UserID: userID, Kind: kind, TokenHash: auth.HashToken(token),
		ExpiresAt: time.Now().UTC().Add(ttl),
	}); err != nil {
		return "", err
	}
	return token, nil
}

func (a *Auth) sendVerificationEmail(ctx context.Context, user db.User) {
	token, err := a.newEmailToken(ctx, user.ID, "verify_email", verifyTokenTTL)
	if err != nil {
		a.log.WarnContext(ctx, "verification token creation failed", "error", err)
		return
	}
	sendAsync(ctx, a.email, a.log, email.Message{
		To:      user.Email,
		Subject: "Verify your punchcard email",
		Text: fmt.Sprintf("Welcome to punchcard!\n\nVerify your email at:\n%s/verify-email?token=%s\n\n"+
			"Or POST this token to /v1/auth/verify-email:\n%s\n\nThis link expires in 24 hours.",
			a.cfg.PublicBaseURL, token, token),
	})
}

func (a *Auth) resetEmailBody(token string) string {
	return fmt.Sprintf("A password reset was requested for your punchcard account.\n\n"+
		"Reset it at:\n%s/reset-password?token=%s\n\nOr POST this token to /v1/auth/password-reset/confirm:\n%s\n\n"+
		"This link expires in 1 hour. If you didn't request this, ignore this email.",
		a.cfg.PublicBaseURL, token, token)
}

func normalizeEmail(in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" || len(in) > 254 {
		return "", NewError(422, "validation_failed", "a valid email is required")
	}
	addr, err := mail.ParseAddress(in)
	if err != nil || addr.Address != in {
		return "", NewError(422, "validation_failed", "a valid email is required")
	}
	return strings.ToLower(in), nil
}

func validatePassword(pw string) error {
	if len(pw) < minPasswordLen || len(pw) > maxPasswordLen {
		return NewError(422, "validation_failed",
			fmt.Sprintf("password must be between %d and %d characters", minPasswordLen, maxPasswordLen))
	}
	return nil
}
