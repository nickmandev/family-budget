package domain

import "testing"

// resizeLedgerForTest builds the funding fixture ledger with an existing hold
// already applied for the target.
func resizeLedgerForTest(t *testing.T, f *fundingFixture, target ReservationTarget, checking, spend, pay, held int64) *LedgerState {
	t.Helper()
	state := authorizationLedgerForTest(t, f, checking, spend, pay)
	holdFundingForTest(t, state, target, held)
	return state
}

func TestReauthorizeReservation(t *testing.T) {
	f := newFundingFixture(t)
	plan := mustPlanIDForTest(t, "plan-resize")
	reason := mustOverrideReasonForTest(t, "keep the plan viable")
	debitTarget := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}

	tests := []struct {
		name          string
		checking      int64
		spend         int64
		held          int64
		newAmount     int64
		mode          AuthorizationMode
		reason        OverrideReason
		wantShortfall int64
		wantDelta     int64
		wantNoEffects bool
	}{
		{
			name:      "increase with sufficient spendable funds",
			checking:  10_000,
			spend:     3_000,
			held:      1_000,
			newAmount: 2_000,
			mode:      AuthorizationModeNormal,
			wantDelta: 1_000,
		},
		{
			name:          "increase via override leaves an envelope shortfall",
			checking:      10_000,
			spend:         3_000,
			held:          1_000,
			newAmount:     5_000,
			mode:          AuthorizationModeEmergencyOverride,
			reason:        reason,
			wantShortfall: 2_000,
			wantDelta:     4_000,
		},
		{
			name:      "decrease releases the excess without funding checks",
			checking:  100,
			spend:     100,
			held:      1_000,
			newAmount: 400,
			mode:      AuthorizationModeNormal,
			wantDelta: -600,
		},
		{
			name:          "equal amounts are a no-op",
			checking:      10_000,
			spend:         3_000,
			held:          1_000,
			newAmount:     1_000,
			mode:          AuthorizationModeNormal,
			wantNoEffects: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := debitTarget
			state := resizeLedgerForTest(t, f, target, tt.checking, tt.spend, 0, tt.held)

			outcome, err := ReauthorizeReservation(
				state,
				target,
				mustPositiveAmountForTest(t, tt.held, f.currency),
				mustPositiveAmountForTest(t, tt.newAmount, f.currency),
				tt.mode,
				tt.reason,
			)
			if err != nil {
				t.Fatalf("ReauthorizeReservation: %v", err)
			}
			if outcome.Mode != tt.mode {
				t.Errorf("Outcome.Mode = %q, want %q", outcome.Mode, tt.mode)
			}
			if got := outcome.Shortfall.MinorUnits(); got != tt.wantShortfall {
				t.Errorf("Outcome.Shortfall = %d, want %d", got, tt.wantShortfall)
			}
			if tt.wantNoEffects {
				if !outcome.Effects.IsEmpty() {
					t.Fatalf("Effects = %+v, want empty", outcome.Effects)
				}
				return
			}
			if len(outcome.Effects.Reservations) != 1 {
				t.Fatalf("Effects.Reservations = %d, want 1", len(outcome.Effects.Reservations))
			}
			change := outcome.Effects.Reservations[0]
			if got := change.Envelope.Delta.MinorUnits(); got != tt.wantDelta {
				t.Errorf("Envelope.Delta = %d, want %d", got, tt.wantDelta)
			}
			if change.Account == nil {
				t.Fatal("Account = nil, want account hold")
			}
			if got := change.Account.Delta.MinorUnits(); got != tt.wantDelta {
				t.Errorf("Account.Delta = %d, want %d", got, tt.wantDelta)
			}

			if err := state.ApplyReservationChanges(outcome.Effects.Reservations...); err != nil {
				t.Fatalf("ApplyReservationChanges: %v", err)
			}
			assertMoney(t, state.ActiveEnvelopeReservations, target.EnvelopeID, tt.newAmount)
			assertMoney(t, state.ActiveAccountReservations, f.checking, tt.newAmount)
			assertMoney(t, state.SpendableEnvelopeBalance, target.EnvelopeID, tt.spend-tt.newAmount)
			assertMoney(t, state.SpendableAccountBalance, f.checking, tt.checking-tt.newAmount)
			assertMoney(t, state.PostedAccountBalance, f.checking, tt.checking)
			assertMoney(t, state.EnvelopeBalance, target.EnvelopeID, tt.spend)
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

func TestReauthorizeReservationRejections(t *testing.T) {
	f := newFundingFixture(t)
	plan := mustPlanIDForTest(t, "plan-resize-reject")
	reason := mustOverrideReasonForTest(t, "medical emergency")
	eur := mustCurrency(t, "EUR", 2)
	debitTarget := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}

	tests := []struct {
		name           string
		checking       int64
		spend          int64
		target         ReservationTarget
		held           PositiveAmount
		heldMinor      int64
		newAmount      PositiveAmount
		newAmountMinor int64
		newCurrency    Currency
		mode           AuthorizationMode
		reason         OverrideReason
		noInitialHold  bool
		wantCode       ErrorCode
	}{
		{
			name:           "normal increase beyond spendable envelope",
			checking:       10_000,
			spend:          3_000,
			target:         debitTarget,
			heldMinor:      1_000,
			newAmountMinor: 5_000,
			mode:           AuthorizationModeNormal,
			wantCode:       CodeInsufficientEnvelopeFunds,
		},
		{
			name:           "override increase without reason",
			checking:       10_000,
			spend:          3_000,
			target:         debitTarget,
			heldMinor:      1_000,
			newAmountMinor: 5_000,
			mode:           AuthorizationModeEmergencyOverride,
			wantCode:       CodeOverrideReasonRequired,
		},
		{
			name:           "normal increase exceeds account spendable",
			checking:       1_000,
			spend:          3_000,
			target:         debitTarget,
			heldMinor:      1_000,
			newAmountMinor: 2_000,
			mode:           AuthorizationModeNormal,
			wantCode:       CodeInsufficientAccountFunds,
		},
		{
			name:           "override never bypasses account liquidity",
			checking:       1_000,
			spend:          3_000,
			target:         debitTarget,
			heldMinor:      1_000,
			newAmountMinor: 2_000,
			mode:           AuthorizationModeEmergencyOverride,
			reason:         reason,
			wantCode:       CodeInsufficientAccountFunds,
		},
		{
			name:           "invalid mode",
			checking:       10_000,
			spend:          3_000,
			target:         debitTarget,
			heldMinor:      1_000,
			newAmountMinor: 2_000,
			mode:           AuthorizationMode("BOGUS"),
			wantCode:       CodeInvalidValue,
		},
		{
			name:           "zero plan id",
			checking:       10_000,
			spend:          3_000,
			target:         ReservationTarget{EnvelopeID: f.spend, AccountID: f.checking},
			heldMinor:      1_000,
			newAmountMinor: 2_000,
			mode:           AuthorizationModeNormal,
			noInitialHold:  true,
			wantCode:       CodeInvalidValue,
		},
		{
			name:           "zero envelope id",
			checking:       10_000,
			spend:          3_000,
			target:         ReservationTarget{PlanID: plan, AccountID: f.checking},
			heldMinor:      1_000,
			newAmountMinor: 2_000,
			mode:           AuthorizationModeNormal,
			noInitialHold:  true,
			wantCode:       CodeInvalidValue,
		},
		{
			name:           "zero held amount",
			checking:       10_000,
			spend:          3_000,
			target:         debitTarget,
			held:           PositiveAmount{},
			newAmountMinor: 2_000,
			mode:           AuthorizationModeNormal,
			noInitialHold:  true,
			wantCode:       CodeInvalidAmount,
		},
		{
			name:      "zero new amount",
			checking:  10_000,
			spend:     3_000,
			target:    debitTarget,
			heldMinor: 1_000,
			newAmount: PositiveAmount{},
			mode:      AuthorizationModeNormal,
			wantCode:  CodeInvalidAmount,
		},
		{
			name:           "new amount currency mismatch",
			checking:       10_000,
			spend:          3_000,
			target:         debitTarget,
			heldMinor:      1_000,
			newAmountMinor: 2_000,
			newCurrency:    eur,
			mode:           AuthorizationModeNormal,
			wantCode:       CodeCurrencyMismatch,
		},
		{
			name:           "unknown envelope",
			checking:       10_000,
			spend:          3_000,
			target:         ReservationTarget{PlanID: plan, EnvelopeID: mustEnvelopeIDForTest(t, "env-ghost"), AccountID: f.checking},
			heldMinor:      1_000,
			newAmountMinor: 2_000,
			mode:           AuthorizationModeNormal,
			noInitialHold:  true,
			wantCode:       CodeEntityNotFoundOrNotInHousehold,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := authorizationLedgerForTest(t, f, tt.checking, tt.spend, 0)
			if !tt.noInitialHold && tt.heldMinor > 0 {
				holdFundingForTest(t, state, tt.target, tt.heldMinor)
			}

			held := tt.held
			if tt.heldMinor > 0 {
				held = mustPositiveAmountForTest(t, tt.heldMinor, f.currency)
			}
			newAmount := tt.newAmount
			if tt.newAmountMinor > 0 {
				newCurrency := tt.newCurrency
				if newCurrency.IsZero() {
					newCurrency = f.currency
				}
				newAmount = mustPositiveAmountForTest(t, tt.newAmountMinor, newCurrency)
			}

			outcome, err := ReauthorizeReservation(state, tt.target, held, newAmount, tt.mode, tt.reason)
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("ReauthorizeReservation error = %v, want code %s", err, tt.wantCode)
			}
			if !outcome.Effects.IsEmpty() {
				t.Errorf("rejected Effects = %+v, want empty", outcome.Effects)
			}
		})
	}
}

