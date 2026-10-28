package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"family-budget/internal/domain"
)

func TestErrorEnvelopeMatchesContractShape(t *testing.T) {
	err := &apiError{
		Status:  http.StatusBadRequest,
		Code:    string(domain.CodeInvalidValue),
		Message: "The request contains an invalid value.",
		Field:   "email",
		Details: struct {
			Reason string `json:"reason"`
		}{Reason: "bad"},
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", nil)
	request = request.WithContext(WithRequestID(request.Context(), "req-123"))
	recorder := httptest.NewRecorder()

	writeError(recorder, request, err)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}

	want := `{"error":{"code":"INVALID_VALUE","message":"The request contains an invalid value.","field":"email","details":{"reason":"bad"},"request_id":"req-123"}}`
	if got := strings.TrimSpace(recorder.Body.String()); got != want {
		t.Fatalf("envelope mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestErrorEnvelopeUsesNullForAbsentFieldAndDetails(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	request = request.WithContext(WithRequestID(request.Context(), "req-456"))
	recorder := httptest.NewRecorder()

	writeError(recorder, request, domain.ErrInsufficientEnvelopeFunds)

	want := `{"error":{"code":"INSUFFICIENT_ENVELOPE_FUNDS","message":"The envelope has insufficient spendable funds.","field":null,"details":null,"request_id":"req-456"}}`
	if got := strings.TrimSpace(recorder.Body.String()); got != want {
		t.Fatalf("envelope mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestErrorEnvelopeGeneratesRequestIDWhenMissing(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	recorder := httptest.NewRecorder()

	writeError(recorder, request, domain.ErrInvalidAmount)

	var envelope ErrorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Error.RequestID == "" {
		t.Fatal("envelope request_id is empty")
	}
	if got := recorder.Header().Get(HeaderRequestID); got != envelope.Error.RequestID {
		t.Fatalf("response header request id %q does not match envelope %q", got, envelope.Error.RequestID)
	}
}

func TestDomainCodesMapToExpectedStatuses(t *testing.T) {
	tests := []struct {
		code   domain.ErrorCode
		status int
	}{
		{domain.CodeHouseholdAccessDenied, http.StatusForbidden},
		{domain.CodeEntityNotFoundOrNotInHousehold, http.StatusNotFound},
		{domain.CodeInvalidEntityState, http.StatusConflict},
		{domain.CodeInvalidAmount, http.StatusBadRequest},
		{domain.CodeCurrencyMismatch, http.StatusBadRequest},
		{domain.CodeInsufficientReadyToAssign, http.StatusUnprocessableEntity},
		{domain.CodeInsufficientEnvelopeFunds, http.StatusUnprocessableEntity},
		{domain.CodeInsufficientAccountFunds, http.StatusUnprocessableEntity},
		{domain.CodeOverrideReasonRequired, http.StatusBadRequest},
		{domain.CodeReservationMismatch, http.StatusConflict},
		{domain.CodeAmountExceedsAuthorization, http.StatusUnprocessableEntity},
		{domain.CodeRefundExceedsRemainder, http.StatusUnprocessableEntity},
		{domain.CodeReversalExceedsRemainder, http.StatusUnprocessableEntity},
		{domain.CodeIdempotencyKeyPayloadMismatch, http.StatusConflict},
		{domain.CodeConcurrencyConflict, http.StatusConflict},
		{domain.CodeAccountScopeChangeRequiresFundingPlan, http.StatusUnprocessableEntity},
		{domain.CodeAccountingInvariantViolation, http.StatusInternalServerError},
		{domain.CodeMoneyOverflow, http.StatusUnprocessableEntity},
		{domain.CodeAmountOutOfRange, http.StatusUnprocessableEntity},
		{domain.CodeInvalidValue, http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			mapped := toAPIError(domain.NewError(test.code, "raw internal detail"))
			if mapped == nil {
				t.Fatal("toAPIError returned nil")
			}
			if mapped.Status != test.status {
				t.Fatalf("status = %d, want %d", mapped.Status, test.status)
			}
			if mapped.Code != string(test.code) {
				t.Fatalf("code = %q, want %q", mapped.Code, test.code)
			}
			if mapped.Message == "" || strings.Contains(mapped.Message, "raw internal detail") {
				t.Fatalf("message %q leaks the domain error detail", mapped.Message)
			}
		})
	}
}

func TestEveryDomainCodeHasAMapping(t *testing.T) {
	codes := []domain.ErrorCode{
		domain.CodeHouseholdAccessDenied,
		domain.CodeEntityNotFoundOrNotInHousehold,
		domain.CodeInvalidEntityState,
		domain.CodeInvalidAmount,
		domain.CodeCurrencyMismatch,
		domain.CodeInsufficientReadyToAssign,
		domain.CodeInsufficientEnvelopeFunds,
		domain.CodeInsufficientAccountFunds,
		domain.CodeOverrideReasonRequired,
		domain.CodeReservationMismatch,
		domain.CodeAmountExceedsAuthorization,
		domain.CodeRefundExceedsRemainder,
		domain.CodeReversalExceedsRemainder,
		domain.CodeIdempotencyKeyPayloadMismatch,
		domain.CodeConcurrencyConflict,
		domain.CodeAccountScopeChangeRequiresFundingPlan,
		domain.CodeAccountingInvariantViolation,
		domain.CodeMoneyOverflow,
		domain.CodeAmountOutOfRange,
		domain.CodeInvalidValue,
	}
	for _, code := range codes {
		if _, ok := codeMappings[string(code)]; !ok {
			t.Errorf("domain code %s has no HTTP mapping", code)
		}
	}
}

func TestUnmappedCodeCollapsesToInternalError(t *testing.T) {
	mapped := toAPIError(domain.NewError(domain.ErrorCode("SOMETHING_NEW"), "unmapped"))
	if mapped.Status != http.StatusInternalServerError || mapped.Code != CodeInternalError {
		t.Fatalf("mapped = %+v, want generic 500", mapped)
	}
}

func TestRawErrorsDoNotLeak(t *testing.T) {
	raw := errors.New("pq: password authentication failed for user \"budget\" (secret-host)")
	mapped := toAPIError(fmt.Errorf("repository failure: %w", raw))

	if mapped.Status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", mapped.Status)
	}
	if mapped.Code != CodeInternalError {
		t.Fatalf("code = %q, want %s", mapped.Code, CodeInternalError)
	}
	encoded, err := json.Marshal(ErrorEnvelope{Error: ErrorDetail{
		Code:      mapped.Code,
		Message:   mapped.Message,
		Field:     nil,
		Details:   mapped.Details,
		RequestID: "req-789",
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{"pq:", "budget", "secret-host", "repository failure"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("envelope leaked %q: %s", secret, encoded)
		}
	}
}

func TestUnauthorizedSetsBearerChallenge(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	recorder := httptest.NewRecorder()

	writeError(recorder, request, newAPIError(http.StatusUnauthorized, CodeUnauthenticated, "Authentication is required."))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if got := recorder.Header().Get("WWW-Authenticate"); got != `Bearer realm="family-budget"` {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
}

func TestParseAndFormatHelpers(t *testing.T) {
	if !isUUID("00000000-0000-7000-8000-000000000001") {
		t.Fatal("valid UUID rejected")
	}
	if !isUUID("00000000-0000-7000-8000-00000000000A") {
		t.Fatal("upper-case UUID rejected")
	}
	if got, err := parseUUID("00000000-0000-7000-8000-00000000000A"); err != nil || got != "00000000-0000-7000-8000-00000000000a" {
		t.Fatalf("parseUUID = %q, %v", got, err)
	}
	if isUUID("not-a-uuid") {
		t.Fatal("invalid UUID accepted")
	}

	date, err := parseEffectiveDate("2026-10-28")
	if err != nil {
		t.Fatalf("parseEffectiveDate: %v", err)
	}
	if got := formatEffectiveDate(date); got != "2026-10-28" {
		t.Fatalf("formatEffectiveDate = %q", got)
	}
}
