package domain

import "testing"

// applyPlanEffectsForTest applies both halves of an effect set to a ledger.
func applyPlanEffectsForTest(t *testing.T, state *LedgerState, effects Effects) {
	t.Helper()
	if err := state.ApplyReservationChanges(effects.Reservations...); err != nil {
		t.Fatalf("ApplyReservationChanges: %v", err)
	}
	if err := state.ApplyPostings(effects.Postings); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}
}

// authorizePlanForTest authorizes a plan through the real authorization layer,
// applies its holds, and records the outcome on the aggregate.
func authorizePlanForTest(t *testing.T, state *LedgerState, plan *PlannedOperation) {
	t.Helper()
	outcome, err := AuthorizePurchase(state, plan.Target(), plan.IntendedAmount(), AuthorizationModeNormal, "")
	if err != nil {
		t.Fatalf("AuthorizePurchase: %v", err)
	}
	applyPlanEffectsForTest(t, state, outcome.Effects)
	accountHold := PositiveAmount{}
	if !plan.Target().AccountID.IsZero() {
		accountHold = plan.IntendedAmount()
	}
	if err := plan.Authorize(outcome, plan.IntendedAmount(), accountHold); err != nil {
		t.Fatalf("PlannedOperation.Authorize: %v", err)
	}
}

