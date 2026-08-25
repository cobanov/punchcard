package http

import (
	"net/http"
	"net/http/httptest"
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

	// v1 serves no SPA, so "/" is a 404 rather than an index page. The
	// human-readable surface is the OpenAPI explorer.
	assertStatus(t, srv.URL+"/", http.StatusNotFound)
	assertStatus(t, srv.URL+"/docs", http.StatusOK)
	assertStatus(t, srv.URL+"/healthz", http.StatusOK)
	assertStatus(t, srv.URL+"/readyz", http.StatusOK)
	assertStatus(t, srv.URL+"/metrics", http.StatusOK)
	assertStatus(t, srv.URL+"/openapi.json", http.StatusOK)
}
