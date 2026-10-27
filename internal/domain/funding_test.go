package domain

import "testing"

// fundingFixture is the shared ledger shape for the funding-check tests: every
// account kind/eligibility combination plus a spending and a payment envelope.
// Resolution tests reuse it.
type fundingFixture struct {
	currency Currency
	checking AccountId
	savings  AccountId
	wallet   AccountId
	excluded AccountId
	card     AccountId
	spend    EnvelopeId
	pay      EnvelopeId
}

func newFundingFixture(t *testing.T) *fundingFixture {
	t.Helper()
	return &fundingFixture{
		currency: mustCurrency(t, "USD", 2),
		checking: mustAccountIDForTest(t, "acct-checking"),
		savings:  mustAccountIDForTest(t, "acct-savings"),
		wallet:   mustAccountIDForTest(t, "acct-wallet"),
		excluded: mustAccountIDForTest(t, "acct-excluded"),
		card:     mustAccountIDForTest(t, "acct-card"),
		spend:    mustEnvelopeIDForTest(t, "env-spend"),
		pay:      mustEnvelopeIDForTest(t, "env-pay"),
	}
}

func (f *fundingFixture) newLedger(t *testing.T) *LedgerState {
	t.Helper()
	return mustLedgerForTest(t, f.currency,
		[]AccountMeta{
			mustAccountMetaForTest(t, f.checking, AccountKindChecking, true),
			mustAccountMetaForTest(t, f.savings, AccountKindSavings, true),
			mustAccountMetaForTest(t, f.wallet, AccountKindCashWallet, true),
			mustAccountMetaForTest(t, f.excluded, AccountKindSavings, false),
			mustAccountMetaForTest(t, f.card, AccountKindCardLiability, false),
		},
		[]EnvelopeId{f.spend, f.pay},
	)
}

// applyFundingPostingsForTest posts the given signed account and envelope
// deltas; zero entries are skipped because postings reject zero deltas.
func applyFundingPostingsForTest(t *testing.T, state *LedgerState, accounts map[AccountId]int64, envelopes map[EnvelopeId]int64) {
	t.Helper()
	postings := PostingSet{}
	for id, minorUnits := range accounts {
		if minorUnits == 0 {
			continue
		}
		if err := postings.AddAccount(id, mustSignedDeltaForTest(t, minorUnits, state.Currency())); err != nil {
			t.Fatalf("AddAccount(%q): %v", id, err)
		}
	}
	for id, minorUnits := range envelopes {
		if minorUnits == 0 {
			continue
		}
		if err := postings.AddEnvelope(id, mustSignedDeltaForTest(t, minorUnits, state.Currency())); err != nil {
			t.Fatalf("AddEnvelope(%q): %v", id, err)
		}
	}
	if err := state.ApplyPostings(postings); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}
}

func applyFundingEffectsForTest(t *testing.T, state *LedgerState, effects Effects) {
	t.Helper()
	if err := state.ApplyPostings(effects.Postings); err != nil {
		t.Fatalf("ApplyPostings(effects): %v", err)
	}
}

// holdFundingForTest creates and applies a reservation hold of minorUnits on
// the target.
func holdFundingForTest(t *testing.T, state *LedgerState, target ReservationTarget, minorUnits int64) {
	t.Helper()
	effects, err := CreateReservationEffects(target, mustPositiveAmountForTest(t, minorUnits, state.Currency()))
	if err != nil {
		t.Fatalf("CreateReservationEffects: %v", err)
	}
	if err := state.ApplyReservationChanges(effects.Reservations...); err != nil {
		t.Fatalf("ApplyReservationChanges: %v", err)
	}
}

func assertFundingMinorUnits(t *testing.T, label string, got Money, want int64) {
	t.Helper()
	if got.MinorUnits() != want {
		t.Errorf("%s = %d minor units, want %d", label, got.MinorUnits(), want)
	}
}

func assertFundingErrorCode(t *testing.T, err error, wantCode ErrorCode) {
	t.Helper()
	if wantCode == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if !HasCode(err, wantCode) {
		t.Fatalf("error = %v, want code %s", err, wantCode)
	}
}

