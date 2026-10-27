package domain

import "testing"

// postingMatrixFixture is the canonical ledger used by the posting-matrix
// conformance test: two eligible accounts (checking, savings), two excluded
// accounts (a cash wallet and a second outside account for wholly-excluded
// transfers), one card liability, and three envelopes (spend, pay, fee).
type postingMatrixFixture struct {
	currency  Currency
	checking  AccountId
	savings   AccountId
	excluded  AccountId
	excluded2 AccountId
	card      AccountId
	spend     EnvelopeId
	pay       EnvelopeId
	fee       EnvelopeId
	plan      PlanId

	debitOriginal JournalOperationId
	cardOriginal  JournalOperationId
}

func newPostingMatrixFixture(t *testing.T) *postingMatrixFixture {
	t.Helper()
	return &postingMatrixFixture{
		currency:      mustCurrency(t, "USD", 2),
		checking:      mustAccountIDForTest(t, "acct-checking"),
		savings:       mustAccountIDForTest(t, "acct-savings"),
		excluded:      mustAccountIDForTest(t, "acct-cash-wallet"),
		excluded2:     mustAccountIDForTest(t, "acct-excluded-outside"),
		card:          mustAccountIDForTest(t, "acct-card"),
		spend:         mustEnvelopeIDForTest(t, "env-spend"),
		pay:           mustEnvelopeIDForTest(t, "env-pay"),
		fee:           mustEnvelopeIDForTest(t, "env-fee"),
		plan:          mustPlanIDForTest(t, "plan-planned-purchase"),
		debitOriginal: mustJournalOpIDForTest(t, "op-debit-original"),
		cardOriginal:  mustJournalOpIDForTest(t, "op-card-original"),
	}
}

func (f *postingMatrixFixture) accountIDs() []AccountId {
	return []AccountId{f.checking, f.savings, f.excluded, f.excluded2, f.card}
}

func (f *postingMatrixFixture) envelopeIDs() []EnvelopeId {
	return []EnvelopeId{f.spend, f.pay, f.fee}
}

func (f *postingMatrixFixture) newLedger(t *testing.T) *LedgerState {
	t.Helper()
	return mustLedgerForTest(t, f.currency,
		[]AccountMeta{
			mustAccountMetaForTest(t, f.checking, AccountKindChecking, true),
			mustAccountMetaForTest(t, f.savings, AccountKindSavings, true),
			mustAccountMetaForTest(t, f.excluded, AccountKindCashWallet, false),
			mustAccountMetaForTest(t, f.excluded2, AccountKindSavings, false),
			mustAccountMetaForTest(t, f.card, AccountKindCardLiability, false),
		},
		f.envelopeIDs(),
	)
}

func (f *postingMatrixFixture) debitOriginalEffects(t *testing.T) Effects {
	t.Helper()
	must := func(effects Effects, err error) Effects {
		t.Helper()
		if err != nil {
			t.Fatalf("effects: %v", err)
		}
		return effects
	}
	return must(DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 1_000, f.currency)))
}

func (f *postingMatrixFixture) cardOriginalEffects(t *testing.T) Effects {
	t.Helper()
	must := func(effects Effects, err error) Effects {
		t.Helper()
		if err != nil {
			t.Fatalf("effects: %v", err)
		}
		return effects
	}
	return must(CreditCardPurchaseEffects(f.card, f.spend, f.pay, mustPositiveAmountForTest(t, 1_000, f.currency)))
}

func (f *postingMatrixFixture) reservationTarget() ReservationTarget {
	return ReservationTarget{PlanID: f.plan, EnvelopeID: f.spend, AccountID: f.checking}
}

