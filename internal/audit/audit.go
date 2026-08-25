package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// Action constants for the audit trail.
const (
	ActionLoginSuccess   = "login.success"
	ActionLoginFailure   = "login.failure"
	ActionLogout         = "logout"
	ActionRegister       = "register"
	ActionEmailVerified  = "email.verified"
	ActionPasswordReset  = "password.reset"
	ActionPasswordChange = "password.change"
	ActionTokenCreate    = "token.create"
	ActionTokenRevoke    = "token.revoke"
	ActionSessionRevoke  = "session.revoke"
	ActionAccountDelete  = "account.delete"
	ActionMemberChange   = "member.change"
	ActionInviteChange   = "invite.change"
	ActionWebhookChange  = "webhook.change"
)

// Logger appends security-relevant events to the audit_log table.
type Logger struct {
	store *repo.Store
	log   *slog.Logger
}

// NewLogger builds an audit Logger.
func NewLogger(store *repo.Store, log *slog.Logger) *Logger {
	return &Logger{store: store, log: log}
}

// Record writes an audit entry. Auditing must never fail the primary
// operation, so errors are logged and swallowed. metadata must contain NO
// secrets.
func (l *Logger) Record(ctx context.Context, userID *uuid.UUID, action, ip string, metadata map[string]any) {
	raw := []byte("{}")
	if len(metadata) > 0 {
		if b, err := json.Marshal(metadata); err == nil {
			raw = b
		}
	}
	var ipp *string
	if ip != "" {
		ipp = &ip
	}
	id, err := uuid.NewV7()
	if err != nil {
		l.log.WarnContext(ctx, "audit: uuid generation failed", "error", err)
		return
	}
	if err := l.store.InsertAuditLog(ctx, db.InsertAuditLogParams{
		ID:       id,
		UserID:   userID,
		Action:   action,
		Ip:       ipp,
		Metadata: raw,
	}); err != nil {
		l.log.WarnContext(ctx, "audit: insert failed", "action", action, "error", err)
	}
}
