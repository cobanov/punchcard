package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/activity"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/events"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// taskResource builds a JSON-friendly view of a task for event payloads (tags
// as a string array rather than raw jsonb bytes).
func taskResource(t db.Task) map[string]any {
	var tags []string
	_ = json.Unmarshal(t.Tags, &tags)
	return map[string]any{
		"id": t.ID, "list_id": t.ListID, "title": t.Title, "body": t.Body,
		"status": t.Status, "due_at": t.DueAt, "priority": t.Priority, "tags": tags,
		"parent_id": t.ParentID, "position": t.Position,
		"created_at": t.CreatedAt, "updated_at": t.UpdatedAt, "completed_at": t.CompletedAt,
	}
}

const (
	maxTitleLen  = 500
	maxBodyLen   = 16384
	defaultLimit = 50
	maxLimit     = 100
	maxBulkOps   = 100
)

// TaskInput is the payload for creating a task.
type TaskInput struct {
	// ID is an optional client-generated UUIDv7 so offline clients get a
	// stable id before first sync (docs/offline-sync.md §5). Nil → server
	// generates one.
	ID       *uuid.UUID
	ListID   uuid.UUID
	Title    string
	Body     string
	DueAt    *time.Time
	Priority int
	Tags     []string
	ParentID *uuid.UUID
}

// validateClientID enforces that client-supplied ids are UUIDv7, keeping PK
// time-sortability (index locality). The embedded timestamp is deliberately
// not validated — offline client clocks skew.
func validateClientID(id uuid.UUID) error {
	if id.Version() != 7 {
		return NewError(422, "validation_failed", "id must be a UUIDv7")
	}
	return nil
}

// TaskPatch is a partial update. Nil fields are unchanged; the Clear* flags
// explicitly null out nullable fields. A ListID different from the task's
// current list moves it (docs below on UpdateTask).
type TaskPatch struct {
	Title       *string
	Body        *string
	Status      *string
	DueAt       *time.Time
	ClearDueAt  bool
	Priority    *int
	Tags        *[]string
	Position    *string
	ParentID    *uuid.UUID
	ClearParent bool
	ListID      *uuid.UUID
}

// isPositionOnly reports whether this patch only moves the task within its
// list. Every other field must be nil or false, so adding a field to TaskPatch
// without adding it here makes this return true wrongly, and an update touching
// only that field writes no activity row — a real edit missing from the log,
// with nothing failing. TestIsPositionOnlyCoversEveryTaskPatchField
// (tasks_patch_test.go) walks the struct by reflection and is what catches it.
func (p TaskPatch) isPositionOnly() bool {
	return p.Position != nil &&
		p.Title == nil && p.Body == nil && p.Status == nil && p.DueAt == nil &&
		p.Priority == nil && p.Tags == nil && p.ListID == nil && p.ParentID == nil &&
		!p.ClearDueAt && !p.ClearParent
}

// TaskFilter drives list queries.
type TaskFilter struct {
	ListID         *uuid.UUID
	Status         string
	DueBefore      *time.Time
	DueAfter       *time.Time
	UpdatedSince   *time.Time
	Tag            string
	Q              string
	IncludeDeleted bool
	Sort           string
	Cursor         string
	Limit          int
}

func marshalTags(tags []string) ([]byte, error) {
	if tags == nil {
		tags = []string{}
	}
	for _, tg := range tags {
		if len(tg) > 100 {
			return nil, NewError(422, "validation_failed", "each tag must be <= 100 characters")
		}
	}
	return json.Marshal(tags)
}

