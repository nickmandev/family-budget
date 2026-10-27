package domain

import "testing"

// mustOverrideReasonForTest builds a valid override reason or fails the test.
func mustOverrideReasonForTest(t *testing.T, raw string) OverrideReason {
	t.Helper()
	reason, err := NewOverrideReason(raw)
	if err != nil {
		t.Fatalf("NewOverrideReason(%q): %v", raw, err)
	}
	return reason
}

// authorizationLedgerForTest builds the shared funding fixture ledger and posts
// the given opening checking, spending-envelope, and payment-envelope balances.
func authorizationLedgerForTest(t *testing.T, f *fundingFixture, checking, spend, pay int64) *LedgerState {
	t.Helper()
	state := f.newLedger(t)
	applyFundingPostingsForTest(t, state, map[AccountId]int64{f.checking: checking}, map[EnvelopeId]int64{f.spend: spend, f.pay: pay})
	return state
}

// assertAuthorizationShape checks the hold shape of a successful outcome: one
// reservation change for the target, full-amount envelope hold, and an account
// hold exactly when the target sets an account.
func assertAuthorizationShape(t *testing.T, outcome AuthorizationOutcome, target ReservationTarget, amount int64) {
	t.Helper()
	if len(outcome.Effects.Reservations) != 1 {
		t.Fatalf("Effects.Reservations = %d, want 1", len(outcome.Effects.Reservations))
	}
	change := outcome.Effects.Reservations[0]
	if change.PlanID != target.PlanID {
		t.Errorf("ReservationChange.PlanID = %q, want %q", change.PlanID, target.PlanID)
	}
	if change.Envelope.EnvelopeID != target.EnvelopeID {
		t.Errorf("Envelope.EnvelopeID = %q, want %q", change.Envelope.EnvelopeID, target.EnvelopeID)
	}
	if got := change.Envelope.Delta.MinorUnits(); got != amount {
		t.Errorf("Envelope.Delta = %d, want %d", got, amount)
	}
	if target.AccountID.IsZero() {
		if change.Account != nil {
			t.Errorf("Account = %+v, want nil for a target without an account hold", *change.Account)
		}
		return
	}
	if change.Account == nil {
		t.Fatal("Account = nil, want account hold")
	}
	if change.Account.AccountID != target.AccountID {
		t.Errorf("Account.AccountID = %q, want %q", change.Account.AccountID, target.AccountID)
	}
	if got := change.Account.Delta.MinorUnits(); got != amount {
		t.Errorf("Account.Delta = %d, want %d", got, amount)
	}
}

