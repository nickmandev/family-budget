package domain

import (
	"testing"
)

func mustSignedDeltaForTest(t *testing.T, minorUnits int64, currency Currency) SignedMoneyDelta {
	t.Helper()
	delta, err := NewSignedMoneyDelta(minorUnits, currency)
	if err != nil {
		t.Fatalf("NewSignedMoneyDelta(%d, %s): %v", minorUnits, currency.Code(), err)
	}
	return delta
}

func mustAccountIDForTest(t *testing.T, raw string) AccountId {
	t.Helper()
	id, err := NewAccountId(raw)
	if err != nil {
		t.Fatalf("NewAccountId(%q): %v", raw, err)
	}
	return id
}

func mustEnvelopeIDForTest(t *testing.T, raw string) EnvelopeId {
	t.Helper()
	id, err := NewEnvelopeId(raw)
	if err != nil {
		t.Fatalf("NewEnvelopeId(%q): %v", raw, err)
	}
	return id
}

func mustPlanIDForTest(t *testing.T, raw string) PlanId {
	t.Helper()
	id, err := NewPlanId(raw)
	if err != nil {
		t.Fatalf("NewPlanId(%q): %v", raw, err)
	}
	return id
}

func mustJournalOpIDForTest(t *testing.T, raw string) JournalOperationId {
	t.Helper()
	id, err := NewJournalOperationId(raw)
	if err != nil {
		t.Fatalf("NewJournalOperationId(%q): %v", raw, err)
	}
	return id
}

func TestNewAccountPosting(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	account := mustAccountIDForTest(t, "acct-checking")

	tests := []struct {
		name     string
		id       AccountId
		delta    SignedMoneyDelta
		wantCode ErrorCode
	}{
		{name: "positive", id: account, delta: mustSignedDeltaForTest(t, 500, usd)},
		{name: "negative", id: account, delta: mustSignedDeltaForTest(t, -500, usd)},
		{name: "zero id", id: "", delta: mustSignedDeltaForTest(t, 500, usd), wantCode: CodeInvalidValue},
		{name: "zero delta", id: account, delta: SignedMoneyDelta{}, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewAccountPosting(tt.id, tt.delta)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewAccountPosting error = %v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewAccountPosting unexpected error: %v", err)
			}
			if got.AccountID != tt.id {
				t.Errorf("AccountID = %q, want %q", got.AccountID, tt.id)
			}
			if got.Delta.MinorUnits() != tt.delta.MinorUnits() {
				t.Errorf("Delta = %d, want %d", got.Delta.MinorUnits(), tt.delta.MinorUnits())
			}
		})
	}
}

func TestNewEnvelopePosting(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	envelope := mustEnvelopeIDForTest(t, "env-groceries")

	tests := []struct {
		name     string
		id       EnvelopeId
		delta    SignedMoneyDelta
		wantCode ErrorCode
	}{
		{name: "positive", id: envelope, delta: mustSignedDeltaForTest(t, 500, usd)},
		{name: "negative", id: envelope, delta: mustSignedDeltaForTest(t, -500, usd)},
		{name: "zero id", id: "", delta: mustSignedDeltaForTest(t, 500, usd), wantCode: CodeInvalidValue},
		{name: "zero delta", id: envelope, delta: SignedMoneyDelta{}, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewEnvelopePosting(tt.id, tt.delta)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewEnvelopePosting error = %v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewEnvelopePosting unexpected error: %v", err)
			}
			if got.EnvelopeID != tt.id {
				t.Errorf("EnvelopeID = %q, want %q", got.EnvelopeID, tt.id)
			}
		})
	}
}

