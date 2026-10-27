package domain

import "testing"

func (f *commandFixture) mustCreateSuggestion(t *testing.T, id SuggestionId, amount int64, merchant string) *Suggestion {
	t.Helper()
	suggestion, err := f.service.CreateSuggestion(f.actor, id, TransactionSourceExternalEntry, mustPositiveAmountForTest(t, amount, f.currency), merchant)
	if err != nil {
		t.Fatalf("CreateSuggestion(%s): %v", id, err)
	}
	return suggestion
}
func assertSuggestionState(t *testing.T, suggestion *Suggestion, want SuggestionState) {
	t.Helper()
	if suggestion == nil {
		t.Fatalf("suggestion is nil, want state %s", want)
	}
	if suggestion.State() != want {
		t.Errorf("suggestion %q state = %s, want %s", suggestion.ID(), suggestion.State(), want)
	}
}

// suggestion returns a snapshot of the stored suggestion under the service
// mutex.
func (f *commandFixture) suggestion(t *testing.T, id SuggestionId) *Suggestion {
	t.Helper()
	f.service.mu.Lock()
	defer f.service.mu.Unlock()
	stored, known := f.service.suggestions[id]
	if !known {
		t.Fatalf("suggestion %q is not stored", id)
	}
	return cloneSuggestion(stored)
}

func TestSuggestionCreateEditCancel(t *testing.T) {
	f := newCommandFixture(t)

	created := f.mustCreateSuggestion(t, "sug-1", 1_000, "Coffee")
	assertSuggestionState(t, created, SuggestionStateSuggested)
	if f.service.Version() != 0 {
		t.Errorf("Version() = %d after suggestion create, want 0", f.service.Version())
	}

	edited, err := f.service.EditSuggestion(f.actor, "sug-1", mustPositiveAmountForTest(t, 1_200, f.currency), "Cafe")
	if err != nil {
		t.Fatalf("EditSuggestion: %v", err)
	}
	assertSuggestionState(t, edited, SuggestionStateEdited)
	if edited.Amount().MinorUnits() != 1_200 || edited.Merchant() != "Cafe" {
		t.Errorf("edited suggestion = (%d, %q), want (1200, Cafe)", edited.Amount().MinorUnits(), edited.Merchant())
	}

	cancelled, err := f.service.CancelSuggestion(f.actor, "sug-1", "duplicate")
	if err != nil {
		t.Fatalf("CancelSuggestion: %v", err)
	}
	assertSuggestionState(t, cancelled, SuggestionStateCancelled)
	if cancelled.CancelReason() != "duplicate" {
		t.Errorf("cancel reason = %q, want duplicate", cancelled.CancelReason())
	}
	// Q10: the source fingerprint fields are preserved after cancellation.
	if cancelled.ID() != "sug-1" || cancelled.Source() != TransactionSourceExternalEntry ||
		cancelled.Amount().MinorUnits() != 1_200 || cancelled.Merchant() != "Cafe" {
		t.Errorf("cancelled suggestion lost fingerprint fields: (%q, %q, %d, %q)", cancelled.ID(), cancelled.Source(), cancelled.Amount().MinorUnits(), cancelled.Merchant())
	}

	// A confirmed suggestion is terminal for edit and cancel.
	f.mustCreateSuggestion(t, "sug-confirmed", 500, "Tea")
	if _, _, err := f.service.ConfirmSuggestionAsExternalTransaction(f.envelope(t, "confirm-terminal"), "sug-confirmed", PurchaseKindDebit, f.checking, f.spend, ""); err != nil {
		t.Fatalf("ConfirmSuggestionAsExternalTransaction: %v", err)
	}
	if _, err := f.service.EditSuggestion(f.actor, "sug-confirmed", mustPositiveAmountForTest(t, 600, f.currency), "Tea2"); !HasCode(err, CodeInvalidEntityState) {
		t.Errorf("edit confirmed suggestion error = %v, want INVALID_ENTITY_STATE", err)
	}
	if _, err := f.service.CancelSuggestion(f.actor, "sug-confirmed", "nope"); !HasCode(err, CodeInvalidEntityState) {
		t.Errorf("cancel confirmed suggestion error = %v, want INVALID_ENTITY_STATE", err)
	}
}

