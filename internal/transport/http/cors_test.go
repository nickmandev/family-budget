package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"family-budget/internal/application"
	"family-budget/internal/auth"
	"family-budget/internal/config"
)

const testCORSOrigin = "https://budget.nickmandev.com"

func newCORSConfiguredServer(t *testing.T, origins []string, authService *auth.Service, app *application.Service) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.HTTP.Port = 0
	cfg.HTTPCORSAllowedOrigins = origins
	server, err := NewServer(Dependencies{
		Config: cfg,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Auth:   authService,
		App:    app,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server
}

func TestCORSMiddlewareAllowedOriginHeaders(t *testing.T) {
	server := newCORSConfiguredServer(t, []string{testCORSOrigin}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("Origin", "https://BUDGET.nickmandev.com")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://BUDGET.nickmandev.com" {
		t.Errorf("allow origin = %q, want exact request origin", got)
	}
	if !headerContains(recorder.Header().Values("Vary"), "Origin") {
		t.Errorf("Vary = %v, want Origin", recorder.Header().Values("Vary"))
	}
	if got := recorder.Header().Get("Access-Control-Expose-Headers"); got != HeaderRequestID {
		t.Errorf("expose headers = %q, want %s", got, HeaderRequestID)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("allow credentials = %q, want no credentials header", got)
	}
}

func TestCORSMiddlewareDisallowedOriginDoesNotBlockRequest(t *testing.T) {
	server := newCORSConfiguredServer(t, []string{testCORSOrigin}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("Origin", "https://attacker.example")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want normal health response 200", recorder.Code)
	}
	assertNoCORSHeaders(t, recorder)
}

func TestCORSMiddlewareAllowedPreflightBypassesAuthentication(t *testing.T) {
	harness := newResourceServer(t)
	server := newCORSConfiguredServer(t, []string{testCORSOrigin}, harness.auth, harness.app)
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/households", nil)
	request.Header.Set("Origin", testCORSOrigin)
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 without bearer authentication", recorder.Code)
	}
	for name, want := range map[string]string{
		"Access-Control-Allow-Origin":   testCORSOrigin,
		"Access-Control-Allow-Methods":  "GET, POST, PATCH, DELETE, OPTIONS",
		"Access-Control-Allow-Headers":  "Authorization, Content-Type, Idempotency-Key",
		"Access-Control-Max-Age":        "600",
		"Access-Control-Expose-Headers": HeaderRequestID,
	} {
		if got := recorder.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if !headerContains(recorder.Header().Values("Vary"), "Origin") {
		t.Errorf("Vary = %v, want Origin", recorder.Header().Values("Vary"))
	}
	if got := recorder.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("allow credentials = %q, want no credentials header", got)
	}
}

func TestCORSMiddlewareDisallowedPreflightReturns204WithoutCORS(t *testing.T) {
	server := newCORSConfiguredServer(t, []string{testCORSOrigin}, nil, nil)
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/households", nil)
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	assertNoCORSHeaders(t, recorder)
}

func TestCORSMiddlewarePreservesExistingVaryValues(t *testing.T) {
	handler := corsMiddleware([]string{testCORSOrigin}, HeaderRequestID)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Origin", testCORSOrigin)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	values := strings.Join(recorder.Header().Values("Vary"), ",")
	if !headerContains(recorder.Header().Values("Vary"), "Origin") || !headerContains(recorder.Header().Values("Vary"), "Accept-Encoding") {
		t.Errorf("Vary = %q, want Origin and Accept-Encoding", values)
	}
}

func TestCORSMiddlewareNoOriginAndDisabledConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		origin  string
	}{
		{name: "no origin", allowed: []string{testCORSOrigin}},
		{name: "disabled", origin: testCORSOrigin},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newCORSConfiguredServer(t, test.allowed, nil, nil)
			request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			assertNoCORSHeaders(t, recorder)
		})
	}
}

func assertNoCORSHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	for name, values := range recorder.Header() {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			t.Errorf("unexpected CORS header %s = %v", name, values)
		}
	}
}

func headerContains(values []string, want string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if strings.TrimSpace(part) == want {
				return true
			}
		}
	}
	return false
}
