package domain

import (
	"reflect"
	"testing"
)

// newReconciliationLedger builds the shared correction fixture ledger and
// funds it with income 5,000 plus allocations of 1,000 each to the spend and
// pay envelopes. Opening state: checking 5,000, spend 1,000, pay 1,000,
// budgetable cash 5,000, envelope sum 2,000, Ready-to-assign 3,000.
func newReconciliationLedger(t *testing.T, f *correctionFixture) *LedgerState {
	t.Helper()
	state := f.newLedger(t)
	income, err := IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 5_000, f.currency))
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

func TestReconciliationAdjustmentEffects(t *testing.T) {
	f := newCorrectionFixture(t)
	usd := f.currency
	zero := mustMoney(t, 0, usd)

	tests := []struct {
		name                 string
		delta                int64
		funding              FundingSource
		wantAccountBalances  map[AccountId]int64
		wantEnvelopeBalances map[EnvelopeId]int64
		wantBudgetable       int64
		wantReady            int64
		wantEnvelopeSum      int64
		wantNegativeReady    bool
	}{
		{
			name:                 "increase default absorbed by ready to assign",
			delta:                500,
			funding:              FundingSource{FromReady: mustMoney(t, 500, usd)},
			wantAccountBalances:  map[AccountId]int64{f.checking: 5_500, f.savings: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 1_000, f.pay: 1_000},
			wantBudgetable:       5_500,
			wantReady:            3_500,
			wantEnvelopeSum:      2_000,
		},
		{
			name:                 "increase attributed to one envelope",
			delta:                500,
			funding:              FundingSource{FromReady: zero, FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, 500, usd)}}},
			wantAccountBalances:  map[AccountId]int64{f.checking: 5_500, f.savings: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 1_500, f.pay: 1_000},
			wantBudgetable:       5_500,
			wantReady:            3_000,
			wantEnvelopeSum:      2_500,
		},
		{
			name:                 "decrease default absorbed by ready to assign",
			delta:                -500,
			funding:              FundingSource{FromReady: mustMoney(t, -500, usd)},
			wantAccountBalances:  map[AccountId]int64{f.checking: 4_500, f.savings: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 1_000, f.pay: 1_000},
			wantBudgetable:       4_500,
			wantReady:            2_500,
			wantEnvelopeSum:      2_000,
		},
		{
			name:                 "decrease drives ready to assign negative",
			delta:                -3_500,
			funding:              FundingSource{FromReady: mustMoney(t, -3_500, usd)},
			wantAccountBalances:  map[AccountId]int64{f.checking: 1_500, f.savings: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 1_000, f.pay: 1_000},
			wantBudgetable:       1_500,
			wantReady:            -500,
			wantEnvelopeSum:      2_000,
			wantNegativeReady:    true,
		},
		{
			name:                 "decrease attributed to one envelope",
			delta:                -500,
			funding:              FundingSource{FromReady: zero, FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, -500, usd)}}},
			wantAccountBalances:  map[AccountId]int64{f.checking: 4_500, f.savings: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 500, f.pay: 1_000},
			wantBudgetable:       4_500,
			wantReady:            3_000,
			wantEnvelopeSum:      1_500,
		},
		{
			name:                 "decrease attributed to envelope may drive it negative",
			delta:                -1_500,
			funding:              FundingSource{FromReady: zero, FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, -1_500, usd)}}},
			wantAccountBalances:  map[AccountId]int64{f.checking: 3_500, f.savings: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: -500, f.pay: 1_000},
			wantBudgetable:       3_500,
			wantReady:            3_000,
			wantEnvelopeSum:      500,
		},
		{
			name:                 "increase mixed funding with exact residual",
			delta:                500,
			funding:              FundingSource{FromReady: mustMoney(t, 200, usd), FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, 300, usd)}}},
			wantAccountBalances:  map[AccountId]int64{f.checking: 5_500, f.savings: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 1_300, f.pay: 1_000},
			wantBudgetable:       5_500,
			wantReady:            3_200,
			wantEnvelopeSum:      2_300,
		},
		{
			name:                 "decrease mixed funding with exact residual",
			delta:                -500,
			funding:              FundingSource{FromReady: mustMoney(t, -200, usd), FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.pay, Amount: mustMoney(t, -300, usd)}}},
			wantAccountBalances:  map[AccountId]int64{f.checking: 4_500, f.savings: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 1_000, f.pay: 700},
			wantBudgetable:       4_500,
			wantReady:            2_800,
			wantEnvelopeSum:      1_700,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newReconciliationLedger(t, f)
			effects, err := ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, tt.delta, usd), tt.funding)
			if err != nil {
				t.Fatalf("ReconciliationAdjustmentEffects: %v", err)
			}
			if effects.IsEmpty() {
				t.Fatal("Effects.IsEmpty() = true, want false")
			}
			if len(effects.Related) != 0 {
				t.Errorf("Effects.Related = %d, want 0", len(effects.Related))
			}
			applyCorrectionEffectsForTest(t, state, effects)

			assertAccountBalances(t, state, tt.wantAccountBalances)
			assertEnvelopeBalances(t, state, tt.wantEnvelopeBalances)
			assertMoney(t, (*LedgerState).BudgetableCash, state, tt.wantBudgetable)
			assertMoney(t, (*LedgerState).ReadyToAssign, state, tt.wantReady)
			assertMoney(t, (*LedgerState).SumEnvelopeBalances, state, tt.wantEnvelopeSum)

			budgetable, err := state.BudgetableCash()
			if err != nil {
				t.Fatalf("BudgetableCash: %v", err)
			}
			ready, err := state.ReadyToAssign()
			if err != nil {
				t.Fatalf("ReadyToAssign: %v", err)
			}
			envelopeSum, err := state.SumEnvelopeBalances()
			if err != nil {
				t.Fatalf("SumEnvelopeBalances: %v", err)
			}
			if err := VerifyBudgetIdentity(budgetable, ready, envelopeSum); err != nil {
				t.Errorf("VerifyBudgetIdentity: %v", err)
			}
			if got := ready.IsNegative(); got != tt.wantNegativeReady {
				t.Errorf("ReadyToAssign().IsNegative() = %v, want %v", got, tt.wantNegativeReady)
			}
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