func TestAuthorizePurchase(t *testing.T) {
	f := newFundingFixture(t)
	plan := mustPlanIDForTest(t, "plan-authorize")
	reason := mustOverrideReasonForTest(t, "cover unexpected medical cost")

	tests := []struct {
		name            string
		checking        int64
		spend           int64
		pay             int64
		amount          int64
		target          ReservationTarget
		mode            AuthorizationMode
		reason          OverrideReason
		wantShortfall   int64
		wantReason      string
		wantAccountHold bool
	}{
		{
			name:            "normal debit purchase holds envelope and account",
			checking:        10_000,
			spend:           3_000,
			amount:          1_000,
			target:          ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking},
			mode:            AuthorizationModeNormal,
			wantAccountHold: true,
		},
		{
			name:     "normal card purchase holds spending envelope only",
			checking: 10_000,
			spend:    3_000,
			amount:   1_000,
			target:   ReservationTarget{PlanID: plan, EnvelopeID: f.spend},
			mode:     AuthorizationModeNormal,
		},
		{
			name:            "normal card repayment holds payment envelope and account",
			checking:        10_000,
			pay:             2_000,
			amount:          1_000,
			target:          ReservationTarget{PlanID: plan, EnvelopeID: f.pay, AccountID: f.checking},
			mode:            AuthorizationModeNormal,
			wantAccountHold: true,
		},
		{
			name:            "override authorizes an underfunded envelope",
			checking:        10_000,
			spend:           300,
			amount:          1_000,
			target:          ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking},
			mode:            AuthorizationModeEmergencyOverride,
			reason:          reason,
			wantShortfall:   700,
			wantReason:      "cover unexpected medical cost",
			wantAccountHold: true,
		},
		{
			name:          "override with fully funded envelope records no shortfall",
			checking:      10_000,
			spend:         3_000,
			amount:        1_000,
			target:        ReservationTarget{PlanID: plan, EnvelopeID: f.spend},
			mode:          AuthorizationModeEmergencyOverride,
			reason:        reason,
			wantReason:    "cover unexpected medical cost",
			wantShortfall: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := authorizationLedgerForTest(t, f, tt.checking, tt.spend, tt.pay)
			envelopeStart := tt.spend
			if tt.target.EnvelopeID == f.pay {
				envelopeStart = tt.pay
			}

			outcome, err := AuthorizePurchase(state, tt.target, mustPositiveAmountForTest(t, tt.amount, f.currency), tt.mode, tt.reason)
			if err != nil {
				t.Fatalf("AuthorizePurchase: %v", err)
			}
			if outcome.Mode != tt.mode {
				t.Errorf("Outcome.Mode = %q, want %q", outcome.Mode, tt.mode)
			}
			if got := outcome.Shortfall.MinorUnits(); got != tt.wantShortfall {
				t.Errorf("Outcome.Shortfall = %d, want %d", got, tt.wantShortfall)
			}
			if got := outcome.Reason.String(); got != tt.wantReason {
				t.Errorf("Outcome.Reason = %q, want %q", got, tt.wantReason)
			}
			assertAuthorizationShape(t, outcome, tt.target, tt.amount)

			if err := state.ApplyReservationChanges(outcome.Effects.Reservations...); err != nil {
				t.Fatalf("ApplyReservationChanges: %v", err)
			}
			// Holds reduce spendable balances only; posted balances are untouched.
			assertMoney(t, state.SpendableEnvelopeBalance, tt.target.EnvelopeID, envelopeStart-tt.amount)
			assertMoney(t, state.EnvelopeBalance, tt.target.EnvelopeID, envelopeStart)
			wantAccountSpendable := tt.checking
			if !tt.target.AccountID.IsZero() {
				wantAccountSpendable = tt.checking - tt.amount
			}
			assertMoney(t, state.SpendableAccountBalance, f.checking, wantAccountSpendable)
			assertMoney(t, state.PostedAccountBalance, f.checking, tt.checking)
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

// TestAuthorizePurchaseOverrideDeficitIsVisible asserts that an override
// reservation can make the spendable envelope negative while posted balances
// stay untouched, so no derived budget deficit appears (Q4).
func TestAuthorizePurchaseOverrideDeficitIsVisible(t *testing.T) {
	f := newFundingFixture(t)
	plan := mustPlanIDForTest(t, "plan-override-deficit")
	target := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}
	state := authorizationLedgerForTest(t, f, 10_000, 300, 0)

	outcome, err := AuthorizePurchase(state, target, mustPositiveAmountForTest(t, 1_000, f.currency), AuthorizationModeEmergencyOverride, mustOverrideReasonForTest(t, "medical emergency"))
	if err != nil {
		t.Fatalf("AuthorizePurchase: %v", err)
	}
	if got := outcome.Shortfall.MinorUnits(); got != 700 {
		t.Errorf("Outcome.Shortfall = %d, want 700", got)
	}
	if err := state.ApplyReservationChanges(outcome.Effects.Reservations...); err != nil {
		t.Fatalf("ApplyReservationChanges: %v", err)
	}

	assertMoney(t, state.SpendableEnvelopeBalance, f.spend, -700)
	assertMoney(t, state.EnvelopeBalance, f.spend, 300)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
	deficits, err := state.Deficits()
	if err != nil {
		t.Fatalf("Deficits: %v", err)
	}
	if len(deficits) != 0 {
		t.Errorf("Deficits = %+v, want none: posted balances are untouched", deficits)
	}
	resolution, err := state.ResolutionStatus()
	if err != nil {
		t.Fatalf("ResolutionStatus: %v", err)
	}
	if resolution != ResolutionClear {
		t.Errorf("ResolutionStatus = %q, want %q", resolution, ResolutionClear)
	}
}

