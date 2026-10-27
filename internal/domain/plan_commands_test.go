package domain

import "testing"

// ---------------------------------------------------------------- helpers

func assertPlanCommandState(t *testing.T, plan *PlannedOperation, want PlanState) {
	t.Helper()
	if plan == nil {
		t.Fatalf("plan is nil, want state %s", want)
	}
	if plan.State() != want {
		t.Errorf("plan %q state = %s, want %s", plan.ID(), plan.State(), want)
	}
}

func assertSpendableEnvelope(t *testing.T, state *LedgerState, id EnvelopeId, wantMinor int64) {
	t.Helper()
	balance, err := state.SpendableEnvelopeBalance(id)
	if err != nil {
		t.Fatalf("SpendableEnvelopeBalance(%q): %v", id, err)
	}
	if balance.MinorUnits() != wantMinor {
		t.Errorf("spendable envelope %q = %d, want %d", id, balance.MinorUnits(), wantMinor)
	}
}

func assertSpendableAccount(t *testing.T, state *LedgerState, id AccountId, wantMinor int64) {
	t.Helper()
	balance, err := state.SpendableAccountBalance(id)
	if err != nil {
		t.Fatalf("SpendableAccountBalance(%q): %v", id, err)
	}
	if balance.MinorUnits() != wantMinor {
		t.Errorf("spendable account %q = %d, want %d", id, balance.MinorUnits(), wantMinor)
	}
}

func assertActiveReservations(t *testing.T, state *LedgerState, envelope EnvelopeId, account AccountId, wantMinor int64) {
	t.Helper()
	envelopeHeld, err := state.ActiveEnvelopeReservations(envelope)
	if err != nil {
		t.Fatalf("ActiveEnvelopeReservations(%q): %v", envelope, err)
	}
	if envelopeHeld.MinorUnits() != wantMinor {
		t.Errorf("envelope %q reservation = %d, want %d", envelope, envelopeHeld.MinorUnits(), wantMinor)
	}
	if account.IsZero() {
		return
	}
	accountHeld, err := state.ActiveAccountReservations(account)
	if err != nil {
		t.Fatalf("ActiveAccountReservations(%q): %v", account, err)
	}
	if accountHeld.MinorUnits() != wantMinor {
		t.Errorf("account %q reservation = %d, want %d", account, accountHeld.MinorUnits(), wantMinor)
	}
}

func (f *commandFixture) mustCreatePlan(t *testing.T, id PlanId, kind PlanKind, target ReservationTarget, minorUnits int64) *PlannedOperation {
	t.Helper()
	plan, err := f.service.CreatePurchasePlan(f.actor, id, kind, target, mustPositiveAmountForTest(t, minorUnits, f.currency))
	if err != nil {
		t.Fatalf("CreatePurchasePlan(%s): %v", id, err)
	}
	return plan
}

func (f *commandFixture) mustAuthorizePlan(t *testing.T, key string, id PlanId, mode AuthorizationMode) (*PlannedOperation, JournalOperation) {
	t.Helper()
	plan, op, err := f.service.AuthorizePurchasePlan(f.envelope(t, key), id, mode)
	if err != nil {
		t.Fatalf("AuthorizePurchasePlan(%s): %v", id, err)
	}
	return plan, op
}

func (f *commandFixture) overrideEnvelope(t *testing.T, key, reason string) CommandEnvelope {
	t.Helper()
	env := f.envelope(t, key)
	env.Reason = reason
	return env
}

// ------------------------------------------------------------------ debit

