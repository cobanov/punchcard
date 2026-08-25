package http

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/service"
)

// AgentRunDTO is one reported working interval of an AI coding agent.
//
// Reported is the operative word, and the field name says so: unlike a commit,
// which punchcard fetches from GitHub itself, this is a local client's account
// of what it did. It is evidence, not proof, and no report treats it as time.
type AgentRunDTO struct {
	Tool      string    `json:"tool" doc:"Which agent reported this, e.g. claude-code."`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Seconds   int64     `json:"seconds"`
	Model     string    `json:"model,omitempty"`
	Cwd       string    `json:"cwd,omitempty"`
	Repo      string    `json:"repo,omitempty" doc:"Empty when the working directory had no git remote."`
	ToolCalls *int32    `json:"tool_calls,omitempty"`
}

func agentRunDTO(r db.AgentRun) AgentRunDTO {
	return AgentRunDTO{
		Tool: r.Tool, StartedAt: r.StartedAt, EndedAt: r.EndedAt,
		Seconds:   int64(r.EndedAt.Sub(r.StartedAt).Seconds()),
		Model:     r.Model,
		Cwd:       r.Cwd,
		Repo:      r.RepoFullName,
		ToolCalls: r.ToolCalls,
	}
}

func (d Deps) registerAgentRunRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "agent-runs-record", Method: http.MethodPost, Path: "/v1/agent-runs",
		Summary: "Report agent working intervals", Tags: []string{"agent-runs"},
		Description: "Batch upsert, keyed on (tool, external_id). Flushing the " +
			"same queue twice is safe: the response says how many rows were new " +
			"and how many were already known.",
		DefaultStatus: http.StatusAccepted, Errors: []int{401, 403, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Runs []struct {
				Tool       string    `json:"tool" minLength:"1" maxLength:"64"`
				ExternalID string    `json:"external_id" minLength:"1" maxLength:"200" doc:"Client-generated idempotency key, stable across resends."`
				StartedAt  time.Time `json:"started_at"`
				EndedAt    time.Time `json:"ended_at"`
				Model      string    `json:"model,omitempty" maxLength:"100"`
				Cwd        string    `json:"cwd,omitempty" maxLength:"500"`
				Repo       string    `json:"repo,omitempty" maxLength:"200" doc:"owner/repo; omit when there is no git remote."`
				ToolCalls  *int32    `json:"tool_calls,omitempty" minimum:"0"`
			} `json:"runs" minItems:"1" maxItems:"500"`
		}
	}) (*struct {
		Body struct {
			Accepted   int `json:"accepted"`
			Duplicates int `json:"duplicates" doc:"Runs this server already had. Not an error — the queue is flushed at-least-once."`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		runs := make([]service.AgentRunInput, 0, len(in.Body.Runs))
		for _, r := range in.Body.Runs {
			runs = append(runs, service.AgentRunInput{
				Tool: r.Tool, ExternalID: r.ExternalID,
				StartedAt: r.StartedAt, EndedAt: r.EndedAt,
				Model: r.Model, Cwd: r.Cwd, RepoFullName: r.Repo,
				ToolCalls: r.ToolCalls,
			})
		}
		accepted, duplicates, err := d.Domain.RecordAgentRuns(ctx, p, runs)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Accepted   int `json:"accepted"`
				Duplicates int `json:"duplicates" doc:"Runs this server already had. Not an error — the queue is flushed at-least-once."`
			}
		}{}
		out.Body.Accepted, out.Body.Duplicates = accepted, duplicates
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessions-agent-runs", Method: http.MethodGet, Path: "/v1/sessions/{id}/agent-runs",
		Summary: "The agent runs attributed to a session", Tags: []string{"sessions"},
		Errors: []int{401, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct {
		Body struct {
			AgentRuns []AgentRunDTO `json:"agent_runs"`
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
		rows, err := d.Domain.AgentRunsForSession(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				AgentRuns []AgentRunDTO `json:"agent_runs"`
			}
		}{}
		out.Body.AgentRuns = make([]AgentRunDTO, 0, len(rows))
		for _, r := range rows {
			out.Body.AgentRuns = append(out.Body.AgentRuns, agentRunDTO(r))
		}
		return out, nil
	})
}
