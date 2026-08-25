package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/activity"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/gemini"
	"github.com/cobanov/punchcard/internal/service"
)

// POST /v1/chat — a sentence in, changes to the board out.
//
// The shape of this is deliberately small: the model is given the same
// task/list operations the REST API and the MCP server already expose, and it
// calls them through service.Domain **with the caller's own principal**. That
// last part is the whole security story — the assistant cannot reach a list the
// user cannot reach, because it is not a new authorization surface at all, it
// is the existing one with a different caller. Nothing here re-implements a
// permission check, which is exactly why nothing here can get one wrong.
//
// Mutations land through Domain, so they write the outbox in-tx like every
// other write and arrive on every connected device over SSE. The board updates
// itself; this endpoint does not push anything.

const (
	// The model gets a few turns to look something up, act, and report. Five is
	// generous for "add three things and set a date" and still bounds the worst
	// case — a model that keeps calling list_tasks forever costs the user a
	// spinner, not a bill.
	maxChatTurns = 5
	// A reply is a sentence about a todo list, not an essay.
	maxChatMessage = 2000
)

type chatIn struct {
	Body struct {
		Message string `json:"message" minLength:"1" maxLength:"2000" doc:"What the user typed."`
		ListID  string `json:"list_id,omitempty" format:"uuid" doc:"The list the user is looking at, so \"add milk\" has a home."`
		// The server cannot infer this and the model cannot guess it. Without
		// it "tomorrow at 9" is dated 09:00 UTC, which is 12:00 for a caller in
		// +03:00 — measured, not hypothetical. The browser knows the answer
		// (Intl.DateTimeFormat().resolvedOptions().timeZone), so it sends it.
		Timezone string `json:"timezone,omitempty" maxLength:"64" doc:"IANA zone of the caller, e.g. Europe/Istanbul."`
	}
}

type chatOut struct {
	Body struct {
		Reply string `json:"reply" doc:"One short sentence for the user."`
		// `nullable:"false"` and the normalisation at the end of the handler are
		// one promise made twice: a Go nil slice marshals as `null`, and huma's
		// reflection knows it, so without both the schema advertised
		// `["array","null"]` and the commonest reply of all — a question that
		// changed nothing — put `"actions": null` on the wire. A client that
		// reads `.length` off this field then threw on the answer it asked for.
		Actions []string `json:"actions" nullable:"false" doc:"What was actually done, in order. Always an array; empty when nothing changed."`
		Model   string   `json:"model"`
	}
}