func TestSuggestionConfirmExternalUnderfunded(t *testing.T) {
	f := newCommandFixture(t)
	f.mustCreateSuggestion(t, "sug-external", 1_000, "Groceries")

	// No income, no allocation: the factual record is still posted.
	confirmed, op, err := f.service.ConfirmSuggestionAsExternalTransaction(f.envelope(t, "confirm-external"), "sug-external", PurchaseKindDebit, f.checking, f.spend, "")
	if err != nil {
		t.Fatalf("ConfirmSuggestionAsExternalTransaction: %v", err)
	}
	assertSuggestionState(t, confirmed, SuggestionStateConfirmed)
	if op.Type() != OpExternalTransaction {
		t.Errorf("external confirmation type = %q, want %q", op.Type(), OpExternalTransaction)
	}
	if op.Source() != TransactionSourceExternalEntry {
		t.Errorf("external confirmation source = %q, want %q", op.Source(), TransactionSourceExternalEntry)
	}

	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, -1_000)
	assertEnvelopeBalance(t, state, f.spend, -1_000)
	deficits, err := state.Deficits()
	if err != nil {
		t.Fatalf("Deficits: %v", err)
	}
	if len(deficits) == 0 {
		t.Fatal("Deficits() = none, want the underfunded envelope reported")
	}
	status, err := state.ResolutionStatus()
	if err != nil {
		t.Fatalf("ResolutionStatus: %v", err)
	}
	if status != ResolutionRequiresResolution {
		t.Errorf("ResolutionStatus = %s, want %s", status, ResolutionRequiresResolution)
	}
	assertInvariants(t, state)
}

func TestSuggestionConfirmIdempotency(t *testing.T) {
	f := newCommandFixture(t)
	f.mustCreateSuggestion(t, "sug-idem", 500, "Bus")

	env := f.envelope(t, "confirm-idem")
	_, first, err := f.service.ConfirmSuggestionAsExternalTransaction(env, "sug-idem", PurchaseKindDebit, f.checking, f.spend, "")
	if err != nil {
		t.Fatalf("ConfirmSuggestionAsExternalTransaction: %v", err)
	}
	if f.service.Version() != 1 {
		t.Fatalf("Version() = %d after confirmation, want 1", f.service.Version())
	}
	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, -500)

	replaySuggestion, replay, err := f.service.ConfirmSuggestionAsExternalTransaction(env, "sug-idem", PurchaseKindDebit, f.checking, f.spend, "")
	if err != nil {
		t.Fatalf("replay confirmation: %v", err)
	}
	if replay.ID() != first.ID() {
		t.Errorf("replayed operation id = %q, want %q", replay.ID(), first.ID())
	}
	assertSuggestionState(t, replaySuggestion, SuggestionStateConfirmed)
	if f.service.Version() != 1 {
		t.Errorf("Version() = %d after replay, want 1", f.service.Version())
	}
	state = f.ledger(t)
	assertAccountBalance(t, state, f.checking, -500)

	// A new key on a terminal suggestion must not double-post.
	if _, _, err := f.service.ConfirmSuggestionAsExternalTransaction(f.envelope(t, "confirm-again"), "sug-idem", PurchaseKindDebit, f.checking, f.spend, ""); !HasCode(err, CodeInvalidEntityState) {
		t.Errorf("second confirmation error = %v, want INVALID_ENTITY_STATE", err)
	}
	if f.service.Version() != 1 {
		t.Errorf("Version() = %d after rejected second confirmation, want 1", f.service.Version())
	}

	// The same key with another fingerprint is a payload mismatch.
	conflicting := f.envelopeWithFingerprint(t, "confirm-idem", "1234567890abcdef")
	if _, _, err := f.service.ConfirmSuggestionAsExternalTransaction(conflicting, "sug-idem", PurchaseKindDebit, f.checking, f.spend, ""); !HasCode(err, CodeIdempotencyKeyPayloadMismatch) {
		t.Errorf("fingerprint mismatch error = %v, want IDEMPOTENCY_KEY_PAYLOAD_MISMATCH", err)
	}
}

