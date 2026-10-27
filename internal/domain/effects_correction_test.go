package domain

import (
	"reflect"
	"testing"
)

// correctionFixture is the shared ledger used to verify correction effects:
// two eligible accounts (checking and savings) and two envelopes (spend and
// pay). Opening balances are posted by each test as needed.
type correctionFixture struct {
	currency Currency
	checking AccountId
	savings  AccountId
	spend    EnvelopeId
	pay      EnvelopeId
}

func newCorrectionFixture(t *testing.T) *correctionFixture {
	t.Helper()
	return &correctionFixture{
		currency: mustCurrency(t, "USD", 2),
		checking: mustAccountIDForTest(t, "acct-checking"),
		savings:  mustAccountIDForTest(t, "acct-savings"),
		spend:    mustEnvelopeIDForTest(t, "env-spend"),
		pay:      mustEnvelopeIDForTest(t, "env-pay"),
	}
}

func (f *correctionFixture) newLedger(t *testing.T) *LedgerState {
	t.Helper()
	return mustLedgerForTest(t, f.currency,
		[]AccountMeta{
			mustAccountMetaForTest(t, f.checking, AccountKindChecking, true),
			mustAccountMetaForTest(t, f.savings, AccountKindSavings, true),
		},
		[]EnvelopeId{f.spend, f.pay},
	)
}

// fundCorrectionLedger posts income into checking and allocates it to the
// spend and pay envelopes so derived Ready-to-assign is zero and both
// envelopes are funded.
func (f *correctionFixture) fundCorrectionLedger(t *testing.T) *LedgerState {
	t.Helper()
	state := f.newLedger(t)
	income, err := IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 2_000, f.currency))
	if err != nil {
		t.Fatalf("IncomeReceivedEffects: %v", err)
	}
	spendAllocation, err := AllocateFundsEffects(f.spend, mustPositiveAmountForTest(t, 1_000, f.currency))
	if err != nil {
		t.Fatalf("AllocateFundsEffects(spend): %v", err)
	}
	payAllocation, err := AllocateFundsEffects(f.pay, mustPositiveAmountForTest(t, 1_000, f.currency))
	if err != nil {
		t.Fatalf("AllocateFundsEffects(pay): %v", err)
	}
	applyCorrectionEffectsForTest(t, state, income)
	applyCorrectionEffectsForTest(t, state, spendAllocation)
	applyCorrectionEffectsForTest(t, state, payAllocation)
	return state
}

func applyCorrectionEffectsForTest(t *testing.T, state *LedgerState, effects Effects) {
	t.Helper()
	if err := state.ApplyPostings(effects.Postings); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}
	if err := state.ApplyReservationChanges(effects.Reservations...); err != nil {
		t.Fatalf("ApplyReservationChanges: %v", err)
	}
}

func assertRelatedLinksForTest(t *testing.T, effects Effects, want ...RelatedOperationRef) {
	t.Helper()
	if len(effects.Related) != len(want) {
		t.Fatalf("Effects.Related = %+v, want %+v", effects.Related, want)
	}
	for i := range want {
		if effects.Related[i] != want[i] {
			t.Errorf("Effects.Related[%d] = %+v, want %+v", i, effects.Related[i], want[i])
		}
	}
}

func mustRelatedOperationRefForTest(t *testing.T, kind RelatedRefKind, opID JournalOperationId) RelatedOperationRef {
	t.Helper()
	ref, err := NewRelatedOperationRef(kind, opID)
	if err != nil {
		t.Fatalf("NewRelatedOperationRef(%q, %q): %v", kind, opID, err)
	}
	return ref
}

