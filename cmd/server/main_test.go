package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
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

func TestRunRejectsMissingDatabaseComponents(t *testing.T) {
	opened := false
	err := run(context.Background(), runDeps{
		loadConfig: func() (config.Config, error) {
			return config.LoadFrom(func(string) (string, bool) { return "", false })
		},
		openDatabase: func(config.Config) (database, error) {
			opened = true
			return &fakeDatabase{}, nil
		},
		readFile: os.ReadFile,
		stderr:   io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), config.EnvDatabaseHost) {
		t.Fatalf("error = %v, want %s validation failure", err, config.EnvDatabaseHost)
	}
	if opened {
		t.Fatal("database was opened despite invalid configuration")
	}
}

// validConfig returns a config with a complete, deliberately secret-bearing
// database component set.
func validConfig() config.Config {
	cfg := config.Default()
	cfg.Database.Host = "db.example"
	cfg.Database.Port = 5432
	cfg.Database.User = "budget"
	cfg.Database.Password = "supersecret"
	cfg.Database.Name = "family"
	cfg.Database.SSLMode = "disable"
	return cfg
}

func TestRunRedactsDatabaseCredentialsOnOpenFailure(t *testing.T) {
	cfg := validConfig()
	dsn, err := cfg.Database.DSN()
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}
	err = run(context.Background(), runDeps{
		loadConfig: func() (config.Config, error) { return cfg, nil },
		openDatabase: func(config.Config) (database, error) {
			return nil, errors.New("dial failed: " + dsn + " password=supersecret")
		},
		readFile: os.ReadFile,
		stderr:   io.Discard,
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
	cfg := validConfig()
	cfg.HTTP.Port = 0
	dsn, err := cfg.Database.DSN()
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}
	db := &fakeDatabase{pingErr: errors.New("ping " + dsn)}

	err = run(context.Background(), runDeps{
		loadConfig:   func() (config.Config, error) { return cfg, nil },
		openDatabase: func(config.Config) (database, error) { return db, nil },
		readFile:     os.ReadFile,
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
	server, err := buildServer(cfg, &fakeDatabase{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), os.ReadFile)
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

func TestBuildServerLeavesBankRoutesUnregisteredWhenDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.HTTP.Port = 0
	server, err := buildServer(cfg, &fakeDatabase{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), func(path string) ([]byte, error) {
		t.Fatalf("readFile called while Enable Banking is disabled for %q", path)
		return nil, errors.New("unexpected private key read")
	})
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	for _, route := range server.Routes() {
		if strings.Contains(route, "/bank/") || strings.Contains(route, "/callbacks/") {
			t.Errorf("bank route %q registered while Enable Banking is disabled", route)
		}
	}
}

func TestBuildServerRegistersBankRoutesWhenEnabled(t *testing.T) {
	cfg := enabledBankingConfig()
	privateKeyPEM := testPrivateKeyPEM(t)
	server, err := buildServer(cfg, &fakeDatabase{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), func(path string) ([]byte, error) {
		if path != cfg.EnableBanking.PrivateKeyFile {
			t.Fatalf("readFile path = %q, want %q", path, cfg.EnableBanking.PrivateKeyFile)
		}
		return privateKeyPEM, nil
	})
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	for _, route := range []string{
		"GET /api/v1/households/{id}/bank/aspsps",
		"POST /api/v1/households/{id}/bank/connections",
		"GET /api/v1/households/{id}/bank/connections",
		"DELETE /api/v1/households/{id}/bank/connections/{connectionID}",
		"GET /callbacks/enablebanking",
	} {
		if !contains(server.Routes(), route) {
			t.Errorf("route %q is missing", route)
		}
	}
}

func TestBuildServerReportsPrivateKeyPathWithoutContents(t *testing.T) {
	cfg := enabledBankingConfig()
	const privateKeyContents = "PRIVATE KEY CONTENT MUST NOT APPEAR"
	server, err := buildServer(cfg, &fakeDatabase{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), func(string) ([]byte, error) {
		return nil, fs.ErrPermission
	})
	if err == nil {
		t.Fatal("buildServer succeeded despite private key read failure")
	}
	if !strings.Contains(err.Error(), cfg.EnableBanking.PrivateKeyFile) {
		t.Errorf("error %q does not name private key path %q", err, cfg.EnableBanking.PrivateKeyFile)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error %q does not report permission denied", err)
	}
	if strings.Contains(err.Error(), privateKeyContents) {
		t.Errorf("error leaked private key contents: %v", err)
	}
	if server != nil {
		t.Fatal("buildServer returned a server after private key read failure")
	}
}

func TestBuildServerDoesNotExposeInvalidPEM(t *testing.T) {
	cfg := enabledBankingConfig()
	const invalidPEM = "-----BEGIN PRIVATE KEY-----\nnot-a-real-private-key\n-----END PRIVATE KEY-----"
	server, err := buildServer(cfg, &fakeDatabase{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), func(string) ([]byte, error) {
		return []byte(invalidPEM), nil
	})
	if err == nil {
		t.Fatal("buildServer succeeded with invalid PEM")
	}
	if strings.Contains(err.Error(), invalidPEM) {
		t.Errorf("error leaked PEM contents: %v", err)
	}
	if server != nil {
		t.Fatal("buildServer returned a server with invalid PEM")
	}
}

func enabledBankingConfig() config.Config {
	cfg := config.Default()
	cfg.HTTP.Port = 0
	cfg.EnableBanking.Enabled = true
	cfg.EnableBanking.EncryptionKey = make([]byte, 32)
	cfg.EnableBanking.ApplicationID = "123e4567-e89b-12d3-a456-426614174000"
	cfg.EnableBanking.PrivateKeyFile = "/run/secrets/enablebanking.pem"
	cfg.EnableBanking.RedirectURL = "https://example.com/callback"
	return cfg
}

func testPrivateKeyPEM(t *testing.T) []byte {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test RSA key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
}

func TestRunShutsDownOnContextCancel(t *testing.T) {
	cfg := validConfig()
	cfg.HTTP.Port = 0
	db := &fakeDatabase{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, runDeps{
			loadConfig:   func() (config.Config, error) { return cfg, nil },
			openDatabase: func(config.Config) (database, error) { return db, nil },
			readFile:     os.ReadFile,
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
	target := validConfig().Database.Target()
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
