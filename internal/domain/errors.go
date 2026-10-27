// Package domain contains the framework-neutral accounting core for the
// household envelope-budgeting model: validated value objects and stable
// domain errors.
//
// The package deliberately depends only on the Go standard library. It must
// not import persistence (GORM, database/sql), transport (net/http), or any
// third-party package. Money is always integer minor units and every
// arithmetic operation is checked.
package domain

import (
	"errors"
	"fmt"
)

// ErrorCode is a stable, machine-readable domain error code. The codes are
// part of the domain contract and may be mapped to user-facing messages by the
// API layer; they must not change casually.
type ErrorCode string

// Domain error codes from DOMAIN_MODEL.md §9, followed by the implementation
// additions this package needs: MONEY_OVERFLOW and AMOUNT_OUT_OF_RANGE for
// checked money arithmetic (IMPLEMENTATION_DECISIONS.md Q8) and INVALID_VALUE
// for malformed value-object input such as currency codes, calendar dates,
// identifiers, enum strings, and idempotency metadata.
const (
	CodeHouseholdAccessDenied                 ErrorCode = "HOUSEHOLD_ACCESS_DENIED"
	CodeEntityNotFoundOrNotInHousehold        ErrorCode = "ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD"
	CodeInvalidEntityState                    ErrorCode = "INVALID_ENTITY_STATE"
	CodeInvalidAmount                         ErrorCode = "INVALID_AMOUNT"
	CodeCurrencyMismatch                      ErrorCode = "CURRENCY_MISMATCH"
	CodeInsufficientReadyToAssign             ErrorCode = "INSUFFICIENT_READY_TO_ASSIGN"
	CodeInsufficientEnvelopeFunds             ErrorCode = "INSUFFICIENT_ENVELOPE_FUNDS"
	CodeInsufficientAccountFunds              ErrorCode = "INSUFFICIENT_ACCOUNT_FUNDS"
	CodeOverrideReasonRequired                ErrorCode = "OVERRIDE_REASON_REQUIRED"
	CodeReservationMismatch                   ErrorCode = "RESERVATION_MISMATCH"
	CodeAmountExceedsAuthorization            ErrorCode = "AMOUNT_EXCEEDS_AUTHORIZATION"
	CodeRefundExceedsRemainder                ErrorCode = "REFUND_EXCEEDS_REMAINDER"
	CodeReversalExceedsRemainder              ErrorCode = "REVERSAL_EXCEEDS_REMAINDER"
	CodeIdempotencyKeyPayloadMismatch         ErrorCode = "IDEMPOTENCY_KEY_PAYLOAD_MISMATCH"
	CodeConcurrencyConflict                   ErrorCode = "CONCURRENCY_CONFLICT"
	CodeAccountScopeChangeRequiresFundingPlan ErrorCode = "ACCOUNT_SCOPE_CHANGE_REQUIRES_FUNDING_PLAN"
	CodeAccountingInvariantViolation          ErrorCode = "ACCOUNTING_INVARIANT_VIOLATION"
	CodeMoneyOverflow                         ErrorCode = "MONEY_OVERFLOW"
	CodeAmountOutOfRange                      ErrorCode = "AMOUNT_OUT_OF_RANGE"
	CodeInvalidValue                          ErrorCode = "INVALID_VALUE"
)

// Error is a domain error carrying a stable ErrorCode, a human-readable
// message, and an optional wrapped cause.
//
// Matching follows the errors.Is / errors.As conventions: two *Error values
// are considered the same domain error when their codes match, regardless of
// message. Use CodeOf or HasCode when a code comparison is more convenient
// than a sentinel.
type Error struct {
	Code    ErrorCode
	Message string
	cause   error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

// Unwrap returns the optional wrapped cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is reports whether target is a domain error with the same code. This lets
// callers write errors.Is(err, ErrInvalidAmount) for any error built from that
// code, including errors created with NewErrorf.
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	return e.Code == other.Code
}

// NewError builds a domain error with the given code and message.
func NewError(code ErrorCode, message string) *Error {
	return &Error{Code: code, Message: message}
}

// NewErrorf builds a domain error with the given code and a formatted message.
func NewErrorf(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WrapError builds a domain error with the given code and message while
// retaining cause for errors.Is/errors.As traversal.
func WrapError(code ErrorCode, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

// CodeOf returns the domain error code carried by err, or the empty code when
// err is nil or does not wrap a *Error.
func CodeOf(err error) ErrorCode {
	var domainErr *Error
	if errors.As(err, &domainErr) {
		return domainErr.Code
	}
	return ""
}

// HasCode reports whether err is or wraps a domain error with the given code.
func HasCode(err error, code ErrorCode) bool {
	return CodeOf(err) == code
}

// Sentinels for the domain error codes. They exist so callers can write
// errors.Is(err, ErrInvalidAmount) without importing the raw code strings.
var (
	ErrHouseholdAccessDenied                 = NewError(CodeHouseholdAccessDenied, "actor may not access this household")
	ErrEntityNotFoundOrNotInHousehold        = NewError(CodeEntityNotFoundOrNotInHousehold, "entity does not exist or is not in this household")
	ErrInvalidEntityState                    = NewError(CodeInvalidEntityState, "entity is not in a state that permits this operation")
	ErrInvalidAmount                         = NewError(CodeInvalidAmount, "amount is invalid")
	ErrCurrencyMismatch                      = NewError(CodeCurrencyMismatch, "currencies do not match")
	ErrInsufficientReadyToAssign             = NewError(CodeInsufficientReadyToAssign, "insufficient ready-to-assign funds")
	ErrInsufficientEnvelopeFunds             = NewError(CodeInsufficientEnvelopeFunds, "insufficient envelope funds")
	ErrInsufficientAccountFunds              = NewError(CodeInsufficientAccountFunds, "insufficient account funds")
	ErrOverrideReasonRequired                = NewError(CodeOverrideReasonRequired, "an emergency override requires a reason")
	ErrReservationMismatch                   = NewError(CodeReservationMismatch, "reservation does not match the operation")
	ErrAmountExceedsAuthorization            = NewError(CodeAmountExceedsAuthorization, "amount exceeds the authorized amount")
	ErrRefundExceedsRemainder                = NewError(CodeRefundExceedsRemainder, "refund exceeds the refundable remainder")
	ErrReversalExceedsRemainder              = NewError(CodeReversalExceedsRemainder, "reversal exceeds the reversible remainder")
	ErrIdempotencyKeyPayloadMismatch         = NewError(CodeIdempotencyKeyPayloadMismatch, "idempotency key was reused with a different payload")
	ErrConcurrencyConflict                   = NewError(CodeConcurrencyConflict, "the household ledger changed concurrently")
	ErrAccountScopeChangeRequiresFundingPlan = NewError(CodeAccountScopeChangeRequiresFundingPlan, "account scope change requires an explicit funding plan")
	ErrAccountingInvariantViolation          = NewError(CodeAccountingInvariantViolation, "accounting invariant violated")
	ErrMoneyOverflow                         = NewError(CodeMoneyOverflow, "integer overflow in money arithmetic")
	ErrAmountOutOfRange                      = NewError(CodeAmountOutOfRange, "amount is outside the supported domain range")
	ErrInvalidValue                          = NewError(CodeInvalidValue, "value is invalid")
)
