package http

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cobanov/punchcard/internal/service"
)

type bulkTaskPayload struct {
	// ID is only meaningful for create ops: an optional client-generated
	// UUIDv7 (offline sync). For update/complete/delete the op-level id names
	// the target instead.
	ID          *string    `json:"id,omitempty" format:"uuid" doc:"Create only: optional client-generated UUIDv7 (offline sync)."`
	ListID      *string    `json:"list_id,omitempty" format:"uuid"`
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
}

type bulkOpInput struct {
	Op   string           `json:"op" enum:"create,update,complete,delete"`
	ID   *string          `json:"id,omitempty" format:"uuid" doc:"Required for update/complete/delete."`
	Task *bulkTaskPayload `json:"task,omitempty" doc:"Fields for create/update."`
	// When this op really happened, for work done offline and synced later.
	// Optional: a command queued by an older build carries no `at`, and a
	// required field would drop it on the floor. Clamped server-side.
	At *time.Time `json:"at,omitempty" doc:"RFC3339. When the change was made on the client."`
}

type bulkErrorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type bulkResultDTO struct {
	Index int           `json:"index"`
	OK    bool          `json:"ok"`
	Task  *TaskDTO      `json:"task,omitempty"`
	Error *bulkErrorDTO `json:"error,omitempty"`
}

func (d Deps) registerBulkRoute(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "tasks-bulk", Method: http.MethodPost, Path: "/v1/tasks/bulk",
		Summary: "Apply up to 100 task operations (partial success)", Tags: []string{"tasks"},
		Errors: []int{401, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Operations []bulkOpInput `json:"operations" minItems:"1" maxItems:"100"`
		}
	}) (*struct {
		Body struct {
			Results []bulkResultDTO `json:"results"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		ops := make([]service.BulkOp, 0, len(in.Body.Operations))
		for _, item := range in.Body.Operations {
			op := service.BulkOp{Op: item.Op}
			id, perr := uuidPtr(item.ID)
			if perr != nil {
				return nil, perr
			}
			op.ID = id
			op.At = item.At
			if item.Op == "create" && item.Task != nil {
				ti, cerr := toTaskInput(item.Task)
				if cerr != nil {
					return nil, cerr
				}
				op.Create = ti
			}
			if item.Op == "update" && item.Task != nil {
				patch, uerr := toTaskPatch(item.Task)
				if uerr != nil {
					return nil, uerr
				}
				op.Patch = patch
			}
			ops = append(ops, op)
		}
		results, err := d.Domain.BulkTasks(ctx, p, ops)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Results []bulkResultDTO `json:"results"`
			}
		}{}
		out.Body.Results = make([]bulkResultDTO, 0, len(results))
		for _, r := range results {
			dto := bulkResultDTO{Index: r.Index, OK: r.OK}
			if r.Task != nil {
				t := taskDTO(*r.Task)
				dto.Task = &t
			}
			if !r.OK {
				dto.Error = &bulkErrorDTO{Code: r.Code, Message: r.Message}
			}
			out.Body.Results = append(out.Body.Results, dto)
		}
		return out, nil
	})
}

func toTaskInput(pl *bulkTaskPayload) (*service.TaskInput, error) {
	if pl == nil || pl.ListID == nil || pl.Title == nil {
		return nil, NewProblem(422, "validation_failed", "create requires task.list_id and task.title")
	}
	listID, err := parseUUID(*pl.ListID)
	if err != nil {
		return nil, err
	}
	clientID, err := uuidPtr(pl.ID)
	if err != nil {
		return nil, err
	}
	parentID, err := uuidPtr(pl.ParentID)
	if err != nil {
		return nil, err
	}
	ti := &service.TaskInput{ID: clientID, ListID: listID, Title: *pl.Title, ParentID: parentID}
	if pl.Body != nil {
		ti.Body = *pl.Body
	}
	if pl.Priority != nil {
		ti.Priority = *pl.Priority
	}
	if pl.Tags != nil {
		ti.Tags = *pl.Tags
	}
	ti.DueAt = pl.DueAt
	return ti, nil
}

func toTaskPatch(pl *bulkTaskPayload) (*service.TaskPatch, error) {
	parentID, err := uuidPtr(pl.ParentID)
	if err != nil {
		return nil, err
	}
	listID, err := uuidPtr(pl.ListID)
	if err != nil {
		return nil, err
	}
	return &service.TaskPatch{
		Title: pl.Title, Body: pl.Body, Status: pl.Status, DueAt: pl.DueAt,
		ClearDueAt: pl.ClearDueAt, Priority: pl.Priority, Tags: pl.Tags,
		Position: pl.Position, ParentID: parentID, ClearParent: pl.ClearParent,
		ListID: listID,
	}, nil
}