// CreateTask creates a task in a list the principal can edit.
func (d *Domain) CreateTask(ctx context.Context, p *auth.Principal, in TaskInput) (db.Task, error) {
	if _, err := d.authorizeList(ctx, p, in.ListID, RoleEditor, true); err != nil {
		return db.Task{}, err
	}
	if err := validateTaskFields(in.Title, in.Body, in.Priority); err != nil {
		return db.Task{}, err
	}
	if in.ParentID != nil {
		if err := d.validateParent(ctx, p, *in.ParentID, in.ListID); err != nil {
			return db.Task{}, err
		}
	}
	count, err := d.store.CountTasksInList(ctx, in.ListID)
	if err != nil {
		return db.Task{}, fmt.Errorf("count tasks: %w", err)
	}
	if int(count) >= d.cfg.MaxTasksPerList {
		return db.Task{}, ErrQuotaExceeded
	}
	// A new task goes to the *top* of its list. The compose row sits at the top
	// of the column, so appending put what you just typed at the far end of the
	// list — often off-screen — which reads as the app having dropped it.
	// Reported exactly that way: "I typed a task, clicked away, and it went to
	// the bottom."
	minPos, err := d.store.MinPositionInList(ctx, in.ListID)
	if err != nil {
		return db.Task{}, fmt.Errorf("min position: %w", err)
	}
	tags, err := marshalTags(in.Tags)
	if err != nil {
		return db.Task{}, err
	}
	var id uuid.UUID
	if in.ID != nil {
		if err := validateClientID(*in.ID); err != nil {
			return db.Task{}, err
		}
		id = *in.ID
	} else {
		id, err = uuid.NewV7()
		if err != nil {
			return db.Task{}, err
		}
	}
	created := p.UserID
	var task db.Task
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		t, e := q.CreateTask(ctx, db.CreateTaskParams{
			ID: id, ListID: in.ListID, Title: strings.TrimSpace(in.Title), Body: in.Body,
			Status: "open", DueAt: in.DueAt, Priority: i16(in.Priority), Tags: tags,
			ParentID: in.ParentID, Position: PositionBetween("", minPos), CreatedBy: &created,
		})
		if e != nil {
			return e
		}
		task = t
		return events.Write(ctx, q, events.TypeTaskCreated, &in.ListID, actorOf(p), taskResource(t), nil)
	})
	if err != nil {
		if repo.IsUniqueViolation(err) {
			// Only reachable with a client-supplied id: a replayed offline
			// create whose idempotency key has expired. The client GETs the
			// task to confirm it is the one it created.
			return db.Task{}, NewError(409, "id_conflict", "a task with this id already exists")
		}
		return db.Task{}, fmt.Errorf("create task: %w", err)
	}
	return task, nil
}

func (d *Domain) validateParent(ctx context.Context, p *auth.Principal, parentID, listID uuid.UUID) error {
	row, err := d.store.GetTaskForUser(ctx, db.GetTaskForUserParams{ID: parentID, UserID: p.UserID})
	if err != nil {
		if repo.IsNotFound(err) {
			return NewError(422, "validation_failed", "parent task not found")
		}
		return fmt.Errorf("get parent: %w", err)
	}
	if row.Task.DeletedAt != nil || row.Task.ListID != listID {
		return NewError(422, "validation_failed", "parent task must be a live task in the same list")
	}
	if row.Task.ParentID != nil {
		return NewError(422, "validation_failed", "tasks support only one level of nesting")
	}
	return nil
}

// GetTask returns a single task the principal can read.
func (d *Domain) GetTask(ctx context.Context, p *auth.Principal, id uuid.UUID) (db.Task, error) {
	task, _, err := d.authorizeTask(ctx, p, id, RoleViewer, false, false)
	return task, err
}

