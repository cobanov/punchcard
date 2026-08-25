package http

import (
	"context"
	"log/slog"
	"net"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/gemini"
	"github.com/cobanov/punchcard/internal/http/embedded"
	"github.com/cobanov/punchcard/internal/http/legal"
	appmcp "github.com/cobanov/punchcard/internal/mcp"
	"github.com/cobanov/punchcard/internal/oauth"
	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/ratelimit"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/service"
)

// DBPinger is the minimal database dependency the readiness endpoint needs. The
// transport layer depends on this interface rather than importing pgx directly,
// keeping DB access confined to internal/repo. *pgxpool.Pool
// satisfies it.
type DBPinger interface {
	Ping(ctx context.Context) error
}

// Deps is everything the HTTP layer needs. Pool and Migrator may be nil when
// the router is built purely to emit the OpenAPI document (no handler runs).
type Deps struct {
	Config      *config.Config
	Logger      *slog.Logger
	Metrics     *observability.Metrics
	Pool        DBPinger
	Migrator    *repo.Migrator
	Store       *repo.Store
	Auth        *service.Auth
	Domain      *service.Domain
	OAuth       *oauth.Providers
	Gemini      *gemini.Client
	AuthLimiter ratelimit.Limiter
	APILimiter  ratelimit.Limiter
	sseHub      *sseHub

	trustedProxyNets []*net.IPNet
	csrfToken        string
}

// apiVersion is the OpenAPI document version. Bumped alongside releases.
const apiVersion = "1.0.11"

// BuildRouter assembles the chi mux and the huma API. It is the single place
// routes are wired, reused by both the running server and OpenAPI generation
// so the emitted spec always matches what the server serves.
func BuildRouter(d Deps) (*chi.Mux, huma.API) {
	// Default the limiters so the router is safe to build with a partial Deps
	// (e.g. OpenAPI generation) and in production.
	if d.APILimiter == nil {
		d.APILimiter = ratelimit.NewTokenBucket(d.Config.RateLimitPerMin, d.Config.RateLimitBurst)
	}
	if d.AuthLimiter == nil {
		d.AuthLimiter = ratelimit.NewTokenBucket(d.Config.AuthRateLimitPerMin, d.Config.AuthRateLimitBurst)
	}
	if d.sseHub == nil {
		d.sseHub = newSSEHub(d.Config.SSEMaxConnPerUser)
	}
	if d.OAuth == nil {
		d.OAuth = oauth.New(d.Config)
	}
	// Defaulted here rather than in main so a partial Deps (OpenAPI generation,
	// tests) still gets a client. With no key it reports Enabled()==false and
	// the chat route answers 503 with a reason — the route exists either way,
	// because an endpoint that is present on one deployment and absent on
	// another is harder to diagnose than one that explains itself.
	if d.Gemini == nil {
		d.Gemini = gemini.New(d.Config.GeminiAPIKey, d.Config.GeminiModel)
	}
	if d.trustedProxyNets == nil {
		if len(d.Config.TrustedProxies) > 0 {
			d.trustedProxyNets = parseCIDRs(d.Config.TrustedProxies)
		} else {
			d.trustedProxyNets = defaultTrustedProxies
		}
	}
	if d.csrfToken == "" {
		d.csrfToken = deriveCSRFToken(d.Config)
	}

	r := chi.NewMux()

	r.Use(chimw.Recoverer)
	r.Use(observability.RequestID)
	r.Use(d.securityHeaders)
	r.Use(requestLogger(d.Logger, d.Config.MetricsPath))
	r.Use(metricsMiddleware(d.Metrics))
	r.Use(maxBodyBytes(1 << 20))
	r.Use(d.corsMiddleware)
	r.Use(d.authMiddleware)
	r.Use(d.rateLimitMiddleware)
	r.Use(d.idempotencyMiddleware)

	// SSE stream — plain streaming handler, not a huma operation.
	r.Get("/v1/events/stream", d.handleSSE())

	// Social login — 302 redirect flow + a provider probe;
	// plain handlers, not huma operations.
	d.registerOAuthRoutes(r)

	// Operational endpoints (plain handlers, not part of the versioned API).
	r.Get("/healthz", handleHealthz())
	r.Get("/readyz", handleReadyz(d))
	metricsHandler := promhttp.HandlerFor(d.Metrics.Registry, promhttp.HandlerOpts{Registry: d.Metrics.Registry})
	if d.Config.MetricsToken != "" {
		metricsHandler = requireBearer(d.Config.MetricsToken, metricsHandler)
	}
	r.Handle(d.Config.MetricsPath, metricsHandler)

	// The versioned, OpenAPI-described API.
	api := humachi.New(r, humaConfig(d.Config))
	d.registerAuthRoutes(api)
	d.registerMeRoutes(api)
	d.registerListRoutes(api)
	d.registerTaskRoutes(api)
	d.registerChangesRoute(api)
	d.registerAccountRoutes(api)
	d.registerWebhookRoutes(api)
	d.registerChatRoutes(api)
	d.registerActivityRoute(api)

	// MCP streamable-HTTP transport. Tools call the REST API on the
	// loopback interface with the caller's Bearer PAT.
	r.Mount("/mcp", appmcp.HTTPHandler(internalBaseURL(d.Config)))

	// Legal documents. Public, unauthenticated, and served as their own HTML
	// rather than as SPA routes: App Store Connect requires a reachable privacy
	// policy URL and App Review opens it directly, as do link previews and
	// crawlers. Registered before the SPA fallback so they win over it.
	r.Get("/privacy", legal.Handler("privacy.html"))
	r.Get("/terms", legal.Handler("terms.html"))
	// The Support URL App Store Connect asks for has to lead to a working
	// support page — a mailto: link there is a routine rejection.
	r.Get("/support", legal.Handler("support.html"))

	// Embedded web UI: anything not matched above falls through to
	// the SPA, which serves its own assets and index.html for client routes.
	r.NotFound(embedded.Handler().ServeHTTP)

	return r, api
}

// internalBaseURL is the loopback URL the process uses to call its own API.
func internalBaseURL(cfg *config.Config) string {
	addr := cfg.HTTPAddr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return "http://127.0.0.1" + addr[i:]
	}
	return "http://127.0.0.1:8080"
}

// humaConfig returns the OpenAPI/huma configuration for the API.
func humaConfig(cfg *config.Config) huma.Config {
	c := huma.DefaultConfig("punchcard", apiVersion)
	c.Info.Description = "Agent-native todo service. API-first: anything the UI can do, the API can do."
	// huma's built-in interactive explorer defaults to /docs; move it so /docs
	// can serve the human-friendly single-page guide (internal/http/embedded).
	c.DocsPath = "/api-explorer"
	if cfg != nil && cfg.PublicBaseURL != "" {
		c.Servers = []*huma.Server{{URL: cfg.PublicBaseURL}}
	}
	return c
}
