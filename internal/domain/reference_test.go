package domain

import "testing"

func TestParseRelatedRefKind(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     RelatedRefKind
		wantCode ErrorCode
	}{
		{name: "original", raw: "ORIGINAL", want: RelatedRefOriginal},
		{name: "reversal", raw: "REVERSAL", want: RelatedRefReversal},
		{name: "refund", raw: "REFUND", want: RelatedRefRefund},
		{name: "replacement", raw: "REPLACEMENT", want: RelatedRefReplacement},
		{name: "plan", raw: "PLAN", want: RelatedRefPlan},
		{name: "lowercase", raw: "original", wantCode: CodeInvalidValue},
		{name: "empty", raw: "", wantCode: CodeInvalidValue},
		{name: "unknown", raw: "PARENT", wantCode: CodeInvalidValue},
		{name: "padded", raw: " ORIGINAL", wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRelatedRefKind(tt.raw)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("ParseRelatedRefKind(%q) error = %v, want code %s", tt.raw, err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRelatedRefKind(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("ParseRelatedRefKind(%q) = %q, want %q", tt.raw, got, tt.want)
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

func TestRelatedRefKindIsValid(t *testing.T) {
	if (RelatedRefKind("")).IsValid() {
		t.Error("zero RelatedRefKind must be invalid")
	}
	if (RelatedRefKind("ORIGINAL_LIKE")).IsValid() {
		t.Error("unknown RelatedRefKind must be invalid")
	}
}

func TestNewRelatedOperationRef(t *testing.T) {
	op := mustJournalOpIDForTest(t, "op-1")

	tests := []struct {
		name     string
		kind     RelatedRefKind
		opID     JournalOperationId
		wantCode ErrorCode
	}{
		{name: "valid original", kind: RelatedRefOriginal, opID: op},
		{name: "valid plan", kind: RelatedRefPlan, opID: op},
		{name: "invalid kind", kind: RelatedRefKind("PARENT"), opID: op, wantCode: CodeInvalidValue},
		{name: "zero kind", kind: "", opID: op, wantCode: CodeInvalidValue},
		{name: "zero operation id", kind: RelatedRefReversal, opID: "", wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewRelatedOperationRef(tt.kind, tt.opID)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewRelatedOperationRef error = %v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewRelatedOperationRef unexpected error: %v", err)
			}
			if got.Kind != tt.kind {
				t.Errorf("Kind = %q, want %q", got.Kind, tt.kind)
			}
			if got.OperationID != tt.opID {
				t.Errorf("OperationID = %q, want %q", got.OperationID, tt.opID)
			}
		})
	}
}
