package http

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/service"
)

// SessionDTO is a work session.
//
// Seconds is computed, never stored: ended_at - started_at is the only source of
// truth, and a stored duration would eventually disagree with times the user
// just corrected. A running session reports the elapsed time so far.
type SessionDTO struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id"`
	Note      string     `json:"note"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at" doc:"null while the timer is running."`
	Seconds   int64      `json:"seconds"`
	Running   bool       `json:"running"`
	Source    string     `json:"source"`
	SyncState string     `json:"commit_sync_state" enum:"pending,ok,error,skipped"`
	SyncError string     `json:"commit_sync_error,omitempty"`
}

func sessionDTO(ws db.WorkSession) SessionDTO {
	end := ws.EndedAt
	seconds := int64(time.Since(ws.StartedAt).Seconds())
	if end != nil {
		seconds = int64(end.Sub(ws.StartedAt).Seconds())
	}
	syncErr := ""
	if ws.SyncError != nil {
		syncErr = *ws.SyncError
	}
	return SessionDTO{
		ID: ws.ID.String(), ProjectID: ws.ProjectID.String(), Note: ws.Note,
		StartedAt: ws.StartedAt, EndedAt: end, Seconds: seconds,
		Running: end == nil, Source: ws.Source,
		SyncState: ws.SyncState, SyncError: syncErr,
	}
}

// CommitDTO is a commit attributed to a session.
type CommitDTO struct {
	SHA         string    `json:"sha"`
	Repo        string    `json:"repo"`
	Message     string    `json:"message"`
	CommittedAt time.Time `json:"committed_at"`
	URL         string    `json:"url,omitempty"`
}

func commitDTO(c db.Commit) CommitDTO {
	return CommitDTO{
		SHA: c.Sha, Repo: c.RepoFullName, Message: c.Message,
		CommittedAt: c.CommittedAt, URL: c.Url,
	}
}

type sessionBody struct {
	Body SessionDTO
}

// defaultRange is the window a listing covers when the caller names neither end:
// the last thirty days up to now, which is what a "recent work" screen wants.
func defaultRange(from, to string) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	f, t := now.AddDate(0, 0, -30), now
	if from != "" {
		parsed, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return f, t, NewProblem(422, "validation_failed", "from must be an RFC 3339 timestamp")
		}
		f = parsed.UTC()
	}
	if to != "" {
		parsed, err := time.Parse(time.RFC3339, to)
		if err != nil {
			return f, t, NewProblem(422, "validation_failed", "to must be an RFC 3339 timestamp")
		}
		t = parsed.UTC()
	}
	if !t.After(f) {
		return f, t, NewProblem(422, "validation_failed", "to must be after from")
	}
	return f, t, nil
}

