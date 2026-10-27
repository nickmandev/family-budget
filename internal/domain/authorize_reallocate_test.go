package domain

import "testing"

// reallocationEnvelopeStart returns the opening posted balance of a fixture
// envelope.
func reallocationEnvelopeStart(f *fundingFixture, id EnvelopeId, spend, pay int64) int64 {
	if id == f.pay {
		return pay
	}
	return spend
}

func TestAuthorizeWithReallocation(t *testing.T) {
	f := newFundingFixture(t)
	plan := mustPlanIDForTest(t, "plan-reallocate")
	reason := mustOverrideReasonForTest(t, "fund the purchase from savings")
	debitTarget := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}
	cardTarget := ReservationTarget{PlanID: plan, EnvelopeID: f.spend}
	repaymentTarget := ReservationTarget{PlanID: plan, EnvelopeID: f.pay, AccountID: f.checking}

	tests := []struct {
		name          string
		checking      int64
		spend         int64
		pay           int64
		source        EnvelopeId
		target        ReservationTarget
		delta         int64
		amount        int64
		mode          AuthorizationMode
		reason        OverrideReason
		wantShortfall int64
	}{
		{
			name:     "normal reallocation covers the shortfall",
			checking: 10_000,
			spend:    300,
			pay:      2_000,
			source:   f.pay,
			target:   debitTarget,
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
		},
		{
			name:     "normal delta larger than the strict shortfall",
			checking: 10_000,
			spend:    900,
			pay:      2_000,
			source:   f.pay,
			target:   debitTarget,
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
		},
		{
			name:          "override reports the remaining shortfall",
			checking:      10_000,
			spend:         300,
			pay:           2_000,
			source:        f.pay,
			target:        debitTarget,
			delta:         300,
			amount:        1_000,
			mode:          AuthorizationModeEmergencyOverride,
			reason:        reason,
			wantShortfall: 400,
		},
		{
			name:     "card target holds no account",
			checking: 10_000,
			spend:    300,
			pay:      2_000,
			source:   f.pay,
			target:   cardTarget,
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
		},
		{
			name:     "repayment target holds payment envelope and account",
			checking: 10_000,
			spend:    2_000,
			pay:      300,
			source:   f.spend,
			target:   repaymentTarget,
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := authorizationLedgerForTest(t, f, tt.checking, tt.spend, tt.pay)

			outcome, err := AuthorizeWithReallocation(
				state,
				tt.source,
				tt.target,
				mustPositiveAmountForTest(t, tt.delta, f.currency),
				mustPositiveAmountForTest(t, tt.amount, f.currency),
				tt.mode,
				tt.reason,
			)
			if err != nil {
				t.Fatalf("AuthorizeWithReallocation: %v", err)
			}
			if outcome.Mode != tt.mode {
				t.Errorf("Outcome.Mode = %q, want %q", outcome.Mode, tt.mode)
			}
			if got := outcome.Shortfall.MinorUnits(); got != tt.wantShortfall {
				t.Errorf("Outcome.Shortfall = %d, want %d", got, tt.wantShortfall)
			}
			assertAuthorizationShape(t, outcome, tt.target, tt.amount)

			// The reallocation postings move delta from source to target.
			sourceStart := reallocationEnvelopeStart(f, tt.source, tt.spend, tt.pay)
			targetStart := reallocationEnvelopeStart(f, tt.target.EnvelopeID, tt.spend, tt.pay)
			sourceDelta, err := outcome.Effects.Postings.EnvelopeDelta(tt.source)
			if err != nil {
				t.Fatalf("EnvelopeDelta(source): %v", err)
			}
			if got := sourceDelta.MinorUnits(); got != -tt.delta {
				t.Errorf("source EnvelopeDelta = %d, want %d", got, -tt.delta)
			}
			targetDelta, err := outcome.Effects.Postings.EnvelopeDelta(tt.target.EnvelopeID)
			if err != nil {
				t.Fatalf("EnvelopeDelta(target): %v", err)
			}
			if got := targetDelta.MinorUnits(); got != tt.delta {
				t.Errorf("target EnvelopeDelta = %d, want %d", got, tt.delta)
			}

			if err := state.ApplyPostings(outcome.Effects.Postings); err != nil {
				t.Fatalf("ApplyPostings: %v", err)
			}
			if err := state.ApplyReservationChanges(outcome.Effects.Reservations...); err != nil {
				t.Fatalf("ApplyReservationChanges: %v", err)
			}

			assertMoney(t, state.EnvelopeBalance, tt.target.EnvelopeID, targetStart+tt.delta)
			assertMoney(t, state.EnvelopeBalance, tt.source, sourceStart-tt.delta)
			assertMoney(t, state.SpendableEnvelopeBalance, tt.target.EnvelopeID, targetStart+tt.delta-tt.amount)
			assertMoney(t, state.PostedAccountBalance, f.checking, tt.checking)
			wantAccountSpendable := tt.checking
			if !tt.target.AccountID.IsZero() {
				wantAccountSpendable = tt.checking - tt.amount
			}
			assertMoney(t, state.SpendableAccountBalance, f.checking, wantAccountSpendable)
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

func TestAuthorizeWithReallocationRejections(t *testing.T) {
	f := newFundingFixture(t)
	plan := mustPlanIDForTest(t, "plan-reallocate-reject")
	reason := mustOverrideReasonForTest(t, "medical emergency")
	eur := mustCurrency(t, "EUR", 2)
	debitTarget := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}

	tests := []struct {
		name          string
		checking      int64
		spend         int64
		pay           int64
		source        EnvelopeId
		target        ReservationTarget
		delta         int64
		deltaCurrency Currency
		amount        int64
		mode          AuthorizationMode
		reason        OverrideReason
		wantCode      ErrorCode
	}{
		{
			name:     "normal delta leaves the target short",
			checking: 10_000,
			spend:    300,
			pay:      2_000,
			source:   f.pay,
			target:   debitTarget,
			delta:    500,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
			wantCode: CodeInsufficientEnvelopeFunds,
		},
		{
			name:     "source envelope insufficient for delta",
			checking: 10_000,
			spend:    300,
			pay:      200,
			source:   f.pay,
			target:   debitTarget,
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
			wantCode: CodeInsufficientEnvelopeFunds,
		},
		{
			name:     "override does not bypass source envelope liquidity",
			checking: 10_000,
			spend:    300,
			pay:      200,
			source:   f.pay,
			target:   debitTarget,
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeEmergencyOverride,
			reason:   reason,
			wantCode: CodeInsufficientEnvelopeFunds,
		},
		{
			name:     "reallocation into itself",
			checking: 10_000,
			spend:    3_000,
			pay:      2_000,
			source:   f.spend,
			target:   debitTarget,
			delta:    100,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
			wantCode: CodeInvalidValue,
		},
		{
			name:     "invalid mode",
			checking: 10_000,
			spend:    300,
			pay:      2_000,
			source:   f.pay,
			target:   debitTarget,
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationMode("BOGUS"),
			wantCode: CodeInvalidValue,
		},
		{
			name:     "override without reason",
			checking: 10_000,
			spend:    300,
			pay:      2_000,
			source:   f.pay,
			target:   debitTarget,
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeEmergencyOverride,
			wantCode: CodeOverrideReasonRequired,
		},
		{
			name:     "zero plan id",
			checking: 10_000,
			spend:    300,
			pay:      2_000,
			source:   f.pay,
			target:   ReservationTarget{EnvelopeID: f.spend},
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero envelope id",
			checking: 10_000,
			spend:    300,
			pay:      2_000,
			source:   f.pay,
			target:   ReservationTarget{PlanID: plan},
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
			wantCode: CodeInvalidValue,
		},
		{
			name:     "unknown source envelope",
			checking: 10_000,
			spend:    300,
			pay:      2_000,
			source:   mustEnvelopeIDForTest(t, "env-ghost"),
			target:   debitTarget,
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
			wantCode: CodeEntityNotFoundOrNotInHousehold,
		},
		{
			name:     "unknown target envelope",
			checking: 10_000,
			spend:    300,
			pay:      2_000,
			source:   f.pay,
			target:   ReservationTarget{PlanID: plan, EnvelopeID: mustEnvelopeIDForTest(t, "env-ghost")},
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
			wantCode: CodeEntityNotFoundOrNotInHousehold,
		},
		{
			name:     "unknown account",
			checking: 10_000,
			spend:    300,
			pay:      2_000,
			source:   f.pay,
			target:   ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: mustAccountIDForTest(t, "acct-ghost")},
			delta:    700,
			amount:   1_000,
			mode:     AuthorizationModeNormal,
			wantCode: CodeEntityNotFoundOrNotInHousehold,
		},
		{
			name:          "delta currency mismatch",
			checking:      10_000,
			spend:         300,
			pay:           2_000,
			source:        f.pay,
			target:        debitTarget,
			delta:         700,
			deltaCurrency: eur,
			amount:        1_000,
			mode:          AuthorizationModeNormal,
			wantCode:      CodeCurrencyMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := authorizationLedgerForTest(t, f, tt.checking, tt.spend, tt.pay)
			deltaCurrency := tt.deltaCurrency
			if deltaCurrency.IsZero() {
				deltaCurrency = f.currency
			}

			outcome, err := AuthorizeWithReallocation(
				state,
				tt.source,
				tt.target,
				mustPositiveAmountForTest(t, tt.delta, deltaCurrency),
				mustPositiveAmountForTest(t, tt.amount, f.currency),
				tt.mode,
				tt.reason,
			)
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("AuthorizeWithReallocation error = %v, want code %s", err, tt.wantCode)
			}
			if !outcome.Effects.IsEmpty() {
				t.Errorf("rejected Effects = %+v, want empty", outcome.Effects)
			}
		})
	}
}

func TestAuthorizeWithReallocationZeroAmounts(t *testing.T) {
	f := newFundingFixture(t)
	plan := mustPlanIDForTest(t, "plan-reallocate-zero")
	target := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}
	state := authorizationLedgerForTest(t, f, 10_000, 3_000, 2_000)

	tests := []struct {
		name   string
		delta  PositiveAmount
		amount PositiveAmount
	}{
		{
			name:   "zero delta",
			delta:  PositiveAmount{},
			amount: mustPositiveAmountForTest(t, 1_000, f.currency),
		},
		{
			name:   "zero amount",
			delta:  mustPositiveAmountForTest(t, 700, f.currency),
			amount: PositiveAmount{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, err := AuthorizeWithReallocation(state, f.pay, target, tt.delta, tt.amount, AuthorizationModeNormal, "")
			if !HasCode(err, CodeInvalidAmount) {
				t.Fatalf("AuthorizeWithReallocation error = %v, want code %s", err, CodeInvalidAmount)
			}
			if !outcome.Effects.IsEmpty() {
				t.Errorf("rejected Effects = %+v, want empty", outcome.Effects)
			}
		})
	}
}