// seededLedger builds a realistic prior state by running earlier posting-matrix
// effect functions: receive income into checking and an excluded account,
// allocate to all three envelopes, then record one debit purchase and one card
// purchase.
//
// Opening state: checking 9,000; savings 0; excluded 2,000; excluded2 0; card
// liability 1,000; spend 1,000; pay 2,000; fee 500. Budgetable cash is 9,000,
// the envelope sum is 3,500, and Ready-to-assign is 5,500.
func (f *postingMatrixFixture) seededLedger(t *testing.T) *LedgerState {
	t.Helper()
	must := func(effects Effects, err error) Effects {
		t.Helper()
		if err != nil {
			t.Fatalf("effects: %v", err)
		}
		return effects
	}
	state := f.newLedger(t)
	seed := []Effects{
		must(IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 10_000, f.currency))),
		must(IncomeReceivedEffects(f.excluded, mustPositiveAmountForTest(t, 2_000, f.currency))),
		must(AllocateFundsEffects(f.spend, mustPositiveAmountForTest(t, 3_000, f.currency))),
		must(AllocateFundsEffects(f.pay, mustPositiveAmountForTest(t, 1_000, f.currency))),
		must(AllocateFundsEffects(f.fee, mustPositiveAmountForTest(t, 500, f.currency))),
		f.debitOriginalEffects(t),
		f.cardOriginalEffects(t),
	}
	for _, effects := range seed {
		applyMatrixEffectsForTest(t, state, effects)
	}
	return state
}

func applyMatrixEffectsForTest(t *testing.T, state *LedgerState, effects Effects) {
	t.Helper()
	if err := state.ApplyPostings(effects.Postings); err != nil {
		t.Fatalf("ApplyPostings: %v", err)
	}
	if err := state.ApplyReservationChanges(effects.Reservations...); err != nil {
		t.Fatalf("ApplyReservationChanges: %v", err)
	}
}

// matrixLedgerSnapshot captures every observable ledger query so expected
// deltas can be asserted against the matrix columns.
type matrixLedgerSnapshot struct {
	accounts          map[AccountId]int64
	envelopes         map[EnvelopeId]int64
	accountReserved   map[AccountId]int64
	envelopeReserved  map[EnvelopeId]int64
	accountSpendable  map[AccountId]int64
	envelopeSpendable map[EnvelopeId]int64
	budgetableCash    int64
	readyToAssign     int64
	envelopeSum       int64
}

func snapshotMatrixLedger(t *testing.T, f *postingMatrixFixture, state *LedgerState) matrixLedgerSnapshot {
	t.Helper()
	snapshot := matrixLedgerSnapshot{
		accounts:          make(map[AccountId]int64),
		envelopes:         make(map[EnvelopeId]int64),
		accountReserved:   make(map[AccountId]int64),
		envelopeReserved:  make(map[EnvelopeId]int64),
		accountSpendable:  make(map[AccountId]int64),
		envelopeSpendable: make(map[EnvelopeId]int64),
	}
	for _, id := range f.accountIDs() {
		posted, err := state.PostedAccountBalance(id)
		if err != nil {
			t.Fatalf("PostedAccountBalance(%q): %v", id, err)
		}
		reserved, err := state.ActiveAccountReservations(id)
		if err != nil {
			t.Fatalf("ActiveAccountReservations(%q): %v", id, err)
		}
		spendable, err := state.SpendableAccountBalance(id)
		if err != nil {
			t.Fatalf("SpendableAccountBalance(%q): %v", id, err)
		}
		snapshot.accounts[id] = posted.MinorUnits()
		snapshot.accountReserved[id] = reserved.MinorUnits()
		snapshot.accountSpendable[id] = spendable.MinorUnits()
	}
	for _, id := range f.envelopeIDs() {
		balance, err := state.EnvelopeBalance(id)
		if err != nil {
			t.Fatalf("EnvelopeBalance(%q): %v", id, err)
		}
		reserved, err := state.ActiveEnvelopeReservations(id)
		if err != nil {
			t.Fatalf("ActiveEnvelopeReservations(%q): %v", id, err)
		}
		spendable, err := state.SpendableEnvelopeBalance(id)
		if err != nil {
			t.Fatalf("SpendableEnvelopeBalance(%q): %v", id, err)
		}
		snapshot.envelopes[id] = balance.MinorUnits()
		snapshot.envelopeReserved[id] = reserved.MinorUnits()
		snapshot.envelopeSpendable[id] = spendable.MinorUnits()
	}
	budgetable, err := state.BudgetableCash()
	if err != nil {
		t.Fatalf("BudgetableCash: %v", err)
	}
	ready, err := state.ReadyToAssign()
	if err != nil {
		t.Fatalf("ReadyToAssign: %v", err)
	}
	envelopeSum, err := state.SumEnvelopeBalances()
	if err != nil {
		t.Fatalf("SumEnvelopeBalances: %v", err)
	}
	snapshot.budgetableCash = budgetable.MinorUnits()
	snapshot.readyToAssign = ready.MinorUnits()
	snapshot.envelopeSum = envelopeSum.MinorUnits()
	return snapshot
}

