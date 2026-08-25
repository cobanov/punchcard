package service

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// seedSession records a finished session over an explicit window.
func (e *testEnv) seedSession(t *testing.T, p *auth.Principal, projectID uuid.UUID, from, to time.Time) db.WorkSession {
	t.Helper()
	ws, err := e.d.StartSession(e.ctx, p, StartSessionInput{
		ProjectID: projectID, StartedAt: &from, StopCurrent: true,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	stopped, err := e.d.StopSession(e.ctx, p, ws.ID, to)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	return stopped
}

func (e *testEnv) pricedProject(t *testing.T, p *auth.Principal, name string, rate int64, billable bool) db.Project {
	t.Helper()
	proj, err := e.d.CreateProject(e.ctx, p, CreateProjectInput{
		Name: name, Currency: "TRY", Billable: billable, HourlyRateCents: &rate,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return proj
}

// 90 minutes at 333.33/hour is 499.995 — the exact answer has a half kuruş in
// it. Integer arithmetic truncates; a float would land somewhere near but not
// reproducibly, and the error would compound across a month.
func TestAmountUsesIntegerArithmetic(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.pricedProject(t, p, "rated", 33333, true)
	e.seedSession(t, p, proj.ID, at("10:00"), at("11:30"))

	totals, err := e.d.SummaryByProject(e.ctx, p, at("00:00"), at("23:00"))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	row := findTotal(t, totals, proj.ID)
	if row.Seconds != 5400 {
		t.Fatalf("seconds = %d, want 5400", row.Seconds)
	}
	if row.AmountCents == nil || *row.AmountCents != 49999 {
		t.Fatalf("amount = %v, want 49999", row.AmountCents)
	}
}

// "Not costed" and "costed at zero" must stay distinguishable all the way out.
func TestNonBillableProjectHasNoAmount(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.pricedProject(t, p, "internal", 10000, false)
	e.seedSession(t, p, proj.ID, at("10:00"), at("11:00"))

	totals, _ := e.d.SummaryByProject(e.ctx, p, at("00:00"), at("23:00"))
	row := findTotal(t, totals, proj.ID)
	if row.AmountCents != nil {
		t.Fatalf("amount = %d, want nil for a non-billable project", *row.AmountCents)
	}
}

func TestUnpricedProjectHasNoAmount(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "unpriced")
	e.seedSession(t, p, proj.ID, at("10:00"), at("11:00"))

	totals, _ := e.d.SummaryByProject(e.ctx, p, at("00:00"), at("23:00"))
	row := findTotal(t, totals, proj.ID)
	if row.AmountCents != nil {
		t.Fatalf("amount = %d, want nil when no rate is set", *row.AmountCents)
	}
}

// A session that only partly overlaps the range contributes only the overlap.
// Counting it whole would let two adjacent months each claim the same hour.
func TestSessionsAreClippedToTheRange(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")
	e.seedSession(t, p, proj.ID, at("09:00"), at("12:00"))

	totals, err := e.d.SummaryByProject(e.ctx, p, at("10:00"), at("11:00"))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	row := findTotal(t, totals, proj.ID)
	if row.Seconds != 3600 {
		t.Fatalf("seconds = %d, want 3600 (only the hour inside the range)", row.Seconds)
	}
}

// The single most load-bearing thing about reports: day boundaries are the
// user's, not UTC's.
func TestDayBucketsFollowTheCallerTimezone(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")
	// 2026-03-01 22:30–23:30 UTC is 2026-03-02 01:30–02:30 in Istanbul.
	e.seedSession(t, p, proj.ID, at("22:30"), at("23:30"))

	from, to := at("00:00"), at("23:59")

	utcDays, err := e.d.SummaryByDay(e.ctx, p, from, to, time.UTC)
	if err != nil {
		t.Fatalf("summary utc: %v", err)
	}
	if len(utcDays) != 1 || utcDays[0].Date != "2026-03-01" {
		t.Fatalf("utc buckets = %+v, want 2026-03-01", utcDays)
	}

	ist, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Skipf("no tzdata for Europe/Istanbul: %v", err)
	}
	istDays, err := e.d.SummaryByDay(e.ctx, p, from, to, ist)
	if err != nil {
		t.Fatalf("summary istanbul: %v", err)
	}
	if len(istDays) != 1 || istDays[0].Date != "2026-03-02" {
		t.Fatalf("istanbul buckets = %+v, want 2026-03-02", istDays)
	}
}

// A running session has no duration yet. Exporting one with an invented end
// would put a number in a spreadsheet that nothing in the product agrees with.
func TestExportSkipsTheRunningSession(t *testing.T) {
	e := newTestEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")
	e.seedSession(t, p, proj.ID, at("09:00"), at("10:00"))
	e.mustStart(t, p, proj.ID, "hâlâ çalışıyor")

	var buf bytes.Buffer
	if err := e.d.ExportCSV(e.ctx, p, at("00:00"), at("23:00"), "declared", &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want a header and one finished session, got %d lines:\n%s", len(lines), buf.String())
	}
	if !strings.HasPrefix(lines[0], "session_id,") {
		t.Fatalf("missing header: %q", lines[0])
	}
}

// Another account's time never appears in your totals.
func TestReportsAreScopedToTheAccount(t *testing.T) {
	e := newTestEnv(t)
	alice, bob := e.newUser(t), e.newUser(t)
	pa := e.mustProject(t, alice, "alice-work")
	e.seedSession(t, alice, pa.ID, at("10:00"), at("12:00"))

	totals, err := e.d.SummaryByProject(e.ctx, bob, at("00:00"), at("23:00"))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(totals) != 0 {
		t.Fatalf("bob sees alice's time: %+v", totals)
	}
}

func findTotal(t *testing.T, totals []ProjectTotal, id uuid.UUID) ProjectTotal {
	t.Helper()
	for _, row := range totals {
		if row.ProjectID == id {
			return row
		}
	}
	t.Fatalf("project %s missing from totals %+v", id, totals)
	return ProjectTotal{}
}