func TestPlanCompleteEffectsDebitLifecycle(t *testing.T) {
	f := newFundingFixture(t)
	state := authorizationLedgerForTest(t, f, 10_000, 3_000, 0)
	planID := mustPlanIDForTest(t, "plan-debit-lifecycle")
	target := ReservationTarget{PlanID: planID, EnvelopeID: f.spend, AccountID: f.checking}
	amount := mustPositiveAmountForTest(t, 1_000, f.currency)
	plan, err := NewPlannedOperation(planID, PlanKindPurchase, target, amount)
	if err != nil {
		t.Fatalf("NewPlannedOperation: %v", err)
	}

	authorizePlanForTest(t, state, plan)
	assertMoney(t, state.ActiveEnvelopeReservations, f.spend, 1_000)
	assertMoney(t, state.ActiveAccountReservations, f.checking, 1_000)
	assertMoney(t, state.SpendableEnvelopeBalance, f.spend, 2_000)
	assertMoney(t, state.SpendableAccountBalance, f.checking, 9_000)

	purchase, err := DebitPurchaseEffects(f.checking, f.spend, amount)
	if err != nil {
		t.Fatalf("DebitPurchaseEffects: %v", err)
	}
	effects, err := PlanCompleteEffects(plan, purchase)
	if err != nil {
		t.Fatalf("PlanCompleteEffects: %v", err)
	}
	if err := plan.Complete(amount, amount, amount); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	applyPlanEffectsForTest(t, state, effects)

	if plan.State() != PlanStateCompleted {
		t.Errorf("State = %s, want COMPLETED", plan.State())
	}
	assertMoney(t, state.PostedAccountBalance, f.checking, 9_000)
	assertMoney(t, state.EnvelopeBalance, f.spend, 2_000)
	assertMoney(t, state.ActiveEnvelopeReservations, f.spend, 0)
	assertMoney(t, state.ActiveAccountReservations, f.checking, 0)
	assertMoney(t, state.SpendableEnvelopeBalance, f.spend, 2_000)
	assertMoney(t, state.SpendableAccountBalance, f.checking, 9_000)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestPlanCancelEffectsLifecycle(t *testing.T) {
	f := newFundingFixture(t)
	state := authorizationLedgerForTest(t, f, 10_000, 3_000, 0)
	planID := mustPlanIDForTest(t, "plan-cancel-lifecycle")
	target := ReservationTarget{PlanID: planID, EnvelopeID: f.spend, AccountID: f.checking}
	amount := mustPositiveAmountForTest(t, 1_000, f.currency)
	plan, err := NewPlannedOperation(planID, PlanKindPurchase, target, amount)
	if err != nil {
		t.Fatalf("NewPlannedOperation: %v", err)
	}

	authorizePlanForTest(t, state, plan)
	if err := plan.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	effects, err := PlanCancelEffects(plan)
	if err != nil {
		t.Fatalf("PlanCancelEffects: %v", err)
	}
	applyPlanEffectsForTest(t, state, effects)

	if plan.State() != PlanStateCancelled {
		t.Errorf("State = %s, want CANCELLED", plan.State())
	}
	assertMoney(t, state.PostedAccountBalance, f.checking, 10_000)
	assertMoney(t, state.EnvelopeBalance, f.spend, 3_000)
	assertMoney(t, state.ActiveEnvelopeReservations, f.spend, 0)
	assertMoney(t, state.ActiveAccountReservations, f.checking, 0)
	assertMoney(t, state.SpendableEnvelopeBalance, f.spend, 3_000)
	assertMoney(t, state.SpendableAccountBalance, f.checking, 10_000)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestPlanCompleteEffectsRepaymentLifecycle(t *testing.T) {
	f := newFundingFixture(t)
	state := authorizationLedgerForTest(t, f, 10_000, 0, 3_000)
	// Start with outstanding card debt so the repayment settles real liability.
	applyFundingPostingsForTest(t, state, map[AccountId]int64{f.card: 2_000}, nil)

	planID := mustPlanIDForTest(t, "plan-repayment-lifecycle")
	target := ReservationTarget{PlanID: planID, EnvelopeID: f.pay, AccountID: f.checking}
	amount := mustPositiveAmountForTest(t, 1_000, f.currency)
	plan, err := NewPlannedOperation(planID, PlanKindRepayment, target, amount)
	if err != nil {
		t.Fatalf("NewPlannedOperation: %v", err)
	}
	if plan.Kind() != PlanKindRepayment {
		t.Fatalf("Kind = %s, want REPAYMENT", plan.Kind())
	}

	authorizePlanForTest(t, state, plan)
	// Q2: a repayment holds both the source cash account and the payment envelope.
	assertMoney(t, state.ActiveEnvelopeReservations, f.pay, 1_000)
	assertMoney(t, state.ActiveAccountReservations, f.checking, 1_000)
	assertMoney(t, state.SpendableEnvelopeBalance, f.pay, 2_000)
	assertMoney(t, state.SpendableAccountBalance, f.checking, 9_000)

	payment, err := CreditCardPaymentEffects(f.checking, f.card, f.pay, amount)
	if err != nil {
		t.Fatalf("CreditCardPaymentEffects: %v", err)
	}
	effects, err := PlanCompleteEffects(plan, payment)
	if err != nil {
		t.Fatalf("PlanCompleteEffects: %v", err)
	}
	if err := plan.Complete(amount, amount, amount); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	applyPlanEffectsForTest(t, state, effects)

	if plan.State() != PlanStateCompleted {
		t.Errorf("State = %s, want COMPLETED", plan.State())
	}
	// Cash leaves the account and the liability, and the payment envelope is spent.
	assertMoney(t, state.PostedAccountBalance, f.checking, 9_000)
	assertMoney(t, state.PostedAccountBalance, f.card, 1_000)
	assertMoney(t, state.EnvelopeBalance, f.pay, 2_000)
	assertMoney(t, state.ActiveEnvelopeReservations, f.pay, 0)
	assertMoney(t, state.ActiveAccountReservations, f.checking, 0)
	assertMoney(t, state.SpendableEnvelopeBalance, f.pay, 2_000)
	assertMoney(t, state.SpendableAccountBalance, f.checking, 9_000)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func TestPlanAdjustmentEffectsLifecycle(t *testing.T) {
	f := newFundingFixture(t)
	state := authorizationLedgerForTest(t, f, 10_000, 3_000, 0)
	planID := mustPlanIDForTest(t, "plan-adjust-lifecycle")
	target := ReservationTarget{PlanID: planID, EnvelopeID: f.spend, AccountID: f.checking}
	amount := mustPositiveAmountForTest(t, 1_000, f.currency)
	plan, err := NewPlannedOperation(planID, PlanKindPurchase, target, amount)
	if err != nil {
		t.Fatalf("NewPlannedOperation: %v", err)
	}
	authorizePlanForTest(t, state, plan)

	// Increase 1000 -> 1500.
	increased := mustPositiveAmountForTest(t, 1_500, f.currency)
	outcome, err := ReauthorizeReservation(state, target, amount, increased, AuthorizationModeNormal, "")
	if err != nil {
		t.Fatalf("ReauthorizeReservation(increase): %v", err)
	}
	if err := plan.Adjust(outcome, PlanStateReserved, increased, increased, increased); err != nil {
		t.Fatalf("Adjust(increase): %v", err)
	}
	increaseDelta := mustSignedDeltaForTest(t, 500, f.currency)
	effects, err := PlanAdjustmentEffects(plan, increaseDelta)
	if err != nil {
		t.Fatalf("PlanAdjustmentEffects(increase): %v", err)
	}
	applyPlanEffectsForTest(t, state, effects)
	assertMoney(t, state.ActiveEnvelopeReservations, f.spend, 1_500)
	assertMoney(t, state.ActiveAccountReservations, f.checking, 1_500)
	assertMoney(t, state.SpendableEnvelopeBalance, f.spend, 1_500)
	assertMoney(t, state.SpendableAccountBalance, f.checking, 8_500)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants after increase: %v", err)
	}

	// Decrease 1500 -> 1200 releases the excess.
	decreased := mustPositiveAmountForTest(t, 1_200, f.currency)
	outcome, err = ReauthorizeReservation(state, target, increased, decreased, AuthorizationModeNormal, "")
	if err != nil {
		t.Fatalf("ReauthorizeReservation(decrease): %v", err)
	}
	if err := plan.Adjust(outcome, PlanStateReserved, decreased, decreased, decreased); err != nil {
		t.Fatalf("Adjust(decrease): %v", err)
	}
	decreaseDelta := mustSignedDeltaForTest(t, -300, f.currency)
	effects, err = PlanAdjustmentEffects(plan, decreaseDelta)
	if err != nil {
		t.Fatalf("PlanAdjustmentEffects(decrease): %v", err)
	}
	applyPlanEffectsForTest(t, state, effects)
	assertMoney(t, state.ActiveEnvelopeReservations, f.spend, 1_200)
	assertMoney(t, state.ActiveAccountReservations, f.checking, 1_200)
	assertMoney(t, state.SpendableEnvelopeBalance, f.spend, 1_800)
	assertMoney(t, state.SpendableAccountBalance, f.checking, 8_800)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants after decrease: %v", err)
	}

	// A downgrade to DRAFT has no adjustment effects.
	if err := plan.Adjust(AuthorizationOutcome{}, PlanStateDraft, decreased, PositiveAmount{}, PositiveAmount{}); err != nil {
		t.Fatalf("Adjust(to draft): %v", err)
	}
	if _, err := PlanAdjustmentEffects(plan, decreaseDelta); !HasCode(err, CodeInvalidEntityState) {
		t.Fatalf("PlanAdjustmentEffects(draft) error = %v, want INVALID_ENTITY_STATE", err)
	}
}

func TestPlanChangeEffectsEmptyAndRejected(t *testing.T) {
	f := newFundingFixture(t)
	delta := mustSignedDeltaForTest(t, 100, f.currency)

	draft := newPlanInState(t, PlanStateDraft)
	effects, err := PlanCancelEffects(draft)
	if err != nil {
		t.Fatalf("PlanCancelEffects(draft): %v", err)
	}
	if !effects.IsEmpty() {
		t.Errorf("PlanCancelEffects(draft) = %+v, want empty", effects)
	}

	cancelledDraft := newPlanInState(t, PlanStateDraft)
	if err := cancelledDraft.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	effects, err = PlanCancelEffects(cancelledDraft)
	if err != nil {
		t.Fatalf("PlanCancelEffects(cancelled draft): %v", err)
	}
	if !effects.IsEmpty() {
		t.Errorf("PlanCancelEffects(cancelled draft) = %+v, want empty", effects)
	}

	tests := []struct {
		name     string
		call     func() (Effects, error)
		wantCode ErrorCode
	}{
		{
			name:     "adjust nil plan",
			call:     func() (Effects, error) { return PlanAdjustmentEffects(nil, delta) },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "adjust zero plan",
			call:     func() (Effects, error) { return PlanAdjustmentEffects(&PlannedOperation{}, delta) },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "adjust draft",
			call:     func() (Effects, error) { return PlanAdjustmentEffects(newPlanInState(t, PlanStateDraft), delta) },
			wantCode: CodeInvalidEntityState,
		},
		{
			name:     "adjust completed",
			call:     func() (Effects, error) { return PlanAdjustmentEffects(newPlanInState(t, PlanStateCompleted), delta) },
			wantCode: CodeInvalidEntityState,
		},
		{
			name:     "adjust cancelled",
			call:     func() (Effects, error) { return PlanAdjustmentEffects(newPlanInState(t, PlanStateCancelled), delta) },
			wantCode: CodeInvalidEntityState,
		},
		{
			name:     "cancel nil plan",
			call:     func() (Effects, error) { return PlanCancelEffects(nil) },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "cancel zero plan",
			call:     func() (Effects, error) { return PlanCancelEffects(&PlannedOperation{}) },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "cancel completed",
			call:     func() (Effects, error) { return PlanCancelEffects(newPlanInState(t, PlanStateCompleted)) },
			wantCode: CodeInvalidEntityState,
		},
		{
			name:     "complete nil plan",
			call:     func() (Effects, error) { return PlanCompleteEffects(nil, Effects{}) },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "complete zero plan",
			call:     func() (Effects, error) { return PlanCompleteEffects(&PlannedOperation{}, Effects{}) },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "complete draft",
			call:     func() (Effects, error) { return PlanCompleteEffects(newPlanInState(t, PlanStateDraft), Effects{}) },
			wantCode: CodeInvalidEntityState,
		},
		{
			name:     "complete cancelled",
			call:     func() (Effects, error) { return PlanCompleteEffects(newPlanInState(t, PlanStateCancelled), Effects{}) },
			wantCode: CodeInvalidEntityState,
		},
		{
			name:     "complete already completed",
			call:     func() (Effects, error) { return PlanCompleteEffects(newPlanInState(t, PlanStateCompleted), Effects{}) },
			wantCode: CodeInvalidEntityState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.call()
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("error = %v, want code %s", err, tt.wantCode)
			}
		})
	}
}
