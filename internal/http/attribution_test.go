package http

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

type attributionBody struct {
	Allocations []struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		Seconds   int64  `json:"seconds"`
		Evidenced bool   `json:"evidenced"`
		Reason    string `json:"reason"`
	} `json:"allocations"`
	Unresolved []struct {
		Place    string `json:"place"`
		FullName string `json:"full_name"`
		Seconds  int64  `json:"seconds"`
	} `json:"unresolved"`
}

// One declared hour on alpha; a 30-minute run in cobanov/beta and a 5-minute
// run in cobanov/gamma (no such project). Beta earns its half hour by name,
// gamma surfaces as unresolved, and alpha keeps the rest — with the total
// summing to exactly the hour.
func TestSessionAttributionSplitsByEvidence(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "attr-split@example.com")
	alphaID := newProject(t, c, base, csrf, "alpha")
	_ = newProject(t, c, base, csrf, "beta")

	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Minute)
	end := start.Add(time.Hour)
	code, raw := do(t, c, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": alphaID, "started_at": start.Format(time.RFC3339)}, csrf)
	must(t, "start", code, http.StatusCreated)
	var ws struct {
		ID string `json:"id"`
	}
	unmarshal(t, raw, &ws)
	must(t, "stop", st(t, c, http.MethodPost, base+"/v1/sessions/"+ws.ID+"/stop",
		map[string]any{"at": end.Format(time.RFC3339)}, csrf), http.StatusOK)

	must(t, "beta run", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "beta-run", start.Add(10*time.Minute), start.Add(40*time.Minute),
			map[string]any{"repo": "cobanov/beta"}), csrf), http.StatusAccepted)
	must(t, "gamma run", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "gamma-run", start.Add(45*time.Minute), start.Add(50*time.Minute),
			map[string]any{"repo": "cobanov/gamma"}), csrf), http.StatusAccepted)

	code, raw = do(t, c, http.MethodGet, base+"/v1/sessions/"+ws.ID+"/attribution", nil, nil)
	must(t, "attribution", code, http.StatusOK)
	var body attributionBody
	unmarshal(t, raw, &body)

	var sum int64
	byName := map[string]int64{}
	reasons := map[string]string{}
	for _, a := range body.Allocations {
		sum += a.Seconds
		byName[a.Name] += a.Seconds
		if a.Evidenced {
			reasons[a.Name] = a.Reason
		}
	}
	if sum != 3600 {
		t.Fatalf("allocations sum to %d, want 3600: %+v", sum, body.Allocations)
	}
	if byName["beta"] != 1800 || reasons["beta"] != "name" {
		t.Fatalf("beta = %ds via %q, want 1800 via name", byName["beta"], reasons["beta"])
	}
	if byName["alpha"] != 1800 {
		t.Fatalf("alpha = %ds, want 1800 (quiet + gamma fallback)", byName["alpha"])
	}
	if len(body.Unresolved) != 1 || body.Unresolved[0].Place != "gamma" ||
		body.Unresolved[0].FullName != "cobanov/gamma" || body.Unresolved[0].Seconds != 300 {
		t.Fatalf("unresolved = %+v, want gamma 300s", body.Unresolved)
	}
}

// Another user's session is a 404, never a 403 (the ownership rule).
func TestSessionAttributionIsInvisibleAcrossAccounts(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	owner, _ := registerActor(t, base, "attr-owner@example.com")
	pid := newProject(t, owner, base, csrf, "mine")
	start := time.Now().UTC().Add(-2 * time.Hour)
	code, raw := do(t, owner, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": pid, "started_at": start.Format(time.RFC3339)}, csrf)
	must(t, "start", code, http.StatusCreated)
	var ws struct {
		ID string `json:"id"`
	}
	unmarshal(t, raw, &ws)

	other, _ := registerActor(t, base, "attr-other@example.com")
	code, _ = do(t, other, http.MethodGet, base+"/v1/sessions/"+ws.ID+"/attribution", nil, nil)
	must(t, "cross-account", code, http.StatusNotFound)
}

// Recording a cluster into a project teaches the link — that is how the link
// table stops being sparse without anyone doing setup. But only when the
// window names exactly one repository: recovering a mixed morning into a
// catch-all project must not teach the catch-all to claim every repo in it.
func TestRecoveryLearnsTheLinkOnlyWhenUnambiguous(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "attr-learn@example.com")
	shopID := newProject(t, c, base, csrf, "webshop")
	junkID := newProject(t, c, base, csrf, "junk")

	// One orphan run in one repo → recover into webshop → link learned.
	a := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Minute)
	must(t, "run", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "learn-1", a, a.Add(20*time.Minute),
			map[string]any{"repo": "cobanov/shop-backend"}), csrf), http.StatusAccepted)
	must(t, "recover", st(t, c, http.MethodPost, base+"/v1/github/unmatched/recover",
		map[string]any{"project_id": shopID, "from": a.Format(time.RFC3339),
			"to": a.Add(25 * time.Minute).Format(time.RFC3339), "note": "shop work"}, csrf),
		http.StatusCreated)

	code, raw := do(t, c, http.MethodGet, base+"/v1/projects/"+shopID+"/repos", nil, nil)
	must(t, "repos", code, http.StatusOK)
	if !strings.Contains(string(raw), "cobanov/shop-backend") {
		t.Fatalf("recovery did not learn the link: %s", raw)
	}

	// Two repos in the window → recover into junk → nothing learned.
	b := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Minute)
	must(t, "run1", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "learn-2a", b, b.Add(10*time.Minute),
			map[string]any{"repo": "cobanov/one"}), csrf), http.StatusAccepted)
	must(t, "run2", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "learn-2b", b.Add(2*time.Minute), b.Add(12*time.Minute),
			map[string]any{"repo": "cobanov/two"}), csrf), http.StatusAccepted)
	must(t, "recover mixed", st(t, c, http.MethodPost, base+"/v1/github/unmatched/recover",
		map[string]any{"project_id": junkID, "from": b.Format(time.RFC3339),
			"to": b.Add(15 * time.Minute).Format(time.RFC3339), "note": "mixed"}, csrf),
		http.StatusCreated)

	code, raw = do(t, c, http.MethodGet, base+"/v1/projects/"+junkID+"/repos", nil, nil)
	must(t, "junk repos", code, http.StatusOK)
	if strings.Contains(string(raw), "cobanov/one") || strings.Contains(string(raw), "cobanov/two") {
		t.Fatalf("a mixed recovery taught links it should not have: %s", raw)
	}
}