func TestAuthorizeCompletion(t *testing.T) {
	f := newFundingFixture(t)
	plan := mustPlanIDForTest(t, "plan-completion")

	tests := []struct {
		name          string
		target        ReservationTarget
		held          int64
		heldOnAccount int64
		authorized    int64
		final         int64
		purchase      func(t *testing.T) Effects
		wantAccounts  map[AccountId]int64
		wantEnvelopes map[EnvelopeId]int64
		wantCard      int64
	}{
		{
			name:          "debit completion below held releases the excess",
			target:        ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking},
			held:          1_000,
			heldOnAccount: 1_000,
			authorized:    1_000,
			final:         800,
			purchase: func(t *testing.T) Effects {
				purchase, err := DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 800, f.currency))
				if err != nil {
					t.Fatalf("DebitPurchaseEffects: %v", err)
				}
				return purchase
			},
			wantAccounts:  map[AccountId]int64{f.checking: 9_200},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 2_200, f.pay: 1_000},
		},
		{
			name:       "card completion at the authorized amount",
			target:     ReservationTarget{PlanID: plan, EnvelopeID: f.spend},
			held:       1_000,
			authorized: 1_000,
			final:      1_000,
			purchase: func(t *testing.T) Effects {
				purchase, err := CreditCardPurchaseEffects(f.card, f.spend, f.pay, mustPositiveAmountForTest(t, 1_000, f.currency))
				if err != nil {
					t.Fatalf("CreditCardPurchaseEffects: %v", err)
				}
				return purchase
			},
			wantAccounts:  map[AccountId]int64{f.checking: 10_000},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 2_000, f.pay: 2_000},
			wantCard:      1_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := authorizationLedgerForTest(t, f, 10_000, 3_000, 1_000)
			holdFundingForTest(t, state, tt.target, tt.held)

			heldOnAccount := PositiveAmount{}
			if tt.heldOnAccount > 0 {
				heldOnAccount = mustPositiveAmountForTest(t, tt.heldOnAccount, f.currency)
			}
			completed, err := AuthorizeCompletion(
				state,
				tt.target,
				mustPositiveAmountForTest(t, tt.held, f.currency),
				heldOnAccount,
				mustPositiveAmountForTest(t, tt.authorized, f.currency),
				mustPositiveAmountForTest(t, tt.final, f.currency),
				tt.purchase(t),
			)
			if err != nil {
				t.Fatalf("AuthorizeCompletion: %v", err)
			}
			if err := state.ApplyPostings(completed.Postings); err != nil {
				t.Fatalf("ApplyPostings: %v", err)
			}
			if err := state.ApplyReservationChanges(completed.Reservations...); err != nil {
				t.Fatalf("ApplyReservationChanges: %v", err)
			}

			assertAccountBalances(t, state, tt.wantAccounts)
			assertEnvelopeBalances(t, state, tt.wantEnvelopes)
			assertMoney(t, state.ActiveEnvelopeReservations, tt.target.EnvelopeID, 0)
			if !tt.target.AccountID.IsZero() {
				assertMoney(t, state.ActiveAccountReservations, tt.target.AccountID, 0)
			}
			if tt.wantCard != 0 {
				assertMoney(t, state.CardLiabilityBalance, f.card, tt.wantCard)
			}
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