func TestPlanDebitPurchaseLifecycle(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "alloc-spend", f.spend, 5_000)

	plan := f.mustCreatePlan(t, "plan-debit", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend, AccountID: f.checking}, 3_000)
	assertPlanCommandState(t, plan, PlanStateDraft)
	assertVersion(t, f, 2)

	authorized, authOp := f.mustAuthorizePlan(t, "auth-debit", "plan-debit", AuthorizationModeNormal)
	assertPlanCommandState(t, authorized, PlanStateReserved)
	if authOp.Type() != OpReservationCreate {
		t.Errorf("authorize operation type = %q, want %q", authOp.Type(), OpReservationCreate)
	}
	state := f.ledger(t)
	assertSpendableEnvelope(t, state, f.spend, 2_000)
	assertSpendableAccount(t, state, f.checking, 7_000)
	assertActiveReservations(t, state, f.spend, f.checking, 3_000)
	assertVersion(t, f, 3)

	completed, completeOp, err := f.service.CompletePurchasePlan(f.envelope(t, "complete-debit"), "plan-debit", mustPositiveAmountForTest(t, 3_000, f.currency), PurchaseKindDebit, "", "")
	if err != nil {
		t.Fatalf("CompletePurchasePlan: %v", err)
	}
	assertPlanCommandState(t, completed, PlanStateCompleted)
	if completeOp.Type() != OpPurchaseComplete {
		t.Errorf("completion operation type = %q, want %q", completeOp.Type(), OpPurchaseComplete)
	}
	assertAccountPosting(t, completeOp, f.checking, -3_000)
	assertEnvelopePosting(t, completeOp, f.spend, -3_000)

	state = f.ledger(t)
	assertAccountBalance(t, state, f.checking, 7_000)
	assertEnvelopeBalance(t, state, f.spend, 2_000)
	assertActiveReservations(t, state, f.spend, f.checking, 0)
	assertSpendableEnvelope(t, state, f.spend, 2_000)
	assertSpendableAccount(t, state, f.checking, 7_000)
	assertInvariants(t, state)
	assertVersion(t, f, 4)
}

// ------------------------------------------------------------------- card

func TestPlanCardPurchaseLifecycle(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "alloc-spend", f.spend, 5_000)

	f.mustCreatePlan(t, "plan-card", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend}, 3_000)
	authorized, _ := f.mustAuthorizePlan(t, "auth-card", "plan-card", AuthorizationModeNormal)
	assertPlanCommandState(t, authorized, PlanStateReserved)

	state := f.ledger(t)
	// Q1: only the spending envelope is held at authorization.
	assertSpendableEnvelope(t, state, f.spend, 2_000)
	assertActiveReservations(t, state, f.spend, "", 3_000)
	payHeld, err := state.ActiveEnvelopeReservations(f.pay)
	if err != nil {
		t.Fatalf("ActiveEnvelopeReservations(pay): %v", err)
	}
	if !payHeld.IsZero() {
		t.Errorf("card-payment envelope reservation = %d at authorization, want 0", payHeld.MinorUnits())
	}
	cardHeld, err := state.ActiveAccountReservations(f.card)
	if err != nil {
		t.Fatalf("ActiveAccountReservations(card): %v", err)
	}
	if !cardHeld.IsZero() {
		t.Errorf("card account reservation = %d at authorization, want 0", cardHeld.MinorUnits())
	}

	completed, op, err := f.service.CompletePurchasePlan(f.envelope(t, "complete-card"), "plan-card", mustPositiveAmountForTest(t, 3_000, f.currency), PurchaseKindCard, f.card, f.pay)
	if err != nil {
		t.Fatalf("CompletePurchasePlan: %v", err)
	}
	assertPlanCommandState(t, completed, PlanStateCompleted)
	if op.Type() != OpPurchaseComplete {
		t.Errorf("completion operation type = %q, want %q", op.Type(), OpPurchaseComplete)
	}
	assertAccountPosting(t, op, f.card, 3_000)
	assertEnvelopePosting(t, op, f.spend, -3_000)
	assertEnvelopePosting(t, op, f.pay, 3_000)

	state = f.ledger(t)
	liability, err := state.CardLiabilityBalance(f.card)
	if err != nil {
		t.Fatalf("CardLiabilityBalance: %v", err)
	}
	if liability.MinorUnits() != 3_000 {
		t.Errorf("card liability = %d, want 3000", liability.MinorUnits())
	}
	assertEnvelopeBalance(t, state, f.spend, 2_000)
	assertEnvelopeBalance(t, state, f.pay, 3_000)
	assertActiveReservations(t, state, f.spend, "", 0)
	assertInvariants(t, state)
}

// -------------------------------------------------------------- repayment

