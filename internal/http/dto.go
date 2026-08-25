package http

import (
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/repo/db"
)

// UserDTO is the public representation of an account.
type UserDTO struct {
	ID               string    `json:"id"`
	Email            string    `json:"email"`
	DisplayName      string    `json:"display_name"`
	AvatarURL        string    `json:"avatar_url,omitempty"`
	EmailVerified    bool      `json:"email_verified"`
	TwoFactorEnabled bool      `json:"two_factor_enabled"`
	CreatedAt        time.Time `json:"created_at"`
}

func userDTO(u db.User) UserDTO {
	name := ""
	if u.DisplayName != nil {
		name = *u.DisplayName
	}
	avatar := ""
	if u.AvatarUrl != nil {
		avatar = *u.AvatarUrl
	}
	return UserDTO{
		ID:               u.ID.String(),
		Email:            u.Email,
		DisplayName:      name,
		AvatarURL:        avatar,
		EmailVerified:    u.EmailVerifiedAt != nil,
		TwoFactorEnabled: u.TotpEnabled,
		CreatedAt:        u.CreatedAt,
	}
}

// TokenDTO is a personal access token without its secret.
type TokenDTO struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Prefix           string     `json:"prefix"`
	Scope            string     `json:"scope"`
	ScopedProjectIDs []string   `json:"scoped_project_ids,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
}

func tokenDTO(t db.ApiToken) TokenDTO {
	dto := TokenDTO{
		ID:         t.ID.String(),
		Name:       t.Name,
		Prefix:     t.TokenPrefix,
		Scope:      t.Scope,
		CreatedAt:  t.CreatedAt,
		ExpiresAt:  t.ExpiresAt,
		LastUsedAt: t.LastUsedAt,
	}
	for _, id := range t.ScopedProjectIds {
		dto.ScopedProjectIDs = append(dto.ScopedProjectIDs, id.String())
	}
	return dto
}

// SessionDTO is a listed session.
type SessionDTO struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	IP         *string   `json:"ip,omitempty"`
	UserAgent  *string   `json:"user_agent,omitempty"`
	Current    bool      `json:"current"`
}

func sessionDTO(s db.AuthSession, current *uuid.UUID) SessionDTO {
	return SessionDTO{
		ID:         s.ID.String(),
		CreatedAt:  s.CreatedAt,
		LastSeenAt: s.LastSeenAt,
		ExpiresAt:  s.ExpiresAt,
		IP:         s.Ip,
		UserAgent:  s.UserAgent,
		Current:    current != nil && *current == s.ID,
	}
}
