package http

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/service"
)

type taskBody struct {
	Body TaskDTO
}

func parseOptTime(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, NewProblem(422, "validation_failed", "timestamps must be RFC 3339, e.g. 2026-07-18T17:00:00Z")
	}
	u := t.UTC()
	return &u, nil
}

func (d Deps) registerTaskRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "tasks-list", Method: http.MethodGet, Path: "/v1/tasks",
		Summary: "List tasks with filters", Tags: []string{"tasks"}, Errors: []int{401, 404, 422},
	}, func(ctx context.Context, in *struct {
		ListID         string `query:"list_id"`
		Status         string `query:"status" enum:"open,done"`
		DueBefore      string `query:"due_before"`
		DueAfter       string `query:"due_after"`
		Tag            string `query:"tag"`
		Q              string `query:"q"`
		UpdatedSince   string `query:"updated_since"`
		IncludeDeleted bool   `query:"include_deleted"`
		Sort           string `query:"sort" enum:"position,created_at,updated_at"`
		Cursor         string `query:"cursor"`
		Limit          int    `query:"limit" minimum:"1" maximum:"100"`
	}) (*struct {
		Body struct {
			Tasks      []TaskDTO `json:"tasks"`
			NextCursor string    `json:"next_cursor,omitempty"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		var listID *uuid.UUID
		if in.ListID != "" {
			id, perr := uuid.Parse(in.ListID)
			if perr != nil {
				return nil, NewProblem(422, "validation_failed", "list_id must be a uuid")
			}
			listID = &id
		}
		dueBefore, err := parseOptTime(in.DueBefore)
		if err != nil {
			return nil, err
		}
		dueAfter, err := parseOptTime(in.DueAfter)
		if err != nil {
			return nil, err
		}
		updatedSince, err := parseOptTime(in.UpdatedSince)
		if err != nil {
			return nil, err
		}
		tasks, next, err := d.Domain.ListTasks(ctx, p, service.TaskFilter{
			ListID: listID, Status: in.Status, DueBefore: dueBefore, DueAfter: dueAfter,
			UpdatedSince: updatedSince, Tag: in.Tag, Q: in.Q, IncludeDeleted: in.IncludeDeleted,
			Sort: in.Sort, Cursor: in.Cursor, Limit: in.Limit,
		})
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Tasks      []TaskDTO `json:"tasks"`
				NextCursor string    `json:"next_cursor,omitempty"`
			}
		}{}
		out.Body.Tasks = make([]TaskDTO, 0, len(tasks))
		for _, t := range tasks {
			out.Body.Tasks = append(out.Body.Tasks, taskDTO(t))
		}
		out.Body.NextCursor = next
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "tasks-create", Method: http.MethodPost, Path: "/v1/tasks",
		Summary: "Create a task", Tags: []string{"tasks"}, DefaultStatus: http.StatusCreated,
		Errors: []int{401, 403, 404, 409, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			ID       *string    `json:"id,omitempty" format:"uuid" doc:"Optional client-generated UUIDv7 (offline sync). Same id again → 409 id_conflict."`
			ListID   string     `json:"list_id" format:"uuid"`
			Title    string     `json:"title" minLength:"1" maxLength:"500"`
			Body     string     `json:"body,omitempty" maxLength:"16384"`
			DueAt    *time.Time `json:"due_at,omitempty" doc:"RFC 3339 UTC, e.g. 2026-07-18T17:00:00Z"`
			Priority int        `json:"priority,omitempty" minimum:"0" maximum:"3"`
			Tags     []string   `json:"tags,omitempty"`
			ParentID *string    `json:"parent_id,omitempty" format:"uuid"`
		}
	}) (*taskBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		clientID, err := uuidPtr(in.Body.ID)
		if err != nil {
			return nil, err
		}
		listID, err := parseUUID(in.Body.ListID)
		if err != nil {
			return nil, err
		}
		parentID, err := uuidPtr(in.Body.ParentID)
		if err != nil {
			return nil, err
		}
		task, err := d.Domain.CreateTask(ctx, p, service.TaskInput{
			ID: clientID, ListID: listID, Title: in.Body.Title, Body: in.Body.Body, DueAt: in.Body.DueAt,
			Priority: in.Body.Priority, Tags: in.Body.Tags, ParentID: parentID,
		})
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &taskBody{Body: taskDTO(task)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "tasks-get", Method: http.MethodGet, Path: "/v1/tasks/{id}",
		Summary: "Get a task", Tags: []string{"tasks"}, Errors: []int{401, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*taskBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		task, err := d.Domain.GetTask(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &taskBody{Body: taskDTO(task)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "tasks-update", Method: http.MethodPatch, Path: "/v1/tasks/{id}",
		Summary: "Update a task", Tags: []string{"tasks"}, Errors: []int{401, 403, 404, 422},
	}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			Title       *string    `json:"title,omitempty" maxLength:"500"`
			Body        *string    `json:"body,omitempty" maxLength:"16384"`
			Status      *string    `json:"status,omitempty" enum:"open,done"`
			DueAt       *time.Time `json:"due_at,omitempty"`
			ClearDueAt  bool       `json:"clear_due_at,omitempty"`
			Priority    *int       `json:"priority,omitempty" minimum:"0" maximum:"3"`
			Tags        *[]string  `json:"tags,omitempty"`
			Position    *string    `json:"position,omitempty"`
			ParentID    *string    `json:"parent_id,omitempty" format:"uuid"`
			ClearParent bool       `json:"clear_parent,omitempty"`
			ListID      *string    `json:"list_id,omitempty" format:"uuid" doc:"Move the task to this list (requires editor on both lists; subtasks move along; a moved subtask is detached)."`
		}
	}) (*taskBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		parentID, err := uuidPtr(in.Body.ParentID)
		if err != nil {
			return nil, err
		}
		listID, err := uuidPtr(in.Body.ListID)
		if err != nil {
			return nil, err
		}
		task, err := d.Domain.UpdateTask(ctx, p, id, service.TaskPatch{
			Title: in.Body.Title, Body: in.Body.Body, Status: in.Body.Status,
			DueAt: in.Body.DueAt, ClearDueAt: in.Body.ClearDueAt, Priority: in.Body.Priority,
			Tags: in.Body.Tags, Position: in.Body.Position, ParentID: parentID, ClearParent: in.Body.ClearParent,
			ListID: listID,
		})
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &taskBody{Body: taskDTO(task)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "tasks-delete", Method: http.MethodDelete, Path: "/v1/tasks/{id}",
		Summary: "Delete (soft) a task", Tags: []string{"tasks"}, DefaultStatus: http.StatusNoContent,
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
		if err := d.Domain.DeleteTask(ctx, p, id); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "tasks-restore", Method: http.MethodPost, Path: "/v1/tasks/{id}/restore",
		Summary: "Restore a soft-deleted task", Tags: []string{"tasks"}, Errors: []int{401, 403, 404, 409},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*taskBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		task, err := d.Domain.RestoreTask(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &taskBody{Body: taskDTO(task)}, nil
	})

	d.registerBulkRoute(api)
}
