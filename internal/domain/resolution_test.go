package domain

import "testing"

// deficitExpectation is the test-side view of one derived BudgetDeficit.
type deficitExpectation struct {
	kind       DeficitKind
	envelopeID EnvelopeId
	amount     int64
}

func assertResolutionDeficits(t *testing.T, state *LedgerState, want []deficitExpectation) {
	t.Helper()
	got, err := state.Deficits()
	if err != nil {
		t.Fatalf("Deficits: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Deficits = %+v, want %d deficit(s): %+v", got, len(want), want)
	}
	for i, expected := range want {
		if got[i].Kind != expected.kind {
			t.Errorf("Deficits[%d].Kind = %q, want %q", i, got[i].Kind, expected.kind)
		}
		if got[i].EnvelopeID != expected.envelopeID {
			t.Errorf("Deficits[%d].EnvelopeID = %q, want %q", i, got[i].EnvelopeID, expected.envelopeID)
		}
		if got[i].Amount.MinorUnits() != expected.amount {
			t.Errorf("Deficits[%d].Amount = %d minor units, want %d", i, got[i].Amount.MinorUnits(), expected.amount)
		}
		if got[i].Amount.IsNegative() {
			t.Errorf("Deficits[%d].Amount = %d minor units, want a positive magnitude", i, got[i].Amount.MinorUnits())
		}
	}
}

func assertResolutionStatus(t *testing.T, state *LedgerState, want ResolutionStatus) {
	t.Helper()
	got, err := state.ResolutionStatus()
	if err != nil {
		t.Fatalf("ResolutionStatus: %v", err)
	}
	if got != want {
		t.Fatalf("ResolutionStatus = %q, want %q", got, want)
	}
}

func applyFundingDebitForTest(t *testing.T, state *LedgerState, account AccountId, envelope EnvelopeId, minorUnits int64) {
	t.Helper()
	effects, err := DebitPurchaseEffects(account, envelope, mustPositiveAmountForTest(t, minorUnits, state.Currency()))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects: %v", err)
	}
	applyFundingEffectsForTest(t, state, effects)
}

func TestDeficits(t *testing.T) {
	f := newFundingFixture(t)

	tests := []struct {
		name   string
		build  func(t *testing.T, state *LedgerState)
		want   []deficitExpectation
		status ResolutionStatus
	}{
		{
			name: "fully funded",
			build: func(t *testing.T, state *LedgerState) {
				applyFundingPostingsForTest(t, state,
					map[AccountId]int64{f.checking: 10_000},
					map[EnvelopeId]int64{f.spend: 4_000, f.pay: 1_000},
				)
			},
			status: ResolutionClear,
		},
		{
			name: "negative ready to assign only",
			build: func(t *testing.T, state *LedgerState) {
				applyFundingPostingsForTest(t, state,
					map[AccountId]int64{f.checking: 1_000},
					map[EnvelopeId]int64{f.spend: 1_500},
				)
			},
			want:   []deficitExpectation{{kind: DeficitReadyToAssign, amount: 500}},
			status: ResolutionRequiresResolution,
		},
		{
			name: "negative envelope only",
			build: func(t *testing.T, state *LedgerState) {
				applyFundingPostingsForTest(t, state,
					map[AccountId]int64{f.checking: 1_000},
					map[EnvelopeId]int64{f.spend: 1_000},
				)
				applyFundingDebitForTest(t, state, f.checking, f.spend, 1_300)
			},
			want:   []deficitExpectation{{kind: DeficitEnvelope, envelopeID: f.spend, amount: 300}},
			status: ResolutionRequiresResolution,
		},
		{
			name: "both negative in deterministic order",
			build: func(t *testing.T, state *LedgerState) {
				applyFundingPostingsForTest(t, state,
					map[AccountId]int64{f.checking: 1_000},
					map[EnvelopeId]int64{f.spend: 2_000, f.pay: 1_000},
				)
				applyFundingDebitForTest(t, state, f.checking, f.spend, 2_500)
				applyFundingDebitForTest(t, state, f.checking, f.pay, 1_500)
			},
			want: []deficitExpectation{
				{kind: DeficitReadyToAssign, amount: 2_000},
				{kind: DeficitEnvelope, envelopeID: f.pay, amount: 500},
				{kind: DeficitEnvelope, envelopeID: f.spend, amount: 500},
			},
			status: ResolutionRequiresResolution,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := f.newLedger(t)
			tt.build(t, state)

			assertResolutionDeficits(t, state, tt.want)
			assertResolutionStatus(t, state, tt.status)

			// Negative balances are legitimate recorded outcomes; the budget
			// identity must still hold, so they are never integrity errors.
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

func TestDeficitClearing(t *testing.T) {
	f := newFundingFixture(t)

	t.Run("income clears negative ready to assign", func(t *testing.T) {
		state := f.newLedger(t)
		applyFundingPostingsForTest(t, state,
			map[AccountId]int64{f.checking: 1_000},
			map[EnvelopeId]int64{f.spend: 1_500},
		)
		assertResolutionDeficits(t, state, []deficitExpectation{{kind: DeficitReadyToAssign, amount: 500}})
		assertResolutionStatus(t, state, ResolutionRequiresResolution)

		income, err := IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 500, f.currency))
		if err != nil {
			t.Fatalf("IncomeReceivedEffects: %v", err)
		}
		applyFundingEffectsForTest(t, state, income)

		assertResolutionDeficits(t, state, nil)
		assertResolutionStatus(t, state, ResolutionClear)
	})

	t.Run("income and allocation clear negative envelope", func(t *testing.T) {
		state := f.newLedger(t)
		applyFundingPostingsForTest(t, state,
			map[AccountId]int64{f.checking: 1_000},
			map[EnvelopeId]int64{f.spend: 1_000},
		)
		applyFundingDebitForTest(t, state, f.checking, f.spend, 1_300)
		assertResolutionDeficits(t, state, []deficitExpectation{{kind: DeficitEnvelope, envelopeID: f.spend, amount: 300}})

		income, err := IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 300, f.currency))
		if err != nil {
			t.Fatalf("IncomeReceivedEffects: %v", err)
		}
		applyFundingEffectsForTest(t, state, income)

		// Funding Ready-to-assign alone does not clear the negative envelope.
		assertResolutionDeficits(t, state, []deficitExpectation{{kind: DeficitEnvelope, envelopeID: f.spend, amount: 300}})
		assertResolutionStatus(t, state, ResolutionRequiresResolution)

		allocate, err := AllocateFundsEffects(f.spend, mustPositiveAmountForTest(t, 300, f.currency))
		if err != nil {
			t.Fatalf("AllocateFundsEffects: %v", err)
		}
		applyFundingEffectsForTest(t, state, allocate)

		assertResolutionDeficits(t, state, nil)
		assertResolutionStatus(t, state, ResolutionClear)
	})
}

