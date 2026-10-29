// Command server is the production process entrypoint. It validates
// configuration, opens the PostgreSQL store, pings it before accepting traffic,
// composes the auth/application/transport layers, and serves until SIGINT or
// SIGTERM.
//
// Schema migrations are applied out-of-band with Goose (see migrations/); this
// binary never runs AutoMigrate or ad-hoc DDL.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"family-budget/internal/adapters/postgres"
	"family-budget/internal/application"
	"family-budget/internal/auth"
	"family-budget/internal/config"
	"family-budget/internal/ports"
	httpapi "family-budget/internal/transport/http"
)

// database is the subset of the PostgreSQL adapter the entrypoint needs. It is
// an interface so the wiring and failure paths can be tested without a live
// database.
type database interface {
	ports.UnitOfWork
	Repositories() ports.Repositories
	Ping(context.Context) error
	Close() error
}

// runDeps holds the process seams: config loading, database opening and the
// diagnostic sink. Production uses the defaults in main; tests replace them.
type runDeps struct {
	loadConfig   func() (config.Config, error)
	openDatabase func(config.Config) (database, error)
	stderr       io.Writer
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := run(ctx, runDeps{
		loadConfig: config.Load,
		openDatabase: func(cfg config.Config) (database, error) {
			dsn, err := cfg.Database.DSN()
			if err != nil {
				return nil, err
			}
			return postgres.Open(dsn, postgres.PoolConfig{
				MaxOpenConns:    cfg.Database.MaxOpenConns,
				MaxIdleConns:    cfg.Database.MaxIdleConns,
				ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
				ConnMaxIdleTime: cfg.Database.ConnMaxIdleTime,
			})
		},
		stderr: os.Stderr,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}

// run validates configuration, connects and verifies the database, wires the
// server and serves until ctx is canceled. Startup errors never include the DSN
// or its credentials.
func run(ctx context.Context, deps runDeps) error {
	if deps.loadConfig == nil {
		return fmt.Errorf("configuration loader is required")
	}
	if deps.openDatabase == nil {
		return fmt.Errorf("database opener is required")
	}
	if deps.stderr == nil {
		deps.stderr = io.Discard
	}

	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	dsn, err := cfg.Database.DSN()
	if err != nil {
		return fmt.Errorf("invalid database configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(deps.stderr, nil))

	db, err := deps.openDatabase(cfg)
	if err != nil {
		return fmt.Errorf("connect to database %s: %s", cfg.Database.Target(), cfg.Database.RedactError(err, dsn))
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			logger.Error("close database", "error", cfg.Database.RedactError(closeErr, dsn))
		}
	}()

	if err := db.Ping(ctx); err != nil {
		return fmt.Errorf("database %s is not reachable: %s", cfg.Database.Target(), cfg.Database.RedactError(err, dsn))
	}

	server, err := buildServer(cfg, db, logger)
	if err != nil {
		return err
	}

	logger.Info("starting api server",
		"addr", server.Addr(),
		"database", cfg.Database.Target(),
		"migrations", "applied out-of-band with goose",
	)
	if err := server.Run(ctx); err != nil {
		return fmt.Errorf("serve http: %w", err)
	}
	return nil
}

// buildServer composes the auth, application and HTTP layers. It does not open
// or migrate a database; the caller supplies an already-verified store.
func buildServer(cfg config.Config, db database, logger *slog.Logger) (*httpapi.Server, error) {
	hasher, err := auth.NewHasher(auth.Argon2Params{
		Time:       cfg.Auth.Argon2.Time,
		MemoryKiB:  cfg.Auth.Argon2.MemoryKiB,
		Threads:    cfg.Auth.Argon2.Threads,
		KeyLength:  cfg.Auth.Argon2.KeyLength,
		SaltLength: cfg.Auth.Argon2.SaltLength,
	})
	if err != nil {
		return nil, fmt.Errorf("configure password hasher: %w", err)
	}

	repos := db.Repositories()
	authService, err := auth.NewService(auth.ServiceDeps{
		Users:       repos.Users,
		Sessions:    repos.Sessions,
		Memberships: repos.Memberships,
		Hasher:      hasher,
		Clock:       postgres.SystemClock{},
		IDs:         postgres.UUIDGenerator{},
		SessionTTL:  cfg.Auth.SessionTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("configure auth service: %w", err)
	}

	appService, err := application.New(application.Dependencies{
		UnitOfWork: db,
		Clock:      postgres.SystemClock{},
		IDs:        postgres.UUIDGenerator{},
	})
	if err != nil {
		return nil, fmt.Errorf("configure application service: %w", err)
	}

	server, err := httpapi.NewServer(httpapi.Dependencies{
		Config:    cfg,
		Logger:    logger,
		Clock:     postgres.SystemClock{},
		Readiness: db.Ping,
		Auth:      authService,
		App:       appService,
	})
	if err != nil {
		return nil, fmt.Errorf("configure http server: %w", err)
	}
	return server, nil
}
