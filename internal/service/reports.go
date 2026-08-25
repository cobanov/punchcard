package service

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// ProjectTotal is one project's share of a date range.
//
// AmountCents is nil when the project is not billable or carries no rate. That
// is not the same as zero: a client rendering 0 would show unpaid work where
// the truth is "this was never costed".
type ProjectTotal struct {
	ProjectID   uuid.UUID
	Name        string
	Client      string
	Color       string
	Seconds     int64
	AmountCents *int64
	Currency    string
	Billable    bool
}

// DayTotal is one local day's total.
type DayTotal struct {
	Date    string // YYYY-MM-DD in the caller's timezone
	Seconds int64
}

// SummaryByProject totals the range per project.
//
// Sessions are clipped to the range, so a session that straddles the boundary
// contributes only the part inside it. Anything else would double-count a range
// against its neighbour.
func (d *Domain) SummaryByProject(ctx context.Context, p *auth.Principal, from, to time.Time) ([]ProjectTotal, error) {
	rows, err := d.store.SummaryByProject(ctx, db.SummaryByProjectParams{
		UserID: p.UserID, FromTs: from.UTC(), ToTs: to.UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("summary by project: %w", err)
	}
	out := make([]ProjectTotal, 0, len(rows))
	for _, r := range rows {
		if !p.AllowsProject(r.ProjectID) {
			continue
		}
		color := ""
		if r.Color != nil {
			color = *r.Color
		}
		out = append(out, ProjectTotal{
			ProjectID: r.ProjectID, Name: r.ProjectName, Client: r.Client, Color: color,
			Seconds: r.Seconds, Currency: r.Currency, Billable: r.Billable,
			AmountCents: amountCents(r.Seconds, r.HourlyRateCents, r.Billable),
		})
	}
	return out, nil
}

// amountCents is seconds × rate ÷ 3600 in integer arithmetic, truncated.
//
// No float touches money anywhere in this codebase. 333.33/hour for ninety
// minutes has exactly one right answer, and binary floating point is not how you
// get it — the error is small per row and compounds across a month's invoice.
func amountCents(seconds int64, rate *int64, billable bool) *int64 {
	if !billable || rate == nil {
		return nil
	}
	amount := seconds * *rate / 3600
	return &amount
}

// SummaryByDay totals the range per local day.
//
// The timezone is the caller's, not UTC. A session from 22:30 to 23:30 UTC on
// 1 March happened on 2 March in Istanbul, and a report that files it under the
// first is simply wrong to the person reading it.
func (d *Domain) SummaryByDay(ctx context.Context, p *auth.Principal, from, to time.Time, loc *time.Location) ([]DayTotal, error) {
	if loc == nil {
		loc = time.UTC
	}
	rows, err := d.store.SummaryByDay(ctx, db.SummaryByDayParams{
		UserID: p.UserID, FromTs: from.UTC(), ToTs: to.UTC(), Tz: loc.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("summary by day: %w", err)
	}
	out := make([]DayTotal, 0, len(rows))
	for _, r := range rows {
		out = append(out, DayTotal{Date: r.Day, Seconds: r.Seconds})
	}
	return out, nil
}

// Timezone resolves the caller's reporting timezone, falling back to UTC when
// the stored name is one this system does not know.
func (d *Domain) Timezone(ctx context.Context, p *auth.Principal) *time.Location {
	u, err := d.store.GetUserByID(ctx, p.UserID)
	if err != nil {
		return time.UTC
	}
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// ExportCSV writes one row per session, with its commit count.
func (d *Domain) ExportCSV(ctx context.Context, p *auth.Principal, from, to time.Time, w io.Writer) error {
	sessions, err := d.ListSessions(ctx, p, from, to, nil)
	if err != nil {
		return err
	}
	projects, err := d.store.ListProjects(ctx, db.ListProjectsParams{
		OwnerID: p.UserID, IncludeArchived: true,
	})
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	byID := make(map[uuid.UUID]db.Project, len(projects))
	for _, proj := range projects {
		byID[proj.ID] = proj
	}

	ids := make([]uuid.UUID, 0, len(sessions))
	for _, ws := range sessions {
		ids = append(ids, ws.ID)
	}
	counts := map[uuid.UUID]int64{}
	if len(ids) > 0 {
		rows, cerr := d.store.CountCommitsForSessions(ctx, ids)
		if cerr != nil {
			return fmt.Errorf("count commits: %w", cerr)
		}
		for _, r := range rows {
			counts[r.SessionID] = r.N
		}
	}

	loc := d.Timezone(ctx, p)
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"session_id", "project", "client", "note", "started_at", "ended_at",
		"seconds", "amount_cents", "currency", "commits", "source",
	}); err != nil {
		return err
	}
	for _, ws := range sessions {
		// A running session has no duration yet; exporting it with a made-up end
		// would put a number in a spreadsheet that nothing in the product agrees
		// with.
		if ws.EndedAt == nil {
			continue
		}
		proj := byID[ws.ProjectID]
		seconds := int64(ws.EndedAt.Sub(ws.StartedAt).Seconds())
		amount := ""
		if a := amountCents(seconds, proj.HourlyRateCents, proj.Billable); a != nil {
			amount = strconv.FormatInt(*a, 10)
		}
		if err := cw.Write([]string{
			ws.ID.String(),
			proj.Name,
			proj.Client,
			ws.Note,
			ws.StartedAt.In(loc).Format(time.RFC3339),
			ws.EndedAt.In(loc).Format(time.RFC3339),
			strconv.FormatInt(seconds, 10),
			amount,
			proj.Currency,
			strconv.FormatInt(counts[ws.ID], 10),
			ws.Source,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
