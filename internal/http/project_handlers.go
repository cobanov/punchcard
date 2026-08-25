package http

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/service"
)

// ProjectDTO is the public representation of a project.
//
// HourlyRateCents is a pointer and stays one all the way out to JSON: null means
// "not costed", 0 means "costed at zero", and collapsing those two would make
// the report for an unpriced project read as free work.
type ProjectDTO struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Client          string     `json:"client,omitempty"`
	Color           string     `json:"color,omitempty" doc:"Palette name, not a hex value."`
	HourlyRateCents *int64     `json:"hourly_rate_cents" doc:"Minor units (kuruş/cents). null = not costed."`
	Currency        string     `json:"currency"`
	Billable        bool       `json:"billable"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func projectDTO(p db.Project) ProjectDTO {
	color := ""
	if p.Color != nil {
		color = *p.Color
	}
	return ProjectDTO{
		ID: p.ID.String(), Name: p.Name, Client: p.Client, Color: color,
		HourlyRateCents: p.HourlyRateCents, Currency: p.Currency, Billable: p.Billable,
		ArchivedAt: p.ArchivedAt, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// RepoDTO is a GitHub repository linked to a project.
type RepoDTO struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	FullName  string    `json:"full_name" doc:"owner/name"`
	CreatedAt time.Time `json:"created_at"`
}

func repoDTO(r db.ProjectRepo) RepoDTO {
	return RepoDTO{
		ID: r.ID.String(), ProjectID: r.ProjectID.String(),
		FullName: r.FullName, CreatedAt: r.CreatedAt,
	}
}

type projectBody struct {
	Body ProjectDTO
}

func (d Deps) registerProjectRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "projects-list", Method: http.MethodGet, Path: "/v1/projects",
		Summary: "List projects", Tags: []string{"projects"}, Errors: []int{401},
	}, func(ctx context.Context, in *struct {
		IncludeArchived bool `query:"include_archived" doc:"Include archived projects."`
	}) (*struct {
		Body struct {
			Projects []ProjectDTO `json:"projects"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := d.Domain.ListProjects(ctx, p, in.IncludeArchived)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Projects []ProjectDTO `json:"projects"`
			}
		}{}
		out.Body.Projects = make([]ProjectDTO, 0, len(rows))
		for _, r := range rows {
			out.Body.Projects = append(out.Body.Projects, projectDTO(r))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "projects-create", Method: http.MethodPost, Path: "/v1/projects",
		Summary: "Create a project", Tags: []string{"projects"},
		DefaultStatus: http.StatusCreated, Errors: []int{401, 403, 409, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			Name            string `json:"name" minLength:"1" maxLength:"200"`
			Client          string `json:"client,omitempty" maxLength:"200"`
			Color           string `json:"color,omitempty" enum:"red,amber,green,teal,blue,violet,pink,slate"`
			HourlyRateCents *int64 `json:"hourly_rate_cents,omitempty" minimum:"0"`
			Currency        string `json:"currency,omitempty" doc:"Three-letter code; defaults to TRY."`
			Billable        *bool  `json:"billable,omitempty" doc:"Defaults to true."`
		}
	}) (*projectBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		billable := true
		if in.Body.Billable != nil {
			billable = *in.Body.Billable
		}
		proj, err := d.Domain.CreateProject(ctx, p, service.CreateProjectInput{
			Name: in.Body.Name, Client: in.Body.Client, Color: in.Body.Color,
			HourlyRateCents: in.Body.HourlyRateCents, Currency: in.Body.Currency,
			Billable: billable,
		})
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &projectBody{Body: projectDTO(proj)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "projects-get", Method: http.MethodGet, Path: "/v1/projects/{id}",
		Summary: "Get a project", Tags: []string{"projects"}, Errors: []int{401, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*projectBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		proj, err := d.Domain.GetProject(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &projectBody{Body: projectDTO(proj)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "projects-update", Method: http.MethodPatch, Path: "/v1/projects/{id}",
		Summary: "Update a project", Tags: []string{"projects"}, Errors: []int{401, 403, 404, 409, 422},
	}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			Name            *string `json:"name,omitempty" minLength:"1" maxLength:"200"`
			Client          *string `json:"client,omitempty" maxLength:"200"`
			Color           *string `json:"color,omitempty" doc:"Palette name, or empty string to clear."`
			HourlyRateCents *int64  `json:"hourly_rate_cents,omitempty" minimum:"0"`
			ClearRate       bool    `json:"clear_hourly_rate,omitempty" doc:"Remove the rate entirely."`
			Currency        *string `json:"currency,omitempty"`
			Billable        *bool   `json:"billable,omitempty"`
		}
	}) (*projectBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		upd := service.UpdateProjectInput{
			Name: in.Body.Name, Client: in.Body.Client,
			HourlyRateCents: in.Body.HourlyRateCents, ClearRate: in.Body.ClearRate,
			Currency: in.Body.Currency, Billable: in.Body.Billable,
		}
		// An empty colour is how a client says "remove it"; a JSON null would be
		// indistinguishable from an absent field.
		if in.Body.Color != nil {
			if *in.Body.Color == "" {
				upd.ClearColor = true
			} else {
				upd.Color = in.Body.Color
			}
		}
		proj, err := d.Domain.UpdateProject(ctx, p, id, upd)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &projectBody{Body: projectDTO(proj)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "projects-delete", Method: http.MethodDelete, Path: "/v1/projects/{id}",
		Summary: "Archive a project, or delete it if nothing was ever booked against it",
		Tags:    []string{"projects"}, Errors: []int{401, 403, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct {
		Body struct {
			Archived bool `json:"archived" doc:"true when the project had sessions and was archived instead of deleted."`
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
		archived, err := d.Domain.DeleteProject(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Archived bool `json:"archived" doc:"true when the project had sessions and was archived instead of deleted."`
			}
		}{}
		out.Body.Archived = archived
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "project-repos-list", Method: http.MethodGet, Path: "/v1/projects/{id}/repos",
		Summary: "List the repositories linked to a project", Tags: []string{"projects"},
		Errors: []int{401, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct {
		Body struct {
			Repos []RepoDTO `json:"repos"`
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
		rows, err := d.Domain.ListProjectRepos(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Repos []RepoDTO `json:"repos"`
			}
		}{}
		out.Body.Repos = make([]RepoDTO, 0, len(rows))
		for _, r := range rows {
			out.Body.Repos = append(out.Body.Repos, repoDTO(r))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "project-repos-link", Method: http.MethodPost, Path: "/v1/projects/{id}/repos",
		Summary: "Link a GitHub repository to a project", Tags: []string{"projects"},
		DefaultStatus: http.StatusCreated, Errors: []int{401, 403, 404, 422},
	}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			FullName string `json:"full_name" doc:"owner/name, e.g. cobanov/punchcard"`
		}
	}) (*struct {
		Body RepoDTO
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		row, err := d.Domain.LinkRepo(ctx, p, id, in.Body.FullName)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{ Body RepoDTO }{Body: repoDTO(row)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "project-repos-unlink", Method: http.MethodDelete,
		Path:    "/v1/projects/{id}/repos/{repo_id}",
		Summary: "Unlink a repository from a project", Tags: []string{"projects"},
		DefaultStatus: http.StatusNoContent, Errors: []int{401, 403, 404},
	}, func(ctx context.Context, in *struct {
		ID     string `path:"id" format:"uuid"`
		RepoID string `path:"repo_id" format:"uuid"`
	}) (*struct{}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		repoID, err := parseUUID(in.RepoID)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.UnlinkRepo(ctx, p, id, repoID); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})
}