func (d Deps) registerChatRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "chat", Method: http.MethodPost, Path: "/v1/chat",
		Summary: "Ask the assistant to change your lists",
		Tags:    []string{"chat"},
		Errors:  []int{400, 401, 422, 429, 503},
	}, func(ctx context.Context, in *chatIn) (*chatOut, error) {
		// Authorization first, before a single outbound token is spent.
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if !d.Gemini.Enabled() {
			return nil, huma.Error503ServiceUnavailable(
				"The assistant is not configured on this server (GEMINI_API_KEY is unset).")
		}
		// Everything the model does below runs on the caller's own principal,
		// which is what makes it safe and also what makes it invisible. This is
		// the only place that difference can be recorded.
		ctx = activity.WithOrigin(ctx, activity.Agent)
		msg := strings.TrimSpace(in.Body.Message)
		if msg == "" {
			return nil, huma.Error422UnprocessableEntity("Say something for the assistant to act on.")
		}

		// The model needs to know what lists exist before it can route anything
		// to one, and it needs their ids because every tool takes an id.
		lists, err := d.Domain.ListLists(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}

		runner := &chatRunner{deps: d, principal: p}
		system := buildChatSystemPrompt(lists, in.Body.ListID, in.Body.Timezone)
		contents := []gemini.Content{{Role: "user", Parts: []gemini.Part{{Text: msg}}}}

		var reply string
		for turn := 0; turn < maxChatTurns; turn++ {
			answer, err := d.Gemini.Generate(ctx, system, contents, chatTools())
			if err != nil {
				d.Logger.WarnContext(ctx, "chat: model call failed",
					"err", err, "turn", turn, "actions", len(runner.actions))
				// Anything already committed is on the user's board, and every
				// device has been told about it. Returning an error here would
				// report "unavailable" for changes that visibly landed — the
				// first version of this did exactly that, creating three tasks
				// and answering 503. Stop the loop, keep the work, and let the
				// reply be the list of what was done.
				if len(runner.actions) == 0 {
					return nil, chatProblem(err)
				}
				break
			}
			contents = append(contents, gemini.Content{Role: "model", Parts: answer.Parts})

			var results []gemini.Part
			for _, part := range answer.Parts {
				if part.Text != "" {
					reply = strings.TrimSpace(part.Text)
				}
				if part.FunctionCall == nil {
					continue
				}
				out := runner.call(ctx, part.FunctionCall.Name, part.FunctionCall.Args)
				results = append(results, gemini.Part{FunctionResponse: &gemini.FunctionResponse{
					Name: part.FunctionCall.Name, Response: out,
				}})
			}
			if len(results) == 0 {
				break // no tools asked for: the model is done talking
			}
			// Tool results go back as a "user" turn — the API's convention, not
			// a mistake: from the model's side they are input.
			contents = append(contents, gemini.Content{Role: "user", Parts: results})
		}

		out := &chatOut{}
		// Never nil. `runner.actions` is only ever appended to, so a turn that
		// called no mutating tool leaves it nil — and nil is `null`, not `[]`.
		// "Summarise my daily list" is exactly that shape: list_tasks records
		// nothing, the model answers in prose, and the field the client uses to
		// tell an answer from a receipt arrived as null. See
		// TestChatActionsAreAnEmptyArrayOnTheWire, which asserts the bytes —
		// the struct cannot tell you what was sent.
		out.Body.Actions = runner.actions
		if out.Body.Actions == nil {
			out.Body.Actions = []string{}
		}
		out.Body.Model = d.Gemini.Model()
		out.Body.Reply = chatReply(reply, runner.actions)
		return out, nil
	})
}

// chatReply keeps the user from ever seeing an empty bubble. A model that spent
// its last turn calling tools sometimes returns no closing text; the actions it
// took are a better answer than silence anyway.
func chatReply(reply string, actions []string) string {
	if reply != "" {
		if len(reply) > maxChatMessage {
			return reply[:maxChatMessage]
		}
		return reply
	}
	if len(actions) > 0 {
		return strings.Join(actions, ", ") + "."
	}
	return "I could not work out what to change — try naming the list and the task."
}

// buildChatSystemPrompt tells the model what it is, what exists, and what day
// it is. That last one is not a nicety: without it the model dates "tomorrow"
// from its training cutoff. Measured against gemini-3-flash-preview, "yarına
// dişçiyi ara" came back with due_at in 2025.
func buildChatSystemPrompt(lists []service.ListWithRole, activeID, tz string) string {
	now := time.Now()
	// An unknown or unparseable zone falls back to the server's, which is at
	// least a real place rather than UTC pretending to be one.
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			now = now.In(loc)
		}
	}
	var b strings.Builder
	b.WriteString(`You manage a person's todo board. Use the tools to make the change they ask for, then reply with ONE short sentence saying what you did, in the user's own language.

Rules:
- Prefer acting over asking. If the list is obvious from the request, use it.
- Never invent a task id. Call list_tasks first to find one.
- Create several tasks in one create_tasks call rather than many create_task calls.
- If the request is not about the todo board, say so briefly and change nothing.
`)
	// The offset is spelled out because "send UTC" is what produced the bug
	// this replaces: the user asks for 9am meaning their own 9am, and a model
	// told only "UTC" sends 09:00Z, which lands at noon for a caller in +03:00.
	fmt.Fprintf(&b, "\nRight now it is %s where the user is.\nTimes the user says are in THAT zone. Send due dates as RFC3339 with that offset (e.g. tomorrow at 9am is %s).\n",
		now.Format("Monday, 2 January 2006 15:04 -07:00"),
		time.Date(now.Year(), now.Month(), now.Day()+1, 9, 0, 0, 0, now.Location()).Format(time.RFC3339))

	b.WriteString("\nLists (use these ids verbatim):\n")
	for _, l := range lists {
		mark := ""
		if l.List.ID.String() == activeID {
			mark = "  <- the list the user is looking at"
		}
		fmt.Fprintf(&b, "- %s  id=%s  role=%s%s\n", l.List.Name, l.List.ID, l.Role, mark)
	}
	if len(lists) == 0 {
		b.WriteString("- (none yet; create one with create_list before adding tasks)\n")
	}
	return b.String()
}

