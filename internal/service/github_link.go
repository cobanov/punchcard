package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/github"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// GitHubScope is the OAuth scope the commit scanner needs. `repo` rather than
// `public_repo` because most of the work worth tracking is in private
// repositories, and a scan that silently skips them is worse than none.
const GitHubScope = "repo"

// GitHubStatus is what a client may know about the connection. It deliberately
// carries no token, not even a prefix.
type GitHubStatus struct {
	Connected   bool
	Login       string
	Scopes      string
	ConnectedAt *time.Time
	LastScanAt  *time.Time
	LastError   string
}

// ErrGitHubNotConfigured is returned when the deployment has no token key, so
// no token can be stored.
var ErrGitHubNotConfigured = NewError(http.StatusServiceUnavailable, "github_not_configured",
	"this deployment has no GITHUB_TOKEN_KEY, so GitHub cannot be connected")

// ConnectGitHub stores (or replaces) the caller's GitHub connection.
func (d *Domain) ConnectGitHub(ctx context.Context, p *auth.Principal, login, token, scopes string) error {
	if !p.FirstParty() {
		return ErrSessionOnly
	}
	if d.github == nil {
		return ErrGitHubNotConfigured
	}
	if login == "" || token == "" {
		return NewError(422, "validation_failed", "login and token are both required")
	}
	sealed, err := d.github.Seal(token)
	if err != nil {
		return fmt.Errorf("seal github token: %w", err)
	}
	if _, err := d.store.UpsertGitHubConnection(ctx, db.UpsertGitHubConnectionParams{
		UserID: p.UserID, GithubLogin: login, AccessTokenEnc: sealed, Scopes: scopes,
	}); err != nil {
		return fmt.Errorf("store github connection: %w", err)
	}
	return nil
}

// GitHubStatus reports the connection without exposing the token.
func (d *Domain) GitHubStatus(ctx context.Context, p *auth.Principal) (GitHubStatus, error) {
	conn, err := d.store.GetGitHubConnection(ctx, p.UserID)
	if err != nil {
		if isNoRows(err) {
			return GitHubStatus{Connected: false}, nil
		}
		return GitHubStatus{}, fmt.Errorf("get github connection: %w", err)
	}
	lastErr := ""
	if conn.LastError != nil {
		lastErr = *conn.LastError
	}
	connectedAt := conn.ConnectedAt
	return GitHubStatus{
		Connected: true, Login: conn.GithubLogin, Scopes: conn.Scopes,
		ConnectedAt: &connectedAt, LastScanAt: conn.LastScanAt, LastError: lastErr,
	}, nil
}

// DisconnectGitHub removes the connection and the stored token.
func (d *Domain) DisconnectGitHub(ctx context.Context, p *auth.Principal) error {
	if !p.FirstParty() {
		return ErrSessionOnly
	}
	if _, err := d.store.DeleteGitHubConnection(ctx, p.UserID); err != nil {
		return fmt.Errorf("delete github connection: %w", err)
	}
	return nil
}

// AddGitHubEmail records another address the user's commits may be authored
// with. The scanner filters by GitHub login, which misses commits made on a
// machine whose git config uses a different address; rather than guess, the user
// says which addresses are also theirs.
func (d *Domain) AddGitHubEmail(ctx context.Context, p *auth.Principal, email string) error {
	if !p.FirstParty() {
		return ErrSessionOnly
	}
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || !strings.Contains(email, "@") {
		return NewError(422, "validation_failed", "a valid email address is required")
	}
	if err := d.store.AddGitHubEmail(ctx, db.AddGitHubEmailParams{UserID: p.UserID, Email: email}); err != nil {
		return fmt.Errorf("add github email: %w", err)
	}
	return nil
}

// ListGitHubEmails returns the extra author addresses.
func (d *Domain) ListGitHubEmails(ctx context.Context, p *auth.Principal) ([]string, error) {
	rows, err := d.store.ListGitHubEmails(ctx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("list github emails: %w", err)
	}
	return rows, nil
}

// RemoveGitHubEmail drops an extra author address.
func (d *Domain) RemoveGitHubEmail(ctx context.Context, p *auth.Principal, email string) error {
	if !p.FirstParty() {
		return ErrSessionOnly
	}
	n, err := d.store.DeleteGitHubEmail(ctx, db.DeleteGitHubEmailParams{
		UserID: p.UserID, Email: strings.TrimSpace(strings.ToLower(email)),
	})
	if err != nil {
		return fmt.Errorf("remove github email: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// githubClientFor builds a client bound to the user's stored token.
//
// Returns (nil, nil) when the account has no connection: that is not an error,
// it is a user who has not connected GitHub, and every scan path treats it as
// "nothing to do" rather than something to report.
func (d *Domain) githubClientFor(ctx context.Context, userID uuid.UUID) (*github.Client, string, error) {
	if d.github == nil {
		return nil, "", nil
	}
	conn, err := d.store.GetGitHubConnection(ctx, userID)
	if err != nil {
		if isNoRows(err) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("get github connection: %w", err)
	}
	token, err := d.github.Open(conn.AccessTokenEnc)
	if err != nil {
		// A token that will not decrypt is not recoverable by retrying: the key
		// changed, or the row is corrupt. Say so on the connection so the user
		// can reconnect, rather than failing every scan forever in silence.
		d.markGitHubError(ctx, userID, "stored token could not be decrypted; reconnect GitHub")
		return nil, "", nil
	}
	return github.New(d.githubHTTP, d.githubBaseURL, token), conn.GithubLogin, nil
}

// markGitHubError records why scanning stopped, so the user sees a reason in
// settings instead of an integration that quietly returns nothing.
func (d *Domain) markGitHubError(ctx context.Context, userID uuid.UUID, msg string) {
	if err := d.store.SetGitHubError(ctx, db.SetGitHubErrorParams{UserID: userID, LastError: &msg}); err != nil {
		d.log.WarnContext(ctx, "could not record github error", "error", err.Error())
	}
}

// classifyGitHubError turns a client error into (permanent, message).
//
// Permanent means retrying will not help and the user must act. Everything else
// is transient and belongs in the backoff.
func classifyGitHubError(err error) (permanent bool, msg string) {
	switch {
	case errors.Is(err, github.ErrUnauthorized):
		return true, "GitHub rejected the stored token; reconnect GitHub"
	case errors.Is(err, github.ErrRateLimited):
		return false, "GitHub rate limit reached; the scan will retry"
	default:
		return false, err.Error()
	}
}