func TestRequireReadyToAssign(t *testing.T) {
	f := newFundingFixture(t)

	tests := []struct {
		name     string
		amount   int64
		wantCode ErrorCode
	}{
		{name: "funded", amount: 600},
		{name: "exact cover", amount: 1_000},
		{name: "one minor unit over", amount: 1_001, wantCode: CodeInsufficientReadyToAssign},
		{name: "far short", amount: 5_000, wantCode: CodeInsufficientReadyToAssign},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := f.newLedger(t)
			applyFundingPostingsForTest(t, state, map[AccountId]int64{f.checking: 1_000}, nil)

			err := state.RequireReadyToAssign(mustPositiveAmountForTest(t, tt.amount, f.currency))
			assertFundingErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestRequireEnvelopeFunds(t *testing.T) {
	f := newFundingFixture(t)

	tests := []struct {
		name            string
		envelopeBalance int64
		held            int64
		amount          int64
		wantCode        ErrorCode
	}{
		{name: "funded", envelopeBalance: 1_000, amount: 400},
		{name: "exact cover", envelopeBalance: 1_000, amount: 1_000},
		{name: "one minor unit over", envelopeBalance: 1_000, amount: 1_001, wantCode: CodeInsufficientEnvelopeFunds},
		{name: "reserved funds are not spendable", envelopeBalance: 1_000, held: 400, amount: 700, wantCode: CodeInsufficientEnvelopeFunds},
		{name: "exact spendable after reservation", envelopeBalance: 1_000, held: 400, amount: 600},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := f.newLedger(t)
			applyFundingPostingsForTest(t, state, map[AccountId]int64{f.checking: 5_000}, map[EnvelopeId]int64{f.spend: tt.envelopeBalance})
			if tt.held > 0 {
				holdFundingForTest(t, state, ReservationTarget{
					PlanID:     mustPlanIDForTest(t, "plan-envelope"),
					EnvelopeID: f.spend,
				}, tt.held)
			}

			err := state.RequireEnvelopeFunds(f.spend, mustPositiveAmountForTest(t, tt.amount, f.currency))
			assertFundingErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestRequireAccountFunds(t *testing.T) {
	f := newFundingFixture(t)

	tests := []struct {
		name     string
		posted   int64
		held     int64
		amount   int64
		wantCode ErrorCode
	}{
		{name: "funded", posted: 1_000, amount: 600},
		{name: "exact cover", posted: 1_000, amount: 1_000},
		{name: "one minor unit over", posted: 1_000, amount: 1_001, wantCode: CodeInsufficientAccountFunds},
		{name: "reserved funds are not spendable", posted: 1_000, held: 400, amount: 700, wantCode: CodeInsufficientAccountFunds},
		{name: "exact spendable after reservation", posted: 1_000, held: 400, amount: 600},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := f.newLedger(t)
			applyFundingPostingsForTest(t, state, map[AccountId]int64{f.checking: tt.posted}, map[EnvelopeId]int64{f.spend: tt.posted})
			if tt.held > 0 {
				holdFundingForTest(t, state, ReservationTarget{
					PlanID:     mustPlanIDForTest(t, "plan-account"),
					EnvelopeID: f.spend,
					AccountID:  f.checking,
				}, tt.held)
			}

			err := state.RequireAccountFunds(f.checking, mustPositiveAmountForTest(t, tt.amount, f.currency))
			assertFundingErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestEnvelopeShortfall(t *testing.T) {
	f := newFundingFixture(t)

	tests := []struct {
		name            string
		envelopeBalance int64
		held            int64
		amount          int64
		want            int64
	}{
		{name: "covered", envelopeBalance: 500, held: 200, amount: 100, want: 0},
		{name: "exact cover", envelopeBalance: 500, held: 200, amount: 300, want: 0},
		{name: "insufficient", envelopeBalance: 500, held: 200, amount: 1_000, want: 700},
		{name: "no funds", envelopeBalance: 0, amount: 250, want: 250},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := f.newLedger(t)
			applyFundingPostingsForTest(t, state, map[AccountId]int64{f.checking: 5_000}, map[EnvelopeId]int64{f.spend: tt.envelopeBalance})
			if tt.held > 0 {
				holdFundingForTest(t, state, ReservationTarget{
					PlanID:     mustPlanIDForTest(t, "plan-shortfall"),
					EnvelopeID: f.spend,
				}, tt.held)
			}

			shortfall, err := state.EnvelopeShortfall(f.spend, mustPositiveAmountForTest(t, tt.amount, f.currency))
			if err != nil {
				t.Fatalf("EnvelopeShortfall: %v", err)
			}
			assertFundingMinorUnits(t, "EnvelopeShortfall", shortfall, tt.want)
			if shortfall.IsNegative() {
				t.Errorf("EnvelopeShortfall = %d minor units, want never negative", shortfall.MinorUnits())
			}
		})
	}
}

func TestAccountShortfall(t *testing.T) {
	f := newFundingFixture(t)

	tests := []struct {
		name   string
		posted int64
		held   int64
		amount int64
		want   int64
	}{
		{name: "covered", posted: 1_000, amount: 400, want: 0},
		{name: "exact cover", posted: 1_000, amount: 1_000, want: 0},
		{name: "reserved shortfall", posted: 1_000, held: 300, amount: 900, want: 200},
		{name: "no funds", posted: 0, amount: 250, want: 250},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := f.newLedger(t)
			applyFundingPostingsForTest(t, state, map[AccountId]int64{f.checking: tt.posted}, map[EnvelopeId]int64{f.spend: tt.posted})
			if tt.held > 0 {
				holdFundingForTest(t, state, ReservationTarget{
					PlanID:     mustPlanIDForTest(t, "plan-account-shortfall"),
					EnvelopeID: f.spend,
					AccountID:  f.checking,
				}, tt.held)
			}

			shortfall, err := state.AccountShortfall(f.checking, mustPositiveAmountForTest(t, tt.amount, f.currency))
			if err != nil {
				t.Fatalf("AccountShortfall: %v", err)
			}
			assertFundingMinorUnits(t, "AccountShortfall", shortfall, tt.want)
			if shortfall.IsNegative() {
				t.Errorf("AccountShortfall = %d minor units, want never negative", shortfall.MinorUnits())
			}
		})
	}
}

func TestFundingRejections(t *testing.T) {
	f := newFundingFixture(t)
	unknownEnvelope := mustEnvelopeIDForTest(t, "env-ghost")
	unknownAccount := mustAccountIDForTest(t, "acct-ghost")
	eur := mustCurrency(t, "EUR", 2)
	usdAmount := mustPositiveAmountForTest(t, 100, f.currency)
	eurAmount := mustPositiveAmountForTest(t, 100, eur)

	tests := []struct {
		name     string
		call     func(t *testing.T, state *LedgerState) error
		wantCode ErrorCode
	}{
		{
			name: "unknown envelope require",
			call: func(t *testing.T, state *LedgerState) error {
				return state.RequireEnvelopeFunds(unknownEnvelope, usdAmount)
			},
			wantCode: CodeEntityNotFoundOrNotInHousehold,
		},
		{
			name: "unknown envelope shortfall",
			call: func(t *testing.T, state *LedgerState) error {
				_, err := state.EnvelopeShortfall(unknownEnvelope, usdAmount)
				return err
			},
			wantCode: CodeEntityNotFoundOrNotInHousehold,
		},
		{
			name: "unknown account require",
			call: func(t *testing.T, state *LedgerState) error {
				return state.RequireAccountFunds(unknownAccount, usdAmount)
			},
			wantCode: CodeEntityNotFoundOrNotInHousehold,
		},
		{
			name: "unknown account shortfall",
			call: func(t *testing.T, state *LedgerState) error {
				_, err := state.AccountShortfall(unknownAccount, usdAmount)
				return err
			},
			wantCode: CodeEntityNotFoundOrNotInHousehold,
		},
		{
			name: "ready-to-assign wrong currency",
			call: func(t *testing.T, state *LedgerState) error {
				return state.RequireReadyToAssign(eurAmount)
			},
			wantCode: CodeCurrencyMismatch,
		},
		{
			name: "envelope require wrong currency",
			call: func(t *testing.T, state *LedgerState) error {
				return state.RequireEnvelopeFunds(f.spend, eurAmount)
			},
			wantCode: CodeCurrencyMismatch,
		},
		{
			name: "envelope shortfall wrong currency",
			call: func(t *testing.T, state *LedgerState) error {
				_, err := state.EnvelopeShortfall(f.spend, eurAmount)
				return err
			},
			wantCode: CodeCurrencyMismatch,
		},
		{
			name: "account require wrong currency",
			call: func(t *testing.T, state *LedgerState) error {
				return state.RequireAccountFunds(f.checking, eurAmount)
			},
			wantCode: CodeCurrencyMismatch,
		},
		{
			name: "account shortfall wrong currency",
			call: func(t *testing.T, state *LedgerState) error {
				_, err := state.AccountShortfall(f.checking, eurAmount)
				return err
			},
			wantCode: CodeCurrencyMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := f.newLedger(t)
			applyFundingPostingsForTest(t, state, map[AccountId]int64{f.checking: 1_000}, map[EnvelopeId]int64{f.spend: 500})

			err := tt.call(t, state)
			assertFundingErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestFundingAccountKindAndEligibility(t *testing.T) {
	f := newFundingFixture(t)
	amount := mustPositiveAmountForTest(t, 500, f.currency)

	tests := []struct {
		name     string
		call     func(state *LedgerState) error
		wantCode ErrorCode
	}{
		{
			name: "eligible checking accepted",
			call: func(state *LedgerState) error {
				return state.RequireAccountFunds(f.checking, amount)
			},
		},
		{
			name: "eligible savings accepted",
			call: func(state *LedgerState) error {
				return state.RequireAccountFunds(f.savings, amount)
			},
		},
		{
			name: "eligible cash wallet accepted",
			call: func(state *LedgerState) error {
				return state.RequireAccountFunds(f.wallet, amount)
			},
		},
		{
			name: "card liability rejected",
			call: func(state *LedgerState) error {
				return state.RequireAccountFunds(f.card, amount)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "excluded cash account rejected",
			call: func(state *LedgerState) error {
				return state.RequireAccountFunds(f.excluded, amount)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "card liability shortfall rejected",
			call: func(state *LedgerState) error {
				_, err := state.AccountShortfall(f.card, amount)
				return err
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "excluded cash shortfall rejected",
			call: func(state *LedgerState) error {
				_, err := state.AccountShortfall(f.excluded, amount)
				return err
			},
			wantCode: CodeInvalidValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := f.newLedger(t)
			applyFundingPostingsForTest(t, state,
				map[AccountId]int64{f.checking: 1_000, f.savings: 1_000, f.wallet: 1_000, f.excluded: 1_000},
				nil,
			)

			err := tt.call(state)
			assertFundingErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestFundingNegativeSpendable(t *testing.T) {
	f := newFundingFixture(t)
	state := f.newLedger(t)
	applyFundingPostingsForTest(t, state, map[AccountId]int64{f.checking: 1_000}, map[EnvelopeId]int64{f.spend: 200})

	effects, err := DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 500, f.currency))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects: %v", err)
	}
	applyFundingEffectsForTest(t, state, effects)

	assertMoney(t, state.SpendableEnvelopeBalance, f.spend, -300)

	shortfall, err := state.EnvelopeShortfall(f.spend, mustPositiveAmountForTest(t, 100, f.currency))
	if err != nil {
		t.Fatalf("EnvelopeShortfall: %v", err)
	}
	assertFundingMinorUnits(t, "EnvelopeShortfall", shortfall, 400)

	larger, err := state.EnvelopeShortfall(f.spend, mustPositiveAmountForTest(t, 300, f.currency))
	if err != nil {
		t.Fatalf("EnvelopeShortfall: %v", err)
	}
	assertFundingMinorUnits(t, "EnvelopeShortfall(larger)", larger, 600)

	err = state.RequireEnvelopeFunds(f.spend, mustPositiveAmountForTest(t, 100, f.currency))
	assertFundingErrorCode(t, err, CodeInsufficientEnvelopeFunds)
}
