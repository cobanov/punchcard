package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/repo/db"
)

const (
	totpIssuer        = "punchcard"
	recoveryCodeCount = 10
	twoFactorChallTTL = 5 * time.Minute
)

// twoFactorStore maps a short-lived login challenge to the user awaiting a TOTP
// code. In-process + single-use, like nativeCodeStore (single-instance prod).
type twoFactorChallenge struct {
	userID     uuid.UUID
	native     bool
	clientName string
	expires    time.Time
}
type twoFactorStore struct {
	mu sync.Mutex
	m  map[string]twoFactorChallenge
}

func newTwoFactorStore() *twoFactorStore {
	return &twoFactorStore{m: make(map[string]twoFactorChallenge)}
}

func (s *twoFactorStore) put(c twoFactorChallenge, now time.Time) (string, error) {
	id, err := auth.GenerateSecret(32)
	if err != nil {
		return "", err
	}
	c.expires = now.Add(twoFactorChallTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.m {
		if now.After(e.expires) {
			delete(s.m, k)
		}
	}
	s.m[id] = c
	return id, nil
}

func (s *twoFactorStore) take(id string, now time.Time) (twoFactorChallenge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[id]
	if !ok {
		return twoFactorChallenge{}, false
	}
	delete(s.m, id)
	if now.After(c.expires) {
		return twoFactorChallenge{}, false
	}
	return c, true
}

// TOTPSetup is returned when a user begins enrollment.
type TOTPSetup struct {
	Secret string // base32 secret, to type in manually
	URI    string // otpauth:// provisioning URI for a QR code
}

// SetupTOTP generates a new (not-yet-enabled) TOTP secret for the account and
// stores it encrypted. Session-only. Enrollment completes via EnableTOTP.
func (a *Auth) SetupTOTP(ctx context.Context, p *auth.Principal) (TOTPSetup, error) {
	if !p.FirstParty() {
		return TOTPSetup{}, ErrSessionOnly
	}
	user, err := a.store.GetUserByID(ctx, p.UserID)
	if err != nil {
		return TOTPSetup{}, fmt.Errorf("get user: %w", err)
	}
	if user.TotpEnabled {
		return TOTPSetup{}, NewError(409, "totp_already_enabled", "two-factor auth is already enabled")
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: totpIssuer, AccountName: user.Email})
	if err != nil {
		return TOTPSetup{}, fmt.Errorf("generate totp: %w", err)
	}
	enc, err := a.totpCipher.Encrypt([]byte(key.Secret()))
	if err != nil {
		return TOTPSetup{}, fmt.Errorf("encrypt totp: %w", err)
	}
	if err := a.store.SetTOTPSecret(ctx, db.SetTOTPSecretParams{ID: p.UserID, TotpSecretEnc: enc}); err != nil {
		return TOTPSetup{}, fmt.Errorf("store totp: %w", err)
	}
	return TOTPSetup{Secret: key.Secret(), URI: key.URL()}, nil
}

