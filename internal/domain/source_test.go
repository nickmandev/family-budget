package domain

import "testing"

func TestParseTransactionSource(t *testing.T) {
	valid := []TransactionSource{
		TransactionSourceManual,
		TransactionSourcePlannedPurchase,
		TransactionSourceExternalEntry,
		TransactionSourceOpeningBalance,
		TransactionSourceReconciliation,
	}

	wantText := map[TransactionSource]string{
		TransactionSourceManual:          "MANUAL",
		TransactionSourcePlannedPurchase: "PLANNED_PURCHASE",
		TransactionSourceExternalEntry:   "EXTERNAL_ENTRY",
		TransactionSourceOpeningBalance:  "OPENING_BALANCE",
		TransactionSourceReconciliation:  "RECONCILIATION",
	}

	if len(valid) != 5 {
		t.Fatalf("got %d transaction sources, want exactly 5", len(valid))
	}

	for _, source := range valid {
		t.Run(source.String(), func(t *testing.T) {
			if !source.IsValid() {
				t.Fatalf("%q.IsValid() = false, want true", source)
			}
			if want := wantText[source]; source.String() != want {
				t.Errorf("String() = %q, want %q", source.String(), want)
			}
			parsed, err := ParseTransactionSource(source.String())
			if err != nil {
				t.Fatalf("ParseTransactionSource(%q) unexpected error: %v", source, err)
			}
			if parsed != source {
				t.Errorf("ParseTransactionSource(%q) = %q, want %q", source, parsed, source)
			}
		})
	}

	invalid := []string{"", "manual", "PLANNED_REPAYMENT", "REFUND", "MANUAL ", "UNKNOWN"}
	for _, raw := range invalid {
		t.Run("invalid/"+raw, func(t *testing.T) {
			if _, err := ParseTransactionSource(raw); !HasCode(err, CodeInvalidValue) {
				t.Fatalf("ParseTransactionSource(%q) error = %v, want INVALID_VALUE", raw, err)
			}
		})
	}
}

func TestTransactionSourceIsValidRejectsZeroValue(t *testing.T) {
	if (TransactionSource("")).IsValid() {
		t.Error("zero TransactionSource must be invalid")
	}
}
