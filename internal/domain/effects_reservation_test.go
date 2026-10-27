package domain

import "testing"

// newFundedReservationLedger builds the shared setup fixture ledger and posts
// opening checking +10,000, spending envelope +3,000, and payment envelope
// +1,000. Budgetable cash is 10,000, the envelope sum is 4,000, and
// Ready-to-assign is 6,000 before any reservation.
func newFundedReservationLedger(t *testing.T, f *setupEffectsFixture) *LedgerState {
	t.Helper()
	state := f.newLedger(t)
	opening := PostingSet{}
	if err := opening.AddAccount(f.checking, mustSignedDeltaForTest(t, 10_000, f.currency)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := opening.AddEnvelope(f.spend, mustSignedDeltaForTest(t, 3_000, f.currency)); err != nil {
		t.Fatalf("AddEnvelope(spend): %v", err)
	}
	if err := opening.AddEnvelope(f.pay, mustSignedDeltaForTest(t, 1_000, f.currency)); err != nil {
		t.Fatalf("AddEnvelope(pay): %v", err)
	}
	if err := state.ApplyPostings(opening); err != nil {
		t.Fatalf("ApplyPostings(opening): %v", err)
	}
	return state
}

func TestCreateReservationEffectsShape(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	plan := mustPlanIDForTest(t, "plan-1")
	amount := mustPositiveAmountForTest(t, 1_000, usd)

	tests := []struct {
		name            string
		target          ReservationTarget
		wantAccountHold bool
		wantAccount     int64
	}{
		{
			name:   "planned card purchase holds spending envelope only",
			target: ReservationTarget{PlanID: plan, EnvelopeID: f.spend},
		},
		{
			name:            "planned debit purchase holds envelope and cash account",
			target:          ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking},
			wantAccountHold: true,
			wantAccount:     1_000,
		},
		{
			name:            "planned card repayment holds payment envelope and cash account",
			target:          ReservationTarget{PlanID: plan, EnvelopeID: f.pay, AccountID: f.checking},
			wantAccountHold: true,
			wantAccount:     1_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effects, err := CreateReservationEffects(tt.target, amount)
			if err != nil {
				t.Fatalf("CreateReservationEffects: %v", err)
			}
			if !effects.Postings.IsEmpty() {
				t.Errorf("Effects.Postings = %+v, want empty", effects.Postings)
			}
			if len(effects.Related) != 0 {
				t.Errorf("Effects.Related = %d, want 0", len(effects.Related))
			}
			if len(effects.Reservations) != 1 {
				t.Fatalf("Effects.Reservations = %d, want 1", len(effects.Reservations))
			}
			change := effects.Reservations[0]
			if change.PlanID != plan {
				t.Errorf("ReservationChange.PlanID = %q, want %q", change.PlanID, plan)
			}
			if change.Envelope.EnvelopeID != tt.target.EnvelopeID {
				t.Errorf("Envelope.EnvelopeID = %q, want %q", change.Envelope.EnvelopeID, tt.target.EnvelopeID)
			}
			if got := change.Envelope.Delta.MinorUnits(); got != 1_000 {
				t.Errorf("Envelope.Delta = %d, want 1000", got)
			}
			if !tt.wantAccountHold {
				if change.Account != nil {
					t.Errorf("Account = %+v, want nil", *change.Account)
				}
				return
			}
			if change.Account == nil {
				t.Fatal("Account = nil, want account hold")
			}
			if change.Account.AccountID != tt.target.AccountID {
				t.Errorf("Account.AccountID = %q, want %q", change.Account.AccountID, tt.target.AccountID)
			}
			if got := change.Account.Delta.MinorUnits(); got != tt.wantAccount {
				t.Errorf("Account.Delta = %d, want %d", got, tt.wantAccount)
			}
		})
	}
}

