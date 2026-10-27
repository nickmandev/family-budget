// This file contains the concurrency-oriented property tests: the service's
// mutex plus clone-then-commit execution must make concurrent commands
// serializable, so funds and reservations can never be double-spent.
package domain

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// TestPropertyConcurrentAllocationsNeverOverdraw commits exactly the funds that
// exist, then races more allocations than can be funded. Exactly the affordable
// number must succeed and the envelope must never be over-allocated.
func TestPropertyConcurrentAllocationsNeverOverdraw(t *testing.T) {
	f := newCommandFixture(t)
	income := mustPositiveAmountForTest(t, 1_000, f.currency)
	if _, err := f.service.RecordIncome(f.envelope(t, "income"), f.checking, income); err != nil {
		t.Fatalf("RecordIncome: %v", err)
	}

	const attempts = 24
	amount := mustPositiveAmountForTest(t, 100, f.currency)
	envelopes := make([]CommandEnvelope, attempts)
	for i := range envelopes {
		envelopes[i] = f.envelope(t, fmt.Sprintf("alloc-%d", i))
	}
	results := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = f.service.AllocateFunds(envelopes[i], f.spend, amount)
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for i, err := range results {
		switch {
		case err == nil:
			succeeded++
		case HasCode(err, CodeInsufficientReadyToAssign):
		default:
			t.Fatalf("allocation %d: unexpected error %v", i, err)
		}
	}
	if succeeded != 10 {
		t.Fatalf("%d allocations succeeded, want 10", succeeded)
	}
	ledger := f.ledger(t)
	if balance := petEnvelopeBalance(t, ledger, f.spend); balance != 1_000 {
		t.Fatalf("spend envelope balance %d, want 1000", balance)
	}
	if ready := petReadyToAssign(t, ledger); ready != 0 {
		t.Fatalf("ready-to-assign %d, want 0", ready)
	}
	if got := f.service.Version(); got != 1+10 {
		t.Fatalf("journal version %d, want 11", got)
	}
	petCheckLedger(t, f)
}

// TestPropertyConcurrentAuthorizationSingleWinner races authorization of the
// same DRAFT plan. The plan state check and hold creation are atomic, so exactly
// one command may reserve the funds.
func TestPropertyConcurrentAuthorizationSingleWinner(t *testing.T) {
	f := newCommandFixture(t)
	income := mustPositiveAmountForTest(t, 1_000, f.currency)
	if _, err := f.service.RecordIncome(f.envelope(t, "income"), f.checking, income); err != nil {
		t.Fatalf("RecordIncome: %v", err)
	}
	if _, err := f.service.AllocateFunds(f.envelope(t, "allocate"), f.spend, income); err != nil {
		t.Fatalf("AllocateFunds: %v", err)
	}
	planID := mustPlanIDForTest(t, "plan-concurrent")
	target := ReservationTarget{EnvelopeID: f.spend}
	if _, err := f.service.CreatePurchasePlan(f.actor, planID, PlanKindPurchase, target, mustPositiveAmountForTest(t, 100, f.currency)); err != nil {
		t.Fatalf("CreatePurchasePlan: %v", err)
	}

	const attempts = 24
	envelopes := make([]CommandEnvelope, attempts)
	for i := range envelopes {
		envelopes[i] = f.envelope(t, fmt.Sprintf("authorize-%d", i))
	}
	results := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, results[i] = f.service.AuthorizePurchasePlan(envelopes[i], planID, AuthorizationModeNormal)
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for i, err := range results {
		switch {
		case err == nil:
			succeeded++
		case HasCode(err, CodeInvalidEntityState):
		default:
			t.Fatalf("authorization %d: unexpected error %v", i, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d authorizations succeeded, want 1", succeeded)
	}
	plan, err := f.service.GetPurchasePlan(planID)
	if err != nil {
		t.Fatalf("GetPurchasePlan: %v", err)
	}
	if plan.State() != PlanStateReserved {
		t.Fatalf("plan state %s, want %s", plan.State(), PlanStateReserved)
	}
	if held := plan.HeldAmount().MinorUnits(); held != 100 {
		t.Fatalf("plan held amount %d, want 100", held)
	}
	if got := f.service.Version(); got != 3 {
		t.Fatalf("journal version %d, want 3", got)
	}
	petCheckLedger(t, f)
}

// TestPropertyConcurrentSameKeyAppliesOnce races retries that share one
// idempotency key and fingerprint. Every caller sees the original operation,
// but the journal and ledger contain it once.
func TestPropertyConcurrentSameKeyAppliesOnce(t *testing.T) {
	f := newCommandFixture(t)
	const attempts = 24
	env := f.envelope(t, "same-key")
	amount := mustPositiveAmountForTest(t, 500, f.currency)
	ids := make([]JournalOperationId, attempts)
	results := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			op, err := f.service.RecordIncome(env, f.checking, amount)
			ids[i] = op.ID()
			results[i] = err
		}(i)
	}
	wg.Wait()

	for i := range results {
		if results[i] != nil {
			t.Fatalf("call %d: %v", i, results[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("call %d returned %q, want %q", i, ids[i], ids[0])
		}
	}
	if got := f.service.Version(); got != 1 {
		t.Fatalf("journal version %d, want 1", got)
	}
	ledger := f.ledger(t)
	if balance := petPostedBalance(t, ledger, f.checking); balance != 500 {
		t.Fatalf("checking balance %d, want 500", balance)
	}
	petCheckLedger(t, f)
}

// TestPropertyRejectedSuggestionIsInert verifies that a terminal suggestion
// rejects every further lifecycle command with the same error code and without
// any observable state change.
func TestPropertyRejectedSuggestionIsInert(t *testing.T) {
	f := newCommandFixture(t)
	id := mustSuggestionIDForTest(t, "suggestion-inert")
	amount := mustPositiveAmountForTest(t, 900, f.currency)
	if _, err := f.service.CreateSuggestion(f.actor, id, TransactionSourceExternalEntry, amount, "property"); err != nil {
		t.Fatalf("CreateSuggestion: %v", err)
	}
	if _, err := f.service.CancelSuggestion(f.actor, id, "property"); err != nil {
		t.Fatalf("CancelSuggestion: %v", err)
	}

	before := petSnapshot(t, f)
	if _, err := f.service.EditSuggestion(f.actor, id, amount, "edited"); !HasCode(err, CodeInvalidEntityState) {
		t.Fatalf("EditSuggestion after cancel: %v, want %s", err, CodeInvalidEntityState)
	}
	if _, err := f.service.CancelSuggestion(f.actor, id, "again"); !HasCode(err, CodeInvalidEntityState) {
		t.Fatalf("CancelSuggestion after cancel: %v, want %s", err, CodeInvalidEntityState)
	}
	if _, _, err := f.service.ConfirmSuggestionAsExternalTransaction(
		f.envelope(t, "confirm-cancelled"), id, PurchaseKindDebit, f.checking, f.spend, EnvelopeId(""),
	); !HasCode(err, CodeInvalidEntityState) {
		t.Fatalf("ConfirmSuggestion after cancel: %v, want %s", err, CodeInvalidEntityState)
	}
	after := petSnapshot(t, f)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected suggestion commands changed observable state")
	}
}
