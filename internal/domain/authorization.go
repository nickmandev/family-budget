package domain

import (
	"strings"
	"unicode/utf8"
)

// AuthorizationMode states how a planned purchase was authorized.
type AuthorizationMode string

const (
	// AuthorizationModeNormal means the purchase passed the normal funding and
	// liquidity checks.
	AuthorizationModeNormal AuthorizationMode = "NORMAL"
	// AuthorizationModeEmergencyOverride means an active household member
	// bypassed the envelope funding check with a mandatory reason.
	AuthorizationModeEmergencyOverride AuthorizationMode = "EMERGENCY_OVERRIDE"
)

// ParseAuthorizationMode validates a raw authorization mode.
func ParseAuthorizationMode(value string) (AuthorizationMode, error) {
	mode := AuthorizationMode(value)
	if !mode.IsValid() {
		return "", NewErrorf(CodeInvalidValue, "unknown authorization mode %q", value)
	}
	return mode, nil
}

// IsValid reports whether the mode is one of the defined values.
func (m AuthorizationMode) IsValid() bool {
	switch m {
	case AuthorizationModeNormal, AuthorizationModeEmergencyOverride:
		return true
	default:
		return false
	}
}

// String returns the stored mode text.
func (m AuthorizationMode) String() string {
	return string(m)
}

// maxOverrideReasonLength is the maximum length, in runes, of an override
// reason.
const maxOverrideReasonLength = 500

// OverrideReason is the validated, nonblank justification required for an
// emergency override. It is stored trimmed.
//
// The zero value is invalid. Construct with NewOverrideReason.
type OverrideReason string

// NewOverrideReason trims and validates the reason. A blank or whitespace-only
// reason fails with OVERRIDE_REASON_REQUIRED; an overlong reason fails with
// INVALID_VALUE.
func NewOverrideReason(raw string) (OverrideReason, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", NewError(CodeOverrideReasonRequired, "an emergency override requires a nonblank reason")
	}
	if utf8.RuneCountInString(trimmed) > maxOverrideReasonLength {
		return "", NewErrorf(CodeInvalidValue, "override reason must be at most %d characters", maxOverrideReasonLength)
	}
	return OverrideReason(trimmed), nil
}

// String returns the reason text.
func (r OverrideReason) String() string {
	return string(r)
}

// IsZero reports whether the reason is the invalid zero value.
func (r OverrideReason) IsZero() bool {
	return r == ""
}
