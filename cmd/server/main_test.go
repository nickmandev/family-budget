package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"family-budget/internal/config"
	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

type fakeDatabase struct {
	pingErr  error
	closeErr error
	closed   bool
}

func (f *fakeDatabase) Ping(context.Context) error { return f.pingErr }
func (f *fakeDatabase) Close() error {
	f.closed = true
	return f.closeErr
}

func (f *fakeDatabase) Repositories() ports.Repositories {
	return ports.Repositories{
		Users:       stubUsers{},
		Sessions:    stubSessions{},
		Memberships: stubMemberships{},
	}
}

func (f *fakeDatabase) WithinTransaction(ctx context.Context, fn func(context.Context, ports.Repositories) error) error {
	return fn(ctx, f.Repositories())
}

var _ database = (*fakeDatabase)(nil)

type stubUsers struct{}

func (stubUsers) Create(context.Context, ports.UserRecord) error { return nil }
func (stubUsers) Get(context.Context, domain.UserId) (ports.UserRecord, error) {
	return ports.UserRecord{}, nil
}
func (stubUsers) FindByEmail(context.Context, string) (ports.UserRecord, error) {
	return ports.UserRecord{}, nil
}

type stubSessions struct{}

func (stubSessions) Create(context.Context, ports.SessionRecord) error { return nil }
func (stubSessions) FindByTokenHash(context.Context, string) (ports.SessionRecord, error) {
	return ports.SessionRecord{}, nil
}
func (stubSessions) Revoke(context.Context, string) error { return nil }
func (stubSessions) RevokeAllForUser(context.Context, domain.UserId, time.Time) error {
	return nil
}

type stubMemberships struct{}

func (stubMemberships) Add(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}
func (stubMemberships) Get(context.Context, domain.HouseholdId, domain.UserId) (ports.MembershipRecord, error) {
	return ports.MembershipRecord{}, nil
}
func (stubMemberships) List(context.Context, domain.HouseholdId) ([]ports.MembershipRecord, error) {
	return nil, nil
}
func (stubMemberships) ListForUser(context.Context, domain.UserId) ([]ports.UserMembershipRecord, error) {
	return nil, nil
}
func (stubMemberships) Save(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}

func TestRunRejectsMissingDatabaseURL(t *testing.T) {
	opened := false
	err := run(context.Background(), runDeps{
		loadConfig: func() (config.Config, error) {
			return config.LoadFrom(func(string) (string, bool) { return "", false })
		},
		openDatabase: func(config.Config) (database, error) {
			opened = true
			return &fakeDatabase{}, nil
		},
		stderr: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), config.EnvDatabaseURL) {
		t.Fatalf("error = %v, want %s validation failure", err, config.EnvDatabaseURL)
	}
	if opened {
		t.Fatal("database was opened despite invalid configuration")
	}
}

func TestRunRedactsDatabaseCredentialsOnOpenFailure(t *testing.T) {
	const dsn = "postgres://budget:supersecret@db.example:5432/family?sslmode=disable"
	err := run(context.Background(), runDeps{
		loadConfig: func() (config.Config, error) {
			cfg := config.Default()
			cfg.DatabaseURL = dsn
			return cfg, nil
		},
		openDatabase: func(config.Config) (database, error) {
			return nil, errors.New("dial failed: " + dsn + " password=supersecret")
		},
		stderr: io.Discard,
	})
	if err == nil {
		t.Fatal("expected an open failure")
	}
	for _, secret := range []string{"supersecret", "budget:", dsn} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("startup error leaked %q: %v", secret, err)
		}
	}
	for _, want := range []string{"db.example:5432/family", "connect to database"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("startup error %q does not contain %q", err, want)
		}
	}
}

func TestRunClosesDatabaseWhenPingFails(t *testing.T) {
	cfg := config.Default()
	cfg.DatabaseURL = "postgres://budget:supersecret@db.example/family"
	cfg.HTTP.Port = 0
	db := &fakeDatabase{pingErr: errors.New("ping " + cfg.DatabaseURL)}

	err := run(context.Background(), runDeps{
		loadConfig:   func() (config.Config, error) { return cfg, nil },
		openDatabase: func(config.Config) (database, error) { return db, nil },
		stderr:       io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("error = %v, want not-reachable failure", err)
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Errorf("ping error leaked credentials: %v", err)
	}
	if !db.closed {
		t.Fatal("database was not closed after a failed ping")
	}
}

func TestBuildServerWiresFullAPISurfaceWithoutDatabase(t *testing.T) {
	cfg := config.Default()
	cfg.HTTP.Port = 0
	server, err := buildServer(cfg, &fakeDatabase{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if got := server.Addr(); got != ":0" {
		t.Fatalf("addr = %q, want :0", got)
	}
	routes := server.Routes()
	if len(routes) < 40 {
		t.Fatalf("registered %d routes, want the full API surface", len(routes))
	}
	for _, want := range []string{
		"POST /api/v1/auth/login",
		"GET /api/v1/households/{id}/balances",
		"POST /api/v1/households/{id}/transactions/purchases",
		"POST /api/v1/households/{id}/plans/{planID}/authorize",
		"POST /api/v1/households/{id}/suggestions/{suggestionID}/confirm",
		"GET /readyz",
	} {
		if !contains(routes, want) {
			t.Errorf("route %q is missing", want)
		}
	}
}

func TestRunShutsDownOnContextCancel(t *testing.T) {
	cfg := config.Default()
	cfg.DatabaseURL = "postgres://budget:supersecret@db.example/family"
	cfg.HTTP.Port = 0
	db := &fakeDatabase{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, runDeps{
			loadConfig:   func() (config.Config, error) { return cfg, nil },
			openDatabase: func(config.Config) (database, error) { return db, nil },
			stderr:       io.Discard,
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v, want clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after context cancellation")
	}
	if !db.closed {
		t.Fatal("database was not closed during shutdown")
	}
}

func TestDatabaseTargetOmitsCredentials(t *testing.T) {
	target := databaseTarget("postgres://budget:supersecret@db.example:5432/family?sslmode=disable")
	if target != "db.example:5432/family" {
		t.Fatalf("target = %q", target)
	}
	if strings.Contains(target, "budget") || strings.Contains(target, "supersecret") {
		t.Fatalf("target leaked credentials: %q", target)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
