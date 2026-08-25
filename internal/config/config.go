// Package config loads and validates all runtime configuration from the
// environment (12-factor). The binary never reads a .env file itself: local
// development injects variables via `make run`/direnv, and docker-compose
// injects them from deploy/.env. This keeps the process a pure consumer of the
// environment and easy to reason about in every deployment target.
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// Environment identifies the deployment mode. It gates behaviour such as log
// format defaults and (later) whether HTTP webhook targets are permitted.
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvProduction  Environment = "production"
)

// Config is the fully-parsed, validated configuration for the process. Every
// field maps to a single environment variable. New knobs are added here (never
// read via os.Getenv elsewhere) so the full surface stays documented in one
// place and in .env.example.
type Config struct {
	// Env selects development vs production behaviour.
	Env Environment `env:"APP_ENV" envDefault:"development"`

	// DatabaseURL is the only strictly required variable. pgx DSN or URL form.
	DatabaseURL string `env:"DATABASE_URL,required"`

	// AppSecret is a server secret used to derive the CSRF token (and other
	// non-storage HMACs). When empty a per-process random value is used; set it
	// in production so the derived token is stable across restarts.
	AppSecret string `env:"APP_SECRET"`

	// HTTPAddr is the listen address for the main HTTP server (plain HTTP;
	// TLS is terminated by Caddy in front of the process, see deploy/).
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8080"`

	// PublicBaseURL is the externally reachable base URL. Used to build links
	// in emails and to set absolute references. No trailing slash.
	PublicBaseURL string `env:"PUBLIC_BASE_URL" envDefault:"http://localhost:8080"`

	// MetricsPath is where Prometheus metrics are exposed on the main mux.
	// Both the path and the bearer guard (MetricsToken) are configurable so the
	// endpoint can be moved or protected without a code change.
	MetricsPath string `env:"METRICS_PATH" envDefault:"/metrics"`

	// MetricsToken, when set, requires scrapers to send it as a bearer token to
	// read /metrics. Empty leaves the endpoint open (dev / trusted networks).
	MetricsToken string `env:"METRICS_TOKEN"`

	// LogLevel is one of debug|info|warn|error.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`

	// AutoMigrate runs pending migrations under an advisory lock at `serve`
	// startup. Disable it when migrations are managed out-of-band by ops.
	AutoMigrate bool `env:"AUTO_MIGRATE" envDefault:"true"`

	// ShutdownTimeout bounds graceful drain of HTTP (and, later, SSE + River).
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"20s"`

	// TrustedProxies is the CIDR allow-list of reverse proxies whose forwarded
	// client-IP headers (CF-Connecting-IP / X-Forwarded-For / X-Real-IP) are
	// honored. Empty falls back to loopback + private + CGNAT ranges. Public
	// clients can never spoof their source IP to evade auth rate limiting.
	TrustedProxies []string `env:"TRUSTED_PROXIES" envSeparator:","`

	// CORSAllowedOrigins is the exact allow-list for browser origins. Empty in
	// development means "no CORS handling"; it must be set in production once
	// the web UI is served from a distinct origin.
	CORSAllowedOrigins []string `env:"CORS_ALLOWED_ORIGINS" envSeparator:","`

	// DBMaxConns caps the pgx pool size.
	DBMaxConns int32 `env:"DB_MAX_CONNS" envDefault:"10"`

	// --- Email ---
	// EmailProvider selects the delivery adapter: dev (log only) | smtp.
	EmailProvider string `env:"EMAIL_PROVIDER" envDefault:"dev"`
	EmailFrom     string `env:"EMAIL_FROM" envDefault:"punchcard <no-reply@localhost>"`
	SMTPHost      string `env:"SMTP_HOST"`
	SMTPPort      string `env:"SMTP_PORT" envDefault:"587"`
	SMTPUsername  string `env:"SMTP_USERNAME"`
	SMTPPassword  string `env:"SMTP_PASSWORD"`

	// --- Social login ---
	// OAuth 2.0 authorization-code sign-in. Each provider is enabled only when
	// both its client id and secret are present. Redirect/callback URLs are not
	// configured directly: they are derived from PublicBaseURL as
	// {PUBLIC_BASE_URL}/v1/auth/oauth/{provider}/callback, so the provider app
	// must be registered with exactly that callback.
	GoogleClientID     string `env:"GOOGLE_CLIENT_ID"`
	GoogleClientSecret string `env:"GOOGLE_CLIENT_SECRET"`
	GitHubClientID     string `env:"GITHUB_CLIENT_ID"`
	GitHubClientSecret string `env:"GITHUB_CLIENT_SECRET"`

	// GitHubTokenKey is the base64-encoded 32-byte AES key that seals stored
	// GitHub access tokens. Without it the GitHub integration stays off; there
	// is no plaintext fallback.
	GitHubTokenKey string `env:"GITHUB_TOKEN_KEY"`

	// Sign in with Apple. There is no static secret: APPLE_PRIVATE_KEY is the
	// PKCS#8 PEM of the .p8 the developer portal downloads once, and the client
	// secret is a JWT signed with it per request. APPLE_CLIENT_ID is the
	// **Services ID**, not the app's bundle identifier.
	AppleClientID   string `env:"APPLE_CLIENT_ID"`
	AppleTeamID     string `env:"APPLE_TEAM_ID"`
	AppleKeyID      string `env:"APPLE_KEY_ID"`
	ApplePrivateKey string `env:"APPLE_PRIVATE_KEY"`

	// --- Rate limiting ---
	RateLimitPerMin     int `env:"RATE_LIMIT_PER_MIN" envDefault:"120"`
	RateLimitBurst      int `env:"RATE_LIMIT_BURST" envDefault:"40"`
	AuthRateLimitPerMin int `env:"AUTH_RATE_LIMIT_PER_MIN" envDefault:"10"`
	AuthRateLimitBurst  int `env:"AUTH_RATE_LIMIT_BURST" envDefault:"5"`

	// --- Webhooks ---
	// WebhookEncryptionKey is a base64-encoded 32-byte AES key for encrypting
	// webhook signing secrets at rest. If empty, webhook creation is disabled.
	WebhookEncryptionKey string `env:"WEBHOOK_ENCRYPTION_KEY"`
	// WebhookAllowHTTP permits plain-http webhook targets (self-host only).
	WebhookAllowHTTP bool `env:"WEBHOOK_ALLOW_HTTP" envDefault:"false"`
	// WebhookMaxConcurrency caps global outbound delivery concurrency (egress).
	WebhookMaxConcurrency int `env:"WEBHOOK_MAX_CONCURRENCY" envDefault:"8"`
	// WebhookAutoDisableThreshold disables a webhook after this many consecutive
	// dead deliveries.
	WebhookAutoDisableThreshold int `env:"WEBHOOK_AUTO_DISABLE_THRESHOLD" envDefault:"20"`
	// WebhookPollInterval is how often the dispatcher drains the outbox and
	// attempts due deliveries.
	WebhookPollInterval time.Duration `env:"WEBHOOK_POLL_INTERVAL" envDefault:"2s"`

	// --- SSE ---
	SSEMaxConnPerUser int           `env:"SSE_MAX_CONN_PER_USER" envDefault:"10"`
	SSEPollInterval   time.Duration `env:"SSE_POLL_INTERVAL" envDefault:"1s"`

	// EventGraceWindow is how fresh an event may be before the seq-cursor reads
	// (SSE) will serve it. It guards against bigserial values committing out of
	// order; see db/queries/events.sql.
	EventGraceWindow time.Duration `env:"EVENT_GRACE_WINDOW" envDefault:"1s"`
	// JanitorInterval is how often retention jobs run (old event purge, expired
	// idempotency keys). 0 disables the janitor.
	JanitorInterval time.Duration `env:"JANITOR_INTERVAL" envDefault:"1h"`

	// ScanInterval is how often the GitHub commit queue is drained. A user who
	// stops a timer expects the commits shortly after, so this is short.
	// 0 disables the scanner entirely.
	ScanInterval time.Duration `env:"SCAN_INTERVAL" envDefault:"1m"`
	// ScanRequeueInterval is how often finished sessions from the last seven
	// days go back on the queue, which is what catches commits pushed hours
	// after they were written.
	ScanRequeueInterval time.Duration `env:"SCAN_REQUEUE_INTERVAL" envDefault:"1h"`

	// --- Quotas (generous defaults) ---
	MaxPATsPerUser     int `env:"MAX_PATS_PER_USER" envDefault:"25"`
	MaxProjectsPerUser int `env:"MAX_PROJECTS_PER_USER" envDefault:"500"`
	MaxWebhooksPerUser int `env:"MAX_WEBHOOKS_PER_USER" envDefault:"10"`
}

// GoogleOAuthEnabled reports whether Google sign-in is configured.
func (c *Config) GoogleOAuthEnabled() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != ""
}

// GitHubOAuthEnabled reports whether GitHub sign-in is configured.
func (c *Config) GitHubOAuthEnabled() bool {
	return c.GitHubClientID != "" && c.GitHubClientSecret != ""
}

// AppleOAuthEnabled reports whether Sign in with Apple is configured. All four
// values are needed: the key alone cannot say which team or key it is.
func (c *Config) AppleOAuthEnabled() bool {
	return c.AppleClientID != "" && c.AppleTeamID != "" &&
		c.AppleKeyID != "" && c.ApplePrivateKey != ""
}

// SecureCookies reports whether session cookies should carry the Secure flag.
// True in production (served over HTTPS via Caddy/Cloudflare), false in dev
// (plain http://localhost, where Secure cookies would be dropped by browsers).
func (c *Config) SecureCookies() bool { return c.IsProduction() }

// Load parses the environment into a validated Config, or returns an error
// describing exactly which variable is missing or malformed.
func Load() (*Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return nil, fmt.Errorf("parse config from environment: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	switch c.Env {
	case EnvDevelopment, EnvProduction:
	default:
		return fmt.Errorf("APP_ENV must be %q or %q, got %q", EnvDevelopment, EnvProduction, c.Env)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL must be one of debug|info|warn|error, got %q", c.LogLevel)
	}
	if c.DBMaxConns < 1 {
		return fmt.Errorf("DB_MAX_CONNS must be >= 1, got %d", c.DBMaxConns)
	}
	switch c.EmailProvider {
	case "dev", "smtp":
	default:
		return fmt.Errorf("EMAIL_PROVIDER must be dev|smtp, got %q", c.EmailProvider)
	}
	return nil
}

// IsProduction reports whether the process runs in production mode.
func (c *Config) IsProduction() bool { return c.Env == EnvProduction }
