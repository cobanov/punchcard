package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cobanov/punchcard/internal/service"
)

// ProjectTotalDTO is one project's share of a reporting range.
type ProjectTotalDTO struct {
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Client      string `json:"client,omitempty"`
	Color       string `json:"color,omitempty" doc:"Palette name, not a hex value. Empty when the project has no colour set."`
	Seconds     int64  `json:"seconds"`
	AmountCents *int64 `json:"amount_cents" doc:"Minor units. null when the project is not costed or not billable — which is not the same as zero."`
	Currency    string `json:"currency"`
	Billable    bool   `json:"billable"`
}

// DayTotalDTO is one local day's total.
type DayTotalDTO struct {
	Date    string `json:"date" doc:"YYYY-MM-DD in the account's timezone."`
	Seconds int64  `json:"seconds"`
}

func (d Deps) registerReportRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "reports-summary", Method: http.MethodGet, Path: "/v1/reports/summary",
		Summary: "Totals for a date range, by project or by day", Tags: []string{"reports"},
		Errors: []int{401, 422},
	}, func(ctx context.Context, in *struct {
		From        string `query:"from" doc:"RFC 3339; defaults to 30 days ago."`
		To          string `query:"to" doc:"RFC 3339; defaults to now."`
		GroupBy     string `query:"group_by" enum:"project,day" default:"project"`
		Attribution string `query:"attribution" enum:"declared,evidence" default:"declared" doc:"How project totals are attributed. 'declared' bills every second to the session's own project. 'evidence' partitions each session across the projects its evidence shows active — quiet minutes still follow the declaration. Day totals are identical in both modes."`
	}) (*struct {
		Body struct {
			From     string `json:"from"`
			To       string `json:"to"`
			Timezone string `json:"timezone"`
			// Pointers, so "this grouping was not requested" and "this grouping
			// is empty" stop being the same thing on the wire. A plain slice with
			// omitempty drops an empty result entirely, and every client then has
			// to treat a missing key as an empty list — which the web client did
			// not, and the analytics screen threw on the first quiet week.
			Projects *[]ProjectTotalDTO `json:"projects,omitempty"`
			Days     *[]DayTotalDTO     `json:"days,omitempty"`
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
		loc := d.Domain.Timezone(ctx, p)

		out := &struct {
			Body struct {
				From     string `json:"from"`
				To       string `json:"to"`
				Timezone string `json:"timezone"`
				// Pointers, so "this grouping was not requested" and "this grouping
				// is empty" stop being the same thing on the wire. A plain slice with
				// omitempty drops an empty result entirely, and every client then has
				// to treat a missing key as an empty list — which the web client did
				// not, and the analytics screen threw on the first quiet week.
				Projects *[]ProjectTotalDTO `json:"projects,omitempty"`
				Days     *[]DayTotalDTO     `json:"days,omitempty"`
			}
		}{}
		out.Body.From, out.Body.To = from.Format("2006-01-02T15:04:05Z07:00"), to.Format("2006-01-02T15:04:05Z07:00")
		out.Body.Timezone = loc.String()

		if in.GroupBy == "day" {
			days, derr := d.Domain.SummaryByDay(ctx, p, from, to, loc)
			if derr != nil {
				return nil, d.problem(ctx, derr)
			}
			rows := make([]DayTotalDTO, 0, len(days))
			for _, row := range days {
				rows = append(rows, DayTotalDTO{Date: row.Date, Seconds: row.Seconds})
			}
			out.Body.Days = &rows
			return out, nil
		}

		var totals []service.ProjectTotal
		var terr error
		if in.Attribution == "evidence" {
			totals, terr = d.Domain.SummaryByProjectEvidenced(ctx, p, from, to)
		} else {
			totals, terr = d.Domain.SummaryByProject(ctx, p, from, to)
		}
		if terr != nil {
			return nil, d.problem(ctx, terr)
		}
		rows := make([]ProjectTotalDTO, 0, len(totals))
		for _, row := range totals {
			rows = append(rows, ProjectTotalDTO{
				ProjectID: row.ProjectID.String(), Name: row.Name, Client: row.Client, Color: row.Color,
				Seconds: row.Seconds, AmountCents: row.AmountCents,
				Currency: row.Currency, Billable: row.Billable,
			})
		}
		out.Body.Projects = &rows
		return out, nil
	})

	// The CSV export streams, so it is a plain huma handler with a raw body
	// rather than a typed response: the row count is unbounded and holding a
	// month of sessions in memory to marshal them would be pointless.
	huma.Register(api, huma.Operation{
		OperationID: "reports-export-csv", Method: http.MethodGet, Path: "/v1/reports/export.csv",
		Summary: "Export sessions in a date range as CSV", Tags: []string{"reports"},
		Errors: []int{401, 422},
	}, func(ctx context.Context, in *struct {
		From string `query:"from"`
		To   string `query:"to"`
	}) (*huma.StreamResponse, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		from, to, err := defaultRange(in.From, in.To)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			hctx.SetHeader("Content-Type", "text/csv; charset=utf-8")
			hctx.SetHeader("Content-Disposition", `attachment; filename="punchcard-sessions.csv"`)
			if err := d.Domain.ExportCSV(hctx.Context(), p, from, to, hctx.BodyWriter()); err != nil {
				d.Logger.ErrorContext(hctx.Context(), "CSV export stream failed", "error", err)
			}
		}}, nil
	})
}