// The two report modes agree on the grand total — the sweep only moves
// seconds between projects — and evidence mode bills moved minutes at the
// TARGET project's rate, which is the point of moving them.
func TestEvidencedSummaryRedistributesWithoutChangingTheTotal(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "attr-report@example.com")
	alphaID := newProject(t, c, base, csrf, "alpha")
	betaID := newProject(t, c, base, csrf, "beta")
	// 360000¢/hour = 100¢ a second, so 1800s bills 180000¢.
	must(t, "rate", st(t, c, http.MethodPatch, base+"/v1/projects/"+betaID,
		map[string]any{"hourly_rate_cents": 360000}, csrf), http.StatusOK)

	start := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Minute)
	end := start.Add(time.Hour)
	code, raw := do(t, c, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": alphaID, "started_at": start.Format(time.RFC3339)}, csrf)
	must(t, "start", code, http.StatusCreated)
	var ws struct {
		ID string `json:"id"`
	}
	unmarshal(t, raw, &ws)
	must(t, "stop", st(t, c, http.MethodPost, base+"/v1/sessions/"+ws.ID+"/stop",
		map[string]any{"at": end.Format(time.RFC3339)}, csrf), http.StatusOK)
	must(t, "run", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "rep-1", start.Add(10*time.Minute), start.Add(40*time.Minute),
			map[string]any{"repo": "cobanov/beta"}), csrf), http.StatusAccepted)

	q := "?from=" + start.Add(-5*time.Minute).Format(time.RFC3339) +
		"&to=" + end.Add(5*time.Minute).Format(time.RFC3339) + "&group_by=project"

	type summary struct {
		Projects []struct {
			Name        string `json:"name"`
			Seconds     int64  `json:"seconds"`
			AmountCents *int64 `json:"amount_cents"`
		} `json:"projects"`
	}
	fetch := func(mode string) summary {
		code, raw := do(t, c, http.MethodGet, base+"/v1/reports/summary"+q+"&attribution="+mode, nil, nil)
		must(t, "summary "+mode, code, http.StatusOK)
		var s summary
		unmarshal(t, raw, &s)
		return s
	}

	declared, evidence := fetch("declared"), fetch("evidence")
	total := func(s summary) (sum int64) {
		for _, p := range s.Projects {
			sum += p.Seconds
		}
		return
	}
	if total(declared) != 3600 || total(evidence) != 3600 {
		t.Fatalf("grand totals declared=%d evidence=%d, want 3600 both", total(declared), total(evidence))
	}
	get := func(s summary, name string) (int64, *int64) {
		for _, p := range s.Projects {
			if p.Name == name {
				return p.Seconds, p.AmountCents
			}
		}
		return 0, nil
	}
	if dAlpha, _ := get(declared, "alpha"); dAlpha != 3600 {
		t.Fatalf("declared alpha = %d, want 3600", dAlpha)
	}
	eAlpha, _ := get(evidence, "alpha")
	eBeta, amount := get(evidence, "beta")
	if eAlpha != 1800 || eBeta != 1800 {
		t.Fatalf("evidence split alpha=%d beta=%d, want 1800/1800", eAlpha, eBeta)
	}
	if amount == nil || *amount != 180000 {
		t.Fatalf("beta amount = %v, want 180000 (its own rate over its evidenced seconds)", amount)
	}
	// Default is declared: an absent parameter must not change anyone's numbers.
	code, raw = do(t, c, http.MethodGet, base+"/v1/reports/summary"+q, nil, nil)
	must(t, "default", code, http.StatusOK)
	var def summary
	unmarshal(t, raw, &def)
	if defAlpha, _ := get(def, "alpha"); defAlpha != 3600 {
		t.Fatalf("default mode alpha = %d, want the declared 3600", defAlpha)
	}
}