// ── Tools ───────────────────────────────────────────────────────────────────

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// chatTools mirrors the todo_* set the MCP server exposes, trimmed to what a
// sentence can plausibly ask for. Token, webhook, member and account
// operations are absent here for the same reason they are absent there: a
// leaked agent must not be able to establish persistence or widen its own
// access.
func chatTools() []gemini.Tool {
	return []gemini.Tool{{FunctionDeclarations: []gemini.FunctionDeclaration{
		{
			Name:        "list_tasks",
			Description: "Read tasks. Call this before completing, updating or deleting anything, to get real ids.",
			Parameters: obj(map[string]any{
				"list_id": str("Restrict to one list."),
				"query":   str("Match against the title."),
				"status":  str("open | done | all. Defaults to open."),
			}),
		},
		{
			Name:        "create_tasks",
			Description: "Create one or more tasks in a list. Preferred over repeated create_task.",
			Parameters: obj(map[string]any{
				"list_id": str("Target list id."),
				"titles":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "One title per task."},
			}, "list_id", "titles"),
		},
		{
			Name:        "create_task",
			Description: "Create a single task, optionally with a note and a due date.",
			Parameters: obj(map[string]any{
				"list_id": str("Target list id."),
				"title":   str("The task."),
				"body":    str("An optional note."),
				"due_at":  str("RFC3339 UTC."),
			}, "list_id", "title"),
		},
		{
			Name:        "update_task",
			Description: "Change a task: rename it, set or clear its due date, or move it to another list.",
			Parameters: obj(map[string]any{
				"task_id":      str("Task id from list_tasks."),
				"title":        str("New title."),
				"due_at":       str("RFC3339 UTC."),
				"clear_due_at": map[string]any{"type": "boolean", "description": "Remove the due date."},
				"list_id":      str("Move to this list."),
			}, "task_id"),
		},
		{
			Name:        "complete_task",
			Description: "Mark a task done.",
			Parameters:  obj(map[string]any{"task_id": str("Task id from list_tasks.")}, "task_id"),
		},
		{
			Name:        "delete_task",
			Description: "Delete a task. Recoverable for 30 days.",
			Parameters:  obj(map[string]any{"task_id": str("Task id from list_tasks.")}, "task_id"),
		},
		{
			Name:        "create_list",
			Description: "Create a new list.",
			Parameters:  obj(map[string]any{"name": str("List name.")}, "name"),
		},
	}}}
}

// ── Execution ───────────────────────────────────────────────────────────────

type chatRunner struct {
	deps      Deps
	principal *auth.Principal
	actions   []string
}

func (r *chatRunner) note(format string, args ...any) {
	r.actions = append(r.actions, fmt.Sprintf(format, args...))
}

// fail returns an error to the MODEL rather than to the user. A tool that
// refuses is information the model can act on — it will usually apologise or
// try a different list — whereas failing the request would throw away the work
// already done in earlier turns.
func fail(format string, args ...any) map[string]any {
	return map[string]any{"error": fmt.Sprintf(format, args...)}
}

