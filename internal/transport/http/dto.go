// Package httpapi is the transport layer: routing, middleware, request and
// response DTOs and the mapping from domain outcomes to the API contract.
//
// It depends on net/http, the standard library only, and the project's
// application/ports packages. Handlers stay thin (STACK_DECISIONS §5): decode
// and validate transport input, authenticate/authorize the actor, call an
// application service, then map the outcome with writeError. Handlers never do
// money arithmetic and never write ledger state directly.
package httpapi

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"family-budget/internal/domain"
)

// ErrorDetail is the stable machine-readable error payload from
// docs/API_CONTRACT.md. Field order in the struct is the JSON order the
// contract documents. Field and Details are null when absent.
type ErrorDetail struct {
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	Field     *string `json:"field"`
	Details   any     `json:"details"`
	RequestID string  `json:"request_id"`
}

// ErrorEnvelope is the only error response shape the API returns.
type ErrorEnvelope struct {
	Error ErrorDetail `json:"error"`
}

// DataResponse wraps a single resource. Feature DTOs embed the concrete type
// under data so success and error envelopes never share a shape.
type DataResponse[T any] struct {
	Data T `json:"data"`
}

// ListResponse is the cursor-pagination envelope for potentially unbounded
// collections (API_CONTRACT "Pagination").
type ListResponse[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// formatTimestamp renders an audit instant as RFC 3339 in UTC. Audit
// timestamps are instants, never local wall-clock times.
func formatTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// parseTimestamp parses an RFC 3339 instant and normalizes it to UTC.
func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

// formatEffectiveDate renders a household-local calendar date as YYYY-MM-DD.
// It never converts an instant, because EffectiveDate is a calendar fact
// (PLATFORM_DECISIONS §C).
func formatEffectiveDate(date domain.EffectiveDate) string {
	return date.String()
}

// parseEffectiveDate parses the explicit date-only form. It is a thin wrapper
// so handlers do not reach into the domain package for format details.
func parseEffectiveDate(value string) (domain.EffectiveDate, error) {
	return domain.ParseEffectiveDate(value)
}

// isUUID reports whether value is a canonical 8-4-4-4-12 hexadecimal UUID.
func isUUID(value string) bool {
	_, err := parseUUID(value)
	return err == nil
}

// parseUUID validates a public resource identifier and returns it in canonical
// lower-case form. Public resource IDs are UUIDs (API_CONTRACT); the domain
// keeps them as string-backed strong IDs and adapters convert at the boundary.
func parseUUID(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) != 36 {
		return "", errInvalidUUID
	}
	for i := 0; i < len(trimmed); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if trimmed[i] != '-' {
				return "", errInvalidUUID
			}
			continue
		}
		if !isHex(trimmed[i]) {
			return "", errInvalidUUID
		}
	}
	return strings.ToLower(trimmed), nil
}

var errInvalidUUID = errors.New("invalid UUID")

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// encodeHex is a small helper for token/correlation IDs built from crypto/rand.
func encodeHex(raw []byte) string {
	return hex.EncodeToString(raw)
}

// decodeJSON decodes exactly one JSON value into dst and translates body errors
// to API errors. The body-size limit is installed by MaxBodyBytes; a limit
// breach surfacing during Decode is reported as PAYLOAD_TOO_LARGE rather than
// a generic parse error.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body is required.")
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errPayloadTooLarge
		}
		if errors.Is(err, io.EOF) {
			return newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body is required.")
		}
		return newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body is not valid JSON.")
	}
	// Reject a second JSON value or trailing non-whitespace.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body must contain a single JSON value.")
	}
	return nil
}