func (d Deps) registerSessionRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "sessions-list", Method: http.MethodGet, Path: "/v1/sessions",
		Summary: "List work sessions overlapping a date range", Tags: []string{"sessions"},
		Errors: []int{401, 404, 422},
	}, func(ctx context.Context, in *struct {
		From      string `query:"from" doc:"RFC 3339; defaults to 30 days ago."`
		To        string `query:"to" doc:"RFC 3339; defaults to now."`
		ProjectID string `query:"project_id" doc:"Restrict to one project."`
	}) (*struct {
		Body struct {
			Sessions []SessionDTO `json:"sessions"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		from, to, err := defaultRange(in.From, in.To)
		if err != nil {
			return nil, err
		}
		var projectID *uuid.UUID
		if in.ProjectID != "" {
			id, perr := parseUUID(in.ProjectID)
			if perr != nil {
				return nil, perr
			}
			projectID = &id
		}
		rows, err := d.Domain.ListSessions(ctx, p, from, to, projectID)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Sessions []SessionDTO `json:"sessions"`
			}
		}{}
		out.Body.Sessions = make([]SessionDTO, 0, len(rows))
		for _, r := range rows {
			out.Body.Sessions = append(out.Body.Sessions, sessionDTO(r))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessions-current", Method: http.MethodGet, Path: "/v1/sessions/current",
		Summary: "The running session, if any", Tags: []string{"sessions"},
		Errors: []int{401, 404},
	}, func(ctx context.Context, _ *struct{}) (*sessionBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		ws, err := d.Domain.CurrentSession(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &sessionBody{Body: sessionDTO(ws)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessions-start", Method: http.MethodPost, Path: "/v1/sessions",
		Summary: "Start a timer", Tags: []string{"sessions"},
		DefaultStatus: http.StatusCreated, Errors: []int{401, 403, 404, 409, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			ProjectID   string     `json:"project_id" format:"uuid"`
			Note        string     `json:"note,omitempty" maxLength:"500"`
			StartedAt   *time.Time `json:"started_at,omitempty" doc:"Defaults to now."`
			Source      string     `json:"source,omitempty" enum:"web,cli,extension,mobile,auto"`
			StopCurrent *bool      `json:"stop_current,omitempty" doc:"Defaults to true: starting closes whatever was running."`
		}
	}) (*sessionBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		projectID, err := parseUUID(in.Body.ProjectID)
		if err != nil {
			return nil, err
		}
		stopCurrent := true
		if in.Body.StopCurrent != nil {
			stopCurrent = *in.Body.StopCurrent
		}
		ws, err := d.Domain.StartSession(ctx, p, service.StartSessionInput{
			ProjectID: projectID, Note: in.Body.Note, StartedAt: in.Body.StartedAt,
			Source: in.Body.Source, StopCurrent: stopCurrent,
		})
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &sessionBody{Body: sessionDTO(ws)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessions-stop", Method: http.MethodPost, Path: "/v1/sessions/{id}/stop",
		Summary: "Stop a running session", Tags: []string{"sessions"},
		Errors: []int{401, 403, 404, 422},
	}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			At *time.Time `json:"at,omitempty" doc:"Defaults to now."`
		}
	}) (*sessionBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		at := time.Time{}
		if in.Body.At != nil {
			at = *in.Body.At
		}
		ws, err := d.Domain.StopSession(ctx, p, id, at)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &sessionBody{Body: sessionDTO(ws)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessions-get", Method: http.MethodGet, Path: "/v1/sessions/{id}",
		Summary: "Get a session", Tags: []string{"sessions"}, Errors: []int{401, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*sessionBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		ws, err := d.Domain.GetSession(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &sessionBody{Body: sessionDTO(ws)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessions-update", Method: http.MethodPatch, Path: "/v1/sessions/{id}",
		Summary: "Correct a session's project, note or times", Tags: []string{"sessions"},
		Errors: []int{401, 403, 404, 409, 422},
	}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			ProjectID *string    `json:"project_id,omitempty" format:"uuid"`
			Note      *string    `json:"note,omitempty" maxLength:"500"`
			StartedAt *time.Time `json:"started_at,omitempty"`
			EndedAt   *time.Time `json:"ended_at,omitempty"`
			Reopen    bool       `json:"reopen,omitempty" doc:"Clear ended_at and let the timer run again."`
		}
	}) (*sessionBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		upd := service.UpdateSessionInput{
			Note: in.Body.Note, StartedAt: in.Body.StartedAt,
		}
		if in.Body.ProjectID != nil {
			pid, perr := parseUUID(*in.Body.ProjectID)
			if perr != nil {
				return nil, perr
			}
			upd.ProjectID = &pid
		}
		// JSON cannot distinguish "absent" from "null" through a *time.Time, so
		// reopening gets its own flag rather than an ambiguous null.
		switch {
		case in.Body.Reopen:
			upd.SetEnded, upd.EndedAt = true, nil
		case in.Body.EndedAt != nil:
			upd.SetEnded, upd.EndedAt = true, in.Body.EndedAt
		}
		ws, err := d.Domain.UpdateSession(ctx, p, id, upd)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &sessionBody{Body: sessionDTO(ws)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessions-split", Method: http.MethodPost, Path: "/v1/sessions/{id}/split",
		Summary: "Split a session in two", Tags: []string{"sessions"},
		DefaultStatus: http.StatusCreated, Errors: []int{401, 403, 404, 422},
	}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			At time.Time `json:"at" doc:"Must fall strictly inside the session."`
		}
	}) (*struct {
		Body struct {
			Left  SessionDTO `json:"left"`
			Right SessionDTO `json:"right"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		left, right, err := d.Domain.SplitSession(ctx, p, id, in.Body.At)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Left  SessionDTO `json:"left"`
				Right SessionDTO `json:"right"`
			}
		}{}
		out.Body.Left, out.Body.Right = sessionDTO(left), sessionDTO(right)
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessions-delete", Method: http.MethodDelete, Path: "/v1/sessions/{id}",
		Summary: "Delete a session", Tags: []string{"sessions"},
		DefaultStatus: http.StatusNoContent, Errors: []int{401, 403, 404},
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
		if err := d.Domain.DeleteSession(ctx, p, id); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessions-commits", Method: http.MethodGet, Path: "/v1/sessions/{id}/commits",
		Summary: "The commits attributed to a session", Tags: []string{"sessions"},
		Errors: []int{401, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct {
		Body struct {
			Commits []CommitDTO `json:"commits"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		rows, err := d.Domain.CommitsForSession(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Commits []CommitDTO `json:"commits"`
			}
		}{}
		out.Body.Commits = make([]CommitDTO, 0, len(rows))
		for _, r := range rows {
			out.Body.Commits = append(out.Body.Commits, commitDTO(r))
		}
		return out, nil
	})
}