func TestReservationsNeverCreateDeficits(t *testing.T) {
	f := newFundingFixture(t)

	state := f.newLedger(t)
	applyFundingPostingsForTest(t, state,
		map[AccountId]int64{f.checking: 1_000},
		map[EnvelopeId]int64{f.spend: 500},
	)
	holdFundingForTest(t, state, ReservationTarget{
		PlanID:     mustPlanIDForTest(t, "plan-negative-spendable"),
		EnvelopeID: f.spend,
		AccountID:  f.checking,
	}, 1_200)

	// Reservations may drive spendable amounts negative; posted balances and
	// the budget identity stay non-negative and unchanged.
	assertMoney(t, state.SpendableEnvelopeBalance, f.spend, -700)
	assertMoney(t, state.SpendableAccountBalance, f.checking, -200)

	assertResolutionDeficits(t, state, nil)
	assertResolutionStatus(t, state, ResolutionClear)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestCardPaymentEnvelopeDeficit(t *testing.T) {
	f := newFundingFixture(t)

	state := f.newLedger(t)
	applyFundingPostingsForTest(t, state,
		map[AccountId]int64{f.checking: 10_000},
		map[EnvelopeId]int64{f.spend: 1_000},
	)

	amount := mustPositiveAmountForTest(t, 1_000, f.currency)
	purchase, err := CreditCardPurchaseEffects(f.card, f.spend, f.pay, amount)
	if err != nil {
		t.Fatalf("CreditCardPurchaseEffects: %v", err)
	}
	applyFundingEffectsForTest(t, state, purchase)

	payment, err := CreditCardPaymentEffects(f.checking, f.card, f.pay, amount)
	if err != nil {
		t.Fatalf("CreditCardPaymentEffects: %v", err)
	}
	applyFundingEffectsForTest(t, state, payment)

	refund, err := CardRefundEffects(f.card, mustJournalOpIDForTest(t, "op-card-purchase"), f.spend, f.pay, amount)
	if err != nil {
		t.Fatalf("CardRefundEffects: %v", err)
	}
	applyFundingEffectsForTest(t, state, refund)

	assertMoney(t, state.EnvelopeBalance, f.pay, -1_000)
	assertMoney(t, state.EnvelopeBalance, f.spend, 1_000)

	// A negative card-payment envelope is reported like any envelope deficit;
	// there is no special casing.
	assertResolutionDeficits(t, state, []deficitExpectation{{kind: DeficitEnvelope, envelopeID: f.pay, amount: 1_000}})
	assertResolutionStatus(t, state, ResolutionRequiresResolution)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}
