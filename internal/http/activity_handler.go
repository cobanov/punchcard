package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cobanov/punchcard/internal/service"
)

type activityItemDTO struct {
	ID         string         `json:"id"`
	OccurredAt time.Time      `json:"occurred_at"`
	Origin     string         `json:"origin" doc:"user | agent | mcp | api"`
	Action     string         `json:"action" doc:"e.g. task.created, task.moved"`
	Subject    string         `json:"subject,omitempty" doc:"Who or what the action was about, as it was then: the task or list name for task.*/list.*, and for member.* the affected member's display name, or their email if they have not set one."`
	ListID     *string        `json:"list_id,omitempty"`
	ListName   string         `json:"list_name,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
	ActorID    string         `json:"actor_id"`
	ActorName  string         `json:"actor_name"`
}

type activityResponse struct {
	Body struct {
		Items []activityItemDTO `json:"items"`
		Next  string            `json:"next,omitempty" doc:"Pass as ?before for the next page. Absent on the last page."`
	}
}

// registerActivityRoute wires the log. Read scope is enough — this is a read —
// and the scoping is membership-derived, matching /v1/changes. Writing a second
// scoping rule is how the authorization defects of 0.4.6 happened.
func (d Deps) registerActivityRoute(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "activity-list", Method: http.MethodGet, Path: "/v1/activity",
		Summary:     "What happened, newest first",
		Description: "Every change on the lists you can see, plus your own. Sentences are rendered by the client from these fields.",
		Tags:        []string{"events"},
		Errors:      []int{401, 422},
	}, func(ctx context.Context, in *struct {
		Before string `query:"before" doc:"Cursor from a previous page's next."`
		Limit  int    `query:"limit" minimum:"1" maximum:"200" doc:"Default 50."`
		Origin string `query:"origin" doc:"Comma-separated: user,agent,mcp,api."`
		Mine   bool   `query:"mine" doc:"Only your own actions."`
	}) (*activityResponse, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		f := service.ActivityFilter{Limit: in.Limit, Mine: in.Mine}
		if in.Before != "" {
			at, id, perr := service.ParseActivityCursor(in.Before)
			if perr != nil {
				return nil, NewProblem(422, "validation_failed", "before is not a cursor from a previous page")
			}
			f.Before, f.BeforeID = &at, id
		}
		if in.Origin != "" {
			for _, o := range strings.Split(in.Origin, ",") {
				if o = strings.TrimSpace(o); o != "" {
					f.Origins = append(f.Origins, o)
				}
			}
		}
		page, err := d.Domain.ListActivity(ctx, p, f)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &activityResponse{}
		out.Body.Items = make([]activityItemDTO, 0, len(page.Items))
		for _, it := range page.Items {
			dto := activityItemDTO{
				ID: it.ID.String(), OccurredAt: it.OccurredAt, Origin: it.Origin,
				Action: it.Action, Subject: it.Subject, ListName: it.ListName,
				Detail: it.Detail, ActorID: it.ActorID.String(), ActorName: it.ActorName,
			}
			if it.ListID != nil {
				s := it.ListID.String()
				dto.ListID = &s
			}
			out.Body.Items = append(out.Body.Items, dto)
		}
		out.Body.Next = page.Next
		return out, nil
	})
}