func (r *chatRunner) call(ctx context.Context, name string, raw json.RawMessage) map[string]any {
	var args map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return fail("could not read the arguments: %v", err)
		}
	}
	s := func(k string) string {
		v, _ := args[k].(string)
		return strings.TrimSpace(v)
	}
	id := func(k string) (uuid.UUID, error) { return uuid.Parse(s(k)) }

	switch name {
	case "list_tasks":
		f := service.TaskFilter{Limit: 50, Status: "open"}
		if v := s("status"); v == "done" || v == "all" {
			f.Status = v
		}
		if v := s("list_id"); v != "" {
			lid, err := uuid.Parse(v)
			if err != nil {
				return fail("list_id is not a uuid")
			}
			f.ListID = &lid
		}
		if v := s("query"); v != "" {
			f.Q = v
		}
		tasks, _, err := r.deps.Domain.ListTasks(ctx, r.principal, f)
		if err != nil {
			return fail("%v", err)
		}
		out := make([]map[string]any, 0, len(tasks))
		for _, t := range tasks {
			out = append(out, map[string]any{
				"id": t.ID.String(), "title": t.Title, "status": t.Status, "list_id": t.ListID.String(),
			})
		}
		return map[string]any{"tasks": out}

	case "create_tasks":
		lid, err := id("list_id")
		if err != nil {
			return fail("list_id is not a uuid")
		}
		titles, _ := args["titles"].([]any)
		if len(titles) == 0 {
			return fail("titles was empty")
		}
		created := 0
		for _, raw := range titles {
			title := strings.TrimSpace(fmt.Sprint(raw))
			if title == "" {
				continue
			}
			if _, err := r.deps.Domain.CreateTask(ctx, r.principal, service.TaskInput{ListID: lid, Title: title}); err != nil {
				return fail("%v", err)
			}
			created++
		}
		r.note("created %d task(s)", created)
		return map[string]any{"created": created}

	case "create_task":
		lid, err := id("list_id")
		if err != nil {
			return fail("list_id is not a uuid")
		}
		title := s("title")
		if title == "" {
			return fail("title was empty")
		}
		input := service.TaskInput{ListID: lid, Title: title, Body: s("body")}
		if v := s("due_at"); v != "" {
			when, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return fail("due_at must be RFC3339")
			}
			input.DueAt = &when
		}
		if _, err := r.deps.Domain.CreateTask(ctx, r.principal, input); err != nil {
			return fail("%v", err)
		}
		r.note("created %q", title)
		return map[string]any{"ok": true}

	case "update_task":
		tid, err := id("task_id")
		if err != nil {
			return fail("task_id is not a uuid")
		}
		var patch service.TaskPatch
		if v := s("title"); v != "" {
			patch.Title = &v
		}
		if v := s("due_at"); v != "" {
			when, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return fail("due_at must be RFC3339")
			}
			patch.DueAt = &when
		}
		if v, ok := args["clear_due_at"].(bool); ok && v {
			patch.ClearDueAt = true
		}
		if v := s("list_id"); v != "" {
			lid, err := uuid.Parse(v)
			if err != nil {
				return fail("list_id is not a uuid")
			}
			patch.ListID = &lid
		}
		t, err := r.deps.Domain.UpdateTask(ctx, r.principal, tid, patch)
		if err != nil {
			return fail("%v", err)
		}
		r.note("updated %q", t.Title)
		return map[string]any{"ok": true}

	case "complete_task":
		tid, err := id("task_id")
		if err != nil {
			return fail("task_id is not a uuid")
		}
		t, err := r.deps.Domain.CompleteTask(ctx, r.principal, tid)
		if err != nil {
			return fail("%v", err)
		}
		r.note("completed %q", t.Title)
		return map[string]any{"ok": true}

	case "delete_task":
		tid, err := id("task_id")
		if err != nil {
			return fail("task_id is not a uuid")
		}
		if err := r.deps.Domain.DeleteTask(ctx, r.principal, tid); err != nil {
			return fail("%v", err)
		}
		r.note("deleted a task")
		return map[string]any{"ok": true}

	case "create_list":
		name := s("name")
		if name == "" {
			return fail("name was empty")
		}
		l, err := r.deps.Domain.CreateList(ctx, r.principal, name, nil)
		if err != nil {
			return fail("%v", err)
		}
		r.note("created the list %q", name)
		return map[string]any{"id": l.ID.String()}
	}
	return fail("no such tool: %s", name)
}

// chatProblem maps the assistant's own failures onto statuses the client can
// act on, rather than folding everything into 500. A quota error is temporary
// and worth retrying; a bad key is not, and the difference matters to whoever
// is looking at the log.
func chatProblem(err error) error {
	var ge *gemini.Error
	if ok := asGeminiError(err, &ge); ok {
		switch ge.Code {
		case 429:
			return huma.Error429TooManyRequests("The assistant is over its quota right now — try again shortly.")
		case 401, 403:
			return huma.Error503ServiceUnavailable("The assistant's credentials were rejected.")
		case 404:
			return huma.Error503ServiceUnavailable("The configured model is not available to this key.")
		}
	}
	return huma.Error503ServiceUnavailable("The assistant is unavailable right now.")
}

func asGeminiError(err error, out **gemini.Error) bool {
	if ge, ok := err.(*gemini.Error); ok {
		*out = ge
		return true
	}
	return false
}
