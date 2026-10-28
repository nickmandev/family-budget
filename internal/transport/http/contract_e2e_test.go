package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestContractEndToEndStatuses exercises representative flows through the full
// middleware and handler stack. These are contract tests, not unit tests: each
// case asserts the externally visible status, error code and envelope.
func TestContractEndToEndStatuses(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		server, _ := newAuthServer(t, 1000, time.Minute)
		response := performJSON(server.Handler(), http.MethodGet, "/api/v1/auth/me", "", "")
		assertErrorResponse(t, response, http.StatusUnauthorized, "UNAUTHENTICATED")
	})

	t.Run("cross_household_access_is_not_found", func(t *testing.T) {
		harness := newResourceServer(t)
		household := harness.createHousehold(t)

		if _, err := harness.auth.Register(context.Background(), "stranger@example.com", "correct-horse"); err != nil {
			t.Fatalf("Register stranger: %v", err)
		}
		login, err := harness.auth.Login(context.Background(), "stranger@example.com", "correct-horse")
		if err != nil {
			t.Fatalf("Login stranger: %v", err)
		}

		ownerToken := harness.token
		harness.token = login.Token
		response := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID, "", nil)
		harness.token = ownerToken

		assertErrorResponse(t, response, http.StatusNotFound, "ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD")
	})

	t.Run("idempotency_key_payload_mismatch", func(t *testing.T) {
		harness, household, account, _ := setupBudget(t)
		response := harness.do(t, http.MethodPost,
			"/api/v1/households/"+household.ID+"/accounts/"+account.ID+"/opening-balance",
			`{"effective_date":"2026-10-01","delta_minor":99999}`,
			idempotencyHeaders("opening"))
		assertErrorResponse(t, response, http.StatusConflict, "IDEMPOTENCY_KEY_PAYLOAD_MISMATCH")
	})

	t.Run("insufficient_envelope_funds", func(t *testing.T) {
		harness, household, _, funded := setupBudget(t)
		unfunded := harness.createEnvelope(t, household.ID, "Unfunded")
		response := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/reallocations",
			`{"effective_date":"2026-10-05","source_envelope_id":"`+funded.ID+`","destination_envelope_id":"`+unfunded.ID+`","amount_minor":40000}`,
			idempotencyHeaders("overspend"))
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "INSUFFICIENT_ENVELOPE_FUNDS")
	})

	t.Run("rate_limited", func(t *testing.T) {
		server, _ := newAuthServer(t, 1, time.Hour)
		body := `{"email":"nobody@example.com","password":"wrong-password"}`

		first := performJSON(server.Handler(), http.MethodPost, "/api/v1/auth/login", body, "")
		if first.Code != http.StatusUnauthorized {
			t.Fatalf("first login status = %d, want 401", first.Code)
		}
		second := performJSON(server.Handler(), http.MethodPost, "/api/v1/auth/login", body, "")
		assertErrorResponse(t, second, http.StatusTooManyRequests, "RATE_LIMITED")
		if second.Header().Get("Retry-After") == "" {
			t.Error("429 response is missing Retry-After")
		}
	})
}

func assertErrorResponse(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()

	if response.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", response.Code, status, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, response.Body.String())
	}
	if got := sortedMapKeys(top); strings.Join(got, ",") != "error" {
		t.Fatalf("envelope fields = %v, want [error]", got)
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(top["error"], &inner); err != nil {
		t.Fatalf("decode error detail: %v", err)
	}
	if got := sortedMapKeys(inner); strings.Join(got, ",") != "code,details,field,message,request_id" {
		t.Fatalf("error detail fields = %v, want code,details,field,message,request_id", got)
	}

	var envelope ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode typed envelope: %v", err)
	}
	if envelope.Error.Code != code {
		t.Errorf("code = %q, want %q", envelope.Error.Code, code)
	}
	if envelope.Error.Message == "" {
		t.Error("message is empty")
	}
	if envelope.Error.RequestID == "" {
		t.Error("request_id is empty")
	}
	if got := response.Header().Get("X-Request-ID"); got != envelope.Error.RequestID {
		t.Errorf("X-Request-ID header = %q, want envelope request_id %q", got, envelope.Error.RequestID)
	}
}
