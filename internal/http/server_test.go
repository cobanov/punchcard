package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/testutil"
)

// TestHealthReadinessAndSpec is the Phase 0 gate smoke test: an empty-but-
// running server answers liveness, readiness (real DB + migrations), metrics,
// and serves its OpenAPI document. Runs against real Postgres.
func TestHealthReadinessAndSpec(t *testing.T) {
	pool := testutil.Postgres(t)

	migrator, err := repo.NewMigrator(pool)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	t.Cleanup(func() { _ = migrator.Close() })

	router, _ := BuildRouter(Deps{
		Config:   testConfig(),
		Logger:   observability.NewLogger("error"),
		Metrics:  observability.NewMetrics(),
		Pool:     pool,
		Migrator: migrator,
	})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	assertStatus(t, srv.URL+"/", http.StatusOK)
	assertStatus(t, srv.URL+"/docs", http.StatusOK)
	assertStatus(t, srv.URL+"/healthz", http.StatusOK)
	assertStatus(t, srv.URL+"/readyz", http.StatusOK)
	assertStatus(t, srv.URL+"/metrics", http.StatusOK)
	assertStatus(t, srv.URL+"/openapi.json", http.StatusOK)
}

// A person who types the bare hostname must land on something that says what
// this is. Before this test the root answered chi's default "404 page not
// found" — a live, healthy service that reads as broken to anyone who opens it,
// which is exactly how it was reported.
func TestRootServesALandingPage(t *testing.T) {
	pool := testutil.Postgres(t)
	migrator, err := repo.NewMigrator(pool)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	t.Cleanup(func() { _ = migrator.Close() })

	router, _ := BuildRouter(Deps{
		Config: testConfig(), Logger: observability.NewLogger("error"),
		Metrics: observability.NewMetrics(), Pool: pool, Migrator: migrator,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/") //nolint:gosec // test-controlled localhost URL
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	// The page has one job beyond existing: pointing at the API.
	if !strings.Contains(string(body), "/docs") {
		t.Fatalf("the landing page must link to the API documentation:\n%s", body)
	}
}

// Every other error this API produces is RFC 9457 problem+json. An unmatched
// path answering text/plain means a client's error handling breaks on exactly
// the case it is most likely to hit — a typo'd or outdated URL.
func TestUnknownPathReturnsProblemJSON(t *testing.T) {
	pool := testutil.Postgres(t)
	migrator, err := repo.NewMigrator(pool)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	t.Cleanup(func() { _ = migrator.Close() })

	router, _ := BuildRouter(Deps{
		Config: testConfig(), Logger: observability.NewLogger("error"),
		Metrics: observability.NewMetrics(), Pool: pool, Migrator: migrator,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/nonexistent") //nolint:gosec // test-controlled localhost URL
	if err != nil {
		t.Fatalf("GET /v1/nonexistent: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("content-type = %q, want application/problem+json; body: %s", ct, body)
	}
	if !strings.Contains(string(body), `"code"`) {
		t.Fatalf("problem body has no machine-readable code: %s", body)
	}
}
