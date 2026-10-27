package domain

import "strings"

const (
	maxIdempotencyKeyLength     = 128
	maxRequestFingerprintLength = 128
)

// IdempotencyKey is the stable, household-scoped key a caller supplies so a
// retried command can be recognized and applied only once.
//
// The zero value is invalid. Construct with NewIdempotencyKey.
type IdempotencyKey string

// NewIdempotencyKey trims and validates a non-empty key of at most 128 bytes.
func NewIdempotencyKey(raw string) (IdempotencyKey, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", NewError(CodeInvalidValue, "idempotency key must not be empty")
	}
	if len(trimmed) > maxIdempotencyKeyLength {
		return "", NewErrorf(CodeInvalidValue, "idempotency key must be at most %d bytes", maxIdempotencyKeyLength)
	}
	return IdempotencyKey(trimmed), nil
}

// String returns the key text.
func (k IdempotencyKey) String() string {
	return string(k)
}

// IsZero reports whether the key is the invalid zero value.
func (k IdempotencyKey) IsZero() bool {
	return k == ""
}

// RequestFingerprint is a canonical hexadecimal hash of a command payload. It
// binds an idempotency key to the exact request so a retry with different
// content is rejected with IDEMPOTENCY_KEY_PAYLOAD_MISMATCH.
//
// The zero value is invalid. Construct with NewRequestFingerprint.
type RequestFingerprint string

// NewRequestFingerprint trims and validates a non-empty hexadecimal string of
// at most 128 characters.
func NewRequestFingerprint(raw string) (RequestFingerprint, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", NewError(CodeInvalidValue, "request fingerprint must not be empty")
	}
	if len(trimmed) > maxRequestFingerprintLength {
		return "", NewErrorf(CodeInvalidValue, "request fingerprint must be at most %d characters", maxRequestFingerprintLength)
	}
	if !isHex(trimmed) {
		return "", NewErrorf(CodeInvalidValue, "request fingerprint must be hexadecimal, got %q", trimmed)
	}
	return RequestFingerprint(trimmed), nil
}

// String returns the fingerprint text.
func (f RequestFingerprint) String() string {
	return string(f)
}

// IsZero reports whether the fingerprint is the invalid zero value.
func (f RequestFingerprint) IsZero() bool {
	return f == ""
}

// isHex reports whether s consists only of ASCII hexadecimal digits.
func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