func TestPlanRepaymentLifecycle(t *testing.T) {
	f := newCommandFixture(t)
	opening, err := NewSignedMoneyDelta(3_000, f.currency)
	if err != nil {
		t.Fatalf("NewSignedMoneyDelta: %v", err)
	}
	if _, err := f.service.RecordOpeningBalance(f.envelope(t, "opening-card"), f.card, opening); err != nil {
		t.Fatalf("RecordOpeningBalance: %v", err)
	}
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "alloc-pay", f.pay, 4_000)

	f.mustCreatePlan(t, "plan-repay", PlanKindRepayment, ReservationTarget{EnvelopeID: f.pay, AccountID: f.checking}, 2_000)
	authorized, _ := f.mustAuthorizePlan(t, "auth-repay", "plan-repay", AuthorizationModeNormal)
	assertPlanCommandState(t, authorized, PlanStateReserved)

	state := f.ledger(t)
	// Q2: both the payment envelope and the source cash account are held.
	assertActiveReservations(t, state, f.pay, f.checking, 2_000)
	assertSpendableEnvelope(t, state, f.pay, 2_000)
	assertSpendableAccount(t, state, f.checking, 8_000)

	completed, op, err := f.service.CompletePurchasePlan(f.envelope(t, "complete-repay"), "plan-repay", mustPositiveAmountForTest(t, 2_000, f.currency), PurchaseKindRepayment, f.card, "")
	if err != nil {
		t.Fatalf("CompletePurchasePlan: %v", err)
	}
	assertPlanCommandState(t, completed, PlanStateCompleted)
	if op.Type() != OpPurchaseComplete {
		t.Errorf("completion operation type = %q, want %q", op.Type(), OpPurchaseComplete)
	}
	assertAccountPosting(t, op, f.checking, -2_000)
	assertAccountPosting(t, op, f.card, -2_000)
	assertEnvelopePosting(t, op, f.pay, -2_000)
	spendDelta, err := op.Postings().EnvelopeDelta(f.spend)
	if err != nil {
		t.Fatalf("EnvelopeDelta(spend): %v", err)
	}
	if !spendDelta.IsZero() {
		t.Errorf("repayment posted a spending-envelope delta of %d, want 0", spendDelta.MinorUnits())
	}

	state = f.ledger(t)
	assertAccountBalance(t, state, f.checking, 8_000)
	assertEnvelopeBalance(t, state, f.pay, 2_000)
	liability, err := state.CardLiabilityBalance(f.card)
	if err != nil {
		t.Fatalf("CardLiabilityBalance: %v", err)
	}
	if liability.MinorUnits() != 1_000 {
		t.Errorf("card liability = %d, want 1000", liability.MinorUnits())
	}
	assertActiveReservations(t, state, f.pay, f.checking, 0)
	assertInvariants(t, state)
}

// ------------------------------------------------------------ underfunded

func TestPlanAuthorizeUnderfundedIsAtomic(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 1_000)
	f.mustAllocate(t, "alloc-spend", f.spend, 500)

	f.mustCreatePlan(t, "plan-underfunded", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend, AccountID: f.checking}, 1_000)
	_, _, err := f.service.AuthorizePurchasePlan(f.envelope(t, "auth-underfunded"), "plan-underfunded", AuthorizationModeNormal)
	if !HasCode(err, CodeInsufficientEnvelopeFunds) {
		t.Fatalf("AuthorizePurchasePlan error = %v, want INSUFFICIENT_ENVELOPE_FUNDS", err)
	}

	plan, err := f.service.GetPurchasePlan("plan-underfunded")
	if err != nil {
		t.Fatalf("GetPurchasePlan: %v", err)
	}
	assertPlanCommandState(t, plan, PlanStateDraft)
	state := f.ledger(t)
	assertActiveReservations(t, state, f.spend, f.checking, 0)
	assertSpendableEnvelope(t, state, f.spend, 500)
	assertVersion(t, f, 2)
	if _, err := f.service.Idempotency().Lookup(f.envelope(t, "auth-underfunded").Key); !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
		t.Errorf("idempotency record after failed authorization = %v, want ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD", err)
	}
}

