package http

import (
	"io"
	"net/http"
	"net/http/cookiejar"
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
		MaxProjectsPerUser:  500,
		MaxWebhooksPerUser:  10,
		SSEMaxConnPerUser:   10,
		SSEPollInterval:     150 * time.Millisecond,
		// Zero grace window: router tests assert immediate delivery.
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

// registerActor creates an account with its own cookie jar and returns a client
// already signed in as that user, plus the user's id.
func registerActor(t *testing.T, base, email string) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if code, _ := do(t, c, http.MethodPost, base+"/v1/auth/register",
		map[string]string{"email": email, "password": "supersecret123"}, nil); code != http.StatusCreated {
		t.Fatalf("register %s: %d", email, code)
	}
	_, body := do(t, c, http.MethodGet, base+"/v1/me", nil, nil)
	var me struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &me)
	return c, me.ID
}

func must(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %d, want %d", name, got, want)
	}
}

// st performs a request and returns only its status code, for assertions where
// the body does not matter.
func st(t *testing.T, c *http.Client, method, url string, body any, hdr map[string]string) int {
	t.Helper()
	code, _ := do(t, c, method, url, body, hdr)
	return code
}