func TestAuthorizeCompletionRejections(t *testing.T) {
	f := newFundingFixture(t)
	plan := mustPlanIDForTest(t, "plan-completion-reject")
	eur := mustCurrency(t, "EUR", 2)
	debitTarget := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}
	cardTarget := ReservationTarget{PlanID: plan, EnvelopeID: f.spend}

	held := mustPositiveAmountForTest(t, 1_000, f.currency)
	heldOnAccount := mustPositiveAmountForTest(t, 1_000, f.currency)
	authorized := mustPositiveAmountForTest(t, 1_000, f.currency)
	final := mustPositiveAmountForTest(t, 800, f.currency)

	tests := []struct {
		name          string
		target        ReservationTarget
		held          PositiveAmount
		heldOnAccount PositiveAmount
		authorized    PositiveAmount
		final         PositiveAmount
		wantCode      ErrorCode
	}{
		{
			name:          "final exceeds authorization",
			target:        debitTarget,
			held:          held,
			heldOnAccount: heldOnAccount,
			authorized:    authorized,
			final:         mustPositiveAmountForTest(t, 1_200, f.currency),
			wantCode:      CodeAmountExceedsAuthorization,
		},
		{
			name:          "zero held amount",
			target:        debitTarget,
			held:          PositiveAmount{},
			heldOnAccount: heldOnAccount,
			authorized:    authorized,
			final:         final,
			wantCode:      CodeReservationMismatch,
		},
		{
			name:          "account target without account hold",
			target:        debitTarget,
			held:          held,
			heldOnAccount: PositiveAmount{},
			authorized:    authorized,
			final:         final,
			wantCode:      CodeReservationMismatch,
		},
		{
			name:          "account hold without account target",
			target:        cardTarget,
			held:          held,
			heldOnAccount: heldOnAccount,
			authorized:    authorized,
			final:         final,
			wantCode:      CodeReservationMismatch,
		},
		{
			name:          "zero plan id",
			target:        ReservationTarget{EnvelopeID: f.spend, AccountID: f.checking},
			held:          held,
			heldOnAccount: heldOnAccount,
			authorized:    authorized,
			final:         final,
			wantCode:      CodeInvalidValue,
		},
		{
			name:          "zero envelope id",
			target:        ReservationTarget{PlanID: plan, AccountID: f.checking},
			held:          held,
			heldOnAccount: heldOnAccount,
			authorized:    authorized,
			final:         final,
			wantCode:      CodeInvalidValue,
		},
		{
			name:          "final and authorized currencies differ",
			target:        debitTarget,
			held:          held,
			heldOnAccount: heldOnAccount,
			authorized:    authorized,
			final:         mustPositiveAmountForTest(t, 800, eur),
			wantCode:      CodeCurrencyMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := authorizationLedgerForTest(t, f, 10_000, 3_000, 1_000)
			effects, err := AuthorizeCompletion(state, tt.target, tt.held, tt.heldOnAccount, tt.authorized, tt.final, Effects{})
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("AuthorizeCompletion error = %v, want code %s", err, tt.wantCode)
			}
			if !effects.IsEmpty() {
				t.Errorf("rejected Effects = %+v, want empty", effects)
			}
		})
	}
}