// ---------------------------------------------------------------- override

func TestPlanEmergencyOverride(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 1_000)
	f.mustAllocate(t, "alloc-spend", f.spend, 500)

	target := ReservationTarget{EnvelopeID: f.spend, AccountID: f.checking}
	f.mustCreatePlan(t, "plan-override", PlanKindPurchase, target, 800)

	if _, _, err := f.service.AuthorizePurchasePlan(f.envelope(t, "auth-override-no-reason"), "plan-override", AuthorizationModeEmergencyOverride); !HasCode(err, CodeOverrideReasonRequired) {
		t.Fatalf("override without reason error = %v, want OVERRIDE_REASON_REQUIRED", err)
	}

	plan, op, err := f.service.AuthorizePurchasePlan(f.overrideEnvelope(t, "auth-override", "family emergency"), "plan-override", AuthorizationModeEmergencyOverride)
	if err != nil {
		t.Fatalf("override authorization: %v", err)
	}
	assertPlanCommandState(t, plan, PlanStateReservedException)
	if plan.Mode() != AuthorizationModeEmergencyOverride {
		t.Errorf("plan mode = %q, want %q", plan.Mode(), AuthorizationModeEmergencyOverride)
	}
	if plan.Reason().IsZero() {
		t.Error("override plan has no recorded reason")
	}
	if op.Reason() == "" {
		t.Error("override journal operation has no recorded reason")
	}
	state := f.ledger(t)
	// Shortfall > 0: the envelope is 300 short of the 800 authorization.
	assertSpendableEnvelope(t, state, f.spend, -300)

	// Account liquidity is never bypassed, even under override.
	f.mustCreatePlan(t, "plan-override-account", PlanKindPurchase, target, 2_000)
	if _, _, err := f.service.AuthorizePurchasePlan(f.overrideEnvelope(t, "auth-override-account", "family emergency"), "plan-override-account", AuthorizationModeEmergencyOverride); !HasCode(err, CodeInsufficientAccountFunds) {
		t.Fatalf("override with underfunded account error = %v, want INSUFFICIENT_ACCOUNT_FUNDS", err)
	}
	unchanged, err := f.service.GetPurchasePlan("plan-override-account")
	if err != nil {
		t.Fatalf("GetPurchasePlan: %v", err)
	}
	assertPlanCommandState(t, unchanged, PlanStateDraft)
}

// ----------------------------------------------------------- reallocation

func TestPlanAuthorizeWithReallocation(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "alloc-spend", f.spend, 500)
	f.mustAllocate(t, "alloc-fee", f.fee, 2_000)

	f.mustCreatePlan(t, "plan-realloc", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend}, 1_000)
	plan, op, err := f.service.AuthorizePurchasePlanWithReallocation(f.envelope(t, "auth-realloc"), "plan-realloc", f.fee, mustPositiveAmountForTest(t, 500, f.currency), AuthorizationModeNormal)
	if err != nil {
		t.Fatalf("AuthorizePurchasePlanWithReallocation: %v", err)
	}
	assertPlanCommandState(t, plan, PlanStateReserved)
	if op.Type() != OpReservationCreate {
		t.Errorf("reallocation authorization type = %q, want %q", op.Type(), OpReservationCreate)
	}
	state := f.ledger(t)
	assertEnvelopeBalance(t, state, f.fee, 1_500)
	assertEnvelopeBalance(t, state, f.spend, 1_000)
	assertSpendableEnvelope(t, state, f.spend, 0)

	// Insufficient source envelope.
	f.mustCreatePlan(t, "plan-realloc-short", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend}, 1_000)
	if _, _, err := f.service.AuthorizePurchasePlanWithReallocation(f.envelope(t, "auth-realloc-short"), "plan-realloc-short", f.fee, mustPositiveAmountForTest(t, 5_000, f.currency), AuthorizationModeNormal); !HasCode(err, CodeInsufficientEnvelopeFunds) {
		t.Fatalf("insufficient source error = %v, want INSUFFICIENT_ENVELOPE_FUNDS", err)
	}

	// Source and target must differ.
	f.mustCreatePlan(t, "plan-realloc-same", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend}, 1_000)
	if _, _, err := f.service.AuthorizePurchasePlanWithReallocation(f.envelope(t, "auth-realloc-same"), "plan-realloc-same", f.spend, mustPositiveAmountForTest(t, 500, f.currency), AuthorizationModeNormal); !HasCode(err, CodeInvalidValue) {
		t.Fatalf("same source/target error = %v, want INVALID_VALUE", err)
	}
	for _, id := range []PlanId{"plan-realloc-short", "plan-realloc-same"} {
		unchanged, err := f.service.GetPurchasePlan(id)
		if err != nil {
			t.Fatalf("GetPurchasePlan(%s): %v", id, err)
		}
		assertPlanCommandState(t, unchanged, PlanStateDraft)
	}
}

