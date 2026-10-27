package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAuthorizationMode(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     AuthorizationMode
		wantCode ErrorCode
	}{
		{name: "normal", raw: "NORMAL", want: AuthorizationModeNormal},
		{name: "emergency override", raw: "EMERGENCY_OVERRIDE", want: AuthorizationModeEmergencyOverride},
		{name: "lowercase", raw: "normal", wantCode: CodeInvalidValue},
		{name: "empty", raw: "", wantCode: CodeInvalidValue},
		{name: "unknown", raw: "OVERRIDE", wantCode: CodeInvalidValue},
		{name: "padded", raw: " NORMAL", wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAuthorizationMode(tt.raw)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("ParseAuthorizationMode(%q) error = %v, want code %s", tt.raw, err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAuthorizationMode(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("ParseAuthorizationMode(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			if got.String() != tt.raw {
				t.Errorf("String() = %q, want %q", got.String(), tt.raw)
			}
			if !got.IsValid() {
				t.Errorf("%q.IsValid() = false, want true", got)
			}
		})
	}
}

func TestAuthorizationModeIsValid(t *testing.T) {
	if (AuthorizationMode("")).IsValid() {
		t.Error("zero AuthorizationMode must be invalid")
	}
	if (AuthorizationMode("EMERGENCY")).IsValid() {
		t.Error("unknown AuthorizationMode must be invalid")
	}
}

func TestOverrideReason(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     string
		wantCode ErrorCode
	}{
		{name: "valid", raw: "needed to cover groceries", want: "needed to cover groceries"},
		{name: "trims", raw: "  needed  ", want: "needed"},
		{name: "max runes", raw: strings.Repeat("é", 500), want: strings.Repeat("é", 500)},
		{name: "blank", raw: "", wantCode: CodeOverrideReasonRequired},
		{name: "whitespace only", raw: " \t\n", wantCode: CodeOverrideReasonRequired},
		{name: "too many runes", raw: strings.Repeat("é", 501), wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewOverrideReason(tt.raw)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewOverrideReason(%q) error = %v, want code %s", tt.raw, err, tt.wantCode)
				}
				if !got.IsZero() {
					t.Fatalf("NewOverrideReason(%q) returned non-zero reason on error: %q", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewOverrideReason(%q) unexpected error: %v", tt.raw, err)
			}
			if got.String() != tt.want {
				t.Errorf("NewOverrideReason(%q) = %q, want %q", tt.raw, got.String(), tt.want)
			}
		})
	}
}

func TestOverrideReasonBlankUsesRequiredCode(t *testing.T) {
	_, err := NewOverrideReason("   ")
	if !errors.Is(err, ErrOverrideReasonRequired) {
		t.Fatalf("errors.Is(err, ErrOverrideReasonRequired) = false for %v", err)
	}
}