// EnableTOTP verifies the first code against the pending secret, enables 2FA, and
// returns freshly-generated single-use recovery codes (shown only once).
func (a *Auth) EnableTOTP(ctx context.Context, p *auth.Principal, code, ip string) ([]string, error) {
	if !p.FirstParty() {
		return nil, ErrSessionOnly
	}
	user, err := a.store.GetUserByID(ctx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user.TotpEnabled {
		return nil, NewError(409, "totp_already_enabled", "two-factor auth is already enabled")
	}
	if !a.verifyTOTP(user, code) {
		return nil, NewError(422, "totp_invalid", "that code is incorrect")
	}
	codes, hashes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	err = a.store.WithTx(ctx, func(q *db.Queries) error {
		if e := q.SetTOTPEnabled(ctx, db.SetTOTPEnabledParams{ID: p.UserID, TotpEnabled: true}); e != nil {
			return e
		}
		if e := q.DeleteRecoveryCodes(ctx, p.UserID); e != nil {
			return e
		}
		for _, h := range hashes {
			id, e := uuid.NewV7()
			if e != nil {
				return e
			}
			if e := q.AddRecoveryCode(ctx, db.AddRecoveryCodeParams{ID: id, UserID: p.UserID, CodeHash: h}); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enable totp: %w", err)
	}
	a.audit.Record(ctx, &p.UserID, "totp.enable", ip, nil)
	return codes, nil
}

// DisableTOTP turns off 2FA after verifying a current code (or recovery code).
func (a *Auth) DisableTOTP(ctx context.Context, p *auth.Principal, code, ip string) error {
	if !p.FirstParty() {
		return ErrSessionOnly
	}
	user, err := a.store.GetUserByID(ctx, p.UserID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if !user.TotpEnabled {
		return NewError(409, "totp_not_enabled", "two-factor auth is not enabled")
	}
	if !a.verifyTOTP(user, code) && !a.useRecoveryCode(ctx, user.ID, code) {
		return NewError(422, "totp_invalid", "that code is incorrect")
	}
	err = a.store.WithTx(ctx, func(q *db.Queries) error {
		if e := q.ClearTOTP(ctx, p.UserID); e != nil {
			return e
		}
		return q.DeleteRecoveryCodes(ctx, p.UserID)
	})
	if err != nil {
		return fmt.Errorf("disable totp: %w", err)
	}
	a.audit.Record(ctx, &p.UserID, "totp.disable", ip, nil)
	return nil
}

// verifyTOTP decrypts the stored secret and validates a time-based code.
func (a *Auth) verifyTOTP(user db.User, code string) bool {
	if user.TotpSecretEnc == nil {
		return false
	}
	secret, err := a.totpCipher.Decrypt(user.TotpSecretEnc)
	if err != nil {
		return false
	}
	return totp.Validate(strings.TrimSpace(code), string(secret))
}

// useRecoveryCode consumes a matching unused recovery code, returning true on success.
func (a *Auth) useRecoveryCode(ctx context.Context, userID uuid.UUID, code string) bool {
	norm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if norm == "" {
		return false
	}
	n, err := a.store.UseRecoveryCode(ctx, db.UseRecoveryCodeParams{UserID: userID, CodeHash: auth.HashToken(norm)})
	return err == nil && n > 0
}

// issue2FAChallenge stores a login challenge for a user who passed the password
// step but still owes a TOTP code.
func (a *Auth) issue2FAChallenge(userID uuid.UUID, native bool, clientName string) (string, error) {
	return a.twoFactor.put(twoFactorChallenge{userID: userID, native: native, clientName: clientName}, time.Now())
}

// Complete2FAWeb finishes a cookie login: validates the challenge + code and
// issues a session.
func (a *Auth) Complete2FAWeb(ctx context.Context, challenge, code, ip, userAgent string) (db.User, SessionIssue, error) {
	c, ok := a.twoFactor.take(challenge, time.Now())
	if !ok || c.native {
		return db.User{}, SessionIssue{}, ErrTwoFactorChallenge
	}
	user, err := a.verify2FACode(ctx, c.userID, code, ip)
	if err != nil {
		return db.User{}, SessionIssue{}, err
	}
	sess, err := a.issueSession(ctx, user.ID, ip, userAgent)
	if err != nil {
		return db.User{}, SessionIssue{}, err
	}
	return user, sess, nil
}

// Complete2FANative finishes a bearer login: validates the challenge + code and
// mints a device token.
func (a *Auth) Complete2FANative(ctx context.Context, challenge, code, ip string) (db.User, string, error) {
	c, ok := a.twoFactor.take(challenge, time.Now())
	if !ok || !c.native {
		return db.User{}, "", ErrTwoFactorChallenge
	}
	user, err := a.verify2FACode(ctx, c.userID, code, ip)
	if err != nil {
		return db.User{}, "", err
	}
	token, err := a.MintDeviceToken(ctx, user.ID, c.clientName, ip)
	if err != nil {
		return db.User{}, "", err
	}
	return user, token, nil
}

// verify2FACode accepts either a valid TOTP code or an unused recovery code.
func (a *Auth) verify2FACode(ctx context.Context, userID uuid.UUID, code, ip string) (db.User, error) {
	user, err := a.store.GetUserByID(ctx, userID)
	if err != nil {
		return db.User{}, fmt.Errorf("get user: %w", err)
	}
	if a.verifyTOTP(user, code) || a.useRecoveryCode(ctx, userID, code) {
		a.audit.Record(ctx, &userID, audit.ActionLoginSuccess, ip, map[string]any{"2fa": true})
		return user, nil
	}
	a.audit.Record(ctx, &userID, audit.ActionLoginFailure, ip, map[string]any{"2fa": true})
	return db.User{}, ErrInvalidCredentials
}

// generateRecoveryCodes returns human-friendly codes (xxxxx-xxxxx hex) and their
// storage hashes (of the normalized, dash-stripped, lowercase form).
func generateRecoveryCodes() (codes []string, hashes [][]byte, err error) {
	for i := 0; i < recoveryCodeCount; i++ {
		b := make([]byte, 5)
		if _, e := rand.Read(b); e != nil {
			return nil, nil, fmt.Errorf("read random: %w", e)
		}
		norm := hex.EncodeToString(b) // 10 lowercase hex chars
		codes = append(codes, norm[:5]+"-"+norm[5:])
		hashes = append(hashes, auth.HashToken(norm))
	}
	return codes, hashes, nil
}