func TestCreateReservationLedgerEffect(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	plan := mustPlanIDForTest(t, "plan-1")
	amount := mustPositiveAmountForTest(t, 1_000, usd)

	tests := []struct {
		name            string
		target          ReservationTarget
		wantAccountHold bool
		wantEnvelope    EnvelopeId
		wantEnvelopeBal int64
	}{
		{
			name:            "planned card purchase",
			target:          ReservationTarget{PlanID: plan, EnvelopeID: f.spend},
			wantEnvelope:    f.spend,
			wantEnvelopeBal: 3_000,
		},
		{
			name:            "planned debit purchase",
			target:          ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking},
			wantAccountHold: true,
			wantEnvelope:    f.spend,
			wantEnvelopeBal: 3_000,
		},
		{
			name:            "planned card repayment",
			target:          ReservationTarget{PlanID: plan, EnvelopeID: f.pay, AccountID: f.checking},
			wantAccountHold: true,
			wantEnvelope:    f.pay,
			wantEnvelopeBal: 1_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effects, err := CreateReservationEffects(tt.target, amount)
			if err != nil {
				t.Fatalf("CreateReservationEffects: %v", err)
			}
			state := newFundedReservationLedger(t, f)
			if err := state.ApplyReservationChanges(effects.Reservations...); err != nil {
				t.Fatalf("ApplyReservationChanges: %v", err)
			}

			// Reservations reduce spendable balances only, never posted ones.
			spendableEnvelope, err := state.SpendableEnvelopeBalance(tt.wantEnvelope)
			if err != nil {
				t.Fatalf("SpendableEnvelopeBalance: %v", err)
			}
			if got := spendableEnvelope.MinorUnits(); got != tt.wantEnvelopeBal-1_000 {
				t.Errorf("SpendableEnvelopeBalance = %d, want %d", got, tt.wantEnvelopeBal-1_000)
			}
			assertMoney(t, state.EnvelopeBalance, tt.wantEnvelope, tt.wantEnvelopeBal)

			wantSpendableAccount := int64(10_000)
			if tt.wantAccountHold {
				wantSpendableAccount = 9_000
			}
			spendableAccount, err := state.SpendableAccountBalance(f.checking)
			if err != nil {
				t.Fatalf("SpendableAccountBalance: %v", err)
			}
			if got := spendableAccount.MinorUnits(); got != wantSpendableAccount {
				t.Errorf("SpendableAccountBalance = %d, want %d", got, wantSpendableAccount)
			}

			assertMoney(t, (*LedgerState).BudgetableCash, state, 10_000)
			assertMoney(t, (*LedgerState).ReadyToAssign, state, 6_000)
			assertMoney(t, (*LedgerState).SumEnvelopeBalances, state, 4_000)
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

func TestResizeReservationEffects(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	plan := mustPlanIDForTest(t, "plan-1")
	amount := mustPositiveAmountForTest(t, 1_000, usd)
	target := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}

	tests := []struct {
		name       string
		delta      int64
		wantHeld   int64
		wantHeldOn int64
	}{
		{name: "increase", delta: 500, wantHeld: 1_500, wantHeldOn: 1_500},
		{name: "decrease", delta: -400, wantHeld: 600, wantHeldOn: 600},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effects, err := ResizeReservationEffects(target, mustSignedDeltaForTest(t, tt.delta, usd))
			if err != nil {
				t.Fatalf("ResizeReservationEffects: %v", err)
			}
			if !effects.Postings.IsEmpty() {
				t.Errorf("Effects.Postings = %+v, want empty", effects.Postings)
			}
			if len(effects.Reservations) != 1 {
				t.Fatalf("Effects.Reservations = %d, want 1", len(effects.Reservations))
			}
			change := effects.Reservations[0]
			if got := change.Envelope.Delta.MinorUnits(); got != tt.delta {
				t.Errorf("Envelope.Delta = %d, want %d", got, tt.delta)
			}
			if change.Account == nil {
				t.Fatal("Account = nil, want account hold")
			}
			if got := change.Account.Delta.MinorUnits(); got != tt.delta {
				t.Errorf("Account.Delta = %d, want %d", got, tt.delta)
			}

			state := newFundedReservationLedger(t, f)
			create, err := CreateReservationEffects(target, amount)
			if err != nil {
				t.Fatalf("CreateReservationEffects: %v", err)
			}
			if err := state.ApplyReservationChanges(create.Reservations...); err != nil {
				t.Fatalf("ApplyReservationChanges(create): %v", err)
			}
			if err := state.ApplyReservationChanges(effects.Reservations...); err != nil {
				t.Fatalf("ApplyReservationChanges(resize): %v", err)
			}
			assertMoney(t, state.ActiveEnvelopeReservations, f.spend, tt.wantHeld)
			assertMoney(t, state.ActiveAccountReservations, f.checking, tt.wantHeldOn)

			// Posted balances are untouched by hold transitions.
			assertMoney(t, state.PostedAccountBalance, f.checking, 10_000)
			assertMoney(t, state.EnvelopeBalance, f.spend, 3_000)
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

func TestResizeReservationCannotDriveHoldNegative(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	plan := mustPlanIDForTest(t, "plan-1")
	target := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}

	state := newFundedReservationLedger(t, f)
	create, err := CreateReservationEffects(target, mustPositiveAmountForTest(t, 1_000, usd))
	if err != nil {
		t.Fatalf("CreateReservationEffects: %v", err)
	}
	if err := state.ApplyReservationChanges(create.Reservations...); err != nil {
		t.Fatalf("ApplyReservationChanges(create): %v", err)
	}

	resize, err := ResizeReservationEffects(target, mustSignedDeltaForTest(t, -2_000, usd))
	if err != nil {
		t.Fatalf("ResizeReservationEffects: %v", err)
	}
	err = state.ApplyReservationChanges(resize.Reservations...)
	if !HasCode(err, CodeInvalidAmount) {
		t.Fatalf("ApplyReservationChanges(resize) error = %v, want code %s", err, CodeInvalidAmount)
	}
	// The rejected batch leaves the ledger unchanged.
	assertMoney(t, state.ActiveEnvelopeReservations, f.spend, 1_000)
	assertMoney(t, state.ActiveAccountReservations, f.checking, 1_000)
}

func TestCancelReservationEffects(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	plan := mustPlanIDForTest(t, "plan-1")
	amount := mustPositiveAmountForTest(t, 1_000, usd)

	tests := []struct {
		name        string
		target      ReservationTarget
		wantAccount bool
	}{
		{
			name:        "debit target releases envelope and account",
			target:      ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking},
			wantAccount: true,
		},
		{
			name:   "card target releases envelope only",
			target: ReservationTarget{PlanID: plan, EnvelopeID: f.spend},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newFundedReservationLedger(t, f)
			create, err := CreateReservationEffects(tt.target, amount)
			if err != nil {
				t.Fatalf("CreateReservationEffects: %v", err)
			}
			if err := state.ApplyReservationChanges(create.Reservations...); err != nil {
				t.Fatalf("ApplyReservationChanges(create): %v", err)
			}

			cancel, err := CancelReservationEffects(tt.target, amount, amount)
			if err != nil {
				t.Fatalf("CancelReservationEffects: %v", err)
			}
			if !cancel.Postings.IsEmpty() {
				t.Errorf("Effects.Postings = %+v, want empty", cancel.Postings)
			}
			if len(cancel.Reservations) != 1 {
				t.Fatalf("Effects.Reservations = %d, want 1", len(cancel.Reservations))
			}
			change := cancel.Reservations[0]
			if got := change.Envelope.Delta.MinorUnits(); got != -1_000 {
				t.Errorf("Envelope.Delta = %d, want -1000", got)
			}
			if tt.wantAccount {
				if change.Account == nil {
					t.Fatal("Account = nil, want account hold")
				}
				if got := change.Account.Delta.MinorUnits(); got != -1_000 {
					t.Errorf("Account.Delta = %d, want -1000", got)
				}
			} else if change.Account != nil {
				t.Errorf("Account = %+v, want nil", *change.Account)
			}

			if err := state.ApplyReservationChanges(cancel.Reservations...); err != nil {
				t.Fatalf("ApplyReservationChanges(cancel): %v", err)
			}
			assertMoney(t, state.ActiveEnvelopeReservations, tt.target.EnvelopeID, 0)
			if tt.wantAccount {
				assertMoney(t, state.ActiveAccountReservations, f.checking, 0)
			}
			assertMoney(t, state.PostedAccountBalance, f.checking, 10_000)
			assertMoney(t, state.EnvelopeBalance, f.spend, 3_000)
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

func TestCompleteReservedPurchaseEffects(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	plan := mustPlanIDForTest(t, "plan-1")
	amount := mustPositiveAmountForTest(t, 1_000, usd)

	tests := []struct {
		name                 string
		target               ReservationTarget
		purchase             func(t *testing.T) Effects
		wantAccountBalances  map[AccountId]int64
		wantEnvelopeBalances map[EnvelopeId]int64
		wantCardLiability    int64
	}{
		{
			name:   "complete planned debit purchase",
			target: ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking},
			purchase: func(t *testing.T) Effects {
				purchase, err := DebitPurchaseEffects(f.checking, f.spend, amount)
				if err != nil {
					t.Fatalf("DebitPurchaseEffects: %v", err)
				}
				return purchase
			},
			wantAccountBalances:  map[AccountId]int64{f.checking: 9_000},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 2_000, f.pay: 1_000},
		},
		{
			name:   "complete planned card purchase",
			target: ReservationTarget{PlanID: plan, EnvelopeID: f.spend},
			purchase: func(t *testing.T) Effects {
				purchase, err := CreditCardPurchaseEffects(f.card, f.spend, f.pay, amount)
				if err != nil {
					t.Fatalf("CreditCardPurchaseEffects: %v", err)
				}
				return purchase
			},
			wantAccountBalances:  map[AccountId]int64{f.checking: 10_000},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 2_000, f.pay: 2_000},
			wantCardLiability:    1_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newFundedReservationLedger(t, f)
			create, err := CreateReservationEffects(tt.target, amount)
			if err != nil {
				t.Fatalf("CreateReservationEffects: %v", err)
			}
			if err := state.ApplyReservationChanges(create.Reservations...); err != nil {
				t.Fatalf("ApplyReservationChanges(create): %v", err)
			}

			complete, err := CompleteReservedPurchaseEffects(tt.target, tt.purchase(t), amount, amount)
			if err != nil {
				t.Fatalf("CompleteReservedPurchaseEffects: %v", err)
			}
			if len(complete.Reservations) != 1 {
				t.Fatalf("Effects.Reservations = %d, want 1", len(complete.Reservations))
			}
			change := complete.Reservations[0]
			if got := change.Envelope.Delta.MinorUnits(); got != -1_000 {
				t.Errorf("Envelope.Delta = %d, want -1000", got)
			}
			if !tt.target.AccountID.IsZero() {
				if change.Account == nil {
					t.Fatal("Account = nil, want account hold")
				}
				if got := change.Account.Delta.MinorUnits(); got != -1_000 {
					t.Errorf("Account.Delta = %d, want -1000", got)
				}
			}

			if err := state.ApplyPostings(complete.Postings); err != nil {
				t.Fatalf("ApplyPostings(complete): %v", err)
			}
			if err := state.ApplyReservationChanges(complete.Reservations...); err != nil {
				t.Fatalf("ApplyReservationChanges(complete): %v", err)
			}

			assertAccountBalances(t, state, tt.wantAccountBalances)
			assertEnvelopeBalances(t, state, tt.wantEnvelopeBalances)
			assertMoney(t, state.ActiveEnvelopeReservations, tt.target.EnvelopeID, 0)
			if !tt.target.AccountID.IsZero() {
				assertMoney(t, state.ActiveAccountReservations, tt.target.AccountID, 0)
			}
			if tt.wantCardLiability != 0 {
				assertMoney(t, state.CardLiabilityBalance, f.card, tt.wantCardLiability)
			}
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

func TestReservationEffectsRejections(t *testing.T) {
	f := newSetupEffectsFixture(t)
	usd := f.currency
	plan := mustPlanIDForTest(t, "plan-1")
	target := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}
	amount := mustPositiveAmountForTest(t, 100, usd)

	tests := []struct {
		name     string
		effects  func() (Effects, error)
		wantCode ErrorCode
	}{
		{
			name: "create zero plan",
			effects: func() (Effects, error) {
				return CreateReservationEffects(ReservationTarget{EnvelopeID: f.spend}, amount)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "create zero envelope",
			effects: func() (Effects, error) {
				return CreateReservationEffects(ReservationTarget{PlanID: plan}, amount)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "create zero amount",
			effects: func() (Effects, error) {
				return CreateReservationEffects(target, PositiveAmount{})
			},
			wantCode: CodeInvalidAmount,
		},
		{
			name: "resize zero plan",
			effects: func() (Effects, error) {
				return ResizeReservationEffects(ReservationTarget{EnvelopeID: f.spend}, mustSignedDeltaForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "resize zero envelope",
			effects: func() (Effects, error) {
				return ResizeReservationEffects(ReservationTarget{PlanID: plan}, mustSignedDeltaForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "resize zero delta",
			effects: func() (Effects, error) {
				return ResizeReservationEffects(target, SignedMoneyDelta{})
			},
			wantCode: CodeInvalidAmount,
		},
		{
			name: "cancel zero plan",
			effects: func() (Effects, error) {
				return CancelReservationEffects(ReservationTarget{EnvelopeID: f.spend}, amount, amount)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "cancel zero envelope",
			effects: func() (Effects, error) {
				return CancelReservationEffects(ReservationTarget{PlanID: plan}, amount, amount)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "complete zero plan",
			effects: func() (Effects, error) {
				return CompleteReservedPurchaseEffects(ReservationTarget{EnvelopeID: f.spend}, Effects{}, amount, amount)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "complete zero envelope",
			effects: func() (Effects, error) {
				return CompleteReservedPurchaseEffects(ReservationTarget{PlanID: plan}, Effects{}, amount, amount)
			},
			wantCode: CodeInvalidValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effects, err := tt.effects()
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("effects error = %v, want code %s", err, tt.wantCode)
			}
			if !effects.IsEmpty() {
				t.Errorf("rejected effects = %+v, want empty", effects)
			}
		})
	}
}
