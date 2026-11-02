package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"family-budget/internal/config"
	"family-budget/internal/ports"
)

type fakeDatabase struct {
	mu      sync.Mutex
	pingErr error
	closed  bool
	records []ports.WorkerHeartbeatRecord
	updates chan ports.WorkerHeartbeatRecord
}

type lockedBuffer struct {
	mu sync.Mutex
	strings.Builder
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Builder.Write(value)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Builder.String()
}

func newFakeDatabase() *fakeDatabase {
	return &fakeDatabase{updates: make(chan ports.WorkerHeartbeatRecord, 32)}
}

func (f *fakeDatabase) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingErr
}

func (f *fakeDatabase) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeDatabase) Repositories() ports.Repositories {
	return ports.Repositories{Heartbeats: fakeHeartbeats{db: f}}
}

func (f *fakeDatabase) WithinTransaction(ctx context.Context, fn func(context.Context, ports.Repositories) error) error {
	return fn(ctx, f.Repositories())
}

type fakeHeartbeats struct {
	db *fakeDatabase
}

func (r fakeHeartbeats) Upsert(_ context.Context, record ports.WorkerHeartbeatRecord) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()
	recordCopy := record
	if record.LastErrorAt != nil {
		value := *record.LastErrorAt
		recordCopy.LastErrorAt = &value
	}
	if record.LastErrorSummary != nil {
		value := *record.LastErrorSummary
		recordCopy.LastErrorSummary = &value
	}
	r.db.records = append(r.db.records, recordCopy)
	r.db.updates <- recordCopy
	return nil
}

func (r fakeHeartbeats) Get(_ context.Context, workerName string) (ports.WorkerHeartbeatRecord, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()
	for index := len(r.db.records) - 1; index >= 0; index-- {
		if r.db.records[index].WorkerName == workerName {
			return r.db.records[index], nil
		}
	}
	return ports.WorkerHeartbeatRecord{}, errors.New("not found")
}

func workerConfig(port int) config.Config {
	cfg := config.Default()
	cfg.Database.Host = "localhost"
	cfg.Database.Port = 5432
	cfg.Database.User = "user"
	cfg.Database.Password = "pass"
	cfg.Database.Name = "family"
	cfg.Database.SSLMode = "disable"
	cfg.SyncWorkerPort = port
	cfg.EnableBanking.SyncInterval = 20 * time.Millisecond
	cfg.HTTP.ShutdownTimeout = time.Second
	return cfg
}

func TestDisabledIntegrationLogsIdleAndServesHealth(t *testing.T) {
	port := availablePort(t)
	cfg := workerConfig(port)
	db := newFakeDatabase()
	var logs lockedBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(ctx, runDeps{
		loadConfig:   func() (config.Config, error) { return cfg, nil },
		openDatabase: func(config.Config) (database, error) { return db, nil },
		stderr:       &logs,
	})

	response := getEventually(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.StatusCode)
	}
	if !strings.Contains(logs.String(), "sync worker idle: enable banking is disabled") {
		t.Fatalf("logs = %q, want idle message", logs.String())
	}
	cancel()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run returned %v, want clean shutdown", err)
	}
	if !db.wasClosed() {
		t.Fatal("database was not closed during shutdown")
	}
}

func TestEnabledTicksPersistTruncatedErrorAndContinue(t *testing.T) {
	port := availablePort(t)
	cfg := workerConfig(port)
	cfg.EnableBanking.Enabled = true
	cfg.EnableBanking.Country = "BG"
	db := newFakeDatabase()
	var syncCalls sync.Mutex
	callCount := 0
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(ctx, runDeps{
		loadConfig:   func() (config.Config, error) { return cfg, nil },
		openDatabase: func(config.Config) (database, error) { return db, nil },
		stderr:       io.Discard,
		sync: func(context.Context) error {
			syncCalls.Lock()
			callCount++
			syncCalls.Unlock()
			return errors.New(strings.Repeat("x", 650))
		},
	})

	var first, second ports.WorkerHeartbeatRecord
	select {
	case first = <-db.updates:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not write a heartbeat")
	}
	select {
	case second = <-db.updates:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not continue after the failed tick")
	}
	cancel()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run returned %v, want clean shutdown", err)
	}

	if first.WorkerName != workerName || first.StartedAt.IsZero() || first.LastTickAt.IsZero() {
		t.Fatalf("first heartbeat = %+v", first)
	}
	if second.LastErrorAt == nil || second.LastErrorSummary == nil {
		t.Fatalf("second heartbeat has no recorded error: %+v", second)
	}
	if got := len([]rune(*second.LastErrorSummary)); got != 500 {
		t.Errorf("error summary length = %d runes, want 500", got)
	}
	syncCalls.Lock()
	calls := callCount
	syncCalls.Unlock()
	if calls < 2 {
		t.Errorf("sync calls = %d, want at least 2 after an error", calls)
	}
}

func TestReadyzReturnsUnavailableWhenPingFails(t *testing.T) {
	port := availablePort(t)
	cfg := workerConfig(port)
	db := newFakeDatabase()
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(ctx, runDeps{
		loadConfig:   func() (config.Config, error) { return cfg, nil },
		openDatabase: func(config.Config) (database, error) { return db, nil },
		stderr:       io.Discard,
	})

	url := fmt.Sprintf("http://127.0.0.1:%d/readyz", port)
	response := getEventually(t, url)
	response.Body.Close()
	db.mu.Lock()
	db.pingErr = errors.New("database unavailable")
	db.mu.Unlock()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want 503", response.StatusCode)
	}
	if got := responseBody(t, response); got != "{\"status\":\"unready\"}\n" {
		t.Errorf("ready body = %q, want unready JSON", got)
	}
	cancel()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run returned %v, want clean shutdown", err)
	}
}

func TestRunReturnsNilOnGracefulShutdown(t *testing.T) {
	port := availablePort(t)
	cfg := workerConfig(port)
	db := newFakeDatabase()
	ctx, cancel := context.WithCancel(context.Background())
	done := startRun(ctx, runDeps{
		loadConfig:   func() (config.Config, error) { return cfg, nil },
		openDatabase: func(config.Config) (database, error) { return db, nil },
		stderr:       io.Discard,
	})
	response := getEventually(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	response.Body.Close()
	cancel()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run returned %v, want nil", err)
	}
}

func availablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release test port: %v", err)
	}
	return port
}

func startRun(ctx context.Context, deps runDeps) <-chan error {
	done := make(chan error, 1)
	go func() { done <- run(ctx, deps) }()
	return done
}

func getEventually(t *testing.T, url string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			return response
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become available", url)
	return nil
}

func waitRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop after context cancellation")
		return nil
	}
}

func (f *fakeDatabase) wasClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func responseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(data)
}
