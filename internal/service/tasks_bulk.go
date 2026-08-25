package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/activity"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// BulkOp is one operation in a bulk request.
type BulkOp struct {
	Op     string // create | update | complete | delete
	ID     *uuid.UUID
	Create *TaskInput
	Patch  *TaskPatch
	// At is when this op really happened, for a batch flushed after time
	// offline. Nil means "now"; see the per-op application in BulkTasks.
	At *time.Time
}

// BulkResult is the per-item outcome (partial success is allowed).
type BulkResult struct {
	Index   int
	OK      bool
	Task    *db.Task
	Code    string
	Message string
}

// BulkTasks applies up to 100 task operations, reporting per-item results.
// Each operation is authorized independently, so a bulk call can never exceed
// the caller's permissions.
func (d *Domain) BulkTasks(ctx context.Context, p *auth.Principal, ops []BulkOp) ([]BulkResult, error) {
	if len(ops) == 0 || len(ops) > maxBulkOps {
		return nil, NewError(422, "validation_failed", "a bulk request must contain 1-100 operations")
	}
	results := make([]BulkResult, len(ops))
	for i, op := range ops {
		res := BulkResult{Index: i}
		// Per-op, not per-request: one batch covers a whole offline session, so
		// stamping every op with the first one's time would be a different lie
		// from the one being fixed.
		opCtx := ctx
		if op.At != nil {
			opCtx = activity.At(ctx, *op.At)
		}
		task, err := d.applyBulkOp(opCtx, p, op)
		if err != nil {
			var se *Error
			if errors.As(err, &se) {
				res.Code, res.Message = se.Code, se.Message
			} else {
				res.Code, res.Message = "internal_error", "internal error"
				d.log.ErrorContext(ctx, "bulk op failed", "index", i, "op", op.Op, "error", err.Error())
			}
		} else {
			res.OK = true
			if op.Op != "delete" {
				t := task
				res.Task = &t
			}
		}
		results[i] = res
	}
	return results, nil
}

func (d *Domain) applyBulkOp(ctx context.Context, p *auth.Principal, op BulkOp) (db.Task, error) {
	switch op.Op {
	case "create":
		if op.Create == nil {
			return db.Task{}, NewError(422, "validation_failed", "create requires task fields")
		}
		return d.CreateTask(ctx, p, *op.Create)
	case "update":
		if op.ID == nil || op.Patch == nil {
			return db.Task{}, NewError(422, "validation_failed", "update requires id and fields")
		}
		return d.UpdateTask(ctx, p, *op.ID, *op.Patch)
	case "complete":
		if op.ID == nil {
			return db.Task{}, NewError(422, "validation_failed", "complete requires id")
		}
		return d.CompleteTask(ctx, p, *op.ID)
	case "delete":
		if op.ID == nil {
			return db.Task{}, NewError(422, "validation_failed", "delete requires id")
		}
		return db.Task{}, d.DeleteTask(ctx, p, *op.ID)
	default:
		return db.Task{}, NewError(422, "validation_failed", "unknown op: "+op.Op)
	}
}
