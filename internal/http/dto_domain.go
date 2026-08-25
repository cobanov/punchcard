package http

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/repo/db"
)

// ListDTO is the public representation of a list.
type ListDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	OwnerID    string `json:"owner_id"`
	IsPersonal bool   `json:"is_personal"`
	Role       string `json:"role"`
	// A colour name, or absent when the list has none. A pointer so "no colour"
	// is null rather than the empty string — the client has to distinguish an
	// uncoloured list from one whose colour failed to arrive.
	Color     *string   `json:"color,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func listDTO(l db.List, role string) ListDTO {
	return ListDTO{
		ID: l.ID.String(), Name: l.Name, OwnerID: l.OwnerID.String(),
		IsPersonal: l.IsPersonal, Role: role, Color: l.Color,
		CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
	}
}

// TaskDTO is the public representation of a task.
type TaskDTO struct {
	ID          string     `json:"id"`
	ListID      string     `json:"list_id"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	Status      string     `json:"status"`
	DueAt       *time.Time `json:"due_at,omitempty"`
	Priority    int        `json:"priority"`
	Tags        []string   `json:"tags"`
	ParentID    *string    `json:"parent_id,omitempty"`
	Position    string     `json:"position"`
	CreatedBy   *string    `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

func taskDTO(t db.Task) TaskDTO {
	tags := []string{}
	_ = json.Unmarshal(t.Tags, &tags)
	dto := TaskDTO{
		ID: t.ID.String(), ListID: t.ListID.String(), Title: t.Title, Body: t.Body,
		Status: t.Status, DueAt: t.DueAt, Priority: int(t.Priority), Tags: tags,
		Position: t.Position, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		CompletedAt: t.CompletedAt, DeletedAt: t.DeletedAt,
	}
	if t.ParentID != nil {
		s := t.ParentID.String()
		dto.ParentID = &s
	}
	if t.CreatedBy != nil {
		s := t.CreatedBy.String()
		dto.CreatedBy = &s
	}
	return dto
}

// MemberDTO is a list member.
type MemberDTO struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

func memberDTO(m db.ListMembersRow) MemberDTO {
	return MemberDTO{UserID: m.UserID.String(), Email: m.Email, Role: m.Role, CreatedAt: m.CreatedAt}
}

// InviteDTO is a pending invite (never carries the token).
type InviteDTO struct {
	ID        string    `json:"id"`
	ListID    string    `json:"list_id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func inviteDTO(i db.Invite) InviteDTO {
	return InviteDTO{
		ID: i.ID.String(), ListID: i.ListID.String(), Email: i.Email, Role: i.Role,
		CreatedAt: i.CreatedAt, ExpiresAt: i.ExpiresAt,
	}
}

func uuidPtr(s *string) (*uuid.UUID, error) {
	if s == nil {
		return nil, nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil, NewProblem(422, "validation_failed", "invalid uuid: "+*s)
	}
	return &id, nil
}