// ----------------------------------------------------------------- resize

func TestPlanResize(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "alloc-spend", f.spend, 5_000)

	target := ReservationTarget{EnvelopeID: f.spend, AccountID: f.checking}
	f.mustCreatePlan(t, "plan-resize", PlanKindPurchase, target, 2_000)
	f.mustAuthorizePlan(t, "auth-resize", "plan-resize", AuthorizationModeNormal)

	// Increase beyond spendable funds is rejected and leaves the plan intact.
	_, _, err := f.service.ResizePurchasePlan(f.envelope(t, "resize-too-big"), "plan-resize", mustPositiveAmountForTest(t, 6_000, f.currency), AuthorizationModeNormal)
	if !HasCode(err, CodeInsufficientEnvelopeFunds) {
		t.Fatalf("over-funded resize error = %v, want INSUFFICIENT_ENVELOPE_FUNDS", err)
	}
	plan, err := f.service.GetPurchasePlan("plan-resize")
	if err != nil {
		t.Fatalf("GetPurchasePlan: %v", err)
	}
	if plan.IntendedAmount().MinorUnits() != 2_000 || plan.HeldAmount().MinorUnits() != 2_000 {
		t.Errorf("plan after failed resize = (%d intended, %d held), want (2000, 2000)", plan.IntendedAmount().MinorUnits(), plan.HeldAmount().MinorUnits())
	}

	// Decrease releases the excess.
	resized, resizeOp, err := f.service.ResizePurchasePlan(f.envelope(t, "resize-down"), "plan-resize", mustPositiveAmountForTest(t, 1_500, f.currency), AuthorizationModeNormal)
	if err != nil {
		t.Fatalf("decrease resize: %v", err)
	}
	assertPlanCommandState(t, resized, PlanStateReserved)
	if resizeOp.Type() != OpReservationResize {
		t.Errorf("resize operation type = %q, want %q", resizeOp.Type(), OpReservationResize)
	}
	if resized.HeldAmount().MinorUnits() != 1_500 {
		t.Errorf("resized held amount = %d, want 1500", resized.HeldAmount().MinorUnits())
	}
	state := f.ledger(t)
	assertSpendableEnvelope(t, state, f.spend, 3_500)
	assertActiveReservations(t, state, f.spend, f.checking, 1_500)

	// An override resize needs a fresh reason; a minuscule test envelope forces
	// the exception state first.
	f2 := newCommandFixture(t)
	f2.mustRecordIncome(t, "income", 10_000)
	f2.mustAllocate(t, "alloc-spend", f2.spend, 5_000)
	overrideTarget := ReservationTarget{EnvelopeID: f2.spend, AccountID: f2.checking}
	f2.mustCreatePlan(t, "plan-override-resize", PlanKindPurchase, overrideTarget, 6_000)
	if _, _, err := f2.service.AuthorizePurchasePlan(f2.overrideEnvelope(t, "auth-override-resize", "emergency"), "plan-override-resize", AuthorizationModeEmergencyOverride); err != nil {
		t.Fatalf("override authorization for resize: %v", err)
	}
	if _, _, err := f2.service.ResizePurchasePlan(f2.envelope(t, "resize-override-no-reason"), "plan-override-resize", mustPositiveAmountForTest(t, 7_000, f2.currency), AuthorizationModeEmergencyOverride); !HasCode(err, CodeOverrideReasonRequired) {
		t.Fatalf("override resize without reason error = %v, want OVERRIDE_REASON_REQUIRED", err)
	}
	resizedOverride, overrideOp, err := f2.service.ResizePurchasePlan(f2.overrideEnvelope(t, "resize-override", "still an emergency"), "plan-override-resize", mustPositiveAmountForTest(t, 7_000, f2.currency), AuthorizationModeEmergencyOverride)
	if err != nil {
		t.Fatalf("override resize: %v", err)
	}
	assertPlanCommandState(t, resizedOverride, PlanStateReservedException)
	if overrideOp.Type() != OpReservationResize {
		t.Errorf("override resize operation type = %q, want %q", overrideOp.Type(), OpReservationResize)
	}
	if resizedOverride.HeldAmount().MinorUnits() != 7_000 {
		t.Errorf("override resized held amount = %d, want 7000", resizedOverride.HeldAmount().MinorUnits())
	}
}

