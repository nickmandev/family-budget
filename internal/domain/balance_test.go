package domain

import "testing"

func mustLedgerForTest(t *testing.T, currency Currency, accounts []AccountMeta, envelopes []EnvelopeId) *LedgerState {
	t.Helper()
	state, err := NewLedgerState(currency, accounts, envelopes)
	if err != nil {
		t.Fatalf("NewLedgerState: %v", err)
	}
	return state
}

func mustAccountMetaForTest(t *testing.T, id AccountId, kind AccountKind, eligible bool) AccountMeta {
	t.Helper()
	meta, err := NewAccountMeta(id, kind, eligible)
	if err != nil {
		t.Fatalf("NewAccountMeta(%q, %q, %v): %v", id, kind, eligible, err)
	}
	return meta
}

func mustPostingSetForTest(t *testing.T) PostingSet {
	t.Helper()
	return PostingSet{}
}

func TestParseAccountKind(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     AccountKind
		wantCode ErrorCode
	}{
		{name: "checking", raw: "CHECKING", want: AccountKindChecking},
		{name: "savings", raw: "SAVINGS", want: AccountKindSavings},
		{name: "cash wallet", raw: "CASH_WALLET", want: AccountKindCashWallet},
		{name: "card liability", raw: "CREDIT_CARD_LIABILITY", want: AccountKindCardLiability},
		{name: "lowercase", raw: "checking", wantCode: CodeInvalidValue},
		{name: "empty", raw: "", wantCode: CodeInvalidValue},
		{name: "unknown", raw: "BROKERAGE", wantCode: CodeInvalidValue},
		{name: "padded", raw: " CHECKING", wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAccountKind(tt.raw)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("ParseAccountKind(%q) error = %v, want code %s", tt.raw, err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAccountKind(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("ParseAccountKind(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			if got.String() != tt.raw {
				t.Errorf("String() = %q, want %q", got.String(), tt.raw)
			}
			if !got.IsValid() {
				t.Errorf("%q.IsValid() = false, want true", got)
			}
		})
	}
}