func TestPostingSetAddAndDeltas(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	savings := mustAccountIDForTest(t, "acct-savings")
	groceries := mustEnvelopeIDForTest(t, "env-groceries")

	set := PostingSet{}
	if err := set.AddAccount(checking, mustSignedDeltaForTest(t, 1_000, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := set.AddAccount(checking, mustSignedDeltaForTest(t, -250, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := set.AddAccount(savings, mustSignedDeltaForTest(t, 45, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := set.AddEnvelope(groceries, mustSignedDeltaForTest(t, -400, usd)); err != nil {
		t.Fatalf("AddEnvelope: %v", err)
	}

	got, err := set.AccountDelta(checking)
	if err != nil {
		t.Fatalf("AccountDelta: %v", err)
	}
	if got.MinorUnits() != 750 {
		t.Errorf("AccountDelta(checking) = %d, want 750", got.MinorUnits())
	}
	if !got.Currency().Equal(usd) {
		t.Errorf("AccountDelta currency = %s, want USD", got.Currency())
	}

	got, err = set.AccountDelta(savings)
	if err != nil {
		t.Fatalf("AccountDelta: %v", err)
	}
	if got.MinorUnits() != 45 {
		t.Errorf("AccountDelta(savings) = %d, want 45", got.MinorUnits())
	}

	unknownAccount := mustAccountIDForTest(t, "acct-unknown")
	got, err = set.AccountDelta(unknownAccount)
	if err != nil {
		t.Fatalf("AccountDelta(unknown): %v", err)
	}
	if !got.IsZero() {
		t.Errorf("AccountDelta(unknown) = %d, want zero", got.MinorUnits())
	}
	if got.Currency().Code() != "USD" {
		t.Errorf("AccountDelta(unknown) currency = %q, want USD inferred from set", got.Currency().Code())
	}

	got, err = set.EnvelopeDelta(groceries)
	if err != nil {
		t.Fatalf("EnvelopeDelta: %v", err)
	}
	if got.MinorUnits() != -400 {
		t.Errorf("EnvelopeDelta(groceries) = %d, want -400", got.MinorUnits())
	}

	unknownEnvelope := mustEnvelopeIDForTest(t, "env-unknown")
	got, err = set.EnvelopeDelta(unknownEnvelope)
	if err != nil {
		t.Fatalf("EnvelopeDelta(unknown): %v", err)
	}
	if !got.IsZero() {
		t.Errorf("EnvelopeDelta(unknown) = %d, want zero", got.MinorUnits())
	}
}

func TestPostingSetAddRejectsInvalid(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	set := PostingSet{}
	if err := set.AddAccount("", mustSignedDeltaForTest(t, 1, usd)); !HasCode(err, CodeInvalidValue) {
		t.Errorf("AddAccount(zero id) error = %v, want INVALID_VALUE", err)
	}
	if err := set.AddEnvelope("", mustSignedDeltaForTest(t, 1, usd)); !HasCode(err, CodeInvalidValue) {
		t.Errorf("AddEnvelope(zero id) error = %v, want INVALID_VALUE", err)
	}
	if !set.IsEmpty() {
		t.Errorf("set after rejected additions is not empty: %+v", set)
	}
}

func TestPostingSetNegate(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	groceries := mustEnvelopeIDForTest(t, "env-groceries")

	set := PostingSet{}
	if err := set.AddAccount(checking, mustSignedDeltaForTest(t, 1_000, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := set.AddEnvelope(groceries, mustSignedDeltaForTest(t, -400, usd)); err != nil {
		t.Fatalf("AddEnvelope: %v", err)
	}

	negated, err := set.Negate()
	if err != nil {
		t.Fatalf("Negate: %v", err)
	}
	if len(negated.Accounts) != len(set.Accounts) || len(negated.Envelopes) != len(set.Envelopes) {
		t.Fatalf("Negate lengths = %d/%d, want %d/%d", len(negated.Accounts), len(negated.Envelopes), len(set.Accounts), len(set.Envelopes))
	}
	for i, original := range set.Accounts {
		want := -original.Delta.MinorUnits()
		if negated.Accounts[i].Delta.MinorUnits() != want {
			t.Errorf("negated account delta [%d] = %d, want %d", i, negated.Accounts[i].Delta.MinorUnits(), want)
		}
		if negated.Accounts[i].AccountID != original.AccountID {
			t.Errorf("negated account id [%d] = %q, want %q", i, negated.Accounts[i].AccountID, original.AccountID)
		}
	}
	for i, original := range set.Envelopes {
		want := -original.Delta.MinorUnits()
		if negated.Envelopes[i].Delta.MinorUnits() != want {
			t.Errorf("negated envelope delta [%d] = %d, want %d", i, negated.Envelopes[i].Delta.MinorUnits(), want)
		}
	}

	merged, err := MergePostings(set, negated)
	if err != nil {
		t.Fatalf("MergePostings: %v", err)
	}
	accountTotal, err := merged.AccountDelta(checking)
	if err != nil {
		t.Fatalf("AccountDelta: %v", err)
	}
	if !accountTotal.IsZero() {
		t.Errorf("merged account delta = %d, want zero", accountTotal.MinorUnits())
	}
	envelopeTotal, err := merged.EnvelopeDelta(groceries)
	if err != nil {
		t.Fatalf("EnvelopeDelta: %v", err)
	}
	if !envelopeTotal.IsZero() {
		t.Errorf("merged envelope delta = %d, want zero", envelopeTotal.MinorUnits())
	}
}

func TestPostingSetIsEmpty(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")

	if !(PostingSet{}).IsEmpty() {
		t.Error("zero PostingSet must be empty")
	}
	set := PostingSet{}
	if err := set.AddAccount(checking, mustSignedDeltaForTest(t, 1, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if set.IsEmpty() {
		t.Error("PostingSet with a posting must not be empty")
	}
}

func TestMergePostings(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	savings := mustAccountIDForTest(t, "acct-savings")

	first := PostingSet{}
	if err := first.AddAccount(checking, mustSignedDeltaForTest(t, 100, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	second := PostingSet{}
	if err := second.AddAccount(checking, mustSignedDeltaForTest(t, 50, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := second.AddAccount(savings, mustSignedDeltaForTest(t, -20, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	merged, err := MergePostings(first, second)
	if err != nil {
		t.Fatalf("MergePostings: %v", err)
	}
	if len(merged.Accounts) != 3 {
		t.Errorf("merged account postings = %d, want 3", len(merged.Accounts))
	}
	got, err := merged.AccountDelta(checking)
	if err != nil {
		t.Fatalf("AccountDelta: %v", err)
	}
	if got.MinorUnits() != 150 {
		t.Errorf("merged checking delta = %d, want 150", got.MinorUnits())
	}
	got, err = merged.AccountDelta(savings)
	if err != nil {
		t.Fatalf("AccountDelta: %v", err)
	}
	if got.MinorUnits() != -20 {
		t.Errorf("merged savings delta = %d, want -20", got.MinorUnits())
	}
}

func TestPostingSetAccountDeltaOverflow(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")

	set := PostingSet{}
	if err := set.AddAccount(checking, mustSignedDeltaForTest(t, MaxMinorUnits, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := set.AddAccount(checking, mustSignedDeltaForTest(t, 1, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	_, err := set.AccountDelta(checking)
	if !HasCode(err, CodeAmountOutOfRange) && !HasCode(err, CodeMoneyOverflow) {
		t.Fatalf("AccountDelta overflow error = %v, want AMOUNT_OUT_OF_RANGE or MONEY_OVERFLOW", err)
	}
}

func TestPostingSetAccountDeltaCurrencyMismatch(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	eur := mustCurrency(t, "EUR", 2)
	checking := mustAccountIDForTest(t, "acct-checking")

	set := PostingSet{}
	if err := set.AddAccount(checking, mustSignedDeltaForTest(t, 100, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := set.AddAccount(checking, mustSignedDeltaForTest(t, 100, eur)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if _, err := set.AccountDelta(checking); !HasCode(err, CodeCurrencyMismatch) {
		t.Fatalf("AccountDelta across currencies error = %v, want CURRENCY_MISMATCH", err)
	}
}

func TestEffectsIsEmpty(t *testing.T) {
	if !(Effects{}).IsEmpty() {
		t.Error("zero Effects must be empty")
	}
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	effects := Effects{}
	if err := effects.Postings.AddAccount(checking, mustSignedDeltaForTest(t, 1, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if effects.IsEmpty() {
		t.Error("Effects with a posting must not be empty")
	}
}

func TestMergeEffects(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	groceries := mustEnvelopeIDForTest(t, "env-groceries")
	plan := mustPlanIDForTest(t, "plan-1")
	original := mustJournalOpIDForTest(t, "op-original")

	postingsA := PostingSet{}
	if err := postingsA.AddAccount(checking, mustSignedDeltaForTest(t, 100, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	hold, err := NewEnvelopeHold(groceries, mustSignedDeltaForTest(t, 300, usd))
	if err != nil {
		t.Fatalf("NewEnvelopeHold: %v", err)
	}
	change, err := NewReservationChange(plan, hold, nil)
	if err != nil {
		t.Fatalf("NewReservationChange: %v", err)
	}
	ref, err := NewRelatedOperationRef(RelatedRefOriginal, original)
	if err != nil {
		t.Fatalf("NewRelatedOperationRef: %v", err)
	}

	postingsB := PostingSet{}
	if err := postingsB.AddEnvelope(groceries, mustSignedDeltaForTest(t, 100, usd)); err != nil {
		t.Fatalf("AddEnvelope: %v", err)
	}

	merged, err := MergeEffects(
		Effects{Postings: postingsA, Reservations: []ReservationChange{change}, Related: []RelatedOperationRef{ref}},
		Effects{Postings: postingsB, Related: []RelatedOperationRef{ref}},
	)
	if err != nil {
		t.Fatalf("MergeEffects: %v", err)
	}
	if len(merged.Reservations) != 1 {
		t.Errorf("merged reservations = %d, want 1", len(merged.Reservations))
	}
	if len(merged.Related) != 1 {
		t.Errorf("merged related refs = %d, want 1 (deduped)", len(merged.Related))
	}
	accountDelta, err := merged.Postings.AccountDelta(checking)
	if err != nil {
		t.Fatalf("AccountDelta: %v", err)
	}
	if accountDelta.MinorUnits() != 100 {
		t.Errorf("merged account delta = %d, want 100", accountDelta.MinorUnits())
	}
	envelopeDelta, err := merged.Postings.EnvelopeDelta(groceries)
	if err != nil {
		t.Fatalf("EnvelopeDelta: %v", err)
	}
	if envelopeDelta.MinorUnits() != 100 {
		t.Errorf("merged envelope delta = %d, want 100", envelopeDelta.MinorUnits())
	}
}

func TestMergeEffectsKeepsDistinctRelatedRefs(t *testing.T) {
	original := mustJournalOpIDForTest(t, "op-1")
	reversal := mustJournalOpIDForTest(t, "op-2")
	first, err := NewRelatedOperationRef(RelatedRefOriginal, original)
	if err != nil {
		t.Fatalf("NewRelatedOperationRef: %v", err)
	}
	second, err := NewRelatedOperationRef(RelatedRefReversal, original)
	if err != nil {
		t.Fatalf("NewRelatedOperationRef: %v", err)
	}
	third, err := NewRelatedOperationRef(RelatedRefOriginal, reversal)
	if err != nil {
		t.Fatalf("NewRelatedOperationRef: %v", err)
	}

	merged, err := MergeEffects(Effects{Related: []RelatedOperationRef{first, second}}, Effects{Related: []RelatedOperationRef{third}})
	if err != nil {
		t.Fatalf("MergeEffects: %v", err)
	}
	if len(merged.Related) != 3 {
		t.Errorf("merged related refs = %d, want 3", len(merged.Related))
	}
}

func TestMergeEffectsEmpty(t *testing.T) {
	merged, err := MergeEffects()
	if err != nil {
		t.Fatalf("MergeEffects: %v", err)
	}
	if !merged.IsEmpty() {
		t.Errorf("merged empty effects = %+v, want empty", merged)
	}
}
