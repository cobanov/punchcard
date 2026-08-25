package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// AllocationDTO is one project's share of a session's wall-clock.
type AllocationDTO struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Seconds   int64  `json:"seconds"`
	Evidenced bool   `json:"evidenced" doc:"true when evidence earned these seconds; false for the declared fallback."`
	Reason    string `json:"reason" enum:"linked,name,declared"`
}

// UnresolvedPlaceDTO is evidence whose place no project claims. Seconds here
// are activity, not an allocation — those minutes already followed the
// declaration — and the name exists so a client can offer create/link.
type UnresolvedPlaceDTO struct {
	Place     string `json:"place"`
	FullName  string `json:"full_name,omitempty" doc:"owner/repo when known; absent for remoteless directories."`
	Seconds   int64  `json:"seconds"`
	Ambiguous bool   `json:"ambiguous,omitempty" doc:"true when MORE than one project claims the place."`
}

func (d Deps) registerAttributionRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "sessions-attribution", Method: http.MethodGet, Path: "/v1/sessions/{id}/attribution",
		Summary: "How a session's time divides across projects, by its evidence",
		Tags:    []string{"sessions"}, Errors: []int{401, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct {
		Body struct {
			Allocations []AllocationDTO      `json:"allocations"`
			Unresolved  []UnresolvedPlaceDTO `json:"unresolved"`
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
		allocs, unres, err := d.Domain.SessionAttributionNamed(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Allocations []AllocationDTO      `json:"allocations"`
				Unresolved  []UnresolvedPlaceDTO `json:"unresolved"`
			}
		}{}
		// Both lists are always arrays, never omitted: an absent key and an
		// empty one are the same fact, and a client that has to tell them apart
		// is a client that will one day get it wrong.
		out.Body.Allocations = make([]AllocationDTO, 0, len(allocs))
		for _, a := range allocs {
			out.Body.Allocations = append(out.Body.Allocations, AllocationDTO{
				ProjectID: a.ProjectID.String(), Name: a.Name,
				Seconds: a.Seconds, Evidenced: a.Evidenced, Reason: string(a.Reason),
			})
		}
		out.Body.Unresolved = make([]UnresolvedPlaceDTO, 0, len(unres))
		for _, u := range unres {
			out.Body.Unresolved = append(out.Body.Unresolved, UnresolvedPlaceDTO{
				Place: u.Key, FullName: u.FullName, Seconds: u.Seconds, Ambiguous: u.Ambiguous,
			})
		}
		return out, nil
	})
}