func TestAuthorizePurchaseRejections(t *testing.T) {
	f := newFundingFixture(t)
	plan := mustPlanIDForTest(t, "plan-authorize-reject")
	reason := mustOverrideReasonForTest(t, "medical emergency")
	eur := mustCurrency(t, "EUR", 2)
	debitTarget := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}
	unknownTarget := ReservationTarget{PlanID: plan, EnvelopeID: mustEnvelopeIDForTest(t, "env-ghost")}
	unknownAccountTarget := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: mustAccountIDForTest(t, "acct-ghost")}

	tests := []struct {
		name     string
		checking int64
		spend    int64
		amount   int64
		currency Currency
		target   ReservationTarget
		mode     AuthorizationMode
		reason   OverrideReason
		wantCode ErrorCode
	}{
		{
			name:     "normal underfunded envelope",
			checking: 10_000,
			spend:    300,
			amount:   1_000,
			target:   debitTarget,
			mode:     AuthorizationModeNormal,
			wantCode: CodeInsufficientEnvelopeFunds,
		},
		{
			name:     "normal underfunded account",
			checking: 300,
			spend:    3_000,
			amount:   1_000,
			target:   debitTarget,
			mode:     AuthorizationModeNormal,
			wantCode: CodeInsufficientAccountFunds,
		},
		{
			name:     "normal envelope funded but account underfunded",
			checking: 500,
			spend:    3_000,
			amount:   1_000,
			target:   debitTarget,
			mode:     AuthorizationModeNormal,
			wantCode: CodeInsufficientAccountFunds,
		},
		{
			name:     "override without reason on underfunded envelope",
			checking: 10_000,
			spend:    300,
			amount:   1_000,
			target:   debitTarget,
			mode:     AuthorizationModeEmergencyOverride,
			wantCode: CodeOverrideReasonRequired,
		},
		{
			name:     "override without reason on funded envelope",
			checking: 10_000,
			spend:    3_000,
			amount:   1_000,
			target:   debitTarget,
			mode:     AuthorizationModeEmergencyOverride,
			wantCode: CodeOverrideReasonRequired,
		},
		{
			name:     "override never bypasses account liquidity",
			checking: 300,
			spend:    300,
			amount:   1_000,
			target:   debitTarget,
			mode:     AuthorizationModeEmergencyOverride,
			reason:   reason,
			wantCode: CodeInsufficientAccountFunds,
		},
		{
			name:     "unknown envelope",
			checking: 10_000,
			spend:    3_000,
			amount:   1_000,
			target:   unknownTarget,
			mode:     AuthorizationModeNormal,
			wantCode: CodeEntityNotFoundOrNotInHousehold,
		},
		{
			name:     "unknown account",
			checking: 10_000,
			spend:    3_000,
			amount:   1_000,
			target:   unknownAccountTarget,
			mode:     AuthorizationModeNormal,
			wantCode: CodeEntityNotFoundOrNotInHousehold,
		},
		{
			name:     "invalid mode",
			checking: 10_000,
			spend:    3_000,
			amount:   1_000,
			target:   debitTarget,
			mode:     AuthorizationMode("BOGUS"),
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero plan id",
			checking: 10_000,
			spend:    3_000,
			amount:   1_000,
			target:   ReservationTarget{EnvelopeID: f.spend},
			mode:     AuthorizationModeNormal,
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero envelope id",
			checking: 10_000,
			spend:    3_000,
			amount:   1_000,
			target:   ReservationTarget{PlanID: plan},
			mode:     AuthorizationModeNormal,
			wantCode: CodeInvalidValue,
		},
		{
			name:     "currency mismatch",
			checking: 10_000,
			spend:    3_000,
			amount:   1_000,
			currency: eur,
			target:   debitTarget,
			mode:     AuthorizationModeNormal,
			wantCode: CodeCurrencyMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := authorizationLedgerForTest(t, f, tt.checking, tt.spend, 0)
			amountCurrency := tt.currency
			if amountCurrency.IsZero() {
				amountCurrency = f.currency
			}
			amount := mustPositiveAmountForTest(t, tt.amount, amountCurrency)

			outcome, err := AuthorizePurchase(state, tt.target, amount, tt.mode, tt.reason)
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("AuthorizePurchase error = %v, want code %s", err, tt.wantCode)
			}
			if !outcome.Effects.IsEmpty() {
				t.Errorf("rejected Effects = %+v, want empty", outcome.Effects)
			}
		})
	}
}