func assertMatrixDeltas[T comparable](t *testing.T, kind string, before, after, want map[T]int64, ids []T) {
	t.Helper()
	for _, id := range ids {
		got := after[id] - before[id]
		expected := want[id]
		if got != expected {
			t.Errorf("%s %v delta = %d, want %d", kind, id, got, expected)
		}
	}
}

func assertMatrixBudgetUnchanged(t *testing.T, before, after matrixLedgerSnapshot) {
	t.Helper()
	if after.budgetableCash != before.budgetableCash {
		t.Errorf("BudgetableCash delta = %d, want 0", after.budgetableCash-before.budgetableCash)
	}
	if after.readyToAssign != before.readyToAssign {
		t.Errorf("ReadyToAssign delta = %d, want 0", after.readyToAssign-before.readyToAssign)
	}
	if after.envelopeSum != before.envelopeSum {
		t.Errorf("SumEnvelopeBalances delta = %d, want 0", after.envelopeSum-before.envelopeSum)
	}
}

// postingMatrixCase is one conformance case. row is the exact POSTING_MATRIX
// section 2 label, caseName optionally distinguishes multiple scenarios for
// the same matrix row, and the want* maps are the exact account/envelope
// deltas asserted after applying effects.
type postingMatrixCase struct {
	row                   string
	caseName              string
	prepare               func(t *testing.T, f *postingMatrixFixture, state *LedgerState)
	effects               func(t *testing.T, f *postingMatrixFixture) Effects
	wantAccounts          map[AccountId]int64
	wantEnvelopes         map[EnvelopeId]int64
	wantAccountReserved   map[AccountId]int64
	wantEnvelopeReserved  map[EnvelopeId]int64
	wantAccountSpendable  map[AccountId]int64
	wantEnvelopeSpendable map[EnvelopeId]int64
	wantRelated           []RelatedOperationRef
	reservationOnly       bool
	checkSpendable        bool
}

