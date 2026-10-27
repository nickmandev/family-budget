package domain

import "testing"

func mustPositiveAmountForTest(t *testing.T, minorUnits int64, currency Currency) PositiveAmount {
	t.Helper()
	amount, err := NewPositiveAmountFromMinorUnits(minorUnits, currency)
	if err != nil {
		t.Fatalf("NewPositiveAmountFromMinorUnits(%d, %s): %v", minorUnits, currency.Code(), err)
	}
	return amount
}

// setupEffectsFixture is the shared ledger used to verify the budget effect of
// setup and budget postings: one eligible checking account, one excluded
// savings account, one card liability, and two envelopes.
type setupEffectsFixture struct {
	currency Currency
	checking AccountId
	excluded AccountId
	card     AccountId
	spend    EnvelopeId
	pay      EnvelopeId
}

func newSetupEffectsFixture(t *testing.T) *setupEffectsFixture {
	t.Helper()
	return &setupEffectsFixture{
		currency: mustCurrency(t, "USD", 2),
		checking: mustAccountIDForTest(t, "acct-checking"),
		excluded: mustAccountIDForTest(t, "acct-excluded"),
		card:     mustAccountIDForTest(t, "acct-card"),
		spend:    mustEnvelopeIDForTest(t, "env-spend"),
		pay:      mustEnvelopeIDForTest(t, "env-pay"),
	}
}

func (f *setupEffectsFixture) newLedger(t *testing.T) *LedgerState {
	t.Helper()
	return mustLedgerForTest(t, f.currency,
		[]AccountMeta{
			mustAccountMetaForTest(t, f.checking, AccountKindChecking, true),
			mustAccountMetaForTest(t, f.excluded, AccountKindSavings, false),
			mustAccountMetaForTest(t, f.card, AccountKindCardLiability, false),
		},
		[]EnvelopeId{f.spend, f.pay},
	)
}