func TestReconciliationAdjustmentEffectsRejections(t *testing.T) {
	f := newCorrectionFixture(t)
	usd := f.currency
	zero := mustMoney(t, 0, usd)

	tests := []struct {
		name     string
		effects  func(t *testing.T) (Effects, error)
		wantCode ErrorCode
	}{
		{
			name: "zero account",
			effects: func(t *testing.T) (Effects, error) {
				return ReconciliationAdjustmentEffects("", mustSignedDeltaForTest(t, 500, usd), FundingSource{FromReady: mustMoney(t, 500, usd)})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "ready residual mismatch",
			effects: func(t *testing.T) (Effects, error) {
				return ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{
					FromReady:     mustMoney(t, 200, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, 100, usd)}},
				})
			},
			wantCode: CodeInvalidAmount,
		},
		{
			name: "only ready funding leaves residual",
			effects: func(t *testing.T) (Effects, error) {
				return ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{FromReady: zero})
			},
			wantCode: CodeInvalidAmount,
		},
		{
			name: "envelope contribution sign opposite to delta",
			effects: func(t *testing.T) (Effects, error) {
				return ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{
					FromReady:     zero,
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, -500, usd)}},
				})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "ready portion sign opposite to delta",
			effects: func(t *testing.T) (Effects, error) {
				return ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{FromReady: mustMoney(t, -500, usd)})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "zero-value ready money",
			effects: func(t *testing.T) (Effects, error) {
				return ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "zero-value envelope money",
			effects: func(t *testing.T) (Effects, error) {
				return ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{
					FromReady:     mustMoney(t, 500, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: Money{}}},
				})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "zero envelope amount",
			effects: func(t *testing.T) (Effects, error) {
				return ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{
					FromReady:     mustMoney(t, 500, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: zero}},
				})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "zero envelope id",
			effects: func(t *testing.T) (Effects, error) {
				return ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{
					FromReady:     zero,
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: "", Amount: mustMoney(t, 500, usd)}},
				})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "duplicate envelope funding",
			effects: func(t *testing.T) (Effects, error) {
				return ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{
					FromReady: zero,
					FromEnvelopes: []EnvelopeFunding{
						{EnvelopeID: f.spend, Amount: mustMoney(t, 300, usd)},
						{EnvelopeID: f.spend, Amount: mustMoney(t, 200, usd)},
					},
				})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "zero-value signed delta",
			effects: func(t *testing.T) (Effects, error) {
				return ReconciliationAdjustmentEffects(f.checking, SignedMoneyDelta{}, FundingSource{FromReady: zero})
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

// reconciliationLedgerSnapshot captures every observable ledger query so the
// no-op rows can prove they change nothing.
type reconciliationLedgerSnapshot struct {
	accounts          map[AccountId]int64
	envelopes         map[EnvelopeId]int64
	accountReserved   map[AccountId]int64
	envelopeReserved  map[EnvelopeId]int64
	accountSpendable  map[AccountId]int64
	envelopeSpendable map[EnvelopeId]int64
	budgetableCash    int64
	readyToAssign     int64
	envelopeSum       int64
}

func snapshotReconciliationLedger(t *testing.T, f *correctionFixture, state *LedgerState) reconciliationLedgerSnapshot {
	t.Helper()
	snapshot := reconciliationLedgerSnapshot{
		accounts:          make(map[AccountId]int64),
		envelopes:         make(map[EnvelopeId]int64),
		accountReserved:   make(map[AccountId]int64),
		envelopeReserved:  make(map[EnvelopeId]int64),
		accountSpendable:  make(map[AccountId]int64),
		envelopeSpendable: make(map[EnvelopeId]int64),
	}
	for _, id := range []AccountId{f.checking, f.savings} {
		posted, err := state.PostedAccountBalance(id)
		if err != nil {
			t.Fatalf("PostedAccountBalance(%q): %v", id, err)
		}
		reserved, err := state.ActiveAccountReservations(id)
		if err != nil {
			t.Fatalf("ActiveAccountReservations(%q): %v", id, err)
		}
		spendable, err := state.SpendableAccountBalance(id)
		if err != nil {
			t.Fatalf("SpendableAccountBalance(%q): %v", id, err)
		}
		snapshot.accounts[id] = posted.MinorUnits()
		snapshot.accountReserved[id] = reserved.MinorUnits()
		snapshot.accountSpendable[id] = spendable.MinorUnits()
	}
	for _, id := range []EnvelopeId{f.spend, f.pay} {
		balance, err := state.EnvelopeBalance(id)
		if err != nil {
			t.Fatalf("EnvelopeBalance(%q): %v", id, err)
		}
		reserved, err := state.ActiveEnvelopeReservations(id)
		if err != nil {
			t.Fatalf("ActiveEnvelopeReservations(%q): %v", id, err)
		}
		spendable, err := state.SpendableEnvelopeBalance(id)
		if err != nil {
			t.Fatalf("SpendableEnvelopeBalance(%q): %v", id, err)
		}
		snapshot.envelopes[id] = balance.MinorUnits()
		snapshot.envelopeReserved[id] = reserved.MinorUnits()
		snapshot.envelopeSpendable[id] = spendable.MinorUnits()
	}
	budgetable, err := state.BudgetableCash()
	if err != nil {
		t.Fatalf("BudgetableCash: %v", err)
	}
	ready, err := state.ReadyToAssign()
	if err != nil {
		t.Fatalf("ReadyToAssign: %v", err)
	}
	envelopeSum, err := state.SumEnvelopeBalances()
	if err != nil {
		t.Fatalf("SumEnvelopeBalances: %v", err)
	}
	snapshot.budgetableCash = budgetable.MinorUnits()
	snapshot.readyToAssign = ready.MinorUnits()
	snapshot.envelopeSum = envelopeSum.MinorUnits()
	return snapshot
}

func TestReconciliationNoOpEffects(t *testing.T) {
	f := newCorrectionFixture(t)

	tests := []struct {
		name    string
		effects func() Effects
	}{
		{name: "monthly boundary and rollover", effects: MonthlyBoundaryEffects},
		{name: "suggestion lifecycle", effects: SuggestionLifecycleEffects},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effects := tt.effects()
			if !effects.IsEmpty() {
				t.Fatalf("Effects = %+v, want empty", effects)
			}
			if !effects.Postings.IsEmpty() {
				t.Errorf("Effects.Postings = %+v, want empty", effects.Postings)
			}
			if len(effects.Reservations) != 0 {
				t.Errorf("Effects.Reservations = %d, want 0", len(effects.Reservations))
			}
			if len(effects.Related) != 0 {
				t.Errorf("Effects.Related = %d, want 0", len(effects.Related))
			}

			state := newReconciliationLedger(t, f)
			before := snapshotReconciliationLedger(t, f, state)
			applyCorrectionEffectsForTest(t, state, effects)
			after := snapshotReconciliationLedger(t, f, state)
			if !reflect.DeepEqual(before, after) {
				t.Errorf("ledger changed after no-op effects:\nbefore = %+v\nafter  = %+v", before, after)
			}
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}
