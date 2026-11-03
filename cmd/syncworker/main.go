// Command syncworker is the separately deployable background-process
// entrypoint. Phase 1 reports worker liveness; bank synchronization is added
// behind SyncFunc in a later phase.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"family-budget/internal/adapters/postgres"
	"family-budget/internal/config"
	"family-budget/internal/ports"
)

const workerName = "enablebanking-sync"

// SyncFunc performs one background sync pass. Phase 2 provides the real
// implementation; the skeleton logs and returns nil. Errors must not include
// provider payloads, tokens or account numbers because they become persisted
// and logged error summaries.
type SyncFunc func(ctx context.Context) error

type database interface {
	ports.UnitOfWork
	Repositories() ports.Repositories
	Ping(context.Context) error
	Close() error
}

type runDeps struct {
	loadConfig   func() (config.Config, error)
	openDatabase func(config.Config) (database, error)
	stderr       io.Writer
	sync         SyncFunc
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
		fmt.Fprintln(os.Stderr, "sync worker:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, deps runDeps) error {
	if deps.loadConfig == nil {
		return errors.New("configuration loader is required")
	}
	if deps.openDatabase == nil {
		return errors.New("database opener is required")
	}
	if deps.stderr == nil {
		deps.stderr = io.Discard
	}
	if deps.sync == nil {
		deps.sync = func(context.Context) error { return nil }
	}

	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	dsn, err := cfg.Database.DSN()
	if err != nil {
		return fmt.Errorf("invalid database configuration: %w", err)
	}
	if cfg.SyncWorkerPort < 1 || cfg.SyncWorkerPort > 65535 {
		return fmt.Errorf("%s must be between 1 and 65535", config.EnvSyncWorkerPort)
	}
	if cfg.EnableBanking.SyncInterval <= 0 {
		return fmt.Errorf("%s must be positive", config.EnvEnableBankingSyncInterval)
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			writeHealth(w, http.StatusServiceUnavailable, "unready")
			return
		}
		writeHealth(w, http.StatusOK, "ready")
	})
	server := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.SyncWorkerPort),
		Handler:           mux,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen for health checks: %w", err)
	}
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	startedAt := time.Now().UTC()
	if cfg.EnableBanking.Enabled {
		logger.Info("sync worker started", "interval", cfg.EnableBanking.SyncInterval, "country", cfg.EnableBanking.Country)
	} else {
		logger.Info("sync worker idle: enable banking is disabled")
	}

	ticker := time.NewTicker(cfg.EnableBanking.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("shut down health server: %w", err)
			}
			return nil
		case err := <-serveErr:
			if err != nil {
				return fmt.Errorf("serve health server: %w", err)
			}
			return nil
		case now := <-ticker.C:
			if !cfg.EnableBanking.Enabled {
				continue
			}
			runTick(ctx, db, logger, deps.sync, startedAt, now.UTC())
		}
	}
}

func runTick(ctx context.Context, db database, logger *slog.Logger, syncFn SyncFunc, startedAt, now time.Time) {
	syncErr := syncFn(ctx)
	record := ports.WorkerHeartbeatRecord{
		WorkerName: workerName,
		StartedAt:  startedAt,
		LastTickAt: now,
	}
	if syncErr != nil {
		summary := truncateSummary(syncErr.Error(), 500)
		errorAt := now
		record.LastErrorAt = &errorAt
		record.LastErrorSummary = &summary
		logger.Error("sync tick failed", "error", summary)
	}
	if err := db.WithinTransaction(ctx, func(txCtx context.Context, repos ports.Repositories) error {
		if repos.Heartbeats == nil {
			return errors.New("worker heartbeat repository is unavailable")
		}
		return repos.Heartbeats.Upsert(txCtx, record)
	}); err != nil {
		logger.Error("persist sync worker heartbeat failed", "error", err)
	}
}

func truncateSummary(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return strings.TrimSpace(value)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeHealth(w, http.StatusOK, "ok")
}

func writeHealth(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "{\"status\":%q}\n", value)
}