// assertEffectsShape checks the exact raw posting shape: one posting per
// expected account/envelope, no reservation or related-operation effects, and
// a non-empty effect set.
func assertEffectsShape(t *testing.T, effects Effects, wantAccounts map[AccountId]int64, wantEnvelopes map[EnvelopeId]int64) {
	t.Helper()
	if effects.IsEmpty() {
		t.Fatal("Effects.IsEmpty() = true, want false")
	}
	if len(effects.Reservations) != 0 {
		t.Errorf("Effects.Reservations = %d, want 0", len(effects.Reservations))
	}
	if len(effects.Related) != 0 {
		t.Errorf("Effects.Related = %d, want 0", len(effects.Related))
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
}

// applyAndAssertLedgerEffect applies the effects to a fresh ledger and asserts
// the derived budget outcome: Ready-to-assign, every envelope balance, the
// envelope sum, and the accounting invariants.
func applyAndAssertLedgerEffect(t *testing.T, state *LedgerState, effects Effects, wantReadyToAssign int64, wantEnvelopes map[EnvelopeId]int64, envelopeIDs []EnvelopeId) {
	t.Helper()
	if err := state.ApplyPostings(effects.Postings); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}

	ready, err := state.ReadyToAssign()
	if err != nil {
		t.Fatalf("ReadyToAssign: %v", err)
	}
	if ready.MinorUnits() != wantReadyToAssign {
		t.Errorf("ReadyToAssign = %d, want %d", ready.MinorUnits(), wantReadyToAssign)
	}

	var wantSum int64
	for _, id := range envelopeIDs {
		want := wantEnvelopes[id]
		wantSum += want
		got, err := state.EnvelopeBalance(id)
		if err != nil {
			t.Fatalf("EnvelopeBalance(%q): %v", id, err)
		}
		if got.MinorUnits() != want {
			t.Errorf("EnvelopeBalance(%q) = %d, want %d", id, got.MinorUnits(), want)
		}
	}
	sum, err := state.SumEnvelopeBalances()
	if err != nil {
		t.Fatalf("SumEnvelopeBalances: %v", err)
	}
	if sum.MinorUnits() != wantSum {
		t.Errorf("SumEnvelopeBalances = %d, want %d", sum.MinorUnits(), wantSum)
	}

	budgetable, err := state.BudgetableCash()
	if err != nil {
		t.Fatalf("BudgetableCash: %v", err)
	}
	if err := VerifyBudgetIdentity(budgetable, ready, sum); err != nil {
		t.Errorf("VerifyBudgetIdentity: %v", err)
	}
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestSetupAndBudgetEffects(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency

	tests := []struct {
		name               string
		effects            func(t *testing.T) (Effects, error)
		wantAccounts       map[AccountId]int64
		wantEnvelopes      map[EnvelopeId]int64
		wantReadyToAssign  int64
		wantLedgerEnvelope map[EnvelopeId]int64
	}{
		{
			name: "opening eligible cash",
			effects: func(t *testing.T) (Effects, error) {
				return OpeningBalanceEffects(f.checking, mustSignedDeltaForTest(t, 1_000, usd))
			},
			wantAccounts:       map[AccountId]int64{f.checking: 1_000},
			wantReadyToAssign:  1_000,
			wantLedgerEnvelope: map[EnvelopeId]int64{},
		},
		{
			name: "opening excluded cash",
			effects: func(t *testing.T) (Effects, error) {
				return OpeningBalanceEffects(f.excluded, mustSignedDeltaForTest(t, 2_000, usd))
			},
			wantAccounts:       map[AccountId]int64{f.excluded: 2_000},
			wantReadyToAssign:  0,
			wantLedgerEnvelope: map[EnvelopeId]int64{},
		},
		{
			name: "opening card liability",
			effects: func(t *testing.T) (Effects, error) {
				return OpeningBalanceEffects(f.card, mustSignedDeltaForTest(t, 3_000, usd))
			},
			wantAccounts:       map[AccountId]int64{f.card: 3_000},
			wantReadyToAssign:  0,
			wantLedgerEnvelope: map[EnvelopeId]int64{},
		},
		{
			name: "opening eligible cash negative",
			effects: func(t *testing.T) (Effects, error) {
				return OpeningBalanceEffects(f.checking, mustSignedDeltaForTest(t, -500, usd))
			},
			wantAccounts:       map[AccountId]int64{f.checking: -500},
			wantReadyToAssign:  -500,
			wantLedgerEnvelope: map[EnvelopeId]int64{},
		},
		{
			name: "income into eligible account",
			effects: func(t *testing.T) (Effects, error) {
				return IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 750, usd))
			},
			wantAccounts:       map[AccountId]int64{f.checking: 750},
			wantReadyToAssign:  750,
			wantLedgerEnvelope: map[EnvelopeId]int64{},
		},
		{
			name: "income into excluded account",
			effects: func(t *testing.T) (Effects, error) {
				return IncomeReceivedEffects(f.excluded, mustPositiveAmountForTest(t, 750, usd))
			},
			wantAccounts:       map[AccountId]int64{f.excluded: 750},
			wantReadyToAssign:  0,
			wantLedgerEnvelope: map[EnvelopeId]int64{},
		},
		{
			name: "unrelated reimbursement into eligible account",
			effects: func(t *testing.T) (Effects, error) {
				return ReimbursementReceivedEffects(f.checking, mustPositiveAmountForTest(t, 300, usd))
			},
			wantAccounts:       map[AccountId]int64{f.checking: 300},
			wantReadyToAssign:  300,
			wantLedgerEnvelope: map[EnvelopeId]int64{},
		},
		{
			name: "allocate to envelope",
			effects: func(t *testing.T) (Effects, error) {
				return AllocateFundsEffects(f.spend, mustPositiveAmountForTest(t, 400, usd))
			},
			wantEnvelopes:      map[EnvelopeId]int64{f.spend: 400},
			wantReadyToAssign:  -400,
			wantLedgerEnvelope: map[EnvelopeId]int64{f.spend: 400},
		},
		{
			name: "reallocate between envelopes",
			effects: func(t *testing.T) (Effects, error) {
				return ReallocateFundsEffects(f.spend, f.pay, mustPositiveAmountForTest(t, 250, usd))
			},
			wantEnvelopes:      map[EnvelopeId]int64{f.spend: -250, f.pay: 250},
			wantReadyToAssign:  0,
			wantLedgerEnvelope: map[EnvelopeId]int64{f.spend: -250, f.pay: 250},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effects, err := tt.effects(t)
			if err != nil {
				t.Fatalf("effects: %v", err)
			}
			assertEffectsShape(t, effects, tt.wantAccounts, tt.wantEnvelopes)
			applyAndAssertLedgerEffect(t, f.newLedger(t), effects, tt.wantReadyToAssign, tt.wantLedgerEnvelope, []EnvelopeId{f.spend, f.pay})
		})
	}
}

func TestSetupAndBudgetEffectsRejections(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency

	tests := []struct {
		name     string
		effects  func(t *testing.T) (Effects, error)
		wantCode ErrorCode
	}{
		{
			name: "opening zero account",
			effects: func(t *testing.T) (Effects, error) {
				return OpeningBalanceEffects("", mustSignedDeltaForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "opening zero delta",
			effects: func(t *testing.T) (Effects, error) {
				return OpeningBalanceEffects(f.checking, SignedMoneyDelta{})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "income zero account",
			effects: func(t *testing.T) (Effects, error) {
				return IncomeReceivedEffects("", mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "reimbursement zero account",
			effects: func(t *testing.T) (Effects, error) {
				return ReimbursementReceivedEffects("", mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "allocate zero envelope",
			effects: func(t *testing.T) (Effects, error) {
				return AllocateFundsEffects("", mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "reallocate same envelope",
			effects: func(t *testing.T) (Effects, error) {
				return ReallocateFundsEffects(f.spend, f.spend, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "reallocate zero source",
			effects: func(t *testing.T) (Effects, error) {
				return ReallocateFundsEffects("", f.pay, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "reallocate zero destination",
			effects: func(t *testing.T) (Effects, error) {
				return ReallocateFundsEffects(f.spend, "", mustPositiveAmountForTest(t, 100, usd))
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