// UpdateTask applies a partial update (editor only).
//
// Cross-list move: a ListID different from the task's current list moves it.
// The principal must be an editor on BOTH lists (membership on the target is
// checked exactly like any list access — non-members get 404). Subtasks move
// with their parent in the same transaction; moving a subtask by itself
// detaches it. Unless the patch carries an explicit position, the task lands
// at the end of the destination list. For sync correctness the move emits a
// task.deleted event on the source list and a task.created on the destination
// — members of only one side each get a coherent story, and offline clients
// converge without a new event type.
func (d *Domain) UpdateTask(ctx context.Context, p *auth.Principal, id uuid.UUID, patch TaskPatch) (db.Task, error) {
	cur, _, err := d.authorizeTask(ctx, p, id, RoleEditor, true, false)
	if err != nil {
		return db.Task{}, err
	}

	destList := cur.ListID
	move := patch.ListID != nil && *patch.ListID != cur.ListID
	if move {
		if patch.ParentID != nil {
			return db.Task{}, NewError(422, "validation_failed", "cannot set a parent while moving lists")
		}
		if _, err := d.authorizeList(ctx, p, *patch.ListID, RoleEditor, true); err != nil {
			return db.Task{}, err
		}
		destList = *patch.ListID
		kids, err := d.store.CountSubtasks(ctx, &id)
		if err != nil {
			return db.Task{}, fmt.Errorf("count subtasks: %w", err)
		}
		count, err := d.store.CountTasksInList(ctx, destList)
		if err != nil {
			return db.Task{}, fmt.Errorf("count tasks: %w", err)
		}
		if int(count)+1+int(kids) > d.cfg.MaxTasksPerList {
			return db.Task{}, ErrQuotaExceeded
		}
	}

	title := cur.Title
	if patch.Title != nil {
		title = strings.TrimSpace(*patch.Title)
	}
	body := cur.Body
	if patch.Body != nil {
		body = *patch.Body
	}
	priority := int(cur.Priority)
	if patch.Priority != nil {
		priority = *patch.Priority
	}
	if err := validateTaskFields(title, body, priority); err != nil {
		return db.Task{}, err
	}

	dueAt := cur.DueAt
	switch {
	case patch.ClearDueAt:
		dueAt = nil
	case patch.DueAt != nil:
		dueAt = patch.DueAt
	}

	parentID := cur.ParentID
	switch {
	case move:
		parentID = nil // a moved subtask detaches; a moved parent keeps its children (moved below)
	case patch.ClearParent:
		parentID = nil
	case patch.ParentID != nil:
		if *patch.ParentID == id {
			return db.Task{}, NewError(422, "validation_failed", "a task cannot be its own parent")
		}
		// validateParent guards the other end of the edge — that the prospective
		// parent is not itself a subtask. Both ends have to be checked: a task
		// that already has children becoming a subtask makes the chain two deep
		// just as surely, and the grandchild is invisible to every UI and to the
		// cross-list move (MoveSubtasksToList only follows one level, so moving
		// such a chain strands the grandchild with a parent_id pointing into
		// another list).
		kids, kerr := d.store.CountSubtasks(ctx, &id)
		if kerr != nil {
			return db.Task{}, fmt.Errorf("count subtasks: %w", kerr)
		}
		if kids > 0 {
			return db.Task{}, NewError(422, "validation_failed", "a task with subtasks cannot become a subtask")
		}
		if err := d.validateParent(ctx, p, *patch.ParentID, cur.ListID); err != nil {
			return db.Task{}, err
		}
		parentID = patch.ParentID
	}

	tags := cur.Tags
	if patch.Tags != nil {
		tags, err = marshalTags(*patch.Tags)
		if err != nil {
			return db.Task{}, err
		}
	}
	position := cur.Position
	switch {
	case patch.Position != nil:
		// A client-supplied position is the only way a key that PositionBetween
		// could never generate reaches the column. One such key breaks ordering
		// for the whole list: nothing can be placed before a key ending in the
		// alphabet's lowest digit, so "new task lands on top" silently lands it
		// second instead. See ValidPosition.
		if !ValidPosition(*patch.Position) {
			return db.Task{}, NewError(422, "validation_failed",
				fmt.Sprintf("position must be 1-%d characters from [0-9A-Za-z] and must not end in '0'", MaxPositionLen))
		}
		position = *patch.Position
	case move:
		maxPos, perr := d.store.MaxPositionInList(ctx, destList)
		if perr != nil {
			return db.Task{}, fmt.Errorf("max position: %w", perr)
		}
		position = PositionBetween(maxPos, "") // land at the end of the destination
	}
	if patch.Status != nil && *patch.Status != "open" && *patch.Status != "done" {
		return db.Task{}, NewError(422, "validation_failed", "status must be 'open' or 'done'")
	}

	// A reorder changes only where a row sits. Deciding that here, where the
	// patch is in hand, rather than inside the writer, which would have to
	// infer it from a payload that looks identical to a real edit.
	if patch.isPositionOnly() {
		ctx = activity.Suppress(ctx)
	}

	var out db.Task
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		// Snapshot the children before the move so the source list's deletion
		// events carry their pre-move state.
		var kidsBefore []db.Task
		if move {
			var e error
			kidsBefore, e = q.ListSubtasks(ctx, &id)
			if e != nil {
				return e
			}
		}
		t, e := q.UpdateTask(ctx, db.UpdateTaskParams{
			ID: id, Title: title, Body: body, DueAt: dueAt, Priority: i16(priority),
			Tags: tags, Position: position, ParentID: parentID, ListID: destList,
		})
		if e != nil {
			return e
		}
		out = t
		statusChanged := patch.Status != nil && *patch.Status != cur.Status
		if statusChanged {
			out, e = q.SetTaskStatus(ctx, db.SetTaskStatusParams{ID: id, Status: *patch.Status})
			if e != nil {
				return e
			}
		}
		if !move {
			evType := events.TypeTaskUpdated
			if statusChanged && *patch.Status == "done" {
				evType = events.TypeTaskCompleted
			}
			return events.Write(ctx, q, evType, &cur.ListID, actorOf(p), taskResource(out), nil)
		}

		// Move: children follow, then the deleted(src)+created(dst) event pair
		// for the task and each child (see the method doc).
		kidsAfter, e := q.MoveSubtasksToList(ctx, db.MoveSubtasksToListParams{ParentID: &id, ListID: destList})
		if e != nil {
			return e
		}
		fromName, _ := q.GetListName(ctx, cur.ListID)
		toName, _ := q.GetListName(ctx, destList)
		// One sentence for the pair below. See activity.Replace.
		ctx := activity.Replace(ctx, activity.Fields{
			Action: "task.moved", Subject: out.Title,
			ListID: &destList, ListName: toName,
			Detail: map[string]any{"from_list": fromName, "to_list": toName},
		})
		if e := events.Write(ctx, q, events.TypeTaskDeleted, &cur.ListID, actorOf(p), taskResource(cur), nil); e != nil {
			return e
		}
		if e := events.Write(ctx, q, events.TypeTaskCreated, &destList, actorOf(p), taskResource(out), nil); e != nil {
			return e
		}
		for i := range kidsBefore {
			if e := events.Write(ctx, q, events.TypeTaskDeleted, &cur.ListID, actorOf(p), taskResource(kidsBefore[i]), nil); e != nil {
				return e
			}
		}
		for i := range kidsAfter {
			if e := events.Write(ctx, q, events.TypeTaskCreated, &destList, actorOf(p), taskResource(kidsAfter[i]), nil); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return db.Task{}, fmt.Errorf("update task: %w", err)
	}
	return out, nil
}