func TestAccountKindIsLiquidCash(t *testing.T) {
	tests := []struct {
		kind AccountKind
		want bool
	}{
		{kind: AccountKindChecking, want: true},
		{kind: AccountKindSavings, want: true},
		{kind: AccountKindCashWallet, want: true},
		{kind: AccountKindCardLiability, want: false},
		{kind: AccountKind(""), want: false},
	}
	for _, tt := range tests {
		if got := tt.kind.IsLiquidCash(); got != tt.want {
			t.Errorf("%q.IsLiquidCash() = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestNewAccountMeta(t *testing.T) {
	checking := mustAccountIDForTest(t, "acct-checking")
	card := mustAccountIDForTest(t, "acct-card")

	tests := []struct {
		name     string
		id       AccountId
		kind     AccountKind
		eligible bool
		wantCode ErrorCode
	}{
		{name: "eligible checking", id: checking, kind: AccountKindChecking, eligible: true},
		{name: "excluded checking", id: checking, kind: AccountKindChecking, eligible: false},
		{name: "eligible savings", id: checking, kind: AccountKindSavings, eligible: true},
		{name: "eligible cash wallet", id: checking, kind: AccountKindCashWallet, eligible: true},
		{name: "card not eligible", id: card, kind: AccountKindCardLiability, eligible: false},
		{name: "card eligible rejected", id: card, kind: AccountKindCardLiability, eligible: true, wantCode: CodeInvalidValue},
		{name: "zero id", id: "", kind: AccountKindChecking, eligible: true, wantCode: CodeInvalidValue},
		{name: "invalid kind", id: checking, kind: AccountKind("BROKERAGE"), eligible: true, wantCode: CodeInvalidValue},
		{name: "zero kind", id: checking, kind: AccountKind(""), eligible: false, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewAccountMeta(tt.id, tt.kind, tt.eligible)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewAccountMeta error = %v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewAccountMeta unexpected error: %v", err)
			}
			if got.ID != tt.id || got.Kind != tt.kind || got.BudgetEligible != tt.eligible {
				t.Errorf("NewAccountMeta = %+v, want id=%q kind=%q eligible=%v", got, tt.id, tt.kind, tt.eligible)
			}
		})
	}
}

func TestNewLedgerStateValidation(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	card := mustAccountIDForTest(t, "acct-card")
	groceries := mustEnvelopeIDForTest(t, "env-groceries")

	validChecking := mustAccountMetaForTest(t, checking, AccountKindChecking, true)
	validCard := mustAccountMetaForTest(t, card, AccountKindCardLiability, false)

	tests := []struct {
		name      string
		currency  Currency
		accounts  []AccountMeta
		envelopes []EnvelopeId
		wantCode  ErrorCode
	}{
		{name: "valid empty", currency: usd},
		{name: "valid accounts and envelopes", currency: usd, accounts: []AccountMeta{validChecking, validCard}, envelopes: []EnvelopeId{groceries}},
		{name: "zero currency", currency: Currency{}, wantCode: CodeInvalidValue},
		{name: "invalid currency", currency: Currency{code: "usd", exponent: 2}, wantCode: CodeInvalidValue},
		{name: "zero account id", currency: usd, accounts: []AccountMeta{{Kind: AccountKindChecking}}, wantCode: CodeInvalidValue},
		{name: "duplicate account", currency: usd, accounts: []AccountMeta{validChecking, validChecking}, wantCode: CodeInvalidValue},
		{name: "invalid account kind", currency: usd, accounts: []AccountMeta{{ID: checking, Kind: "BROKERAGE"}}, wantCode: CodeInvalidValue},
		{name: "card eligible", currency: usd, accounts: []AccountMeta{{ID: card, Kind: AccountKindCardLiability, BudgetEligible: true}}, wantCode: CodeInvalidValue},
		{name: "zero envelope id", currency: usd, envelopes: []EnvelopeId{""}, wantCode: CodeInvalidValue},
		{name: "duplicate envelope", currency: usd, envelopes: []EnvelopeId{groceries, groceries}, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewLedgerState(tt.currency, tt.accounts, tt.envelopes)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewLedgerState error = %v, want code %s", err, tt.wantCode)
				}
				if got != nil {
					t.Fatalf("NewLedgerState returned non-nil state on error: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewLedgerState unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("NewLedgerState returned nil state")
			}
			if !got.Currency().Equal(tt.currency) {
				t.Errorf("Currency() = %s, want %s", got.Currency(), tt.currency)
			}
		})
	}
}