func postingMatrixCases(t *testing.T, f *postingMatrixFixture) []postingMatrixCase {
	t.Helper()
	must := func(effects Effects, err error) Effects {
		t.Helper()
		if err != nil {
			t.Fatalf("effects: %v", err)
		}
		return effects
	}
	usd := f.currency
	zero := mustMoney(t, 0, usd)
	target := f.reservationTarget()
	reservedAmount := mustPositiveAmountForTest(t, 1_000, usd)

	prepareCreateReservation := func(t *testing.T, f *postingMatrixFixture, state *LedgerState) {
		t.Helper()
		applyMatrixEffectsForTest(t, state, must(CreateReservationEffects(target, reservedAmount)))
	}

	return []postingMatrixCase{
		{
			row: "Opening eligible cash balance",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(OpeningBalanceEffects(f.checking, mustSignedDeltaForTest(t, 1_000, usd)))
			},
			wantAccounts: map[AccountId]int64{f.checking: 1_000},
		},
		{
			row: "Opening excluded cash balance",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(OpeningBalanceEffects(f.excluded, mustSignedDeltaForTest(t, 1_000, usd)))
			},
			wantAccounts: map[AccountId]int64{f.excluded: 1_000},
		},
		{
			row: "Opening card liability",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(OpeningBalanceEffects(f.card, mustSignedDeltaForTest(t, 1_000, usd)))
			},
			wantAccounts: map[AccountId]int64{f.card: 1_000},
		},
		{
			row: "Receive income into eligible account",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 1_000, usd)))
			},
			wantAccounts: map[AccountId]int64{f.checking: 1_000},
		},
		{
			row: "Receive income into excluded account",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(IncomeReceivedEffects(f.excluded, mustPositiveAmountForTest(t, 1_000, usd)))
			},
			wantAccounts: map[AccountId]int64{f.excluded: 1_000},
		},
		{
			row: "Allocate to envelope x",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(AllocateFundsEffects(f.spend, mustPositiveAmountForTest(t, 1_000, usd)))
			},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 1_000},
		},
		{
			row: "Reallocate x -> y",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ReallocateFundsEffects(f.spend, f.fee, mustPositiveAmountForTest(t, 400, usd)))
			},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: -400, f.fee: 400},
		},
		{
			row: "Debit/cash purchase",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 700, usd)))
			},
			wantAccounts:  map[AccountId]int64{f.checking: -700},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: -700},
		},
		{
			row: "Credit-card purchase",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(CreditCardPurchaseEffects(f.card, f.spend, f.pay, mustPositiveAmountForTest(t, 500, usd)))
			},
			wantAccounts:  map[AccountId]int64{f.card: 500},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: -500, f.pay: 500},
		},
		{
			row: "Credit-card payment",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(CreditCardPaymentEffects(f.checking, f.card, f.pay, mustPositiveAmountForTest(t, 1_000, usd)))
			},
			wantAccounts:  map[AccountId]int64{f.checking: -1_000, f.card: -1_000},
			wantEnvelopes: map[EnvelopeId]int64{f.pay: -1_000},
		},
		{
			row: "Card fee / interest",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(CardFeeOrInterestEffects(f.card, f.fee, f.pay, mustPositiveAmountForTest(t, 500, usd)))
			},
			wantAccounts:  map[AccountId]int64{f.card: 500},
			wantEnvelopes: map[EnvelopeId]int64{f.fee: -500, f.pay: 500},
		},
		{
			row: "Transfer: eligible cash account to eligible cash account",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(TransferEligibleToEligibleEffects(f.checking, f.savings, mustPositiveAmountForTest(t, 1_000, usd)))
			},
			wantAccounts: map[AccountId]int64{f.checking: -1_000, f.savings: 1_000},
		},
		{
			row: "Transfer: excluded cash to eligible cash",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(TransferExcludedToEligibleEffects(f.excluded, f.checking, mustPositiveAmountForTest(t, 1_000, usd)))
			},
			wantAccounts: map[AccountId]int64{f.excluded: -1_000, f.checking: 1_000},
		},
		{
			row: "Transfer: eligible cash to excluded cash",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 500, usd), FundingSource{
					FromReady:     mustMoney(t, 300, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, 200, usd)}},
				}))
			},
			wantAccounts:  map[AccountId]int64{f.checking: -500, f.excluded: 500},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: -200},
		},
		{
			row: "Transfer wholly within excluded accounts",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(TransferExcludedToExcludedEffects(f.excluded, f.excluded2, mustPositiveAmountForTest(t, 500, usd)))
			},
			wantAccounts: map[AccountId]int64{f.excluded: -500, f.excluded2: 500},
		},
		{
			row: "Refund to eligible cash from debit/cash purchase",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(CashRefundEffects(f.checking, f.debitOriginal, f.spend, mustPositiveAmountForTest(t, 300, usd)))
			},
			wantAccounts:  map[AccountId]int64{f.checking: 300},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 300},
			wantRelated:   []RelatedOperationRef{mustRelatedOperationRefForTest(t, RelatedRefOriginal, f.debitOriginal)},
		},
		{
			row: "Refund to credit card from card purchase",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(CardRefundEffects(f.card, f.cardOriginal, f.spend, f.pay, mustPositiveAmountForTest(t, 300, usd)))
			},
			wantAccounts:  map[AccountId]int64{f.card: -300},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 300, f.pay: -300},
			wantRelated:   []RelatedOperationRef{mustRelatedOperationRefForTest(t, RelatedRefOriginal, f.cardOriginal)},
		},
		{
			row: "Unrelated reimbursement received into eligible cash",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ReimbursementReceivedEffects(f.checking, mustPositiveAmountForTest(t, 300, usd)))
			},
			wantAccounts: map[AccountId]int64{f.checking: 300},
		},
		{
			row: "Purchase reservation created",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(CreateReservationEffects(target, reservedAmount))
			},
			wantAccountReserved:   map[AccountId]int64{f.checking: 1_000},
			wantEnvelopeReserved:  map[EnvelopeId]int64{f.spend: 1_000},
			wantAccountSpendable:  map[AccountId]int64{f.checking: -1_000},
			wantEnvelopeSpendable: map[EnvelopeId]int64{f.spend: -1_000},
			reservationOnly:       true,
			checkSpendable:        true,
		},
		{
			row:     "Reservation amount increased",
			prepare: prepareCreateReservation,
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ResizeReservationEffects(target, mustSignedDeltaForTest(t, 500, usd)))
			},
			wantAccountReserved:   map[AccountId]int64{f.checking: 500},
			wantEnvelopeReserved:  map[EnvelopeId]int64{f.spend: 500},
			wantAccountSpendable:  map[AccountId]int64{f.checking: -500},
			wantEnvelopeSpendable: map[EnvelopeId]int64{f.spend: -500},
			reservationOnly:       true,
			checkSpendable:        true,
		},
		{
			row:     "Reservation amount decreased",
			prepare: prepareCreateReservation,
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ResizeReservationEffects(target, mustSignedDeltaForTest(t, -400, usd)))
			},
			wantAccountReserved:   map[AccountId]int64{f.checking: -400},
			wantEnvelopeReserved:  map[EnvelopeId]int64{f.spend: -400},
			wantAccountSpendable:  map[AccountId]int64{f.checking: 400},
			wantEnvelopeSpendable: map[EnvelopeId]int64{f.spend: 400},
			reservationOnly:       true,
			checkSpendable:        true,
		},
		{
			row:     "Reservation cancelled",
			prepare: prepareCreateReservation,
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(CancelReservationEffects(target, reservedAmount, reservedAmount))
			},
			wantAccountReserved:   map[AccountId]int64{f.checking: -1_000},
			wantEnvelopeReserved:  map[EnvelopeId]int64{f.spend: -1_000},
			wantAccountSpendable:  map[AccountId]int64{f.checking: 1_000},
			wantEnvelopeSpendable: map[EnvelopeId]int64{f.spend: 1_000},
			reservationOnly:       true,
			checkSpendable:        true,
		},
		{
			row:     "Reserved purchase completed",
			prepare: prepareCreateReservation,
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				purchase := must(DebitPurchaseEffects(f.checking, f.spend, reservedAmount))
				return must(CompleteReservedPurchaseEffects(target, purchase, reservedAmount, reservedAmount))
			},
			wantAccounts:         map[AccountId]int64{f.checking: -1_000},
			wantEnvelopes:        map[EnvelopeId]int64{f.spend: -1_000},
			wantAccountReserved:  map[AccountId]int64{f.checking: -1_000},
			wantEnvelopeReserved: map[EnvelopeId]int64{f.spend: -1_000},
			checkSpendable:       true,
		},
		{
			row: "Actual external purchase recorded after the fact",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 700, usd)))
			},
			wantAccounts:  map[AccountId]int64{f.checking: -700},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: -700},
		},
		{
			row: "Reversal of committed purchase/event",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ReversalEffects(f.debitOriginalEffects(t).Postings, f.debitOriginal))
			},
			wantAccounts:  map[AccountId]int64{f.checking: 1_000},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 1_000},
			wantRelated:   []RelatedOperationRef{mustRelatedOperationRefForTest(t, RelatedRefReversal, f.debitOriginal)},
		},
		{
			row: "Replacement after correction",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				corrected := must(DebitPurchaseEffects(f.savings, f.spend, mustPositiveAmountForTest(t, 800, usd)))
				return must(ReplacementEffects(f.debitOriginalEffects(t).Postings, f.debitOriginal, corrected))
			},
			wantAccounts:  map[AccountId]int64{f.checking: 1_000, f.savings: -800},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 200},
			wantRelated: []RelatedOperationRef{
				mustRelatedOperationRefForTest(t, RelatedRefReversal, f.debitOriginal),
				mustRelatedOperationRefForTest(t, RelatedRefReplacement, f.debitOriginal),
			},
		},
		{
			row: "Metadata-only correction",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(MetadataCorrectionEffects(f.debitOriginal))
			},
			wantRelated: []RelatedOperationRef{mustRelatedOperationRefForTest(t, RelatedRefOriginal, f.debitOriginal)},
		},
		{
			row: "Category correction",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(CategoryCorrectionEffects(f.debitOriginal, f.spend, f.fee, mustPositiveAmountForTest(t, 1_000, usd)))
			},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 1_000, f.fee: -1_000},
			wantRelated:   []RelatedOperationRef{mustRelatedOperationRefForTest(t, RelatedRefReplacement, f.debitOriginal)},
		},
		{
			row: "Account/source correction",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				corrected := must(DebitPurchaseEffects(f.savings, f.spend, mustPositiveAmountForTest(t, 900, usd)))
				return must(AccountCorrectionEffects(f.debitOriginalEffects(t).Postings, f.debitOriginal, corrected))
			},
			wantAccounts:  map[AccountId]int64{f.checking: 1_000, f.savings: -900},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 100},
			wantRelated: []RelatedOperationRef{
				mustRelatedOperationRefForTest(t, RelatedRefReversal, f.debitOriginal),
				mustRelatedOperationRefForTest(t, RelatedRefReplacement, f.debitOriginal),
			},
		},
		{
			row: "Reconciliation increase in eligible cash",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{FromReady: mustMoney(t, 500, usd)}))
			},
			wantAccounts: map[AccountId]int64{f.checking: 500},
		},
		{
			row:      "Reconciliation increase in eligible cash",
			caseName: "Reconciliation increase in eligible cash attributed to an envelope",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{
					FromReady:     zero,
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, 500, usd)}},
				}))
			},
			wantAccounts:  map[AccountId]int64{f.checking: 500},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 500},
		},
		{
			row:      "Reconciliation increase in eligible cash",
			caseName: "Reconciliation increase in eligible cash with mixed funding",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, 500, usd), FundingSource{
					FromReady:     mustMoney(t, 200, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, 300, usd)}},
				}))
			},
			wantAccounts:  map[AccountId]int64{f.checking: 500},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: 300},
		},
		{
			row: "Reconciliation decrease in eligible cash",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, -500, usd), FundingSource{FromReady: mustMoney(t, -500, usd)}))
			},
			wantAccounts: map[AccountId]int64{f.checking: -500},
		},
		{
			row:      "Reconciliation decrease in eligible cash",
			caseName: "Reconciliation decrease in eligible cash drives Ready-to-assign negative",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, -6_000, usd), FundingSource{FromReady: mustMoney(t, -6_000, usd)}))
			},
			wantAccounts: map[AccountId]int64{f.checking: -6_000},
		},
		{
			row:      "Reconciliation decrease in eligible cash",
			caseName: "Reconciliation decrease in eligible cash attributed to an envelope",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, -500, usd), FundingSource{
					FromReady:     zero,
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, -500, usd)}},
				}))
			},
			wantAccounts:  map[AccountId]int64{f.checking: -500},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: -500},
		},
		{
			row:      "Reconciliation decrease in eligible cash",
			caseName: "Reconciliation decrease in eligible cash drives an envelope negative",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return must(ReconciliationAdjustmentEffects(f.checking, mustSignedDeltaForTest(t, -2_000, usd), FundingSource{
					FromReady:     zero,
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, -2_000, usd)}},
				}))
			},
			wantAccounts:  map[AccountId]int64{f.checking: -2_000},
			wantEnvelopes: map[EnvelopeId]int64{f.spend: -2_000},
		},
		{
			row: "Monthly boundary / rollover",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return MonthlyBoundaryEffects()
			},
		},
		{
			row: "Draft / transaction suggestion created, edited, or cancelled",
			effects: func(t *testing.T, f *postingMatrixFixture) Effects {
				return SuggestionLifecycleEffects()
			},
		},
	}
}

