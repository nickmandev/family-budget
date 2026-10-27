package domain

import "testing"

func TestSpendingEffects(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency

	tests := []struct {
		name               string
		effects            func(t *testing.T) (Effects, error)
		wantAccounts       map[AccountId]int64
		wantEnvelopes      map[EnvelopeId]int64
		wantReadyToAssign  int64
		wantLedgerEnvelope map[EnvelopeId]int64
		wantBudgetableCash int64
		wantCashPosted     int64
		wantCashSpendable  int64
		wantCardLiability  int64
	}{
		{
			name: "debit purchase",
			effects: func(t *testing.T) (Effects, error) {
				return DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 1_000, usd))
			},
			wantAccounts:       map[AccountId]int64{f.checking: -1_000},
			wantEnvelopes:      map[EnvelopeId]int64{f.spend: -1_000},
			wantReadyToAssign:  0,
			wantLedgerEnvelope: map[EnvelopeId]int64{f.spend: -1_000},
			wantBudgetableCash: -1_000,
			wantCashPosted:     -1_000,
			wantCashSpendable:  -1_000,
		},
		{
			name: "credit card purchase",
			effects: func(t *testing.T) (Effects, error) {
				return CreditCardPurchaseEffects(f.card, f.spend, f.pay, mustPositiveAmountForTest(t, 1_000, usd))
			},
			wantAccounts:       map[AccountId]int64{f.card: 1_000},
			wantEnvelopes:      map[EnvelopeId]int64{f.spend: -1_000, f.pay: 1_000},
			wantReadyToAssign:  0,
			wantLedgerEnvelope: map[EnvelopeId]int64{f.spend: -1_000, f.pay: 1_000},
			wantCardLiability:  1_000,
		},
		{
			name: "credit card payment",
			effects: func(t *testing.T) (Effects, error) {
				return CreditCardPaymentEffects(f.checking, f.card, f.pay, mustPositiveAmountForTest(t, 1_000, usd))
			},
			wantAccounts:       map[AccountId]int64{f.checking: -1_000, f.card: -1_000},
			wantEnvelopes:      map[EnvelopeId]int64{f.pay: -1_000},
			wantReadyToAssign:  0,
			wantLedgerEnvelope: map[EnvelopeId]int64{f.pay: -1_000},
			wantBudgetableCash: -1_000,
			wantCashPosted:     -1_000,
			wantCashSpendable:  -1_000,
			wantCardLiability:  -1_000,
		},
		{
			name: "card fee or interest",
			effects: func(t *testing.T) (Effects, error) {
				return CardFeeOrInterestEffects(f.card, f.spend, f.pay, mustPositiveAmountForTest(t, 500, usd))
			},
			wantAccounts:       map[AccountId]int64{f.card: 500},
			wantEnvelopes:      map[EnvelopeId]int64{f.spend: -500, f.pay: 500},
			wantReadyToAssign:  0,
			wantLedgerEnvelope: map[EnvelopeId]int64{f.spend: -500, f.pay: 500},
			wantCardLiability:  500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effects, err := tt.effects(t)
			if err != nil {
				t.Fatalf("effects: %v", err)
			}
			assertEffectsShape(t, effects, tt.wantAccounts, tt.wantEnvelopes)

			state := f.newLedger(t)
			applyAndAssertLedgerEffect(t, state, effects, tt.wantReadyToAssign, tt.wantLedgerEnvelope, []EnvelopeId{f.spend, f.pay})

			budgetable, err := state.BudgetableCash()
			if err != nil {
				t.Fatalf("BudgetableCash: %v", err)
			}
			if budgetable.MinorUnits() != tt.wantBudgetableCash {
				t.Errorf("BudgetableCash = %d, want %d", budgetable.MinorUnits(), tt.wantBudgetableCash)
			}
			assertMoney(t, state.PostedAccountBalance, f.checking, tt.wantCashPosted)
			assertMoney(t, state.SpendableAccountBalance, f.checking, tt.wantCashSpendable)
			assertMoney(t, state.CardLiabilityBalance, f.card, tt.wantCardLiability)
		})
	}
}

func TestSpendingEffectsRejections(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency

	tests := []struct {
		name     string
		effects  func(t *testing.T) (Effects, error)
		wantCode ErrorCode
	}{
		{
			name: "debit zero account",
			effects: func(t *testing.T) (Effects, error) {
				return DebitPurchaseEffects("", f.spend, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "debit zero envelope",
			effects: func(t *testing.T) (Effects, error) {
				return DebitPurchaseEffects(f.checking, "", mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card purchase same spending and payment envelope",
			effects: func(t *testing.T) (Effects, error) {
				return CreditCardPurchaseEffects(f.card, f.spend, f.spend, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card purchase zero card",
			effects: func(t *testing.T) (Effects, error) {
				return CreditCardPurchaseEffects("", f.spend, f.pay, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card payment cash equals card",
			effects: func(t *testing.T) (Effects, error) {
				return CreditCardPaymentEffects(f.card, f.card, f.pay, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card payment zero cash",
			effects: func(t *testing.T) (Effects, error) {
				return CreditCardPaymentEffects("", f.card, f.pay, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card fee same fee and payment envelope",
			effects: func(t *testing.T) (Effects, error) {
				return CardFeeOrInterestEffects(f.card, f.spend, f.spend, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card fee zero card",
			effects: func(t *testing.T) (Effects, error) {
				return CardFeeOrInterestEffects("", f.spend, f.pay, mustPositiveAmountForTest(t, 100, usd))
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

func TestCardPurchaseThenPaymentIsNotDoubleCounted(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	state := f.newLedger(t)

	purchase, err := CreditCardPurchaseEffects(f.card, f.spend, f.pay, mustPositiveAmountForTest(t, 1_000, usd))
	if err != nil {
		t.Fatalf("CreditCardPurchaseEffects: %v", err)
	}
	if err := state.ApplyPostings(purchase.Postings); err != nil {
		t.Fatalf("ApplyPostings(purchase): %v", err)
	}
	assertMoney(t, state.CardLiabilityBalance, f.card, 1_000)
	assertMoney(t, state.EnvelopeBalance, f.spend, -1_000)
	assertMoney(t, state.EnvelopeBalance, f.pay, 1_000)

	payment, err := CreditCardPaymentEffects(f.checking, f.card, f.pay, mustPositiveAmountForTest(t, 1_000, usd))
	if err != nil {
		t.Fatalf("CreditCardPaymentEffects: %v", err)
	}
	if err := state.ApplyPostings(payment.Postings); err != nil {
		t.Fatalf("ApplyPostings(payment): %v", err)
	}

	// Liability ends at zero after +P then -P; the purchase was not posted twice.
	assertMoney(t, state.CardLiabilityBalance, f.card, 0)
	// Net envelope effect is spend -P and pay 0.
	assertMoney(t, state.EnvelopeBalance, f.spend, -1_000)
	assertMoney(t, state.EnvelopeBalance, f.pay, 0)
	// Cash paid once, and the budget identity still holds.
	assertMoney(t, state.PostedAccountBalance, f.checking, -1_000)
	ready, err := state.ReadyToAssign()
	if err != nil {
		t.Fatalf("ReadyToAssign: %v", err)
	}
	if ready.MinorUnits() != 0 {
		t.Errorf("ReadyToAssign = %d, want 0", ready.MinorUnits())
	}
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}