func TestApplyPostingsAccumulates(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	card := mustAccountIDForTest(t, "acct-card")
	groceries := mustEnvelopeIDForTest(t, "env-groceries")

	state := mustLedgerForTest(t, usd,
		[]AccountMeta{
			mustAccountMetaForTest(t, checking, AccountKindChecking, true),
			mustAccountMetaForTest(t, card, AccountKindCardLiability, false),
		},
		[]EnvelopeId{groceries},
	)

	postings := mustPostingSetForTest(t)
	if err := postings.AddAccount(checking, mustSignedDeltaForTest(t, 1_000, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := postings.AddAccount(checking, mustSignedDeltaForTest(t, -250, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := postings.AddAccount(card, mustSignedDeltaForTest(t, 500, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := postings.AddEnvelope(groceries, mustSignedDeltaForTest(t, -400, usd)); err != nil {
		t.Fatalf("AddEnvelope: %v", err)
	}
	if err := state.ApplyPostings(postings); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}

	assertMoney(t, state.PostedAccountBalance, checking, 750)
	assertMoney(t, state.PostedAccountBalance, card, 500)
	assertMoney(t, state.EnvelopeBalance, groceries, -400)
}

func TestApplyPostingsUnknownIDs(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	unknownAccount := mustAccountIDForTest(t, "acct-unknown")
	groceries := mustEnvelopeIDForTest(t, "env-groceries")
	unknownEnvelope := mustEnvelopeIDForTest(t, "env-unknown")

	state := mustLedgerForTest(t, usd,
		[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
		[]EnvelopeId{groceries},
	)

	bad := mustPostingSetForTest(t)
	if err := bad.AddAccount(checking, mustSignedDeltaForTest(t, 100, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := bad.AddAccount(unknownAccount, mustSignedDeltaForTest(t, 100, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := state.ApplyPostings(bad); !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
		t.Fatalf("ApplyPostings(unknown account) error = %v, want ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD", err)
	}
	assertMoney(t, state.PostedAccountBalance, checking, 0)

	badEnvelope := mustPostingSetForTest(t)
	if err := badEnvelope.AddEnvelope(groceries, mustSignedDeltaForTest(t, 100, usd)); err != nil {
		t.Fatalf("AddEnvelope: %v", err)
	}
	if err := badEnvelope.AddEnvelope(unknownEnvelope, mustSignedDeltaForTest(t, 100, usd)); err != nil {
		t.Fatalf("AddEnvelope: %v", err)
	}
	if err := state.ApplyPostings(badEnvelope); !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
		t.Fatalf("ApplyPostings(unknown envelope) error = %v, want ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD", err)
	}
	assertMoney(t, state.EnvelopeBalance, groceries, 0)
}

func TestApplyPostingsCurrencyMismatch(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	eur := mustCurrency(t, "EUR", 2)
	checking := mustAccountIDForTest(t, "acct-checking")

	state := mustLedgerForTest(t, usd,
		[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
		nil,
	)
	postings := mustPostingSetForTest(t)
	if err := postings.AddAccount(checking, mustSignedDeltaForTest(t, 100, eur)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := state.ApplyPostings(postings); !HasCode(err, CodeCurrencyMismatch) {
		t.Fatalf("ApplyPostings across currencies error = %v, want CURRENCY_MISMATCH", err)
	}
	assertMoney(t, state.PostedAccountBalance, checking, 0)
}

func TestApplyPostingsOverflow(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")

	state := mustLedgerForTest(t, usd,
		[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
		nil,
	)
	first := mustPostingSetForTest(t)
	if err := first.AddAccount(checking, mustSignedDeltaForTest(t, MaxMinorUnits, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := state.ApplyPostings(first); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}

	second := mustPostingSetForTest(t)
	if err := second.AddAccount(checking, mustSignedDeltaForTest(t, 1, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	err := state.ApplyPostings(second)
	if !HasCode(err, CodeAmountOutOfRange) && !HasCode(err, CodeMoneyOverflow) {
		t.Fatalf("ApplyPostings overflow error = %v, want AMOUNT_OUT_OF_RANGE or MONEY_OVERFLOW", err)
	}
	assertMoney(t, state.PostedAccountBalance, checking, MaxMinorUnits)
}

func TestBudgetableCash(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	savings := mustAccountIDForTest(t, "acct-savings")
	wallet := mustAccountIDForTest(t, "acct-wallet")
	excluded := mustAccountIDForTest(t, "acct-excluded")
	card := mustAccountIDForTest(t, "acct-card")

	state := mustLedgerForTest(t, usd,
		[]AccountMeta{
			mustAccountMetaForTest(t, checking, AccountKindChecking, true),
			mustAccountMetaForTest(t, savings, AccountKindSavings, true),
			mustAccountMetaForTest(t, wallet, AccountKindCashWallet, true),
			mustAccountMetaForTest(t, excluded, AccountKindChecking, false),
			mustAccountMetaForTest(t, card, AccountKindCardLiability, false),
		},
		nil,
	)

	postings := mustPostingSetForTest(t)
	balances := []struct {
		id     AccountId
		amount int64
	}{
		{checking, 1_000},
		{savings, -200},
		{wallet, 50},
		{excluded, 5_000},
		{card, 300},
	}
	for _, balance := range balances {
		if err := postings.AddAccount(balance.id, mustSignedDeltaForTest(t, balance.amount, usd)); err != nil {
			t.Fatalf("AddAccount: %v", err)
		}
	}
	if err := state.ApplyPostings(postings); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}

	budgetable, err := state.BudgetableCash()
	if err != nil {
		t.Fatalf("BudgetableCash: %v", err)
	}
	// 1000 - 200 + 50 = 850; excluded account and card liability are ignored.
	if budgetable.MinorUnits() != 850 {
		t.Errorf("BudgetableCash = %d, want 850", budgetable.MinorUnits())
	}
}

func TestBudgetableCashEmpty(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	state := mustLedgerForTest(t, usd, nil, nil)
	budgetable, err := state.BudgetableCash()
	if err != nil {
		t.Fatalf("BudgetableCash: %v", err)
	}
	if !budgetable.IsZero() {
		t.Errorf("BudgetableCash = %d, want 0", budgetable.MinorUnits())
	}
}

func TestReadyToAssign(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	food := mustEnvelopeIDForTest(t, "env-food")
	rent := mustEnvelopeIDForTest(t, "env-rent")

	state := mustLedgerForTest(t, usd,
		[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
		[]EnvelopeId{food, rent},
	)

	postings := mustPostingSetForTest(t)
	if err := postings.AddAccount(checking, mustSignedDeltaForTest(t, 1_000, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := postings.AddEnvelope(food, mustSignedDeltaForTest(t, 400, usd)); err != nil {
		t.Fatalf("AddEnvelope: %v", err)
	}
	if err := postings.AddEnvelope(rent, mustSignedDeltaForTest(t, 700, usd)); err != nil {
		t.Fatalf("AddEnvelope: %v", err)
	}
	if err := state.ApplyPostings(postings); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}

	ready, err := state.ReadyToAssign()
	if err != nil {
		t.Fatalf("ReadyToAssign: %v", err)
	}
	if ready.MinorUnits() != -100 {
		t.Errorf("ReadyToAssign = %d, want -100", ready.MinorUnits())
	}
}

func TestReservationsDoNotChangePostedBalances(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	groceries := mustEnvelopeIDForTest(t, "env-groceries")
	plan := mustPlanIDForTest(t, "plan-1")

	state := mustLedgerForTest(t, usd,
		[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
		[]EnvelopeId{groceries},
	)

	postings := mustPostingSetForTest(t)
	if err := postings.AddAccount(checking, mustSignedDeltaForTest(t, 1_000, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := postings.AddEnvelope(groceries, mustSignedDeltaForTest(t, 500, usd)); err != nil {
		t.Fatalf("AddEnvelope: %v", err)
	}
	if err := state.ApplyPostings(postings); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}

	envelopeHold, err := NewEnvelopeHold(groceries, mustSignedDeltaForTest(t, 300, usd))
	if err != nil {
		t.Fatalf("NewEnvelopeHold: %v", err)
	}
	accountHold, err := NewAccountHold(checking, mustSignedDeltaForTest(t, 100, usd))
	if err != nil {
		t.Fatalf("NewAccountHold: %v", err)
	}
	change, err := NewReservationChange(plan, envelopeHold, &accountHold)
	if err != nil {
		t.Fatalf("NewReservationChange: %v", err)
	}
	if err := state.ApplyReservationChanges(change); err != nil {
		t.Fatalf("ApplyReservationChanges: %v", err)
	}

	assertMoney(t, state.PostedAccountBalance, checking, 1_000)
	assertMoney(t, state.EnvelopeBalance, groceries, 500)
	assertMoney(t, state.ActiveEnvelopeReservations, groceries, 300)
	assertMoney(t, state.ActiveAccountReservations, checking, 100)
	assertMoney(t, state.SpendableEnvelopeBalance, groceries, 200)
	assertMoney(t, state.SpendableAccountBalance, checking, 900)
}

func TestApplyReservationChangesAccumulates(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	groceries := mustEnvelopeIDForTest(t, "env-groceries")
	plan := mustPlanIDForTest(t, "plan-1")

	state := mustLedgerForTest(t, usd,
		[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
		[]EnvelopeId{groceries},
	)

	holdOnce, err := NewEnvelopeHold(groceries, mustSignedDeltaForTest(t, 100, usd))
	if err != nil {
		t.Fatalf("NewEnvelopeHold: %v", err)
	}
	changeOne, err := NewReservationChange(plan, holdOnce, nil)
	if err != nil {
		t.Fatalf("NewReservationChange: %v", err)
	}
	changeTwo, err := NewReservationChange(plan, holdOnce, nil)
	if err != nil {
		t.Fatalf("NewReservationChange: %v", err)
	}
	if err := state.ApplyReservationChanges(changeOne, changeTwo); err != nil {
		t.Fatalf("ApplyReservationChanges: %v", err)
	}
	assertMoney(t, state.ActiveEnvelopeReservations, groceries, 200)
}

func TestApplyReservationChangesValidation(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	eur := mustCurrency(t, "EUR", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	unknownAccount := mustAccountIDForTest(t, "acct-unknown")
	groceries := mustEnvelopeIDForTest(t, "env-groceries")
	unknownEnvelope := mustEnvelopeIDForTest(t, "env-unknown")
	plan := mustPlanIDForTest(t, "plan-1")

	newState := func() *LedgerState {
		return mustLedgerForTest(t, usd,
			[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
			[]EnvelopeId{groceries},
		)
	}
	change := func(t *testing.T, envelopeID EnvelopeId, envelopeDelta int64, accountID AccountId, accountDelta int64, currency Currency) ReservationChange {
		t.Helper()
		hold, err := NewEnvelopeHold(envelopeID, mustSignedDeltaForTest(t, envelopeDelta, currency))
		if err != nil {
			t.Fatalf("NewEnvelopeHold: %v", err)
		}
		var account *AccountHold
		if accountDelta != 0 {
			held, err := NewAccountHold(accountID, mustSignedDeltaForTest(t, accountDelta, currency))
			if err != nil {
				t.Fatalf("NewAccountHold: %v", err)
			}
			account = &held
		}
		created, err := NewReservationChange(plan, hold, account)
		if err != nil {
			t.Fatalf("NewReservationChange: %v", err)
		}
		return created
	}

	tests := []struct {
		name             string
		prepare          func(t *testing.T, state *LedgerState)
		changes          func(t *testing.T) []ReservationChange
		wantCode         ErrorCode
		wantEnvReserved  int64
		wantAcctReserved int64
	}{
		{
			name: "unknown envelope",
			changes: func(t *testing.T) []ReservationChange {
				return []ReservationChange{change(t, unknownEnvelope, 100, "", 0, usd)}
			},
			wantCode: CodeEntityNotFoundOrNotInHousehold,
		},
		{
			name: "unknown account",
			changes: func(t *testing.T) []ReservationChange {
				return []ReservationChange{change(t, groceries, 100, unknownAccount, 50, usd)}
			},
			wantCode: CodeEntityNotFoundOrNotInHousehold,
		},
		{
			name: "zero plan",
			changes: func(t *testing.T) []ReservationChange {
				return []ReservationChange{{Envelope: EnvelopeHold{EnvelopeID: groceries, Delta: mustSignedDeltaForTest(t, 100, usd)}}}
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "envelope release below zero",
			changes: func(t *testing.T) []ReservationChange {
				return []ReservationChange{change(t, groceries, -1, "", 0, usd)}
			},
			wantCode: CodeInvalidAmount,
		},
		{
			name: "account release below zero",
			prepare: func(t *testing.T, state *LedgerState) {
				if err := state.ApplyReservationChanges(change(t, groceries, 100, checking, 100, usd)); err != nil {
					t.Fatalf("ApplyReservationChanges: %v", err)
				}
			},
			changes: func(t *testing.T) []ReservationChange {
				return []ReservationChange{change(t, groceries, 1, checking, -101, usd)}
			},
			wantCode:         CodeInvalidAmount,
			wantEnvReserved:  100,
			wantAcctReserved: 100,
		},
		{
			name: "currency mismatch",
			changes: func(t *testing.T) []ReservationChange {
				return []ReservationChange{change(t, groceries, 100, "", 0, eur)}
			},
			wantCode: CodeCurrencyMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newState()
			if tt.prepare != nil {
				tt.prepare(t, state)
			}
			err := state.ApplyReservationChanges(tt.changes(t)...)
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("ApplyReservationChanges error = %v, want code %s", err, tt.wantCode)
			}
			// A rejected batch must not mutate either reservation.
			assertMoney(t, state.ActiveEnvelopeReservations, groceries, tt.wantEnvReserved)
			assertMoney(t, state.ActiveAccountReservations, checking, tt.wantAcctReserved)
		})
	}
}

func TestApplyReservationChangesReleaseToZero(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	groceries := mustEnvelopeIDForTest(t, "env-groceries")
	plan := mustPlanIDForTest(t, "plan-1")
	state := mustLedgerForTest(t, usd, nil, []EnvelopeId{groceries})

	hold, err := NewEnvelopeHold(groceries, mustSignedDeltaForTest(t, 100, usd))
	if err != nil {
		t.Fatalf("NewEnvelopeHold: %v", err)
	}
	release, err := NewEnvelopeHold(groceries, mustSignedDeltaForTest(t, -100, usd))
	if err != nil {
		t.Fatalf("NewEnvelopeHold: %v", err)
	}
	first, err := NewReservationChange(plan, hold, nil)
	if err != nil {
		t.Fatalf("NewReservationChange: %v", err)
	}
	if err := state.ApplyReservationChanges(first); err != nil {
		t.Fatalf("ApplyReservationChanges: %v", err)
	}
	second, err := NewReservationChange(plan, release, nil)
	if err != nil {
		t.Fatalf("NewReservationChange: %v", err)
	}
	if err := state.ApplyReservationChanges(second); err != nil {
		t.Fatalf("ApplyReservationChanges release: %v", err)
	}
	assertMoney(t, state.ActiveEnvelopeReservations, groceries, 0)
}

func TestCardLiabilityBalance(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	card := mustAccountIDForTest(t, "acct-card")
	checking := mustAccountIDForTest(t, "acct-checking")
	state := mustLedgerForTest(t, usd,
		[]AccountMeta{
			mustAccountMetaForTest(t, card, AccountKindCardLiability, false),
			mustAccountMetaForTest(t, checking, AccountKindChecking, true),
		},
		nil,
	)

	charge := mustPostingSetForTest(t)
	if err := charge.AddAccount(card, mustSignedDeltaForTest(t, 500, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := state.ApplyPostings(charge); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}
	assertMoney(t, state.CardLiabilityBalance, card, 500)

	repayment := mustPostingSetForTest(t)
	if err := repayment.AddAccount(card, mustSignedDeltaForTest(t, -200, usd)); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := state.ApplyPostings(repayment); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}
	assertMoney(t, state.CardLiabilityBalance, card, 300)

	if _, err := state.CardLiabilityBalance(checking); !HasCode(err, CodeInvalidValue) {
		t.Fatalf("CardLiabilityBalance(non-card) error = %v, want INVALID_VALUE", err)
	}
}

func TestPostedAccountBalanceUnknown(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	unknown := mustAccountIDForTest(t, "acct-unknown")
	state := mustLedgerForTest(t, usd, nil, nil)
	if _, err := state.PostedAccountBalance(unknown); !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
		t.Fatalf("PostedAccountBalance(unknown) error = %v, want ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD", err)
	}
}

func TestPostedAccountBalanceUntouchedIsZero(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	state := mustLedgerForTest(t, usd,
		[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
		nil,
	)
	got, err := state.PostedAccountBalance(checking)
	if err != nil {
		t.Fatalf("PostedAccountBalance: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("PostedAccountBalance(untouched) = %d, want 0", got.MinorUnits())
	}
}

// assertMoney applies a Money-returning ledger query and compares its minor
// units.
func assertMoney[T any](t *testing.T, query func(T) (Money, error), id T, want int64) {
	t.Helper()
	got, err := query(id)
	if err != nil {
		t.Fatalf("ledger query(%v): %v", id, err)
	}
	if got.MinorUnits() != want {
		t.Errorf("ledger query(%v) = %d, want %d", id, got.MinorUnits(), want)
	}
}
