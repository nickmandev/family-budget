package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"family-budget/internal/domain"
)

func testLogger() (*slog.Logger, *bytes.Buffer) {
	buffer := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})), buffer
}

func decodeErrorEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) ErrorEnvelope {
	t.Helper()
	var envelope ErrorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v (body %q)", err, recorder.Body.String())
	}
	return envelope
}

func TestRequestIDAppearsInEnvelopeAndLog(t *testing.T) {
	logger, logs := testLogger()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, domain.ErrInvalidAmount)
	})
	composed := Chain(handler, RequestID(), Logging(logger), Recovery(logger))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/things", nil)
	recorder := httptest.NewRecorder()
	composed.ServeHTTP(recorder, request)

	requestID := recorder.Header().Get(HeaderRequestID)
	if requestID == "" {
		t.Fatal("response X-Request-ID is empty")
	}
	envelope := decodeErrorEnvelope(t, recorder)
	if envelope.Error.RequestID != requestID {
		t.Fatalf("envelope request_id = %q, want %q", envelope.Error.RequestID, requestID)
	}
	if !strings.Contains(logs.String(), requestID) {
		t.Fatalf("log does not contain request id %q: %s", requestID, logs.String())
	}
	if !strings.Contains(logs.String(), "http_request") {
		t.Fatalf("log does not contain the request line: %s", logs.String())
	}
}

func TestRequestIDPropagatesValidInboundValue(t *testing.T) {
	logger, _ := testLogger()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	composed := Chain(handler, RequestID(), Logging(logger))

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set(HeaderRequestID, "client-correlation-1")
	recorder := httptest.NewRecorder()
	composed.ServeHTTP(recorder, request)

	if got := recorder.Header().Get(HeaderRequestID); got != "client-correlation-1" {
		t.Fatalf("propagated id = %q", got)
	}
}

func TestRequestIDReplacesInvalidInboundValue(t *testing.T) {
	logger, _ := testLogger()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	composed := Chain(handler, RequestID(), Logging(logger))

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set(HeaderRequestID, "bad id with spaces")
	recorder := httptest.NewRecorder()
	composed.ServeHTTP(recorder, request)

	got := recorder.Header().Get(HeaderRequestID)
	if got == "bad id with spaces" || got == "" {
		t.Fatalf("invalid inbound id was not replaced: %q", got)
	}
}

func TestRecoveryTurnsPanicIntoCleanServerError(t *testing.T) {
	logger, logs := testLogger()
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret panic detail")
	})
	// Use the production order, including Timeout: the panic happens in the
	// Timeout goroutine and must be re-raised to Recovery.
	composed := Chain(handler,
		RequestID(),
		Logging(logger),
		Recovery(logger),
		MaxBodyBytes(1024),
		Timeout(time.Second),
	)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/boom", nil)
	recorder := httptest.NewRecorder()
	composed.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	envelope := decodeErrorEnvelope(t, recorder)
	if envelope.Error.Code != CodeInternalError {
		t.Fatalf("code = %q, want %s", envelope.Error.Code, CodeInternalError)
	}
	if strings.Contains(recorder.Body.String(), "secret panic detail") {
		t.Fatalf("response leaked the panic value: %s", recorder.Body.String())
	}
	if !strings.Contains(logs.String(), "panic_recovered") {
		t.Fatalf("panic was not logged: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "secret panic detail") {
		t.Fatalf("server-side log should retain the panic value: %s", logs.String())
	}
}

func TestMaxBodyBytesRejectsDeclaredOversize(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := decodeJSON(w, r, &payload); err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, payload)
	})
	composed := Chain(handler, RequestID(), MaxBodyBytes(10))

	body := `{"padding":"` + strings.Repeat("x", 100) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/things", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	composed.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
	if envelope := decodeErrorEnvelope(t, recorder); envelope.Error.Code != CodePayloadTooLarge {
		t.Fatalf("code = %q", envelope.Error.Code)
	}
}

func TestMaxBodyBytesRejectsStreamedOversize(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := decodeJSON(w, r, &payload); err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, payload)
	})
	composed := Chain(handler, RequestID(), MaxBodyBytes(10))

	body := `{"padding":"` + strings.Repeat("x", 100) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/things", strings.NewReader(body))
	request.ContentLength = -1 // simulate a chunked body without a declared length
	recorder := httptest.NewRecorder()
	composed.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
	if envelope := decodeErrorEnvelope(t, recorder); envelope.Error.Code != CodePayloadTooLarge {
		t.Fatalf("code = %q", envelope.Error.Code)
	}
}

func TestTimeoutReturnsGatewayTimeoutEnvelope(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(50 * time.Millisecond)
	})
	composed := Chain(handler, RequestID(), Timeout(10*time.Millisecond))

	request := httptest.NewRequest(http.MethodGet, "/slow", nil)
	recorder := httptest.NewRecorder()
	composed.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", recorder.Code)
	}
	if envelope := decodeErrorEnvelope(t, recorder); envelope.Error.Code != CodeRequestTimeout {
		t.Fatalf("code = %q", envelope.Error.Code)
	}
}

func TestTimeoutPassesFastResponseThrough(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Test", "ok")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	composed := Chain(handler, RequestID(), Timeout(time.Second))

	request := httptest.NewRequest(http.MethodGet, "/fast", nil)
	recorder := httptest.NewRecorder()
	composed.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", recorder.Code)
	}
	if recorder.Header().Get("X-Test") != "ok" {
		t.Fatal("buffered response dropped the handler header")
	}
	if strings.TrimSpace(recorder.Body.String()) != `{"ok":true}` {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

func TestLoggingNeverRecordsBodiesOrCredentials(t *testing.T) {
	logger, logs := testLogger()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffer := new(bytes.Buffer)
		_, _ = buffer.ReadFrom(r.Body)
		w.WriteHeader(http.StatusOK)
	})
	composed := Chain(handler, RequestID(), Logging(logger))

	body := `{"email":"owner@example.com","password":"correct-horse-battery-staple"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login?token=secret-query", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-session-token")
	recorder := httptest.NewRecorder()
	composed.ServeHTTP(recorder, request)

	logged := logs.String()
	for _, secret := range []string{"correct-horse-battery-staple", "secret-query", "secret-session-token", "owner@example.com"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("log leaked %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, "/api/v1/auth/login") {
		t.Fatalf("log omitted the request path: %s", logged)
	}
}

func TestChainAppliesFirstMiddlewareOutermost(t *testing.T) {
	var order []string
	middleware := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name+"-before")
				next.ServeHTTP(w, r)
				order = append(order, name+"-after")
			})
		}
	}
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		order = append(order, "handler")
	})

	composed := Chain(handler, middleware("outer"), middleware("inner"))
	composed.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	want := []string{"outer-before", "inner-before", "handler", "inner-after", "outer-after"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}