// CompleteTask marks a task done (editor only).
func (d *Domain) CompleteTask(ctx context.Context, p *auth.Principal, id uuid.UUID) (db.Task, error) {
	cur, _, err := d.authorizeTask(ctx, p, id, RoleEditor, true, false)
	if err != nil {
		return db.Task{}, err
	}
	var out db.Task
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		t, e := q.SetTaskStatus(ctx, db.SetTaskStatusParams{ID: id, Status: "done"})
		if e != nil {
			return e
		}
		out = t
		return events.Write(ctx, q, events.TypeTaskCompleted, &cur.ListID, actorOf(p), taskResource(t), nil)
	})
	if err != nil {
		return db.Task{}, fmt.Errorf("complete task: %w", err)
	}
	return out, nil
}

// DeleteTask soft-deletes a task (editor only).
func (d *Domain) DeleteTask(ctx context.Context, p *auth.Principal, id uuid.UUID) error {
	cur, _, err := d.authorizeTask(ctx, p, id, RoleEditor, true, false)
	if err != nil {
		return err
	}
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		if _, e := q.SoftDeleteTask(ctx, id); e != nil {
			return e
		}
		return events.Write(ctx, q, events.TypeTaskDeleted, &cur.ListID, actorOf(p), taskResource(cur), nil)
	})
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	return nil
}

// RestoreTask restores a soft-deleted task (editor only).
func (d *Domain) RestoreTask(ctx context.Context, p *auth.Principal, id uuid.UUID) (db.Task, error) {
	cur, _, err := d.authorizeTask(ctx, p, id, RoleEditor, true, true)
	if err != nil {
		return db.Task{}, err
	}
	var out db.Task
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		t, e := q.RestoreTask(ctx, id)
		if e != nil {
			return e
		}
		out = t
		return events.Write(ctx, q, events.TypeTaskRestored, &cur.ListID, actorOf(p), taskResource(t), nil)
	})
	if err != nil {
		if repo.IsNotFound(err) {
			return db.Task{}, NewError(409, "not_deleted", "task is not deleted")
		}
		return db.Task{}, fmt.Errorf("restore task: %w", err)
	}
	return out, nil
}

func validateTaskFields(title, body string, priority int) error {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > maxTitleLen {
		return NewError(422, "validation_failed", fmt.Sprintf("title must be 1-%d characters", maxTitleLen))
	}
	if len(body) > maxBodyLen {
		return NewError(422, "validation_failed", fmt.Sprintf("body must be <= %d characters", maxBodyLen))
	}
	if priority < 0 || priority > 3 {
		return NewError(422, "validation_failed", "priority must be 0-3")
	}
	return nil
}

// --- listing with cursor pagination ---

type taskCursor struct {
	Pos  string    `json:"p,omitempty"`
	Time time.Time `json:"t,omitempty"`
	ID   uuid.UUID `json:"i"`
}