func TestPostingMatrixConformance(t *testing.T) {
	f := newPostingMatrixFixture(t)
	cases := postingMatrixCases(t, f)

	for _, tt := range cases {
		name := tt.row
		if tt.caseName != "" {
			name = tt.caseName
		}
		t.Run(name, func(t *testing.T) {
			state := f.seededLedger(t)
			if tt.prepare != nil {
				tt.prepare(t, f, state)
			}
			before := snapshotMatrixLedger(t, f, state)

			effects := tt.effects(t, f)
			assertRelatedLinksForTest(t, effects, tt.wantRelated...)
			applyMatrixEffectsForTest(t, state, effects)

			after := snapshotMatrixLedger(t, f, state)
			assertMatrixDeltas(t, "account", before.accounts, after.accounts, tt.wantAccounts, f.accountIDs())
			assertMatrixDeltas(t, "envelope", before.envelopes, after.envelopes, tt.wantEnvelopes, f.envelopeIDs())
			assertMatrixDeltas(t, "account reservation", before.accountReserved, after.accountReserved, tt.wantAccountReserved, f.accountIDs())
			assertMatrixDeltas(t, "envelope reservation", before.envelopeReserved, after.envelopeReserved, tt.wantEnvelopeReserved, f.envelopeIDs())
			if tt.checkSpendable {
				assertMatrixDeltas(t, "account spendable", before.accountSpendable, after.accountSpendable, tt.wantAccountSpendable, f.accountIDs())
				assertMatrixDeltas(t, "envelope spendable", before.envelopeSpendable, after.envelopeSpendable, tt.wantEnvelopeSpendable, f.envelopeIDs())
			}
			if tt.reservationOnly {
				assertMatrixBudgetUnchanged(t, before, after)
			}
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

// TestPostingMatrixConformanceCoversEveryRow guards the conformance table
// against silently dropping a matrix row. Keep the expected labels in the
// same order as POSTING_MATRIX section 2.
func TestPostingMatrixConformanceCoversEveryRow(t *testing.T) {
	f := newPostingMatrixFixture(t)
	cases := postingMatrixCases(t, f)

	expected := []string{
		"Opening eligible cash balance",
		"Opening excluded cash balance",
		"Opening card liability",
		"Receive income into eligible account",
		"Receive income into excluded account",
		"Allocate to envelope x",
		"Reallocate x -> y",
		"Debit/cash purchase",
		"Credit-card purchase",
		"Credit-card payment",
		"Card fee / interest",
		"Transfer: eligible cash account to eligible cash account",
		"Transfer: excluded cash to eligible cash",
		"Transfer: eligible cash to excluded cash",
		"Transfer wholly within excluded accounts",
		"Refund to eligible cash from debit/cash purchase",
		"Refund to credit card from card purchase",
		"Unrelated reimbursement received into eligible cash",
		"Purchase reservation created",
		"Reservation amount increased",
		"Reservation amount decreased",
		"Reservation cancelled",
		"Reserved purchase completed",
		"Actual external purchase recorded after the fact",
		"Reversal of committed purchase/event",
		"Replacement after correction",
		"Metadata-only correction",
		"Category correction",
		"Account/source correction",
		"Reconciliation increase in eligible cash",
		"Reconciliation decrease in eligible cash",
		"Monthly boundary / rollover",
		"Draft / transaction suggestion created, edited, or cancelled",
	}

	present := make(map[string]int, len(cases))
	for _, c := range cases {
		present[c.row]++
	}
	known := make(map[string]struct{}, len(expected))
	for _, row := range expected {
		known[row] = struct{}{}
		if present[row] == 0 {
			t.Errorf("posting matrix row %q has no conformance case", row)
		}
	}
	for _, c := range cases {
		if _, ok := known[c.row]; !ok {
			t.Errorf("conformance case %q covers unknown matrix row %q", c.caseName, c.row)
		}
	}
}