func TestReversalEffects(t *testing.T) {
	f := newCorrectionFixture(t)
	usd := f.currency
	originalOp := mustJournalOpIDForTest(t, "op-original")
	original, err := DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 1_000, usd))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects: %v", err)
	}

	reversal, err := ReversalEffects(original.Postings, originalOp)
	if err != nil {
		t.Fatalf("ReversalEffects: %v", err)
	}

	negated, err := original.Postings.Negate()
	if err != nil {
		t.Fatalf("Negate: %v", err)
	}
	if !reflect.DeepEqual(reversal.Postings, negated) {
		t.Errorf("reversal postings = %+v, want negation %+v", reversal.Postings, negated)
	}
	if got := reversal.Postings.Accounts[0].Delta.MinorUnits(); got != 1_000 {
		t.Errorf("reversal checking delta = %d, want 1000", got)
	}
	if got := reversal.Postings.Envelopes[0].Delta.MinorUnits(); got != 1_000 {
		t.Errorf("reversal spend delta = %d, want 1000", got)
	}
	assertRelatedLinksForTest(t, reversal, mustRelatedOperationRefForTest(t, RelatedRefReversal, originalOp))

	state := f.newLedger(t)
	applyCorrectionEffectsForTest(t, state, original)
	assertMoney(t, state.PostedAccountBalance, f.checking, -1_000)
	assertMoney(t, state.EnvelopeBalance, f.spend, -1_000)
	assertMoney(t, (*LedgerState).ReadyToAssign, state, 0)

	applyCorrectionEffectsForTest(t, state, reversal)
	assertMoney(t, state.PostedAccountBalance, f.checking, 0)
	assertMoney(t, state.PostedAccountBalance, f.savings, 0)
	assertMoney(t, state.EnvelopeBalance, f.spend, 0)
	assertMoney(t, state.EnvelopeBalance, f.pay, 0)
	assertMoney(t, (*LedgerState).BudgetableCash, state, 0)
	assertMoney(t, (*LedgerState).SumEnvelopeBalances, state, 0)
	assertMoney(t, (*LedgerState).ReadyToAssign, state, 0)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestReplacementEffects(t *testing.T) {
	f := newCorrectionFixture(t)
	usd := f.currency
	originalOp := mustJournalOpIDForTest(t, "op-original")
	original, err := DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 1_000, usd))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects(original): %v", err)
	}
	corrected, err := DebitPurchaseEffects(f.savings, f.spend, mustPositiveAmountForTest(t, 600, usd))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects(corrected): %v", err)
	}

	originalCopy := PostingSet{
		Accounts:  append([]AccountPosting(nil), original.Postings.Accounts...),
		Envelopes: append([]EnvelopePosting(nil), original.Postings.Envelopes...),
	}

	replacement, err := ReplacementEffects(original.Postings, originalOp, corrected)
	if err != nil {
		t.Fatalf("ReplacementEffects: %v", err)
	}
	if !reflect.DeepEqual(original.Postings, originalCopy) {
		t.Errorf("original posting set was mutated: got %+v, want %+v", original.Postings, originalCopy)
	}

	negated, err := original.Postings.Negate()
	if err != nil {
		t.Fatalf("Negate: %v", err)
	}
	expectedPostings, err := MergePostings(negated, corrected.Postings)
	if err != nil {
		t.Fatalf("MergePostings: %v", err)
	}
	if !reflect.DeepEqual(replacement.Postings, expectedPostings) {
		t.Errorf("replacement postings = %+v, want negation+corrected %+v", replacement.Postings, expectedPostings)
	}
	assertRelatedLinksForTest(t, replacement,
		mustRelatedOperationRefForTest(t, RelatedRefReversal, originalOp),
		mustRelatedOperationRefForTest(t, RelatedRefReplacement, originalOp),
	)

	withReplacement := f.newLedger(t)
	applyCorrectionEffectsForTest(t, withReplacement, original)
	applyCorrectionEffectsForTest(t, withReplacement, replacement)

	correctedOnly := f.newLedger(t)
	applyCorrectionEffectsForTest(t, correctedOnly, corrected)

	for _, id := range []AccountId{f.checking, f.savings} {
		got, err := withReplacement.PostedAccountBalance(id)
		if err != nil {
			t.Fatalf("PostedAccountBalance(%q): %v", id, err)
		}
		want, err := correctedOnly.PostedAccountBalance(id)
		if err != nil {
			t.Fatalf("PostedAccountBalance(%q): %v", id, err)
		}
		if got.MinorUnits() != want.MinorUnits() {
			t.Errorf("account %q balance after original+replacement = %d, want corrected-only %d", id, got.MinorUnits(), want.MinorUnits())
		}
	}
	for _, id := range []EnvelopeId{f.spend, f.pay} {
		got, err := withReplacement.EnvelopeBalance(id)
		if err != nil {
			t.Fatalf("EnvelopeBalance(%q): %v", id, err)
		}
		want, err := correctedOnly.EnvelopeBalance(id)
		if err != nil {
			t.Fatalf("EnvelopeBalance(%q): %v", id, err)
		}
		if got.MinorUnits() != want.MinorUnits() {
			t.Errorf("envelope %q balance after original+replacement = %d, want corrected-only %d", id, got.MinorUnits(), want.MinorUnits())
		}
	}
	assertMoney(t, (*LedgerState).ReadyToAssign, withReplacement, 0)
	assertMoney(t, (*LedgerState).ReadyToAssign, correctedOnly, 0)
	if err := withReplacement.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestCategoryCorrectionEffects(t *testing.T) {
	f := newCorrectionFixture(t)
	usd := f.currency
	originalOp := mustJournalOpIDForTest(t, "op-original")
	state := f.fundCorrectionLedger(t)

	beforeSum, err := state.SumEnvelopeBalances()
	if err != nil {
		t.Fatalf("SumEnvelopeBalances: %v", err)
	}
	beforeReady, err := state.ReadyToAssign()
	if err != nil {
		t.Fatalf("ReadyToAssign: %v", err)
	}

	correction, err := CategoryCorrectionEffects(originalOp, f.spend, f.pay, mustPositiveAmountForTest(t, 1_000, usd))
	if err != nil {
		t.Fatalf("CategoryCorrectionEffects: %v", err)
	}

	if len(correction.Postings.Accounts) != 0 {
		t.Errorf("account postings = %d, want 0", len(correction.Postings.Accounts))
	}
	if got, err := correction.Postings.AccountDelta(f.checking); err != nil {
		t.Fatalf("AccountDelta: %v", err)
	} else if !got.IsZero() {
		t.Errorf("AccountDelta(checking) = %d, want 0", got.MinorUnits())
	}
	if got, err := correction.Postings.EnvelopeDelta(f.spend); err != nil {
		t.Fatalf("EnvelopeDelta(spend): %v", err)
	} else if got.MinorUnits() != 1_000 {
		t.Errorf("EnvelopeDelta(spend) = %d, want 1000", got.MinorUnits())
	}
	if got, err := correction.Postings.EnvelopeDelta(f.pay); err != nil {
		t.Fatalf("EnvelopeDelta(pay): %v", err)
	} else if got.MinorUnits() != -1_000 {
		t.Errorf("EnvelopeDelta(pay) = %d, want -1000", got.MinorUnits())
	}
	assertRelatedLinksForTest(t, correction,
		mustRelatedOperationRefForTest(t, RelatedRefReplacement, originalOp),
	)

	applyCorrectionEffectsForTest(t, state, correction)
	assertMoney(t, state.EnvelopeBalance, f.spend, 2_000)
	assertMoney(t, state.EnvelopeBalance, f.pay, 0)
	assertMoney(t, state.PostedAccountBalance, f.checking, 2_000)
	assertMoney(t, (*LedgerState).SumEnvelopeBalances, state, beforeSum.MinorUnits())
	assertMoney(t, (*LedgerState).ReadyToAssign, state, beforeReady.MinorUnits())
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestAccountCorrectionEffects(t *testing.T) {
	f := newCorrectionFixture(t)
	usd := f.currency
	originalOp := mustJournalOpIDForTest(t, "op-original")
	original, err := DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 1_000, usd))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects(original): %v", err)
	}
	corrected, err := DebitPurchaseEffects(f.savings, f.spend, mustPositiveAmountForTest(t, 800, usd))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects(corrected): %v", err)
	}

	correction, err := AccountCorrectionEffects(original.Postings, originalOp, corrected)
	if err != nil {
		t.Fatalf("AccountCorrectionEffects: %v", err)
	}
	assertRelatedLinksForTest(t, correction,
		mustRelatedOperationRefForTest(t, RelatedRefReversal, originalOp),
		mustRelatedOperationRefForTest(t, RelatedRefReplacement, originalOp),
	)

	state := f.newLedger(t)
	applyCorrectionEffectsForTest(t, state, original)
	applyCorrectionEffectsForTest(t, state, correction)
	assertMoney(t, state.PostedAccountBalance, f.checking, 0)
	assertMoney(t, state.PostedAccountBalance, f.savings, -800)
	assertMoney(t, state.EnvelopeBalance, f.spend, -800)
	assertMoney(t, state.EnvelopeBalance, f.pay, 0)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestMetadataCorrectionEffects(t *testing.T) {
	f := newCorrectionFixture(t)
	originalOp := mustJournalOpIDForTest(t, "op-original")
	state := f.fundCorrectionLedger(t)

	beforeChecking, err := state.PostedAccountBalance(f.checking)
	if err != nil {
		t.Fatalf("PostedAccountBalance: %v", err)
	}
	beforeSpend, err := state.EnvelopeBalance(f.spend)
	if err != nil {
		t.Fatalf("EnvelopeBalance: %v", err)
	}
	beforeReady, err := state.ReadyToAssign()
	if err != nil {
		t.Fatalf("ReadyToAssign: %v", err)
	}

	correction, err := MetadataCorrectionEffects(originalOp)
	if err != nil {
		t.Fatalf("MetadataCorrectionEffects: %v", err)
	}
	if !correction.Postings.IsEmpty() {
		t.Errorf("Effects.Postings = %+v, want empty", correction.Postings)
	}
	if len(correction.Reservations) != 0 {
		t.Errorf("Effects.Reservations = %d, want 0", len(correction.Reservations))
	}
	if correction.IsEmpty() {
		t.Error("Effects.IsEmpty() = true, want false because of the audit link")
	}
	assertRelatedLinksForTest(t, correction,
		mustRelatedOperationRefForTest(t, RelatedRefOriginal, originalOp),
	)

	applyCorrectionEffectsForTest(t, state, correction)
	assertMoney(t, state.PostedAccountBalance, f.checking, beforeChecking.MinorUnits())
	assertMoney(t, state.EnvelopeBalance, f.spend, beforeSpend.MinorUnits())
	assertMoney(t, (*LedgerState).ReadyToAssign, state, beforeReady.MinorUnits())
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestCorrectionEffectsRejections(t *testing.T) {
	f := newCorrectionFixture(t)
	usd := f.currency
	originalOp := mustJournalOpIDForTest(t, "op-original")
	original, err := DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 1_000, usd))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects: %v", err)
	}
	corrected, err := DebitPurchaseEffects(f.savings, f.spend, mustPositiveAmountForTest(t, 600, usd))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects(corrected): %v", err)
	}

	tests := []struct {
		name     string
		effects  func(t *testing.T) (Effects, error)
		wantCode ErrorCode
	}{
		{
			name: "reversal zero original operation",
			effects: func(t *testing.T) (Effects, error) {
				return ReversalEffects(original.Postings, "")
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "reversal empty original set",
			effects: func(t *testing.T) (Effects, error) {
				return ReversalEffects(PostingSet{}, originalOp)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "replacement zero original operation",
			effects: func(t *testing.T) (Effects, error) {
				return ReplacementEffects(original.Postings, "", corrected)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "replacement empty original set",
			effects: func(t *testing.T) (Effects, error) {
				return ReplacementEffects(PostingSet{}, originalOp, corrected)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "replacement empty corrected",
			effects: func(t *testing.T) (Effects, error) {
				return ReplacementEffects(original.Postings, originalOp, Effects{})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "category correction zero original operation",
			effects: func(t *testing.T) (Effects, error) {
				return CategoryCorrectionEffects("", f.spend, f.pay, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "category correction zero original envelope",
			effects: func(t *testing.T) (Effects, error) {
				return CategoryCorrectionEffects(originalOp, "", f.pay, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "category correction zero corrected envelope",
			effects: func(t *testing.T) (Effects, error) {
				return CategoryCorrectionEffects(originalOp, f.spend, "", mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "category correction same envelope",
			effects: func(t *testing.T) (Effects, error) {
				return CategoryCorrectionEffects(originalOp, f.spend, f.spend, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "account correction zero original operation",
			effects: func(t *testing.T) (Effects, error) {
				return AccountCorrectionEffects(original.Postings, "", corrected)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "account correction empty original set",
			effects: func(t *testing.T) (Effects, error) {
				return AccountCorrectionEffects(PostingSet{}, originalOp, corrected)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "account correction empty corrected",
			effects: func(t *testing.T) (Effects, error) {
				return AccountCorrectionEffects(original.Postings, originalOp, Effects{})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "metadata correction zero original operation",
			effects: func(t *testing.T) (Effects, error) {
				return MetadataCorrectionEffects("")
			},
			wantCode: CodeInvalidValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effects, err := tt.effects(t)
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("effects error = %v, want code %s", err, tt.wantCode)
			}
			if !effects.IsEmpty() {
				t.Errorf("rejected effects = %+v, want empty", effects)
			}
		})
	}
}
