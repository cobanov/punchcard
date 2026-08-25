package auth

import "github.com/google/uuid"

// ScopeRead and ScopeReadWrite are the two API token scopes.
const (
	ScopeRead      = "read"
	ScopeReadWrite = "read_write"
)

// TokenKindPAT and TokenKindDevice are the two things an api_tokens row can be.
// A PAT is issued by the user for a program; a device token is minted by the
// sign-in flow for a first-party client that cannot hold a session cookie.
const (
	TokenKindPAT    = "pat"
	TokenKindDevice = "device"
)

// Principal is the authenticated actor for a request: a human via a session
// cookie, a human via a first-party device token, or a program via a personal
// access token. Sessions always have full (read_write) scope over all the user's
// lists; PATs may be narrowed by scope and by ScopedListIDs.
type Principal struct {
	UserID        uuid.UUID
	Email         string
	EmailVerified bool
	ViaSession    bool
	ViaDevice     bool        // set when authenticated via a first-party device token
	SessionID     *uuid.UUID  // set when authenticated via a session cookie
	TokenID       *uuid.UUID  // set when authenticated via a PAT or device token
	Scope         string      // ScopeRead | ScopeReadWrite
	ScopedListIDs []uuid.UUID // nil = all lists the user can access
}

// FirstParty reports whether this principal is the account's owner operating
// punchcard itself, rather than a program acting on their behalf.
//
// It gates the **account plane** — delete, export, token and session
// management, password, 2FA, profile. That plane was made session-only in 0.4.6
// because the export reaches every list, every soft-deleted task and the
// metadata of the account's other tokens, so no automation token may read it.
// The rule was written as "must be a session", which silently locked out the
// phone: the WKWebView calls the API cross-origin and iOS drops the cookie, so
// the native shells sign in with a device token and had no working Settings
// screen at all — including account deletion, which Apple requires in-app.
//
// The distinction that matters is not the credential's shape but who holds it,
// and that is what this asks. A third-party PAT still cannot touch any of it.
//
// One honest consequence: a stolen device token can now delete the account,
// where before it could only delete every list and task in it. The gap is real
// but narrow, and the alternative was an app that cannot meet 5.1.1(v).
func (p *Principal) FirstParty() bool { return p.ViaSession || p.ViaDevice }

// CanWrite reports whether the principal's scope permits mutations.
func (p *Principal) CanWrite() bool { return p.Scope == ScopeReadWrite }

// TokenScopedToLists reports whether this principal is a PAT restricted to a
// specific set of lists. Sessions and unrestricted PATs return false.
func (p *Principal) TokenScopedToLists() bool { return len(p.ScopedListIDs) > 0 }

// AllowsList reports whether the principal's token scope permits touching the
// given list id. Always true for sessions and unrestricted PATs; for
// list-scoped PATs, true only if the id is in the allow-list.
func (p *Principal) AllowsList(listID uuid.UUID) bool {
	if !p.TokenScopedToLists() {
		return true
	}
	for _, id := range p.ScopedListIDs {
		if id == listID {
			return true
		}
	}
	return false
}
