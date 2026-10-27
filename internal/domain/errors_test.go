package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorCodesAreCompleteAndDistinct(t *testing.T) {
	tests := []struct {
		name     string
		code     ErrorCode
		sentinel *Error
		text     string
	}{
		{name: "household access denied", code: CodeHouseholdAccessDenied, sentinel: ErrHouseholdAccessDenied, text: "HOUSEHOLD_ACCESS_DENIED"},
		{name: "entity not found", code: CodeEntityNotFoundOrNotInHousehold, sentinel: ErrEntityNotFoundOrNotInHousehold, text: "ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD"},
		{name: "invalid entity state", code: CodeInvalidEntityState, sentinel: ErrInvalidEntityState, text: "INVALID_ENTITY_STATE"},
		{name: "invalid amount", code: CodeInvalidAmount, sentinel: ErrInvalidAmount, text: "INVALID_AMOUNT"},
		{name: "currency mismatch", code: CodeCurrencyMismatch, sentinel: ErrCurrencyMismatch, text: "CURRENCY_MISMATCH"},
		{name: "insufficient ready to assign", code: CodeInsufficientReadyToAssign, sentinel: ErrInsufficientReadyToAssign, text: "INSUFFICIENT_READY_TO_ASSIGN"},
		{name: "insufficient envelope funds", code: CodeInsufficientEnvelopeFunds, sentinel: ErrInsufficientEnvelopeFunds, text: "INSUFFICIENT_ENVELOPE_FUNDS"},
		{name: "insufficient account funds", code: CodeInsufficientAccountFunds, sentinel: ErrInsufficientAccountFunds, text: "INSUFFICIENT_ACCOUNT_FUNDS"},
		{name: "override reason required", code: CodeOverrideReasonRequired, sentinel: ErrOverrideReasonRequired, text: "OVERRIDE_REASON_REQUIRED"},
		{name: "reservation mismatch", code: CodeReservationMismatch, sentinel: ErrReservationMismatch, text: "RESERVATION_MISMATCH"},
		{name: "amount exceeds authorization", code: CodeAmountExceedsAuthorization, sentinel: ErrAmountExceedsAuthorization, text: "AMOUNT_EXCEEDS_AUTHORIZATION"},
		{name: "refund exceeds remainder", code: CodeRefundExceedsRemainder, sentinel: ErrRefundExceedsRemainder, text: "REFUND_EXCEEDS_REMAINDER"},
		{name: "reversal exceeds remainder", code: CodeReversalExceedsRemainder, sentinel: ErrReversalExceedsRemainder, text: "REVERSAL_EXCEEDS_REMAINDER"},
		{name: "idempotency payload mismatch", code: CodeIdempotencyKeyPayloadMismatch, sentinel: ErrIdempotencyKeyPayloadMismatch, text: "IDEMPOTENCY_KEY_PAYLOAD_MISMATCH"},
		{name: "concurrency conflict", code: CodeConcurrencyConflict, sentinel: ErrConcurrencyConflict, text: "CONCURRENCY_CONFLICT"},
		{name: "account scope change requires funding plan", code: CodeAccountScopeChangeRequiresFundingPlan, sentinel: ErrAccountScopeChangeRequiresFundingPlan, text: "ACCOUNT_SCOPE_CHANGE_REQUIRES_FUNDING_PLAN"},
		{name: "accounting invariant violation", code: CodeAccountingInvariantViolation, sentinel: ErrAccountingInvariantViolation, text: "ACCOUNTING_INVARIANT_VIOLATION"},
		{name: "money overflow", code: CodeMoneyOverflow, sentinel: ErrMoneyOverflow, text: "MONEY_OVERFLOW"},
		{name: "amount out of range", code: CodeAmountOutOfRange, sentinel: ErrAmountOutOfRange, text: "AMOUNT_OUT_OF_RANGE"},
		{name: "invalid value", code: CodeInvalidValue, sentinel: ErrInvalidValue, text: "INVALID_VALUE"},
	}

	// 17 required codes from DOMAIN_MODEL.md §9 plus three documented
	// implementation additions (MONEY_OVERFLOW, AMOUNT_OUT_OF_RANGE and
	// INVALID_VALUE for malformed value objects).
	if len(tests) != 20 {
		t.Fatalf("got %d codes, want 20", len(tests))
	}

	seen := make(map[ErrorCode]struct{}, len(tests))
	for _, tt := range tests {
		if _, duplicate := seen[tt.code]; duplicate {
			t.Fatalf("duplicate error code %s", tt.code)
		}
		seen[tt.code] = struct{}{}

		t.Run(tt.name, func(t *testing.T) {
			if string(tt.code) != tt.text {
				t.Errorf("code = %q, want %q", tt.code, tt.text)
			}
			if tt.sentinel == nil {
				t.Fatal("sentinel is nil")
			}
			if tt.sentinel.Code != tt.code {
				t.Errorf("sentinel code = %q, want %q", tt.sentinel.Code, tt.code)
			}
			if tt.sentinel.Message == "" {
				t.Error("sentinel message must not be empty")
			}
			if got := CodeOf(tt.sentinel); got != tt.code {
				t.Errorf("CodeOf(sentinel) = %q, want %q", got, tt.code)
			}
			if got := tt.sentinel.Error(); !strings.HasPrefix(got, tt.text) {
				t.Errorf("sentinel.Error() = %q, want prefix %q", got, tt.text)
			}
		})
	}
}

