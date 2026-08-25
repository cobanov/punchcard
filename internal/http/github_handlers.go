package http

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cobanov/punchcard/internal/service"
)

// GitHubStatusDTO is the connection as a client may see it. There is no token
// field, not even a masked one.
type GitHubStatusDTO struct {
	Connected   bool       `json:"connected"`
	Login       string     `json:"login,omitempty"`
	Scopes      string     `json:"scopes,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	LastScanAt  *time.Time `json:"last_scan_at,omitempty"`
	LastError   string     `json:"last_error,omitempty" doc:"Why scanning stopped, if it did. Shown so the integration never fails silently."`
	Emails      []string   `json:"extra_emails,omitempty"`
}

// ClusterDTO is a stretch of work with no session covering it.
//
// It carries two kinds of evidence and keeps them apart. Commits are proof
// punchcard fetched from GitHub; agent runs are what a local hook reported and
// nothing can verify. A client that renders them identically is overstating
// what it knows.
type ClusterDTO struct {
	From time.Time `json:"from" doc:"Suggested start. An agent run knows when work began, so a cluster with one starts there; a cluster of commits alone falls back to 15 minutes before the first."`
	To   time.Time `json:"to"`
	// Repos are owner/repo names. Dirs are bare directory names, from runs that
	// had no git remote — a weaker answer, kept in its own field so a client
	// cannot mistake one for the other.
	Repos              []string      `json:"repos"`
	Dirs               []string      `json:"dirs,omitempty"`
	Commits            []CommitDTO   `json:"commits"`
	AgentRuns          []AgentRunDTO `json:"agent_runs,omitempty"`
	SuggestedProjectID string        `json:"suggested_project_id,omitempty" doc:"Set only when the repository belongs to exactly one project."`
	SuggestedNote      string        `json:"suggested_note,omitempty"`
}

func (d Deps) registerGitHubRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "github-status", Method: http.MethodGet, Path: "/v1/github/status",
		Summary: "Whether GitHub is connected, and why scanning stopped if it did",
		Tags:    []string{"github"}, Errors: []int{401},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body GitHubStatusDTO
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		st, err := d.Domain.GitHubStatus(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct{ Body GitHubStatusDTO }{Body: GitHubStatusDTO{
			Connected: st.Connected, Login: st.Login, Scopes: st.Scopes,
			ConnectedAt: st.ConnectedAt, LastScanAt: st.LastScanAt, LastError: st.LastError,
		}}
		if st.Connected {
			if emails, eerr := d.Domain.ListGitHubEmails(ctx, p); eerr == nil {
				out.Body.Emails = emails
			}
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "github-disconnect", Method: http.MethodDelete, Path: "/v1/github/connection",
		Summary: "Disconnect GitHub and delete the stored token", Tags: []string{"github"},
		DefaultStatus: http.StatusNoContent, Errors: []int{401, 403},
	}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.DisconnectGitHub(ctx, p); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "github-add-email", Method: http.MethodPost, Path: "/v1/github/emails",
		Summary: "Add another address your commits may be authored with",
		Tags:    []string{"github"}, DefaultStatus: http.StatusNoContent,
		Errors: []int{401, 403, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Email string `json:"email" format:"email"`
		}
	}) (*struct{}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.AddGitHubEmail(ctx, p, in.Body.Email); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "github-remove-email", Method: http.MethodDelete, Path: "/v1/github/emails/{email}",
		Summary: "Remove an extra author address", Tags: []string{"github"},
		DefaultStatus: http.StatusNoContent, Errors: []int{401, 403, 404},
	}, func(ctx context.Context, in *struct {
		Email string `path:"email"`
	}) (*struct{}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.RemoveGitHubEmail(ctx, p, in.Email); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "github-unmatched", Method: http.MethodGet, Path: "/v1/github/unmatched",
		Summary: "Commits in a date range that no session covers",
		Description: "Work that happened while no timer was running, grouped into stretches. " +
			"This is how a forgotten timer is recovered: the evidence is already on GitHub.",
		Tags: []string{"github"}, Errors: []int{401, 422},
	}, func(ctx context.Context, in *struct {
		From string `query:"from"`
		To   string `query:"to"`
	}) (*struct {
		Body struct {
			Clusters []ClusterDTO `json:"clusters"`
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
		clusters, err := d.Domain.UnmatchedClusters(ctx, p, from, to)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Clusters []ClusterDTO `json:"clusters"`
			}
		}{}
		out.Body.Clusters = make([]ClusterDTO, 0, len(clusters))
		for _, cl := range clusters {
			dto := ClusterDTO{
				From: cl.From, To: cl.To, Repos: cl.Repos, Dirs: cl.Dirs,
				SuggestedNote: cl.SuggestedNote,
				Commits:       make([]CommitDTO, 0, len(cl.Commits)),
				AgentRuns:     make([]AgentRunDTO, 0, len(cl.Runs)),
			}
			if cl.SuggestedProjectID != nil {
				dto.SuggestedProjectID = cl.SuggestedProjectID.String()
			}
			for _, c := range cl.Commits {
				dto.Commits = append(dto.Commits, commitDTO(c))
			}
			for _, r := range cl.Runs {
				dto.AgentRuns = append(dto.AgentRuns, agentRunDTO(r))
			}
			out.Body.Clusters = append(out.Body.Clusters, dto)
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "github-recover-session", Method: http.MethodPost, Path: "/v1/github/unmatched/recover",
		Summary: "Turn a stretch of unmatched evidence into a session", Tags: []string{"github"},
		DefaultStatus: http.StatusCreated, Errors: []int{401, 403, 404, 409, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			ProjectID string    `json:"project_id" format:"uuid"`
			From      time.Time `json:"from"`
			To        time.Time `json:"to"`
			Note      string    `json:"note,omitempty" maxLength:"500"`
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
		ws, err := d.Domain.SessionFromCluster(ctx, p, service.ClusterToSessionInput{
			ProjectID: projectID, From: in.Body.From, To: in.Body.To, Note: in.Body.Note,
		})
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &sessionBody{Body: sessionDTO(ws)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessions-rescan", Method: http.MethodPost, Path: "/v1/sessions/{id}/rescan",
		Summary: "Queue a fresh commit scan for one session", Tags: []string{"sessions"},
		DefaultStatus: http.StatusAccepted, Errors: []int{401, 403, 404, 422},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct {
		Body struct {
			Queued bool `json:"queued"`
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
		if err := d.Domain.RescanSession(ctx, p, id); err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Queued bool `json:"queued"`
			}
		}{}
		out.Body.Queued = true
		return out, nil
	})
}
