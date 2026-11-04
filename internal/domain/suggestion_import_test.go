package domain

import (
	"strings"
	"testing"
	"time"
)

func validImportProvenanceForTest() ImportProvenance {
	now := time.Now().UTC()
	return ImportProvenance{
		AccountID:      AccountId("account-1"),
		EffectiveDate:  time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC),
		Direction:      SuggestionDirectionDebit,
		Reference:      "reference-1",
		RawDescription: "CARD PURCHASE",
		BatchID:        "batch-1",
		Fingerprint:    "fingerprint-1",
	}
}

func TestSuggestionDirection(t *testing.T) {
	for _, direction := range []SuggestionDirection{SuggestionDirectionDebit, SuggestionDirectionCredit} {
		if !direction.IsValid() {
			t.Errorf("%q should be valid", direction)
		}
		parsed, err := ParseSuggestionDirection(string(direction))
		if err != nil || parsed != direction {
			t.Errorf("ParseSuggestionDirection(%q) = %q, %v", direction, parsed, err)
		}
	}
	if _, err := ParseSuggestionDirection("BOGUS"); !HasCode(err, CodeInvalidValue) {
		t.Fatalf("unknown direction error = %v, want INVALID_VALUE", err)
	}
}

func TestNewImportedSuggestionValidation(t *testing.T) {
	currency := mustCurrency(t, "USD", 2)
	validID := SuggestionId("suggestion-1")
	validAmount := mustPositiveAmountForTest(t, 1_200, currency)
	validSource := TransactionSourceExternalEntry
	today := time.Now().UTC()
	tooFarFuture := time.Date(today.Year(), today.Month(), today.Day()+3, 0, 0, 0, 0, time.UTC)
	tooOld := time.Date(1999, 12, 31, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		id         SuggestionId
		source     TransactionSource
		amount     PositiveAmount
		provenance ImportProvenance
	}{
		{name: "valid", id: validID, source: validSource, amount: validAmount, provenance: validImportProvenanceForTest()},
		{name: "zero suggestion id", source: validSource, amount: validAmount, provenance: validImportProvenanceForTest()},
		{name: "invalid source", id: validID, source: TransactionSource("BOGUS"), amount: validAmount, provenance: validImportProvenanceForTest()},
		{name: "zero amount", id: validID, source: validSource, provenance: validImportProvenanceForTest()},
		{name: "zero account", id: validID, source: validSource, amount: validAmount, provenance: func() ImportProvenance { p := validImportProvenanceForTest(); p.AccountID = ""; return p }()},
		{name: "zero date", id: validID, source: validSource, amount: validAmount, provenance: func() ImportProvenance { p := validImportProvenanceForTest(); p.EffectiveDate = time.Time{}; return p }()},
		{name: "date before 2000", id: validID, source: validSource, amount: validAmount, provenance: func() ImportProvenance { p := validImportProvenanceForTest(); p.EffectiveDate = tooOld; return p }()},
		{name: "date too far in future", id: validID, source: validSource, amount: validAmount, provenance: func() ImportProvenance { p := validImportProvenanceForTest(); p.EffectiveDate = tooFarFuture; return p }()},
		{name: "invalid direction", id: validID, source: validSource, amount: validAmount, provenance: func() ImportProvenance { p := validImportProvenanceForTest(); p.Direction = "UNKNOWN"; return p }()},
		{name: "empty fingerprint", id: validID, source: validSource, amount: validAmount, provenance: func() ImportProvenance { p := validImportProvenanceForTest(); p.Fingerprint = ""; return p }()},
		{name: "long fingerprint", id: validID, source: validSource, amount: validAmount, provenance: func() ImportProvenance {
			p := validImportProvenanceForTest()
			p.Fingerprint = strings.Repeat("x", 257)
			return p
		}()},
		{name: "empty batch id", id: validID, source: validSource, amount: validAmount, provenance: func() ImportProvenance { p := validImportProvenanceForTest(); p.BatchID = ""; return p }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewImportedSuggestion(tt.id, tt.source, tt.amount, "Merchant", tt.provenance)
			if tt.name != "valid" {
				if !HasCode(err, CodeInvalidValue) {
					t.Fatalf("NewImportedSuggestion error = %v, want INVALID_VALUE", err)
				}
				if got != nil {
					t.Fatalf("constructor returned suggestion on invalid input: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewImportedSuggestion: %v", err)
			}
			provenance, ok := got.Provenance()
			if !ok || provenance.EffectiveDate.Location() != time.UTC || provenance.EffectiveDate.Hour() != 0 {
				t.Fatalf("provenance date was not normalized to UTC midnight: %+v, present=%v", provenance, ok)
			}
		})
	}
}

func TestImportedSuggestionProvenanceSurvivesLifecycle(t *testing.T) {
	currency := mustCurrency(t, "USD", 2)
	original := validImportProvenanceForTest()

	newSuggestion := func(t *testing.T) *Suggestion {
		t.Helper()
		suggestion, err := NewImportedSuggestion(
			SuggestionId("imported-"+t.Name()), TransactionSourceExternalEntry,
			mustPositiveAmountForTest(t, 1_200, currency), "Merchant", original,
		)
		if err != nil {
			t.Fatalf("NewImportedSuggestion: %v", err)
		}
		return suggestion
	}
	assertProvenance := func(t *testing.T, suggestion *Suggestion) {
		t.Helper()
		got, ok := suggestion.Provenance()
		if !ok || got != original {
			t.Fatalf("Provenance() = %+v, %v; want %+v, true", got, ok, original)
		}
	}

	t.Run("edit", func(t *testing.T) {
		suggestion := newSuggestion(t)
		if err := suggestion.Edit(mustPositiveAmountForTest(t, 1_500, currency), "Edited merchant"); err != nil {
			t.Fatalf("Edit: %v", err)
		}
		assertProvenance(t, suggestion)
	})
	t.Run("confirm", func(t *testing.T) {
		suggestion := newSuggestion(t)
		if err := suggestion.Confirm(); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
		assertProvenance(t, suggestion)
	})
	t.Run("cancel", func(t *testing.T) {
		suggestion := newSuggestion(t)
		if err := suggestion.Cancel("dismissed"); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		assertProvenance(t, suggestion)
	})
}

func TestManualSuggestionHasNoImportProvenance(t *testing.T) {
	suggestion, _ := newSuggestionFixture(t)
	if provenance, ok := suggestion.Provenance(); ok {
		t.Fatalf("manual suggestion provenance = %+v, want absent", provenance)
	}
}