// ----------------------------------------------------------------- cancel

func TestPlanCancel(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "alloc-spend", f.spend, 5_000)
	f.mustCreatePlan(t, "plan-cancel", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend, AccountID: f.checking}, 2_000)
	f.mustAuthorizePlan(t, "auth-cancel", "plan-cancel", AuthorizationModeNormal)

	plan, op, err := f.service.CancelPurchasePlan(f.envelope(t, "cancel"), "plan-cancel")
	if err != nil {
		t.Fatalf("CancelPurchasePlan: %v", err)
	}
	assertPlanCommandState(t, plan, PlanStateCancelled)
	if op.Type() != OpReservationCancel {
		t.Errorf("cancel operation type = %q, want %q", op.Type(), OpReservationCancel)
	}
	state := f.ledger(t)
	assertActiveReservations(t, state, f.spend, f.checking, 0)
	assertSpendableEnvelope(t, state, f.spend, 5_000)
	assertSpendableAccount(t, state, f.checking, 10_000)
	assertEnvelopeBalance(t, state, f.spend, 5_000)
	assertInvariants(t, state)
	version := f.service.Version()

	if _, _, err := f.service.CancelPurchasePlan(f.envelope(t, "cancel-again"), "plan-cancel"); !HasCode(err, CodeInvalidEntityState) {
		t.Fatalf("second cancel error = %v, want INVALID_ENTITY_STATE", err)
	}
	if f.service.Version() != version {
		t.Errorf("Version() changed after rejected second cancel")
	}

	// Cancelling a DRAFT appends no operation.
	f.mustCreatePlan(t, "plan-cancel-draft", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend}, 1_000)
	draftPlan, draftOp, err := f.service.CancelPurchasePlan(f.envelope(t, "cancel-draft"), "plan-cancel-draft")
	if err != nil {
		t.Fatalf("CancelPurchasePlan(draft): %v", err)
	}
	assertPlanCommandState(t, draftPlan, PlanStateCancelled)
	if !draftOp.ID().IsZero() {
		t.Errorf("draft cancel appended operation %q, want no operation", draftOp.ID())
	}
	if f.service.Version() != version {
		t.Errorf("Version() = %d after draft cancel, want %d", f.service.Version(), version)
	}
}

// ------------------------------------------------------- complete details