func TestErrorsIsMatchesByCode(t *testing.T) {
	custom := NewErrorf(CodeInvalidAmount, "zero is not positive")

	if !errors.Is(custom, ErrInvalidAmount) {
		t.Error("errors.Is(custom, ErrInvalidAmount) = false, want true")
	}
	if errors.Is(custom, ErrCurrencyMismatch) {
		t.Error("errors.Is(custom, ErrCurrencyMismatch) = true, want false")
	}
	if !HasCode(custom, CodeInvalidAmount) {
		t.Error("HasCode(custom, CodeInvalidAmount) = false, want true")
	}
	if HasCode(custom, CodeCurrencyMismatch) {
		t.Error("HasCode(custom, CodeCurrencyMismatch) = true, want false")
	}

	wrapped := fmt.Errorf("command rejected: %w", custom)
	if !errors.Is(wrapped, ErrInvalidAmount) {
		t.Error("errors.Is(wrapped, ErrInvalidAmount) = false, want true")
	}
	if CodeOf(wrapped) != CodeInvalidAmount {
		t.Errorf("CodeOf(wrapped) = %q, want %q", CodeOf(wrapped), CodeInvalidAmount)
	}
	if !HasCode(wrapped, CodeInvalidAmount) {
		t.Error("HasCode(wrapped, CodeInvalidAmount) = false, want true")
	}

	if errors.Is(nil, ErrInvalidAmount) {
		t.Error("errors.Is(nil, ErrInvalidAmount) = true, want false")
	}
	if CodeOf(nil) != "" {
		t.Errorf("CodeOf(nil) = %q, want empty", CodeOf(nil))
	}
	if HasCode(nil, CodeInvalidAmount) {
		t.Error("HasCode(nil, CodeInvalidAmount) = true, want false")
	}
	if CodeOf(errors.New("plain error")) != "" {
		t.Errorf("CodeOf(plain error) = %q, want empty", CodeOf(errors.New("plain error")))
	}
	if CodeOf(ErrInvalidAmount) != CodeInvalidAmount {
		t.Errorf("CodeOf(ErrInvalidAmount) = %q, want %q", CodeOf(ErrInvalidAmount), CodeInvalidAmount)
	}
}

func TestErrorAsAndCause(t *testing.T) {
	cause := errors.New("database unavailable")
	err := WrapError(CodeConcurrencyConflict, "household version changed", cause)

	var domainErr *Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("errors.As failed for %v", err)
	}
	if domainErr.Code != CodeConcurrencyConflict {
		t.Errorf("Code = %q, want %q", domainErr.Code, CodeConcurrencyConflict)
	}
	if domainErr.Message != "household version changed" {
		t.Errorf("Message = %q, want %q", domainErr.Message, "household version changed")
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is(err, cause) = false, want true")
	}
	if got := err.Error(); got != "CONCURRENCY_CONFLICT: household version changed" {
		t.Errorf("Error() = %q, want %q", got, "CONCURRENCY_CONFLICT: household version changed")
	}
	if NewError(CodeInvalidAmount, "x").Is(errors.New("x")) {
		t.Error("domain error must not match a non-domain error")
	}
}

func TestErrorFormatting(t *testing.T) {
	if got := NewError(CodeInvalidAmount, "").Error(); got != "INVALID_AMOUNT" {
		t.Errorf("Error() = %q, want %q", got, "INVALID_AMOUNT")
	}
	if got := NewError(CodeInvalidAmount, "bad amount").Error(); got != "INVALID_AMOUNT: bad amount" {
		t.Errorf("Error() = %q, want %q", got, "INVALID_AMOUNT: bad amount")
	}
	if got := NewErrorf(CodeAmountOutOfRange, "amount %d is too large", 42).Error(); got != "AMOUNT_OUT_OF_RANGE: amount 42 is too large" {
		t.Errorf("Error() = %q, want %q", got, "AMOUNT_OUT_OF_RANGE: amount 42 is too large")
	}

	var nilErr *Error
	if got := nilErr.Error(); got != "<nil>" {
		t.Errorf("nil Error() = %q, want %q", got, "<nil>")
	}
	if nilErr.Is(ErrInvalidAmount) {
		t.Error("nil Error.Is = true, want false")
	}
	if nilErr.Unwrap() != nil {
		t.Errorf("nil Error.Unwrap() = %v, want nil", nilErr.Unwrap())
	}
}
