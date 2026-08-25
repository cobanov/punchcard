package http

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/observability"
)

// testCSRF returns the X-CSRF-Token header map matching the token the router
// derives from testConfig's AppSecret (see deriveCSRFToken).
func testCSRF() map[string]string {
	return map[string]string{csrfHeader: deriveCSRFToken(testConfig())}
}

// testConfig returns a minimal, valid config for tests that do not load the
// environment.
func testConfig() *config.Config {
	return &config.Config{
		Env:                 config.EnvDevelopment,
		DatabaseURL:         "unused-in-router-tests",
		AppSecret:           "test-app-secret-for-csrf",
		HTTPAddr:            ":0",
		PublicBaseURL:       "http://localhost:8080",
		MetricsPath:         "/metrics",
		LogLevel:            "error",
		DBMaxConns:          5,
		EmailProvider:       "dev",
		RateLimitPerMin:     600,
		RateLimitBurst:      200,
		AuthRateLimitPerMin: 60,
		AuthRateLimitBurst:  30,
		MaxPATsPerUser:      25,
		MaxListsPerUser:     200,
		MaxTasksPerList:     10000,
		MaxMembersPerList:   50,
		MaxWebhooksPerList:  10,
		MaxInvitesPerUser:   100,
		SSEMaxConnPerUser:   10,
		SSEPollInterval:     150 * time.Millisecond,
		// Zero grace window: router tests assert immediate delivery. The
		// window's own behavior is covered in internal/service/changes_test.go.
		EventGraceWindow: 0,
	}
}

// testDeps returns Deps sufficient to build the router without a database
// (used for OpenAPI generation, where no handler runs).
func testDeps() Deps {
	return Deps{
		Config:  testConfig(),
		Logger:  observability.NewLogger("error"),
		Metrics: observability.NewMetrics(),
	}
}

func assertStatus(t *testing.T, url string, want int) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test-controlled localhost URL
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("GET %s: status = %d, want %d", url, resp.StatusCode, want)
	}
}