func TestPlanCompleteIdempotencyAndGuards(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "alloc-spend", f.spend, 5_000)
	f.mustAllocate(t, "alloc-pay", f.pay, 3_000)

	target := ReservationTarget{EnvelopeID: f.spend, AccountID: f.checking}
	f.mustCreatePlan(t, "plan-complete", PlanKindPurchase, target, 3_000)
	f.mustAuthorizePlan(t, "auth-complete", "plan-complete", AuthorizationModeNormal)

	env := f.envelope(t, "complete-once")
	firstPlan, firstOp, err := f.service.CompletePurchasePlan(env, "plan-complete", mustPositiveAmountForTest(t, 3_000, f.currency), PurchaseKindDebit, "", "")
	if err != nil {
		t.Fatalf("CompletePurchasePlan: %v", err)
	}
	assertPlanCommandState(t, firstPlan, PlanStateCompleted)
	version := f.service.Version()
	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, 7_000)

	replayPlan, replayOp, err := f.service.CompletePurchasePlan(env, "plan-complete", mustPositiveAmountForTest(t, 3_000, f.currency), PurchaseKindDebit, "", "")
	if err != nil {
		t.Fatalf("CompletePurchasePlan replay: %v", err)
	}
	if replayOp.ID() != firstOp.ID() {
		t.Errorf("replayed completion id = %q, want %q", replayOp.ID(), firstOp.ID())
	}
	assertPlanCommandState(t, replayPlan, PlanStateCompleted)
	if f.service.Version() != version {
		t.Errorf("Version() = %d after replay, want %d", f.service.Version(), version)
	}
	state = f.ledger(t)
	assertAccountBalance(t, state, f.checking, 7_000)

	// final > authorized is rejected without changing state.
	f.mustCreatePlan(t, "plan-over-authorize", PlanKindPurchase, target, 2_000)
	f.mustAuthorizePlan(t, "auth-over-authorize", "plan-over-authorize", AuthorizationModeNormal)
	before := f.service.Version()
	if _, _, err := f.service.CompletePurchasePlan(f.envelope(t, "complete-over"), "plan-over-authorize", mustPositiveAmountForTest(t, 2_500, f.currency), PurchaseKindDebit, "", ""); !HasCode(err, CodeAmountExceedsAuthorization) {
		t.Fatalf("over-authorized completion error = %v, want AMOUNT_EXCEEDS_AUTHORIZATION", err)
	}
	guarded, err := f.service.GetPurchasePlan("plan-over-authorize")
	if err != nil {
		t.Fatalf("GetPurchasePlan: %v", err)
	}
	assertPlanCommandState(t, guarded, PlanStateReserved)
	if f.service.Version() != before {
		t.Errorf("Version() changed after rejected completion")
	}

	// Kind mismatch: a repayment plan cannot complete with card-purchase postings.
	f.mustCreatePlan(t, "plan-kind-mismatch", PlanKindRepayment, ReservationTarget{EnvelopeID: f.pay, AccountID: f.checking}, 1_000)
	f.mustAuthorizePlan(t, "auth-kind-mismatch", "plan-kind-mismatch", AuthorizationModeNormal)
	if _, _, err := f.service.CompletePurchasePlan(f.envelope(t, "complete-mismatch"), "plan-kind-mismatch", mustPositiveAmountForTest(t, 1_000, f.currency), PurchaseKindCard, f.card, f.pay); !HasCode(err, CodeInvalidValue) {
		t.Fatalf("kind mismatch error = %v, want INVALID_VALUE", err)
	}
	mismatched, err := f.service.GetPurchasePlan("plan-kind-mismatch")
	if err != nil {
		t.Fatalf("GetPurchasePlan: %v", err)
	}
	assertPlanCommandState(t, mismatched, PlanStateReserved)
}

// --------------------------------------------------------------- registry

func TestPurchasePlanRegistry(t *testing.T) {
	f := newCommandFixture(t)
	f.mustCreatePlan(t, "plan-b", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend}, 1_000)
	f.mustCreatePlan(t, "plan-a", PlanKindPurchase, ReservationTarget{EnvelopeID: f.fee}, 2_000)

	plans := f.service.PurchasePlans()
	if len(plans) != 2 {
		t.Fatalf("PurchasePlans() length = %d, want 2", len(plans))
	}
	if plans[0].ID() != "plan-a" || plans[1].ID() != "plan-b" {
		t.Errorf("PurchasePlans order = [%q, %q], want [plan-a, plan-b]", plans[0].ID(), plans[1].ID())
	}
	// Snapshots are independent of the stored aggregate.
	plans[0].state = PlanStateCompleted
	stored, err := f.service.GetPurchasePlan("plan-a")
	if err != nil {
		t.Fatalf("GetPurchasePlan: %v", err)
	}
	assertPlanCommandState(t, stored, PlanStateDraft)

	if _, err := f.service.GetPurchasePlan("plan-missing"); !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
		t.Errorf("missing plan error = %v, want ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD", err)
	}
}
