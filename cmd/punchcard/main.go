// Command punchcard is the single binary for the service. Subcommands:
//
//	serve       run the HTTP API server (and, later, MCP HTTP + River workers)
//	migrate     apply (up) or roll back (down) database migrations
//	version     print the build version
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/email"
	apphttp "github.com/cobanov/punchcard/internal/http"
	"github.com/cobanov/punchcard/internal/oauth"
	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/service"
	"github.com/cobanov/punchcard/internal/webhooks"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		usage()
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "serve":
		return exit(cmdServe(ctx))
	case "migrate":
		return exit(cmdMigrate(ctx, os.Args[2:]))
	case "version", "--version", "-v":
		fmt.Printf("punchcard %s\n", version)
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		return 2
	}
}

func exit(err error) int {
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func cmdServe(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := observability.NewLogger(cfg.LogLevel)
	metrics := observability.NewMetrics()

	pool, err := repo.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	migrator, err := repo.NewMigrator(pool)
	if err != nil {
		return err
	}
	defer func() { _ = migrator.Close() }()

	if cfg.AutoMigrate {
		logger.Info("applying migrations under advisory lock")
		if err := migrator.Up(ctx); err != nil {
			return err
		}
	}

	store := repo.NewStore(pool)
	sender := email.New(cfg, logger)
	auditor := audit.NewLogger(store, logger)

	var cipher *webhooks.Cipher
	if cfg.WebhookEncryptionKey != "" {
		cipher, err = webhooks.NewCipher(cfg.WebhookEncryptionKey)
		if err != nil {
			return err
		}
		dispatcher := webhooks.NewDispatcher(store, cipher, logger, cfg.WebhookAllowHTTP, cfg.WebhookMaxConcurrency, cfg.WebhookAutoDisableThreshold, cfg.WebhookPollInterval, cfg.MaxWebhooksPerList)
		go dispatcher.Run(ctx)
	} else {
		logger.Warn("WEBHOOK_ENCRYPTION_KEY not set; webhook features disabled")
	}

	janitor := repo.NewJanitor(store, logger, cfg.JanitorInterval)
	go janitor.Run(ctx)

	authSvc := service.NewAuth(store, sender, auditor, logger, cfg)
	domainSvc := service.NewDomain(store, auditor, sender, cipher, logger, cfg)
	oauthProviders := oauth.New(cfg)
	logger.Info("social login providers",
		"google", cfg.GoogleOAuthEnabled(), "github", cfg.GitHubOAuthEnabled())

	srv := apphttp.New(apphttp.Deps{
		Config:   cfg,
		Logger:   logger,
		Metrics:  metrics,
		Pool:     pool,
		Migrator: migrator,
		Store:    store,
		Auth:     authSvc,
		Domain:   domainSvc,
		OAuth:    oauthProviders,
	})
	return srv.Run(ctx)
}

func cmdMigrate(ctx context.Context, args []string) error {
	direction := "up"
	if len(args) > 0 {
		direction = args[0]
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := observability.NewLogger(cfg.LogLevel)

	pool, err := repo.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	migrator, err := repo.NewMigrator(pool)
	if err != nil {
		return err
	}
	defer func() { _ = migrator.Close() }()

	switch direction {
	case "up":
		logger.Info("migrating up")
		return migrator.Up(ctx)
	case "down":
		logger.Info("migrating down one step")
		return migrator.Down(ctx)
	default:
		return fmt.Errorf("migrate: unknown direction %q (use up|down)", direction)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `punchcard — developer time tracking

Usage:
  punchcard <command> [args]

Commands:
  serve                 Run the HTTP API server
  migrate [up|down]     Apply or roll back database migrations (default: up)
  version               Print the build version
  help                  Show this help

Configuration is read from the environment; see .env.example.
`)
}
