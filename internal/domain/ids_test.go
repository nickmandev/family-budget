package domain

import (
	"reflect"
	"strings"
	"testing"
)

// idConstructor adapts each strong identifier constructor to a common shape.
type idConstructor struct {
	name string
	new  func(string) (string, error)
}

func identifierConstructors() []idConstructor {
	return []idConstructor{
		{name: "HouseholdId", new: func(raw string) (string, error) {
			id, err := NewHouseholdId(raw)
			return id.String(), err
		}},
		{name: "UserId", new: func(raw string) (string, error) {
			id, err := NewUserId(raw)
			return id.String(), err
		}},
		{name: "AccountId", new: func(raw string) (string, error) {
			id, err := NewAccountId(raw)
			return id.String(), err
		}},
		{name: "EnvelopeId", new: func(raw string) (string, error) {
			id, err := NewEnvelopeId(raw)
			return id.String(), err
		}},
		{name: "PlanId", new: func(raw string) (string, error) {
			id, err := NewPlanId(raw)
			return id.String(), err
		}},
		{name: "SuggestionId", new: func(raw string) (string, error) {
			id, err := NewSuggestionId(raw)
			return id.String(), err
		}},
		{name: "JournalOperationId", new: func(raw string) (string, error) {
			id, err := NewJournalOperationId(raw)
			return id.String(), err
		}},
		{name: "ReservationId", new: func(raw string) (string, error) {
			id, err := NewReservationId(raw)
			return id.String(), err
		}},
	}
}

func TestStrongIdentifiers(t *testing.T) {
	for _, constructor := range identifierConstructors() {
		t.Run(constructor.name, func(t *testing.T) {
			tests := []struct {
				name     string
				raw      string
				want     string
				wantCode ErrorCode
			}{
				{name: "accepts value", raw: "abc-123", want: "abc-123"},
				{name: "trims surrounding space", raw: "  abc-123  ", want: "abc-123"},
				{name: "accepts max length", raw: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
				{name: "rejects empty", raw: "", wantCode: CodeInvalidValue},
				{name: "rejects whitespace", raw: " \t\n", wantCode: CodeInvalidValue},
				{name: "rejects too long", raw: strings.Repeat("a", 65), wantCode: CodeInvalidValue},
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					got, err := constructor.new(tt.raw)
					if tt.wantCode != "" {
						if !HasCode(err, tt.wantCode) {
							t.Fatalf("%s(%q) error = %v, want code %s", constructor.name, tt.raw, err, tt.wantCode)
						}
						if got != "" {
							t.Fatalf("%s(%q) returned %q on error, want zero value", constructor.name, tt.raw, got)
						}
						return
					}
					if err != nil {
						t.Fatalf("%s(%q) unexpected error: %v", constructor.name, tt.raw, err)
					}
					if got != tt.want {
						t.Errorf("%s(%q) = %q, want %q", constructor.name, tt.raw, got, tt.want)
					}
				})
			}
		})
	}
}

func TestStrongIdentifierZeroValues(t *testing.T) {
	zeroChecks := map[string]func() bool{
		"HouseholdId":        func() bool { return HouseholdId("").IsZero() },
		"UserId":             func() bool { return UserId("").IsZero() },
		"AccountId":          func() bool { return AccountId("").IsZero() },
		"EnvelopeId":         func() bool { return EnvelopeId("").IsZero() },
		"PlanId":             func() bool { return PlanId("").IsZero() },
		"SuggestionId":       func() bool { return SuggestionId("").IsZero() },
		"JournalOperationId": func() bool { return JournalOperationId("").IsZero() },
		"ReservationId":      func() bool { return ReservationId("").IsZero() },
	}
	for name, check := range zeroChecks {
		if !check() {
			t.Errorf("%s zero value must report IsZero", name)
		}
	}
	if HouseholdId("household-1").IsZero() {
		t.Error("constructed HouseholdId must not report IsZero")
	}
	if AccountId("account-1").IsZero() {
		t.Error("constructed AccountId must not report IsZero")
	}
}

func TestStrongIdentifierTypesAreDistinct(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(HouseholdId("")),
		reflect.TypeOf(UserId("")),
		reflect.TypeOf(AccountId("")),
		reflect.TypeOf(EnvelopeId("")),
		reflect.TypeOf(PlanId("")),
		reflect.TypeOf(SuggestionId("")),
		reflect.TypeOf(JournalOperationId("")),
		reflect.TypeOf(ReservationId("")),
	}

	seen := make(map[reflect.Type]struct{}, len(types))
	for _, typ := range types {
		if _, duplicate := seen[typ]; duplicate {
			t.Fatalf("identifier type %s is declared more than once", typ)
		}
		seen[typ] = struct{}{}
	}
	if len(seen) != len(types) {
		t.Fatalf("got %d distinct identifier types, want %d", len(seen), len(types))
	}
}
