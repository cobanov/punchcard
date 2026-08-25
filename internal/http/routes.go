package http

import (
	"context"
	"log/slog"
	"net"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/http/app"
	"github.com/cobanov/punchcard/internal/http/landing"
	"github.com/cobanov/punchcard/internal/http/legal"
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
	d.registerProjectRoutes(api)
	d.registerSessionRoutes(api)
	d.registerReportRoutes(api)
	d.registerGitHubRoutes(api)
	d.registerAccountRoutes(api)
	d.registerWebhookRoutes(api)

	// The one page this release has. v1 serves no SPA, and before this existed
	// the bare hostname answered chi's default "404 page not found" — a healthy
	// service that read as broken to whoever opened it.
	r.Get("/", landing.Handler())

	// The web client. Small, static, and meant to be replaced — see
	// internal/http/app.
	r.Get("/app", app.Handler())

	// Legal documents. Public, unauthenticated, and served as their own HTML
	// rather than as SPA routes: App Store Connect requires a reachable privacy
	// policy URL and App Review opens it directly, as do link previews and
	// crawlers. Registered before the SPA fallback so they win over it.
	r.Get("/privacy", legal.Handler("privacy.html"))
	r.Get("/terms", legal.Handler("terms.html"))
	// The Support URL App Store Connect asks for has to lead to a working
	// support page — a mailto: link there is a routine rejection.
	r.Get("/support", legal.Handler("support.html"))

	// Everything unmatched answers in the same shape as every other error this
	// API produces. chi's default is text/plain, which would break a client's
	// error handling on exactly the case it is most likely to hit: a typo'd or
	// outdated URL.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		writeProblem(w, http.StatusNotFound, "not_found", "no route matches "+req.URL.Path)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed",
			req.Method+" is not allowed on "+req.URL.Path)
	})

	return r, api
}

// humaConfig returns the OpenAPI/huma configuration for the API.
func humaConfig(cfg *config.Config) huma.Config {
	c := huma.DefaultConfig("punchcard", apiVersion)
	c.Info.Description = "Developer time tracking. Projects, work sessions and the GitHub commits behind them."
	if cfg != nil && cfg.PublicBaseURL != "" {
		c.Servers = []*huma.Server{{URL: cfg.PublicBaseURL}}
	}
	return c
}
