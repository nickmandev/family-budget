package domain

import "testing"

func TestRefundEffects(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	original := mustJournalOpIDForTest(t, "op-original-purchase")

	tests := []struct {
		name          string
		effects       func(t *testing.T) (Effects, error)
		wantAccounts  map[AccountId]int64
		wantEnvelopes map[EnvelopeId]int64
	}{
		{
			name: "cash refund",
			effects: func(t *testing.T) (Effects, error) {
				return CashRefundEffects(f.checking, original, f.spend, mustPositiveAmountForTest(t, 1_000, usd))
			},
			wantAccounts:  map[AccountId]int64{f.checking: 1_000},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 1_000},
		},
		{
			name: "card refund",
			effects: func(t *testing.T) (Effects, error) {
				return CardRefundEffects(f.card, original, f.spend, f.pay, mustPositiveAmountForTest(t, 500, usd))
			},
			wantAccounts:  map[AccountId]int64{f.card: -500},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 500, f.pay: -500},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effects, err := tt.effects(t)
			if err != nil {
				t.Fatalf("effects: %v", err)
			}
			assertRefundShape(t, effects, tt.wantAccounts, tt.wantEnvelopes, original)
		})
	}
}

func TestCashRefundLedgerEffect(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	original := mustJournalOpIDForTest(t, "op-original-purchase")
	state := f.newLedger(t)

	// Fund the ledger, then buy P on debit so the refund restores both sides.
	opening := PostingSet{}
	if err := opening.AddAccount(f.checking, mustSignedDeltaForTest(t, 10_000, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := opening.AddEnvelope(f.spend, mustSignedDeltaForTest(t, 3_000, usd)); err != nil {
		t.Fatalf("AddEnvelope: %v", err)
	}
	if err := state.ApplyPostings(opening); err != nil {
		t.Fatalf("ApplyPostings(opening): %v", err)
	}

	purchase, err := DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 1_000, usd))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects: %v", err)
	}
	if err := state.ApplyPostings(purchase.Postings); err != nil {
		t.Fatalf("ApplyPostings(purchase): %v", err)
	}
	assertMoney(t, state.PostedAccountBalance, f.checking, 9_000)
	assertMoney(t, state.EnvelopeBalance, f.spend, 2_000)

	refund, err := CashRefundEffects(f.checking, original, f.spend, mustPositiveAmountForTest(t, 1_000, usd))
	if err != nil {
		t.Fatalf("CashRefundEffects: %v", err)
	}
	if err := state.ApplyPostings(refund.Postings); err != nil {
		t.Fatalf("ApplyPostings(refund): %v", err)
	}

	// The refund restores the eligible cash balance and the original spending
	// envelope. Because A = R + sum(E) and both sides rise by P, derived
	// Ready-to-assign is unchanged by this posting shape.
	assertMoney(t, state.PostedAccountBalance, f.checking, 10_000)
	assertMoney(t, state.EnvelopeBalance, f.spend, 3_000)
	assertMoney(t, (*LedgerState).BudgetableCash, state, 10_000)
	assertMoney(t, (*LedgerState).SumEnvelopeBalances, state, 3_000)
	assertMoney(t, (*LedgerState).ReadyToAssign, state, 7_000)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestCardRefundLedgerEffect(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	original := mustJournalOpIDForTest(t, "op-original-purchase")
	amount := mustPositiveAmountForTest(t, 1_000, usd)

	state := f.newLedger(t)
	opening := PostingSet{}
	if err := opening.AddAccount(f.checking, mustSignedDeltaForTest(t, 10_000, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := state.ApplyPostings(opening); err != nil {
		t.Fatalf("ApplyPostings(opening): %v", err)
	}

	purchase, err := CreditCardPurchaseEffects(f.card, f.spend, f.pay, amount)
	if err != nil {
		t.Fatalf("CreditCardPurchaseEffects: %v", err)
	}
	if err := state.ApplyPostings(purchase.Postings); err != nil {
		t.Fatalf("ApplyPostings(purchase): %v", err)
	}

	refund, err := CardRefundEffects(f.card, original, f.spend, f.pay, amount)
	if err != nil {
		t.Fatalf("CardRefundEffects: %v", err)
	}
	if err := state.ApplyPostings(refund.Postings); err != nil {
		t.Fatalf("ApplyPostings(refund): %v", err)
	}

	// Purchase +P then refund -P leaves no liability. The spending envelope is
	// restored and the payment envelope returns to zero; no cash moved.
	assertMoney(t, state.CardLiabilityBalance, f.card, 0)
	assertMoney(t, state.EnvelopeBalance, f.spend, 0)
	assertMoney(t, state.EnvelopeBalance, f.pay, 0)
	assertMoney(t, state.PostedAccountBalance, f.checking, 10_000)
	assertMoney(t, (*LedgerState).BudgetableCash, state, 10_000)
	assertMoney(t, (*LedgerState).SumEnvelopeBalances, state, 0)
	assertMoney(t, (*LedgerState).ReadyToAssign, state, 10_000)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestCardRefundAfterRepaymentLeavesPaymentEnvelopeNegative(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	original := mustJournalOpIDForTest(t, "op-original-purchase")
	amount := mustPositiveAmountForTest(t, 1_000, usd)

	state := f.newLedger(t)
	opening := PostingSet{}
	if err := opening.AddAccount(f.checking, mustSignedDeltaForTest(t, 10_000, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := state.ApplyPostings(opening); err != nil {
		t.Fatalf("ApplyPostings(opening): %v", err)
	}

	// Sequence: card purchase, full repayment, then refund.
	purchase, err := CreditCardPurchaseEffects(f.card, f.spend, f.pay, amount)
	if err != nil {
		t.Fatalf("CreditCardPurchaseEffects: %v", err)
	}
	if err := state.ApplyPostings(purchase.Postings); err != nil {
		t.Fatalf("ApplyPostings(purchase): %v", err)
	}

	payment, err := CreditCardPaymentEffects(f.checking, f.card, f.pay, amount)
	if err != nil {
		t.Fatalf("CreditCardPaymentEffects: %v", err)
	}
	if err := state.ApplyPostings(payment.Postings); err != nil {
		t.Fatalf("ApplyPostings(payment): %v", err)
	}
	assertMoney(t, state.CardLiabilityBalance, f.card, 0)
	assertMoney(t, state.EnvelopeBalance, f.pay, 0)
	assertMoney(t, state.PostedAccountBalance, f.checking, 9_000)

	refund, err := CardRefundEffects(f.card, original, f.spend, f.pay, amount)
	if err != nil {
		t.Fatalf("CardRefundEffects: %v", err)
	}
	if err := state.ApplyPostings(refund.Postings); err != nil {
		t.Fatalf("ApplyPostings(refund): %v", err)
	}

	// The already-repaid card-payment envelope goes negative; that is a
	// legitimate traceable settlement condition (Q4) and identity still holds.
	assertMoney(t, state.CardLiabilityBalance, f.card, -1_000)
	assertMoney(t, state.EnvelopeBalance, f.spend, 0)
	assertMoney(t, state.EnvelopeBalance, f.pay, -1_000)
	assertMoney(t, state.PostedAccountBalance, f.checking, 9_000)
	assertMoney(t, (*LedgerState).BudgetableCash, state, 9_000)
	assertMoney(t, (*LedgerState).SumEnvelopeBalances, state, -1_000)
	assertMoney(t, (*LedgerState).ReadyToAssign, state, 10_000)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestRefundEffectsRejections(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	original := mustJournalOpIDForTest(t, "op-original-purchase")

	tests := []struct {
		name     string
		effects  func(t *testing.T) (Effects, error)
		wantCode ErrorCode
	}{
		{
			name: "cash refund zero account",
			effects: func(t *testing.T) (Effects, error) {
				return CashRefundEffects("", original, f.spend, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "cash refund zero original",
			effects: func(t *testing.T) (Effects, error) {
				return CashRefundEffects(f.checking, "", f.spend, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "cash refund zero envelope",
			effects: func(t *testing.T) (Effects, error) {
				return CashRefundEffects(f.checking, original, "", mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card refund zero card",
			effects: func(t *testing.T) (Effects, error) {
				return CardRefundEffects("", original, f.spend, f.pay, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card refund zero original",
			effects: func(t *testing.T) (Effects, error) {
				return CardRefundEffects(f.card, "", f.spend, f.pay, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card refund zero spending envelope",
			effects: func(t *testing.T) (Effects, error) {
				return CardRefundEffects(f.card, original, "", f.pay, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card refund zero payment envelope",
			effects: func(t *testing.T) (Effects, error) {
				return CardRefundEffects(f.card, original, f.spend, "", mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card refund same spending and payment envelope",
			effects: func(t *testing.T) (Effects, error) {
				return CardRefundEffects(f.card, original, f.spend, f.spend, mustPositiveAmountForTest(t, 100, usd))
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

// assertRefundShape checks the exact posting shape and that exactly one
// ORIGINAL related reference points at the expected operation.
func assertRefundShape(t *testing.T, effects Effects, wantAccounts map[AccountId]int64, wantEnvelopes map[EnvelopeId]int64, wantOriginal JournalOperationId) {
	t.Helper()
	if effects.IsEmpty() {
		t.Fatal("Effects.IsEmpty() = true, want false")
	}
	if len(effects.Reservations) != 0 {
		t.Errorf("Effects.Reservations = %d, want 0", len(effects.Reservations))
	}
	if len(effects.Postings.Accounts) != len(wantAccounts) {
		t.Errorf("account postings = %d, want %d", len(effects.Postings.Accounts), len(wantAccounts))
	}
	if len(effects.Postings.Envelopes) != len(wantEnvelopes) {
		t.Errorf("envelope postings = %d, want %d", len(effects.Postings.Envelopes), len(wantEnvelopes))
	}
	for id, want := range wantAccounts {
		got, err := effects.Postings.AccountDelta(id)
		if err != nil {
			t.Fatalf("AccountDelta(%q): %v", id, err)
		}
		if got.MinorUnits() != want {
			t.Errorf("AccountDelta(%q) = %d, want %d", id, got.MinorUnits(), want)
		}
	}
	for id, want := range wantEnvelopes {
		got, err := effects.Postings.EnvelopeDelta(id)
		if err != nil {
			t.Fatalf("EnvelopeDelta(%q): %v", id, err)
		}
		if got.MinorUnits() != want {
			t.Errorf("EnvelopeDelta(%q) = %d, want %d", id, got.MinorUnits(), want)
		}
	}
	if len(effects.Related) != 1 {
		t.Fatalf("Effects.Related = %d, want 1", len(effects.Related))
	}
	ref := effects.Related[0]
	if ref.Kind != RelatedRefOriginal {
		t.Errorf("Related.Kind = %q, want %q", ref.Kind, RelatedRefOriginal)
	}
	if ref.OperationID != wantOriginal {
		t.Errorf("Related.OperationID = %q, want %q", ref.OperationID, wantOriginal)
	}
}