// TestAuthorizeAndCompleteFlow exercises the full funded lifecycle: authorize,
// apply holds, complete within authorization, and confirm posted balances moved
// while reservations are released and the budget stays clear.
func TestAuthorizeAndCompleteFlow(t *testing.T) {
	f := newFundingFixture(t)
	plan := mustPlanIDForTest(t, "plan-flow")
	target := ReservationTarget{PlanID: plan, EnvelopeID: f.spend, AccountID: f.checking}
	state := authorizationLedgerForTest(t, f, 10_000, 3_000, 0)

	before, err := state.ResolutionStatus()
	if err != nil {
		t.Fatalf("ResolutionStatus(before): %v", err)
	}

	outcome, err := AuthorizePurchase(state, target, mustPositiveAmountForTest(t, 1_000, f.currency), AuthorizationModeNormal, "")
	if err != nil {
		t.Fatalf("AuthorizePurchase: %v", err)
	}
	if err := state.ApplyReservationChanges(outcome.Effects.Reservations...); err != nil {
		t.Fatalf("ApplyReservationChanges: %v", err)
	}
	assertMoney(t, state.SpendableEnvelopeBalance, f.spend, 2_000)
	assertMoney(t, state.SpendableAccountBalance, f.checking, 9_000)

	amount := mustPositiveAmountForTest(t, 1_000, f.currency)
	purchase, err := DebitPurchaseEffects(f.checking, f.spend, amount)
	if err != nil {
		t.Fatalf("DebitPurchaseEffects: %v", err)
	}
	completed, err := AuthorizeCompletion(state, target, amount, amount, amount, amount, purchase)
	if err != nil {
		t.Fatalf("AuthorizeCompletion: %v", err)
	}
	if err := state.ApplyPostings(completed.Postings); err != nil {
		t.Fatalf("ApplyPostings(completed): %v", err)
	}
	if err := state.ApplyReservationChanges(completed.Reservations...); err != nil {
		t.Fatalf("ApplyReservationChanges(completed): %v", err)
	}

	assertMoney(t, state.PostedAccountBalance, f.checking, 9_000)
	assertMoney(t, state.EnvelopeBalance, f.spend, 2_000)
	assertMoney(t, state.ActiveEnvelopeReservations, f.spend, 0)
	assertMoney(t, state.ActiveAccountReservations, f.checking, 0)
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
	after, err := state.ResolutionStatus()
	if err != nil {
		t.Fatalf("ResolutionStatus(after): %v", err)
	}
	if after != before {
		t.Errorf("ResolutionStatus = %q, want unchanged %q", after, before)
	}
	if after != ResolutionClear {
		t.Errorf("ResolutionStatus = %q, want %q", after, ResolutionClear)
	}
}