func encodeTaskCursor(c taskCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeTaskCursor(s string) (taskCursor, bool) {
	if s == "" {
		return taskCursor{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return taskCursor{}, false
	}
	var c taskCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return taskCursor{}, false
	}
	return c, true
}

// ListTasks returns a filtered, cursor-paginated page of tasks plus the cursor
// for the next page ("" if none).
func (d *Domain) ListTasks(ctx context.Context, p *auth.Principal, f TaskFilter) ([]db.Task, string, error) {
	if f.ListID != nil && !p.AllowsList(*f.ListID) {
		return nil, "", ErrNotFound
	}
	limit := f.Limit
	if limit <= 0 || limit > maxLimit {
		if limit <= 0 {
			limit = defaultLimit
		} else {
			limit = maxLimit
		}
	}
	cur, hasCur := decodeTaskCursor(f.Cursor)

	var rows []db.Task
	var err error
	switch f.Sort {
	case "created_at":
		rows, err = d.listByTime(ctx, p, f, cur, hasCur, limit, false)
	case "updated_at":
		rows, err = d.listByTime(ctx, p, f, cur, hasCur, limit, true)
	default: // position
		rows, err = d.listByPosition(ctx, p, f, cur, hasCur, limit)
	}
	if err != nil {
		return nil, "", err
	}

	next := ""
	if len(rows) > limit {
		last := rows[limit-1]
		rows = rows[:limit]
		switch f.Sort {
		case "created_at":
			next = encodeTaskCursor(taskCursor{Time: last.CreatedAt, ID: last.ID})
		case "updated_at":
			next = encodeTaskCursor(taskCursor{Time: last.UpdatedAt, ID: last.ID})
		default:
			next = encodeTaskCursor(taskCursor{Pos: last.Position, ID: last.ID})
		}
	}
	return rows, next, nil
}

func (d *Domain) listByPosition(ctx context.Context, p *auth.Principal, f TaskFilter, cur taskCursor, hasCur bool, limit int) ([]db.Task, error) {
	rows, err := d.store.ListTasksByPosition(ctx, db.ListTasksByPositionParams{
		UserID: p.UserID, ListID: f.ListID, ScopedListIds: p.ScopedListIDs,
		Status: f.Status, IncludeDeleted: f.IncludeDeleted,
		DueBefore: f.DueBefore, DueAfter: f.DueAfter, Tag: f.Tag, Q: f.Q, UpdatedSince: f.UpdatedSince,
		CursorSet: hasCur, CursorPosition: cur.Pos, CursorID: cur.ID, Lim: i32(limit + 1),
	})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	out := make([]db.Task, len(rows))
	for i, r := range rows {
		out[i] = r.Task
	}
	return out, nil
}

func (d *Domain) listByTime(ctx context.Context, p *auth.Principal, f TaskFilter, cur taskCursor, hasCur bool, limit int, updated bool) ([]db.Task, error) {
	if updated {
		rows, err := d.store.ListTasksByUpdated(ctx, db.ListTasksByUpdatedParams{
			UserID: p.UserID, ListID: f.ListID, ScopedListIds: p.ScopedListIDs,
			Status: f.Status, IncludeDeleted: f.IncludeDeleted,
			DueBefore: f.DueBefore, DueAfter: f.DueAfter, Tag: f.Tag, Q: f.Q, UpdatedSince: f.UpdatedSince,
			CursorSet: hasCur, CursorTime: cur.Time, CursorID: cur.ID, Lim: i32(limit + 1),
		})
		if err != nil {
			return nil, fmt.Errorf("list tasks: %w", err)
		}
		out := make([]db.Task, len(rows))
		for i, r := range rows {
			out[i] = r.Task
		}
		return out, nil
	}
	rows, err := d.store.ListTasksByCreated(ctx, db.ListTasksByCreatedParams{
		UserID: p.UserID, ListID: f.ListID, ScopedListIds: p.ScopedListIDs,
		Status: f.Status, IncludeDeleted: f.IncludeDeleted,
		DueBefore: f.DueBefore, DueAfter: f.DueAfter, Tag: f.Tag, Q: f.Q, UpdatedSince: f.UpdatedSince,
		CursorSet: hasCur, CursorTime: cur.Time, CursorID: cur.ID, Lim: i32(limit + 1),
	})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	out := make([]db.Task, len(rows))
	for i, r := range rows {
		out[i] = r.Task
	}
	return out, nil
}