func TestSuggestionConvertToPlannedPurchase(t *testing.T) {
	f := newCommandFixture(t)
	f.mustCreateSuggestion(t, "sug-convert", 1_000, "Planned")

	suggestion, plan, err := f.service.ConvertSuggestionToPlannedPurchase(f.actor, "sug-convert", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend})
	if err != nil {
		t.Fatalf("ConvertSuggestionToPlannedPurchase: %v", err)
	}
	assertSuggestionState(t, suggestion, SuggestionStateConfirmed)
	assertPlanCommandState(t, plan, PlanStateDraft)
	if plan.IntendedAmount().MinorUnits() != 1_000 {
		t.Errorf("converted plan amount = %d, want 1000", plan.IntendedAmount().MinorUnits())
	}
	if f.service.Version() != 0 {
		t.Errorf("Version() = %d after conversion, want 0 (nothing posted)", f.service.Version())
	}
	state := f.ledger(t)
	assertActiveReservations(t, state, f.spend, "", 0)
	assertEnvelopeBalance(t, state, f.spend, 0)

	// Confirmed is terminal: no second plan.
	if _, _, err := f.service.ConvertSuggestionToPlannedPurchase(f.actor, "sug-convert", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend}); !HasCode(err, CodeInvalidEntityState) {
		t.Errorf("repeat conversion error = %v, want INVALID_ENTITY_STATE", err)
	}

	// A cancelled suggestion cannot be converted.
	f.mustCreateSuggestion(t, "sug-cancelled", 700, "Dismissed")
	if _, err := f.service.CancelSuggestion(f.actor, "sug-cancelled", "not needed"); err != nil {
		t.Fatalf("CancelSuggestion: %v", err)
	}
	if _, _, err := f.service.ConvertSuggestionToPlannedPurchase(f.actor, "sug-cancelled", PlanKindPurchase, ReservationTarget{EnvelopeID: f.spend}); !HasCode(err, CodeInvalidEntityState) {
		t.Errorf("convert cancelled suggestion error = %v, want INVALID_ENTITY_STATE", err)
	}

	// A repayment plan needs a source cash account (Q2), just like direct
	// plan creation.
	f.mustCreateSuggestion(t, "sug-repay", 900, "Card payment")
	if _, _, err := f.service.ConvertSuggestionToPlannedPurchase(f.actor, "sug-repay", PlanKindRepayment, ReservationTarget{EnvelopeID: f.pay}); !HasCode(err, CodeInvalidValue) {
		t.Errorf("repayment conversion without account error = %v, want INVALID_VALUE", err)
	}
	stillSuggested := f.suggestion(t, "sug-repay")
	assertSuggestionState(t, stillSuggested, SuggestionStateSuggested)

	repaymentSuggestion, repaymentPlan, err := f.service.ConvertSuggestionToPlannedPurchase(f.actor, "sug-repay", PlanKindRepayment, ReservationTarget{EnvelopeID: f.pay, AccountID: f.checking})
	if err != nil {
		t.Fatalf("repayment conversion: %v", err)
	}
	assertSuggestionState(t, repaymentSuggestion, SuggestionStateConfirmed)
	assertPlanCommandState(t, repaymentPlan, PlanStateDraft)
	if repaymentPlan.Kind() != PlanKindRepayment {
		t.Errorf("converted plan kind = %q, want %q", repaymentPlan.Kind(), PlanKindRepayment)
	}
}

func TestSuggestionConfirmIsAtomicOnFailure(t *testing.T) {
	f := newCommandFixture(t)
	eur := mustCurrency(t, "EUR", 2)
	suggestion, err := f.service.CreateSuggestion(f.actor, "sug-currency", TransactionSourceExternalEntry, mustPositiveAmountForTest(t, 1_000, eur), "Foreign")
	if err != nil {
		t.Fatalf("CreateSuggestion: %v", err)
	}
	assertSuggestionState(t, suggestion, SuggestionStateSuggested)

	env := f.envelope(t, "confirm-currency")
	if _, _, err := f.service.ConfirmSuggestionAsExternalTransaction(env, "sug-currency", PurchaseKindDebit, f.checking, f.spend, ""); !HasCode(err, CodeCurrencyMismatch) {
		t.Fatalf("currency-mismatched confirmation error = %v, want CURRENCY_MISMATCH", err)
	}

	unchanged := f.suggestion(t, "sug-currency")
	assertSuggestionState(t, unchanged, SuggestionStateSuggested)
	if f.service.Version() != 0 {
		t.Errorf("Version() = %d after failed confirmation, want 0", f.service.Version())
	}
	if _, err := f.service.Idempotency().Lookup(env.Key); !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
		t.Errorf("idempotency record after failed confirmation = %v, want ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD", err)
	}
}
