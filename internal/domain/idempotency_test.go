package domain

import (
	"strings"
	"testing"
)

func TestIdempotencyKey(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     string
		wantCode ErrorCode
	}{
		{name: "valid", raw: "abc-123", want: "abc-123"},
		{name: "trims", raw: "  abc-123  ", want: "abc-123"},
		{name: "max length", raw: strings.Repeat("a", 128), want: strings.Repeat("a", 128)},
		{name: "empty", raw: "", wantCode: CodeInvalidValue},
		{name: "whitespace", raw: " \t\n", wantCode: CodeInvalidValue},
		{name: "too long", raw: strings.Repeat("a", 129), wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewIdempotencyKey(tt.raw)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewIdempotencyKey(%q) error = %v, want code %s", tt.raw, err, tt.wantCode)
				}
				if !got.IsZero() {
					t.Fatalf("NewIdempotencyKey(%q) returned non-zero key on error: %q", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewIdempotencyKey(%q) unexpected error: %v", tt.raw, err)
			}
			if got.String() != tt.want {
				t.Errorf("NewIdempotencyKey(%q) = %q, want %q", tt.raw, got.String(), tt.want)
			}
		})
	}
}

func TestRequestFingerprint(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     string
		wantCode ErrorCode
	}{
		{name: "lowercase hex", raw: strings.Repeat("ab", 64), want: strings.Repeat("ab", 64)},
		{name: "uppercase hex", raw: "ABCDEF0123456789", want: "ABCDEF0123456789"},
		{name: "trims", raw: "  deadbeef  ", want: "deadbeef"},
		{name: "empty", raw: "", wantCode: CodeInvalidValue},
		{name: "whitespace", raw: "   ", wantCode: CodeInvalidValue},
		{name: "non hex", raw: "xyz", wantCode: CodeInvalidValue},
		{name: "too long", raw: strings.Repeat("a", 129), wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewRequestFingerprint(tt.raw)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewRequestFingerprint(%q) error = %v, want code %s", tt.raw, err, tt.wantCode)
				}
				if !got.IsZero() {
					t.Fatalf("NewRequestFingerprint(%q) returned non-zero fingerprint on error: %q", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewRequestFingerprint(%q) unexpected error: %v", tt.raw, err)
			}
			if got.String() != tt.want {
				t.Errorf("NewRequestFingerprint(%q) = %q, want %q", tt.raw, got.String(), tt.want)
			}
		})
	}
}
