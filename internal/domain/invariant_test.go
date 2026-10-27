package domain

import "testing"

func TestVerifyBudgetIdentity(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	eur := mustCurrency(t, "EUR", 2)

	tests := []struct {
		name           string
		budgetableCash Money
		readyToAssign  Money
		envelopeSum    Money
		wantCode       ErrorCode
	}{
		{name: "balanced", budgetableCash: mustMoney(t, 1_000, usd), readyToAssign: mustMoney(t, 400, usd), envelopeSum: mustMoney(t, 600, usd)},
		{name: "balanced negative R", budgetableCash: mustMoney(t, 500, usd), readyToAssign: mustMoney(t, -100, usd), envelopeSum: mustMoney(t, 600, usd)},
		{name: "all zero", budgetableCash: mustMoney(t, 0, usd), readyToAssign: mustMoney(t, 0, usd), envelopeSum: mustMoney(t, 0, usd)},
		{name: "budgetable mismatch", budgetableCash: mustMoney(t, 1_000, usd), readyToAssign: mustMoney(t, 400, usd), envelopeSum: mustMoney(t, 700, usd), wantCode: CodeAccountingInvariantViolation},
		{name: "ready mismatch", budgetableCash: mustMoney(t, 1_000, usd), readyToAssign: mustMoney(t, 500, usd), envelopeSum: mustMoney(t, 600, usd), wantCode: CodeAccountingInvariantViolation},
		{name: "currency mismatch", budgetableCash: mustMoney(t, 1_000, eur), readyToAssign: mustMoney(t, 400, usd), envelopeSum: mustMoney(t, 600, usd), wantCode: CodeCurrencyMismatch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyBudgetIdentity(tt.budgetableCash, tt.readyToAssign, tt.envelopeSum)
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("VerifyBudgetIdentity unexpected error: %v", err)
				}
				return
			}
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("VerifyBudgetIdentity error = %v, want code %s", err, tt.wantCode)
			}
		})
	}
}

func TestCheckInvariantsPassesForLegitimateDeficits(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	food := mustEnvelopeIDForTest(t, "env-food")

	t.Run("negative ready to assign", func(t *testing.T) {
		state := mustLedgerForTest(t, usd,
			[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
			[]EnvelopeId{food},
		)
		postings := mustPostingSetForTest(t)
		if err := postings.AddAccount(checking, mustSignedDeltaForTest(t, 100, usd)); err != nil {
			t.Fatalf("AddAccount: %v", err)
		}
		if err := postings.AddEnvelope(food, mustSignedDeltaForTest(t, 200, usd)); err != nil {
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
			t.Fatalf("ReadyToAssign = %d, want -100", ready.MinorUnits())
		}
		if err := state.CheckInvariants(); err != nil {
			t.Fatalf("CheckInvariants unexpected error for negative R: %v", err)
		}
	})

	t.Run("negative envelope balance", func(t *testing.T) {
		state := mustLedgerForTest(t, usd,
			[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
			[]EnvelopeId{food},
		)
		postings := mustPostingSetForTest(t)
		if err := postings.AddAccount(checking, mustSignedDeltaForTest(t, 100, usd)); err != nil {
			t.Fatalf("AddAccount: %v", err)
		}
		if err := postings.AddEnvelope(food, mustSignedDeltaForTest(t, -50, usd)); err != nil {
			t.Fatalf("AddEnvelope: %v", err)
		}
		if err := state.ApplyPostings(postings); err != nil {
			t.Fatalf("ApplyPostings: %v", err)
		}
		if err := state.CheckInvariants(); err != nil {
			t.Fatalf("CheckInvariants unexpected error for negative envelope balance: %v", err)
		}
	})

	t.Run("balanced with negative R and negative envelope", func(t *testing.T) {
		state := mustLedgerForTest(t, usd,
			[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
			[]EnvelopeId{food},
		)
		postings := mustPostingSetForTest(t)
		if err := postings.AddAccount(checking, mustSignedDeltaForTest(t, 10, usd)); err != nil {
			t.Fatalf("AddAccount: %v", err)
		}
		if err := postings.AddEnvelope(food, mustSignedDeltaForTest(t, 40, usd)); err != nil {
			t.Fatalf("AddEnvelope: %v", err)
		}
		if err := state.ApplyPostings(postings); err != nil {
			t.Fatalf("ApplyPostings: %v", err)
		}
		if err := state.CheckInvariants(); err != nil {
			t.Fatalf("CheckInvariants unexpected error: %v", err)
		}
	})
}

func TestCheckInvariantsEmptyLedger(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	state := mustLedgerForTest(t, usd, nil, nil)
	if err := state.CheckInvariants(); err != nil {
		t.Fatalf("CheckInvariants unexpected error: %v", err)
	}
}

func TestCheckInvariantsDetectsFabricatedNegativeReservation(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	checking := mustAccountIDForTest(t, "acct-checking")
	food := mustEnvelopeIDForTest(t, "env-food")

	t.Run("envelope", func(t *testing.T) {
		state := mustLedgerForTest(t, usd,
			[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
			[]EnvelopeId{food},
		)
		state.envelopes[food].reserved = mustMoney(t, -1, usd)
		if err := state.CheckInvariants(); !HasCode(err, CodeAccountingInvariantViolation) {
			t.Fatalf("CheckInvariants error = %v, want ACCOUNTING_INVARIANT_VIOLATION", err)
		}
	})

	t.Run("account", func(t *testing.T) {
		state := mustLedgerForTest(t, usd,
			[]AccountMeta{mustAccountMetaForTest(t, checking, AccountKindChecking, true)},
			[]EnvelopeId{food},
		)
		state.accounts[checking].reserved = mustMoney(t, -1, usd)
		if err := state.CheckInvariants(); !HasCode(err, CodeAccountingInvariantViolation) {
			t.Fatalf("CheckInvariants error = %v, want ACCOUNTING_INVARIANT_VIOLATION", err)
		}
	})
}

func TestCheckInvariantsDetectsFabricatedCardEligibility(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	card := mustAccountIDForTest(t, "acct-card")
	state := mustLedgerForTest(t, usd,
		[]AccountMeta{mustAccountMetaForTest(t, card, AccountKindCardLiability, false)},
		nil,
	)
	state.accounts[card].meta.BudgetEligible = true
	if err := state.CheckInvariants(); !HasCode(err, CodeAccountingInvariantViolation) {
		t.Fatalf("CheckInvariants error = %v, want ACCOUNTING_INVARIANT_VIOLATION", err)
	}
}
