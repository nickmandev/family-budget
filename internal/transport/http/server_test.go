package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"family-budget/internal/config"
)

func newTestServer(t *testing.T, readiness func(context.Context) error) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.HTTP.Port = 0
	cfg.HTTP.RequestTimeout = 2 * time.Second
	cfg.HTTP.MaxBodyBytes = 1024
	cfg.HTTP.ShutdownTimeout = time.Second

	server, err := NewServer(Dependencies{
		Config:    cfg,
		Logger:    slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Readiness: readiness,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server
}

func TestHealthEndpoints(t *testing.T) {
	server := newTestServer(t, nil)

	for _, path := range []string{"/healthz", "/readyz", "/api/v1/health"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("content type = %q", got)
			}
			var body map[string]string
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["status"] == "" {
				t.Fatalf("body = %v, want a status field", body)
			}
			if recorder.Header().Get(HeaderRequestID) == "" {
				t.Fatal("health response has no request id")
			}
		})
	}
}

func TestReadyzFailureIsGeneric(t *testing.T) {
	server := newTestServer(t, func(context.Context) error {
		return errors.New("password authentication failed for db host 10.0.0.5")
	})

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if envelope := decodeErrorEnvelope(t, recorder); envelope.Error.Code != CodeServiceUnavailable {
		t.Fatalf("code = %q, want %s", envelope.Error.Code, CodeServiceUnavailable)
	}
	for _, secret := range []string{"password", "10.0.0.5", "authentication"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("readiness response leaked %q: %s", secret, recorder.Body.String())
		}
	}
}

func TestUnknownRouteUsesErrorEnvelope(t *testing.T) {
	server := newTestServer(t, nil)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	if envelope := decodeErrorEnvelope(t, recorder); envelope.Error.Code != CodeNotFound {
		t.Fatalf("code = %q, want %s", envelope.Error.Code, CodeNotFound)
	}
}

func TestRunShutsDownOnContextCancel(t *testing.T) {
	server := newTestServer(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- server.Run(ctx) }()

	// Give ListenAndServe a moment to bind, then request shutdown.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
