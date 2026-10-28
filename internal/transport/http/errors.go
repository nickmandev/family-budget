package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"family-budget/internal/domain"
)

// Transport-level error codes. Domain codes live in internal/domain and are
// never redefined here; these cover only transport and authentication
// concerns that have no domain equivalent. Both kinds share one translation
// table so an HTTP status is chosen in exactly one place.
const (
	CodeBadRequest             = "BAD_REQUEST"
	CodeUnauthenticated        = "UNAUTHENTICATED"
	CodeInvalidCredentials     = "INVALID_CREDENTIALS"
	CodeEmailAlreadyRegistered = "EMAIL_ALREADY_REGISTERED"
	CodeRateLimited            = "RATE_LIMITED"
	CodePayloadTooLarge        = "PAYLOAD_TOO_LARGE"
	CodeRequestTimeout         = "REQUEST_TIMEOUT"
	CodeServiceUnavailable     = "SERVICE_UNAVAILABLE"
	CodeNotFound               = "NOT_FOUND"
	CodeMethodNotAllowed       = "METHOD_NOT_ALLOWED"
	CodeInternalError          = "INTERNAL_ERROR"
)

// apiError is a transport error with a stable code, an HTTP status and a safe
// user-facing message. It intentionally carries no wrapped cause: raw database
// errors, stack traces and secrets must never reach the client.
type apiError struct {
	Status  int
	Code    string
	Message string
	Field   string
	Details any
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

// ErrorCode reports the stable machine-readable code.
func (e *apiError) ErrorCode() string { return e.Code }

// ErrorField reports the offending request field, when there is one.
func (e *apiError) ErrorField() string { return e.Field }

// ErrorDetails reports optional non-sensitive context.
func (e *apiError) ErrorDetails() any { return e.Details }

func newAPIError(status int, code, message string) *apiError {
	return &apiError{Status: status, Code: code, Message: message}
}

func newFieldAPIError(status int, code, message, field string) *apiError {
	return &apiError{Status: status, Code: code, Message: message, Field: field}
}

var (
	errBadRequest         = newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body is invalid.")
	errPayloadTooLarge    = newAPIError(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "The request body is too large.")
	errRequestTimeout     = newAPIError(http.StatusGatewayTimeout, CodeRequestTimeout, "The request timed out.")
	errServiceUnavailable = newAPIError(http.StatusServiceUnavailable, CodeServiceUnavailable, "The service is not ready.")
	errNotFound           = newAPIError(http.StatusNotFound, CodeNotFound, "The requested resource was not found.")
	errRateLimited        = newAPIError(http.StatusTooManyRequests, CodeRateLimited, "Too many attempts; try again later.")
	errInternal           = newAPIError(http.StatusInternalServerError, CodeInternalError, "An internal error occurred.")
)

// codeMapping is the one place a stable code becomes an HTTP status and a safe
// user-facing message. Codes remain independent of statuses: changing a status
// never changes the code, and the same code always maps to the same status.
type codeMapping struct {
	status  int
	message string
}

// codeMappings is the single domain-code/auth-code -> HTTP translation table.
//
// Rationale for the statuses:
//   - INVALID_* / malformed input -> 400 (the request cannot be understood);
//   - access and existence are deliberately distinct: HOUSEHOLD_ACCESS_DENIED
//     is 403 for an authenticated actor, while ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD
//     is 404 for both "absent" and "another household" so cross-household
//     existence cannot leak;
//   - insufficient funds and amount-limit breaches -> 422 (well-formed but
//     semantically unprocessable), keeping them distinct from a state conflict;
//   - state/idempotency/concurrency conflicts -> 409;
//   - ACCOUNTING_INVARIANT_VIOLATION -> 500 with a generic message, because it
//     indicates a server defect or corrupted projection, not client input.
var codeMappings = map[string]codeMapping{
	string(domain.CodeHouseholdAccessDenied):                 {http.StatusForbidden, "You do not have access to this household."},
	string(domain.CodeEntityNotFoundOrNotInHousehold):        {http.StatusNotFound, "The requested resource was not found."},
	string(domain.CodeInvalidEntityState):                    {http.StatusConflict, "The resource is not in a state that permits this operation."},
	string(domain.CodeInvalidAmount):                         {http.StatusBadRequest, "The amount is invalid."},
	string(domain.CodeCurrencyMismatch):                      {http.StatusBadRequest, "The currency does not match the household currency."},
	string(domain.CodeInsufficientReadyToAssign):             {http.StatusUnprocessableEntity, "There is not enough ready-to-assign money."},
	string(domain.CodeInsufficientEnvelopeFunds):             {http.StatusUnprocessableEntity, "The envelope has insufficient spendable funds."},
	string(domain.CodeInsufficientAccountFunds):              {http.StatusUnprocessableEntity, "The account has insufficient funds."},
	string(domain.CodeOverrideReasonRequired):                {http.StatusBadRequest, "An emergency override requires a reason."},
	string(domain.CodeReservationMismatch):                   {http.StatusConflict, "The reservation does not match the operation."},
	string(domain.CodeAmountExceedsAuthorization):            {http.StatusUnprocessableEntity, "The amount exceeds the authorized amount."},
	string(domain.CodeRefundExceedsRemainder):                {http.StatusUnprocessableEntity, "The refund exceeds the refundable remainder."},
	string(domain.CodeReversalExceedsRemainder):              {http.StatusUnprocessableEntity, "The reversal exceeds the reversible remainder."},
	string(domain.CodeIdempotencyKeyPayloadMismatch):         {http.StatusConflict, "The idempotency key was reused with a different request."},
	string(domain.CodeConcurrencyConflict):                   {http.StatusConflict, "The household budget changed concurrently; retry the request."},
	string(domain.CodeAccountScopeChangeRequiresFundingPlan): {http.StatusUnprocessableEntity, "Changing account scope requires an explicit funding plan."},
	string(domain.CodeAccountingInvariantViolation):          {http.StatusInternalServerError, "The operation could not be completed."},
	string(domain.CodeMoneyOverflow):                         {http.StatusUnprocessableEntity, "The amount is outside the supported range."},
	string(domain.CodeAmountOutOfRange):                      {http.StatusUnprocessableEntity, "The amount is outside the supported range."},
	string(domain.CodeInvalidValue):                          {http.StatusBadRequest, "The request contains an invalid value."},

	CodeBadRequest:             {http.StatusBadRequest, "The request body is invalid."},
	CodeUnauthenticated:        {http.StatusUnauthorized, "Authentication is required."},
	CodeInvalidCredentials:     {http.StatusUnauthorized, "Invalid credentials."},
	CodeEmailAlreadyRegistered: {http.StatusConflict, "An account with this email already exists."},
	CodeRateLimited:            {http.StatusTooManyRequests, "Too many attempts; try again later."},
	CodePayloadTooLarge:        {http.StatusRequestEntityTooLarge, "The request body is too large."},
	CodeRequestTimeout:         {http.StatusGatewayTimeout, "The request timed out."},
	CodeServiceUnavailable:     {http.StatusServiceUnavailable, "The service is not ready."},
	CodeNotFound:               {http.StatusNotFound, "The requested resource was not found."},
	CodeMethodNotAllowed:       {http.StatusMethodNotAllowed, "The method is not allowed."},
	CodeInternalError:          {http.StatusInternalServerError, "An internal error occurred."},
}

// toAPIError maps any error to the transport error shape. Domain errors and
// application errors that expose ErrorCode() are translated through the single
// table above. Everything else becomes a generic 500 so driver messages,
// wrapped causes and implementation details cannot leak.
func toAPIError(err error) *apiError {
	if err == nil {
		return nil
	}

	var direct *apiError
	if errors.As(err, &direct) {
		return direct
	}

	type codedError interface {
		ErrorCode() string
	}
	var coded codedError
	if errors.As(err, &coded) {
		return apiErrorFromCode(coded.ErrorCode(), err)
	}

	if code := domain.CodeOf(err); code != "" {
		return apiErrorFromCode(string(code), err)
	}

	return errInternal
}

func apiErrorFromCode(code string, err error) *apiError {
	mapping, ok := codeMappings[code]
	if !ok {
		// An unmapped code is a programming error. Returning the code would
		// expose internal vocabulary, so collapse to the generic 500.
		return errInternal
	}
	mapped := &apiError{Status: mapping.status, Code: code, Message: mapping.message}
	var fielder interface{ ErrorField() string }
	if errors.As(err, &fielder) {
		mapped.Field = fielder.ErrorField()
	}
	var detailer interface{ ErrorDetails() any }
	if errors.As(err, &detailer) {
		mapped.Details = detailer.ErrorDetails()
	}
	return mapped
}

// writeError renders the API_CONTRACT error envelope. It never includes the
// raw error string: only the table's stable code and safe message, an optional
// field/details supplied by the application, and the request id.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	mapped := toAPIError(err)
	if mapped == nil {
		return
	}

	requestID := RequestIDFromContext(r.Context())
	if requestID == "" {
		requestID = newRequestID()
		w.Header().Set(HeaderRequestID, requestID)
	}

	if mapped.Status == http.StatusUnauthorized {
		// Documented 401 semantics (RFC 6750): the client should present a
		// bearer token. The challenge never names a user or session.
		w.Header().Set("WWW-Authenticate", `Bearer realm="family-budget"`)
	}

	var field *string
	if mapped.Field != "" {
		value := mapped.Field
		field = &value
	}

	envelope := ErrorEnvelope{Error: ErrorDetail{
		Code:      mapped.Code,
		Message:   mapped.Message,
		Field:     field,
		Details:   mapped.Details,
		RequestID: requestID,
	}}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(mapped.Status)
	_ = json.NewEncoder(w).Encode(envelope)
}

// writeJSON writes a success response.
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
