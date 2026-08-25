package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
)

// changeDTO is one delta-feed item. Data is the event envelope written by the
// transactional outbox; it embeds a full resource snapshot, so clients apply
// it directly without a follow-up fetch (docs/offline-sync.md §5).
type changeDTO struct {
	Seq  int64           `json:"seq"`
	Type string          `json:"type" doc:"Event type, e.g. task.updated."`
	Data json.RawMessage `json:"data"`
}

type changesResponse struct {
	Body struct {
		Changes []changeDTO `json:"changes"`
		Next    int64       `json:"next" doc:"Pass as ?since on the next pull."`
		HasMore bool        `json:"has_more"`
		Reset   bool        `json:"reset" doc:"true: since predates event retention; full-resync via the REST collections, then poll from next."`
	}
}

// registerChangesRoute wires the offline-sync delta feed. Same authorization
// surface as SSE: membership evaluated at read time, token list scopes apply.
func (d Deps) registerChangesRoute(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "changes-list", Method: http.MethodGet, Path: "/v1/changes",
		Summary: "Delta feed of changes for offline sync",
		Description: "Returns outbox events after the given cursor for every accessible list, in seq order. " +
			"Omit since to fetch only the current head cursor (bootstrap: fetch head, load collections via REST, then poll from head).",
		Tags: []string{"events"}, Errors: []int{401, 422},
	}, func(ctx context.Context, in *struct {
		Since string `query:"since" doc:"Cursor from a previous pull's next (integer ≥ 0). Omit for head-only."`
		Limit int    `query:"limit" minimum:"1" maximum:"500" doc:"Max changes to return (default 200)."`
	}) (*changesResponse, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		// huma has no optional numeric query params; empty string means the
		// head-only bootstrap mode.
		var since *int64
		if in.Since != "" {
			v, perr := strconv.ParseInt(in.Since, 10, 64)
			if perr != nil || v < 0 {
				return nil, NewProblem(422, "validation_failed", "since must be a non-negative integer")
			}
			since = &v
		}
		page, err := d.Domain.Changes(ctx, p, since, in.Limit)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &changesResponse{}
		out.Body.Changes = make([]changeDTO, 0, len(page.Items))
		for _, c := range page.Items {
			out.Body.Changes = append(out.Body.Changes, changeDTO{Seq: c.Seq, Type: c.Type, Data: c.Payload})
		}
		out.Body.Next = page.Next
		out.Body.HasMore = page.HasMore
		out.Body.Reset = page.Reset
		return out, nil
	})
}
