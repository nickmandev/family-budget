package domain

import (
	"testing"
)

// ---------------------------------------------------------------- helpers

func (f *commandFixture) mustRecordIncome(t *testing.T, key string, minorUnits int64) JournalOperation {
	t.Helper()
	op, err := f.service.RecordIncome(f.envelope(t, key), f.checking, mustPositiveAmountForTest(t, minorUnits, f.currency))
	if err != nil {
		t.Fatalf("RecordIncome(%s): %v", key, err)
	}
	return op
}

func (f *commandFixture) mustAllocate(t *testing.T, key string, envelope EnvelopeId, minorUnits int64) JournalOperation {
	t.Helper()
	op, err := f.service.AllocateFunds(f.envelope(t, key), envelope, mustPositiveAmountForTest(t, minorUnits, f.currency))
	if err != nil {
		t.Fatalf("AllocateFunds(%s): %v", key, err)
	}
	return op
}

func (f *commandFixture) mustDebitPurchase(t *testing.T, key string, envelope EnvelopeId, minorUnits int64) JournalOperation {
	t.Helper()
	op, err := f.service.RecordDebitPurchase(f.envelope(t, key), f.checking, envelope, mustPositiveAmountForTest(t, minorUnits, f.currency))
	if err != nil {
		t.Fatalf("RecordDebitPurchase(%s): %v", key, err)
	}
	return op
}

func assertVersion(t *testing.T, f *commandFixture, want uint64) {
	t.Helper()
	if got := f.service.Version(); got != want {
		t.Errorf("Version() = %d, want %d", got, want)
	}
}

func assertInvariants(t *testing.T, state *LedgerState) {
	t.Helper()
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants: %v", err)
	}
}

func assertAccountBalance(t *testing.T, state *LedgerState, id AccountId, wantMinor int64) {
	t.Helper()
	balance, err := state.PostedAccountBalance(id)
	if err != nil {
		t.Fatalf("PostedAccountBalance(%q): %v", id, err)
	}
	if balance.MinorUnits() != wantMinor {
		t.Errorf("account %q balance = %d, want %d", id, balance.MinorUnits(), wantMinor)
	}
}

func assertEnvelopeBalance(t *testing.T, state *LedgerState, id EnvelopeId, wantMinor int64) {
	t.Helper()
	balance, err := state.EnvelopeBalance(id)
	if err != nil {
		t.Fatalf("EnvelopeBalance(%q): %v", id, err)
	}
	if balance.MinorUnits() != wantMinor {
		t.Errorf("envelope %q balance = %d, want %d", id, balance.MinorUnits(), wantMinor)
	}
}

func assertReadyToAssign(t *testing.T, state *LedgerState, wantMinor int64) {
	t.Helper()
	ready, err := state.ReadyToAssign()
	if err != nil {
		t.Fatalf("ReadyToAssign: %v", err)
	}
	if ready.MinorUnits() != wantMinor {
		t.Errorf("ReadyToAssign() = %d, want %d", ready.MinorUnits(), wantMinor)
	}
}

func assertNoReservations(t *testing.T, op JournalOperation) {
	t.Helper()
	if changes := op.ReservationChanges(); len(changes) != 0 {
		t.Errorf("operation %q carries %d reservation changes, want none", op.ID(), len(changes))
	}
}

func assertAccountPosting(t *testing.T, op JournalOperation, id AccountId, wantMinor int64) {
	t.Helper()
	delta, err := op.Postings().AccountDelta(id)
	if err != nil {
		t.Fatalf("AccountDelta(%q): %v", id, err)
	}
	if delta.MinorUnits() != wantMinor {
		t.Errorf("operation %q account posting %q = %d, want %d", op.ID(), id, delta.MinorUnits(), wantMinor)
	}
}

func assertEnvelopePosting(t *testing.T, op JournalOperation, id EnvelopeId, wantMinor int64) {
	t.Helper()
	delta, err := op.Postings().EnvelopeDelta(id)
	if err != nil {
		t.Fatalf("EnvelopeDelta(%q): %v", id, err)
	}
	if delta.MinorUnits() != wantMinor {
		t.Errorf("operation %q envelope posting %q = %d, want %d", op.ID(), id, delta.MinorUnits(), wantMinor)
	}
}

func assertRelated(t *testing.T, op JournalOperation, kind RelatedRefKind, target JournalOperationId) {
	t.Helper()
	for _, ref := range op.Related() {
		if ref.Kind == kind && ref.OperationID == target {
			return
		}
	}
	t.Errorf("operation %q has no %s link to %q", op.ID(), kind, target)
}

// ------------------------------------------------------------------ setup

func TestCommandOpeningBalance(t *testing.T) {
	f := newCommandFixture(t)
	delta, err := NewSignedMoneyDelta(10_000, f.currency)
	if err != nil {
		t.Fatalf("NewSignedMoneyDelta: %v", err)
	}
	op, err := f.service.RecordOpeningBalance(f.envelope(t, "opening"), f.checking, delta)
	if err != nil {
		t.Fatalf("RecordOpeningBalance: %v", err)
	}
	if op.Type() != OpOpeningBalance {
		t.Errorf("operation type = %q, want %q", op.Type(), OpOpeningBalance)
	}
	if op.Source() != TransactionSourceOpeningBalance {
		t.Errorf("operation source = %q, want %q", op.Source(), TransactionSourceOpeningBalance)
	}
	assertAccountPosting(t, op, f.checking, 10_000)
	assertNoReservations(t, op)

	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, 10_000)
	assertReadyToAssign(t, state, 10_000)
	assertEnvelopeBalance(t, state, f.spend, 0)
	assertInvariants(t, state)
	assertVersion(t, f, 1)
}

func TestCommandIncomeAndReimbursement(t *testing.T) {
	f := newCommandFixture(t)
	income, err := f.service.RecordIncome(f.envelope(t, "income"), f.checking, mustPositiveAmountForTest(t, 10_000, f.currency))
	if err != nil {
		t.Fatalf("RecordIncome: %v", err)
	}
	if income.Type() != OpIncome {
		t.Errorf("income type = %q, want %q", income.Type(), OpIncome)
	}
	if amount, ok := income.Amount(); !ok || amount.MinorUnits() != 10_000 {
		t.Errorf("income amount = (%v, %v), want 10000", amount.MinorUnits(), ok)
	}
	assertAccountPosting(t, income, f.checking, 10_000)

	reimbursement, err := f.service.RecordReimbursement(f.envelope(t, "reimbursement"), f.checking, mustPositiveAmountForTest(t, 3_000, f.currency))
	if err != nil {
		t.Fatalf("RecordReimbursement: %v", err)
	}
	if reimbursement.Type() != OpReimbursement {
		t.Errorf("reimbursement type = %q, want %q", reimbursement.Type(), OpReimbursement)
	}
	assertAccountPosting(t, reimbursement, f.checking, 3_000)

	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, 13_000)
	assertReadyToAssign(t, state, 13_000)
	assertInvariants(t, state)
}

func TestCommandAllocateAndReallocate(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)

	allocate, err := f.service.AllocateFunds(f.envelope(t, "allocate"), f.spend, mustPositiveAmountForTest(t, 4_000, f.currency))
	if err != nil {
		t.Fatalf("AllocateFunds: %v", err)
	}
	if allocate.Type() != OpAllocate {
		t.Errorf("allocate type = %q, want %q", allocate.Type(), OpAllocate)
	}
	assertEnvelopePosting(t, allocate, f.spend, 4_000)
	assertAccountPosting(t, allocate, f.checking, 0)

	reallocate, err := f.service.ReallocateFunds(f.envelope(t, "reallocate"), f.spend, f.fee, mustPositiveAmountForTest(t, 1_500, f.currency))
	if err != nil {
		t.Fatalf("ReallocateFunds: %v", err)
	}
	if reallocate.Type() != OpReallocate {
		t.Errorf("reallocate type = %q, want %q", reallocate.Type(), OpReallocate)
	}
	assertEnvelopePosting(t, reallocate, f.spend, -1_500)
	assertEnvelopePosting(t, reallocate, f.fee, 1_500)

	state := f.ledger(t)
	assertEnvelopeBalance(t, state, f.spend, 2_500)
	assertEnvelopeBalance(t, state, f.fee, 1_500)
	assertReadyToAssign(t, state, 6_000)
	assertInvariants(t, state)
}

// --------------------------------------------------------------- spending

func TestCommandDebitPurchase(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate", f.spend, 5_000)

	op, err := f.service.RecordDebitPurchase(f.envelope(t, "debit"), f.checking, f.spend, mustPositiveAmountForTest(t, 2_000, f.currency))
	if err != nil {
		t.Fatalf("RecordDebitPurchase: %v", err)
	}
	if op.Type() != OpDebitPurchase {
		t.Errorf("type = %q, want %q", op.Type(), OpDebitPurchase)
	}
	assertAccountPosting(t, op, f.checking, -2_000)
	assertEnvelopePosting(t, op, f.spend, -2_000)
	assertNoReservations(t, op)

	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, 8_000)
	assertEnvelopeBalance(t, state, f.spend, 3_000)
	assertReadyToAssign(t, state, 5_000)
	assertInvariants(t, state)
}

func TestCommandCardPurchase(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate-spend", f.spend, 5_000)

	op, err := f.service.RecordCardPurchase(f.envelope(t, "card-purchase"), f.card, f.spend, f.pay, mustPositiveAmountForTest(t, 2_000, f.currency))
	if err != nil {
		t.Fatalf("RecordCardPurchase: %v", err)
	}
	if op.Type() != OpCardPurchase {
		t.Errorf("type = %q, want %q", op.Type(), OpCardPurchase)
	}
	assertAccountPosting(t, op, f.card, 2_000)
	assertEnvelopePosting(t, op, f.spend, -2_000)
	assertEnvelopePosting(t, op, f.pay, 2_000)

	state := f.ledger(t)
	assertAccountBalance(t, state, f.card, 2_000)
	assertEnvelopeBalance(t, state, f.spend, 3_000)
	assertEnvelopeBalance(t, state, f.pay, 2_000)
	assertReadyToAssign(t, state, 5_000)
	assertInvariants(t, state)
}

func TestCommandCardPayment(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate-spend", f.spend, 5_000)
	f.mustAllocate(t, "allocate-pay", f.pay, 5_000)
	if _, err := f.service.RecordCardPurchase(f.envelope(t, "card-purchase"), f.card, f.spend, f.pay, mustPositiveAmountForTest(t, 3_000, f.currency)); err != nil {
		t.Fatalf("RecordCardPurchase: %v", err)
	}

	op, err := f.service.RecordCardPayment(f.envelope(t, "card-payment"), f.checking, f.card, f.pay, mustPositiveAmountForTest(t, 2_000, f.currency))
	if err != nil {
		t.Fatalf("RecordCardPayment: %v", err)
	}
	if op.Type() != OpCardPayment {
		t.Errorf("type = %q, want %q", op.Type(), OpCardPayment)
	}
	assertAccountPosting(t, op, f.checking, -2_000)
	assertAccountPosting(t, op, f.card, -2_000)
	assertEnvelopePosting(t, op, f.pay, -2_000)

	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, 8_000)
	assertAccountBalance(t, state, f.card, 1_000)
	assertEnvelopeBalance(t, state, f.pay, 6_000)
	assertReadyToAssign(t, state, 0)
	assertInvariants(t, state)
}

func TestCommandCardFee(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate-fee", f.fee, 3_000)

	op, err := f.service.RecordCardFee(f.envelope(t, "card-fee"), f.card, f.fee, f.pay, mustPositiveAmountForTest(t, 1_000, f.currency))
	if err != nil {
		t.Fatalf("RecordCardFee: %v", err)
	}
	if op.Type() != OpCardFeeInterest {
		t.Errorf("type = %q, want %q", op.Type(), OpCardFeeInterest)
	}
	assertAccountPosting(t, op, f.card, 1_000)
	assertEnvelopePosting(t, op, f.fee, -1_000)
	assertEnvelopePosting(t, op, f.pay, 1_000)

	state := f.ledger(t)
	assertAccountBalance(t, state, f.card, 1_000)
	assertEnvelopeBalance(t, state, f.fee, 2_000)
	assertEnvelopeBalance(t, state, f.pay, 1_000)
	assertReadyToAssign(t, state, 7_000)
	assertInvariants(t, state)
}

// -------------------------------------------------------------- transfers

func TestCommandTransfers(t *testing.T) {
	t.Run("eligible to eligible", func(t *testing.T) {
		f := newCommandFixture(t)
		f.mustRecordIncome(t, "income", 10_000)

		op, err := f.service.TransferBetweenEligible(f.envelope(t, "transfer"), f.checking, f.savings, mustPositiveAmountForTest(t, 3_000, f.currency))
		if err != nil {
			t.Fatalf("TransferBetweenEligible: %v", err)
		}
		if op.Type() != OpTransferEligible {
			t.Errorf("type = %q, want %q", op.Type(), OpTransferEligible)
		}
		assertAccountPosting(t, op, f.checking, -3_000)
		assertAccountPosting(t, op, f.savings, 3_000)

		state := f.ledger(t)
		assertAccountBalance(t, state, f.checking, 7_000)
		assertAccountBalance(t, state, f.savings, 3_000)
		assertReadyToAssign(t, state, 10_000)
		assertInvariants(t, state)
	})

	t.Run("excluded to eligible", func(t *testing.T) {
		f := newCommandFixture(t)
		delta, err := NewSignedMoneyDelta(5_000, f.currency)
		if err != nil {
			t.Fatalf("NewSignedMoneyDelta: %v", err)
		}
		if _, err := f.service.RecordOpeningBalance(f.envelope(t, "opening"), f.excluded, delta); err != nil {
			t.Fatalf("RecordOpeningBalance: %v", err)
		}

		op, err := f.service.TransferExcludedToEligible(f.envelope(t, "transfer"), f.excluded, f.checking, mustPositiveAmountForTest(t, 2_000, f.currency))
		if err != nil {
			t.Fatalf("TransferExcludedToEligible: %v", err)
		}
		if op.Type() != OpTransferToEligible {
			t.Errorf("type = %q, want %q", op.Type(), OpTransferToEligible)
		}
		assertAccountPosting(t, op, f.excluded, -2_000)
		assertAccountPosting(t, op, f.checking, 2_000)

		state := f.ledger(t)
		assertAccountBalance(t, state, f.excluded, 3_000)
		assertAccountBalance(t, state, f.checking, 2_000)
		assertReadyToAssign(t, state, 2_000)
		assertInvariants(t, state)
	})

	t.Run("eligible out to excluded with envelope funding", func(t *testing.T) {
		f := newCommandFixture(t)
		f.mustRecordIncome(t, "income", 10_000)
		f.mustAllocate(t, "allocate", f.spend, 4_000)

		funding := FundingSource{
			FromReady: mustMoneyForCommandTest(t, 0, f.currency),
			FromEnvelopes: []EnvelopeFunding{
				{EnvelopeID: f.spend, Amount: mustMoneyForCommandTest(t, 3_000, f.currency)},
			},
		}
		op, err := f.service.TransferEligibleOutToExcluded(f.envelope(t, "transfer-out"), f.checking, f.excluded, mustPositiveAmountForTest(t, 3_000, f.currency), funding)
		if err != nil {
			t.Fatalf("TransferEligibleOutToExcluded: %v", err)
		}
		if op.Type() != OpTransferOutOfScope {
			t.Errorf("type = %q, want %q", op.Type(), OpTransferOutOfScope)
		}
		assertAccountPosting(t, op, f.checking, -3_000)
		assertAccountPosting(t, op, f.excluded, 3_000)
		assertEnvelopePosting(t, op, f.spend, -3_000)

		state := f.ledger(t)
		assertAccountBalance(t, state, f.checking, 7_000)
		assertAccountBalance(t, state, f.excluded, 3_000)
		assertEnvelopeBalance(t, state, f.spend, 1_000)
		assertReadyToAssign(t, state, 6_000)
		assertInvariants(t, state)
	})

	t.Run("excluded to excluded", func(t *testing.T) {
		f := newCommandFixture(t)
		delta, err := NewSignedMoneyDelta(5_000, f.currency)
		if err != nil {
			t.Fatalf("NewSignedMoneyDelta: %v", err)
		}
		if _, err := f.service.RecordOpeningBalance(f.envelope(t, "opening"), f.excluded, delta); err != nil {
			t.Fatalf("RecordOpeningBalance: %v", err)
		}

		op, err := f.service.TransferBetweenExcluded(f.envelope(t, "transfer-excluded"), f.excluded, f.excluded2, mustPositiveAmountForTest(t, 2_000, f.currency))
		if err != nil {
			t.Fatalf("TransferBetweenExcluded: %v", err)
		}
		if op.Type() != OpTransferExcluded {
			t.Errorf("type = %q, want %q", op.Type(), OpTransferExcluded)
		}
		state := f.ledger(t)
		assertAccountBalance(t, state, f.excluded, 3_000)
		assertAccountBalance(t, state, f.excluded2, 2_000)
		assertReadyToAssign(t, state, 0)
		assertInvariants(t, state)
	})
}

// ---------------------------------------------------------------- refunds

func TestCommandCashRefund(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate", f.spend, 5_000)
	original := f.mustDebitPurchase(t, "purchase", f.spend, 2_000)

	op, err := f.service.RecordCashRefund(f.envelope(t, "refund"), f.checking, original.ID(), f.spend, mustPositiveAmountForTest(t, 500, f.currency))
	if err != nil {
		t.Fatalf("RecordCashRefund: %v", err)
	}
	if op.Type() != OpCashRefund {
		t.Errorf("type = %q, want %q", op.Type(), OpCashRefund)
	}
	assertAccountPosting(t, op, f.checking, 500)
	assertEnvelopePosting(t, op, f.spend, 500)
	assertRelated(t, op, RelatedRefOriginal, original.ID())

	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, 8_500)
	assertEnvelopeBalance(t, state, f.spend, 3_500)
	assertReadyToAssign(t, state, 5_000)
	assertInvariants(t, state)
}

func TestCommandCardRefund(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate-spend", f.spend, 5_000)
	f.mustAllocate(t, "allocate-pay", f.pay, 2_000)
	original, err := f.service.RecordCardPurchase(f.envelope(t, "purchase"), f.card, f.spend, f.pay, mustPositiveAmountForTest(t, 3_000, f.currency))
	if err != nil {
		t.Fatalf("RecordCardPurchase: %v", err)
	}

	op, err := f.service.RecordCardRefund(f.envelope(t, "refund"), f.card, original.ID(), f.spend, f.pay, mustPositiveAmountForTest(t, 1_000, f.currency))
	if err != nil {
		t.Fatalf("RecordCardRefund: %v", err)
	}
	if op.Type() != OpCardRefund {
		t.Errorf("type = %q, want %q", op.Type(), OpCardRefund)
	}
	assertAccountPosting(t, op, f.card, -1_000)
	assertEnvelopePosting(t, op, f.spend, 1_000)
	assertEnvelopePosting(t, op, f.pay, -1_000)
	assertRelated(t, op, RelatedRefOriginal, original.ID())

	state := f.ledger(t)
	assertAccountBalance(t, state, f.card, 2_000)
	assertEnvelopeBalance(t, state, f.spend, 3_000)
	assertEnvelopeBalance(t, state, f.pay, 4_000)
	assertReadyToAssign(t, state, 3_000)
	assertInvariants(t, state)
}

// ------------------------------------------------------------ corrections

func TestCommandReverseOperation(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate", f.spend, 5_000)
	original := f.mustDebitPurchase(t, "purchase", f.spend, 2_000)

	op, err := f.service.ReverseOperation(f.envelope(t, "reverse"), original.ID(), mustPositiveAmountForTest(t, 800, f.currency), "partial reversal")
	if err != nil {
		t.Fatalf("ReverseOperation: %v", err)
	}
	if op.Type() != OpReversal {
		t.Errorf("type = %q, want %q", op.Type(), OpReversal)
	}
	if op.Reason() != "partial reversal" {
		t.Errorf("reason = %q, want %q", op.Reason(), "partial reversal")
	}
	assertAccountPosting(t, op, f.checking, 800)
	assertEnvelopePosting(t, op, f.spend, 800)
	assertRelated(t, op, RelatedRefReversal, original.ID())

	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, 8_800)
	assertEnvelopeBalance(t, state, f.spend, 3_800)
	assertInvariants(t, state)
}

func TestCommandCorrectTransaction(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate", f.spend, 5_000)
	original := f.mustDebitPurchase(t, "purchase", f.spend, 2_000)

	correction, err := DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 1_500, f.currency))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects: %v", err)
	}
	op, err := f.service.CorrectTransaction(f.envelope(t, "correct"), original.ID(), correction, "merchant amount corrected")
	if err != nil {
		t.Fatalf("CorrectTransaction: %v", err)
	}
	if op.Type() != OpReplacement {
		t.Errorf("type = %q, want %q", op.Type(), OpReplacement)
	}
	assertRelated(t, op, RelatedRefReversal, original.ID())
	assertRelated(t, op, RelatedRefReplacement, original.ID())

	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, 8_500)
	assertEnvelopeBalance(t, state, f.spend, 3_500)
	assertReadyToAssign(t, state, 5_000)
	assertInvariants(t, state)
}

func TestCommandCorrectCategory(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate", f.spend, 5_000)
	original := f.mustDebitPurchase(t, "purchase", f.spend, 2_000)

	before := f.ledger(t)
	beforeSum, err := before.SumEnvelopeBalances()
	if err != nil {
		t.Fatalf("SumEnvelopeBalances: %v", err)
	}

	op, err := f.service.CorrectCategory(f.envelope(t, "category"), original.ID(), f.spend, f.fee, mustPositiveAmountForTest(t, 2_000, f.currency), "wrong envelope")
	if err != nil {
		t.Fatalf("CorrectCategory: %v", err)
	}
	if op.Type() != OpCategoryCorrection {
		t.Errorf("type = %q, want %q", op.Type(), OpCategoryCorrection)
	}
	if len(op.Postings().Accounts) != 0 {
		t.Errorf("category correction carries %d account postings, want 0", len(op.Postings().Accounts))
	}
	assertEnvelopePosting(t, op, f.spend, 2_000)
	assertEnvelopePosting(t, op, f.fee, -2_000)

	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, 8_000)
	assertEnvelopeBalance(t, state, f.spend, 5_000)
	assertEnvelopeBalance(t, state, f.fee, -2_000)
	afterSum, err := state.SumEnvelopeBalances()
	if err != nil {
		t.Fatalf("SumEnvelopeBalances: %v", err)
	}
	if beforeSum.MinorUnits() != afterSum.MinorUnits() {
		t.Errorf("sum(E) changed from %d to %d across category correction", beforeSum.MinorUnits(), afterSum.MinorUnits())
	}
	assertInvariants(t, state)
}

func TestCommandCorrectMetadata(t *testing.T) {
	f := newCommandFixture(t)
	original := f.mustRecordIncome(t, "income", 10_000)
	before := f.ledger(t)
	beforeBalance, err := before.PostedAccountBalance(f.checking)
	if err != nil {
		t.Fatalf("PostedAccountBalance: %v", err)
	}

	op, err := f.service.CorrectMetadata(f.envelope(t, "metadata"), original.ID(), "merchant name fixed")
	if err != nil {
		t.Fatalf("CorrectMetadata: %v", err)
	}
	if op.Type() != OpMetadataCorrection {
		t.Errorf("type = %q, want %q", op.Type(), OpMetadataCorrection)
	}
	postings := op.Postings()
	if len(postings.Accounts) != 0 || len(postings.Envelopes) != 0 {
		t.Errorf("metadata correction postings = %+v, want empty", postings)
	}
	if _, ok := op.Amount(); ok {
		t.Error("metadata correction unexpectedly carries an amount")
	}
	assertRelated(t, op, RelatedRefOriginal, original.ID())

	state := f.ledger(t)
	afterBalance, err := state.PostedAccountBalance(f.checking)
	if err != nil {
		t.Fatalf("PostedAccountBalance: %v", err)
	}
	if beforeBalance.MinorUnits() != afterBalance.MinorUnits() {
		t.Errorf("metadata correction changed balance from %d to %d", beforeBalance.MinorUnits(), afterBalance.MinorUnits())
	}
	assertInvariants(t, state)
}

// --------------------------------------------------------- reconciliation

func TestCommandReconciliation(t *testing.T) {
	t.Run("increase", func(t *testing.T) {
		f := newCommandFixture(t)
		funding := FundingSource{FromReady: mustMoneyForCommandTest(t, 1_000, f.currency)}
		op, err := f.service.ReconcileAccount(f.envelope(t, "reconcile"), f.checking, mustSignedDeltaForCommandTest(t, 1_000, f.currency), funding, "bank statement")
		if err != nil {
			t.Fatalf("ReconcileAccount: %v", err)
		}
		if op.Type() != OpReconcileIncrease {
			t.Errorf("type = %q, want %q", op.Type(), OpReconcileIncrease)
		}
		if op.Source() != TransactionSourceReconciliation {
			t.Errorf("source = %q, want %q", op.Source(), TransactionSourceReconciliation)
		}
		if op.Reason() != "bank statement" {
			t.Errorf("reason = %q, want %q", op.Reason(), "bank statement")
		}

		state := f.ledger(t)
		assertAccountBalance(t, state, f.checking, 1_000)
		assertReadyToAssign(t, state, 1_000)
		assertInvariants(t, state)
	})

	t.Run("decrease negative ready-to-assign", func(t *testing.T) {
		f := newCommandFixture(t)
		funding := FundingSource{FromReady: mustMoneyForCommandTest(t, -1_000, f.currency)}
		op, err := f.service.ReconcileAccount(f.envelope(t, "reconcile"), f.checking, mustSignedDeltaForCommandTest(t, -1_000, f.currency), funding, "missing receipt")
		if err != nil {
			t.Fatalf("ReconcileAccount: %v", err)
		}
		if op.Type() != OpReconcileDecrease {
			t.Errorf("type = %q, want %q", op.Type(), OpReconcileDecrease)
		}

		state := f.ledger(t)
		assertAccountBalance(t, state, f.checking, -1_000)
		assertReadyToAssign(t, state, -1_000)
		assertInvariants(t, state)

		deficits, err := state.Deficits()
		if err != nil {
			t.Fatalf("Deficits: %v", err)
		}
		if len(deficits) != 1 || deficits[0].Kind != DeficitReadyToAssign || deficits[0].Amount.MinorUnits() != 1_000 {
			t.Errorf("Deficits() = %+v, want one READY_TO_ASSIGN deficit of 1000", deficits)
		}
		status, err := state.ResolutionStatus()
		if err != nil {
			t.Fatalf("ResolutionStatus: %v", err)
		}
		if status != ResolutionRequiresResolution {
			t.Errorf("ResolutionStatus() = %q, want %q", status, ResolutionRequiresResolution)
		}
	})

	t.Run("blank reason rejected", func(t *testing.T) {
		f := newCommandFixture(t)
		funding := FundingSource{FromReady: mustMoneyForCommandTest(t, 1_000, f.currency)}
		_, err := f.service.ReconcileAccount(f.envelope(t, "reconcile-blank"), f.checking, mustSignedDeltaForCommandTest(t, 1_000, f.currency), funding, "   ")
		if !HasCode(err, CodeInvalidValue) {
			t.Fatalf("ReconcileAccount blank reason error = %v, want INVALID_VALUE", err)
		}
		assertVersion(t, f, 0)
	})
}

func mustSignedDeltaForCommandTest(t *testing.T, minorUnits int64, currency Currency) SignedMoneyDelta {
	t.Helper()
	delta, err := NewSignedMoneyDelta(minorUnits, currency)
	if err != nil {
		t.Fatalf("NewSignedMoneyDelta(%d): %v", minorUnits, err)
	}
	return delta
}

// ---------------------------------------------------------- funding checks

func TestCommandFundingFailures(t *testing.T) {
	t.Run("allocation beyond ready-to-assign", func(t *testing.T) {
		f := newCommandFixture(t)
		_, err := f.service.AllocateFunds(f.envelope(t, "too-much"), f.spend, mustPositiveAmountForTest(t, 1, f.currency))
		if !HasCode(err, CodeInsufficientReadyToAssign) {
			t.Fatalf("AllocateFunds error = %v, want INSUFFICIENT_READY_TO_ASSIGN", err)
		}
		assertVersion(t, f, 0)
	})

	t.Run("reallocation beyond source", func(t *testing.T) {
		f := newCommandFixture(t)
		f.mustRecordIncome(t, "income", 10_000)
		f.mustAllocate(t, "allocate", f.spend, 1_000)
		_, err := f.service.ReallocateFunds(f.envelope(t, "too-much"), f.spend, f.fee, mustPositiveAmountForTest(t, 2_000, f.currency))
		if !HasCode(err, CodeInsufficientEnvelopeFunds) {
			t.Fatalf("ReallocateFunds error = %v, want INSUFFICIENT_ENVELOPE_FUNDS", err)
		}
		assertVersion(t, f, 2)
	})

	t.Run("transfer out without funding plan", func(t *testing.T) {
		f := newCommandFixture(t)
		f.mustRecordIncome(t, "income", 10_000)
		_, err := f.service.TransferEligibleOutToExcluded(f.envelope(t, "no-funding"), f.checking, f.excluded, mustPositiveAmountForTest(t, 1_000, f.currency), FundingSource{})
		if !HasCode(err, CodeAccountScopeChangeRequiresFundingPlan) {
			t.Fatalf("TransferEligibleOutToExcluded error = %v, want ACCOUNT_SCOPE_CHANGE_REQUIRES_FUNDING_PLAN", err)
		}
		assertVersion(t, f, 1)
	})

	t.Run("transfer out funding total mismatch", func(t *testing.T) {
		f := newCommandFixture(t)
		f.mustRecordIncome(t, "income", 10_000)
		funding := FundingSource{FromReady: mustMoneyForCommandTest(t, 500, f.currency)}
		_, err := f.service.TransferEligibleOutToExcluded(f.envelope(t, "mismatch"), f.checking, f.excluded, mustPositiveAmountForTest(t, 1_000, f.currency), funding)
		if !HasCode(err, CodeInvalidAmount) {
			t.Fatalf("TransferEligibleOutToExcluded error = %v, want INVALID_AMOUNT", err)
		}
		assertVersion(t, f, 1)
	})
}

// --------------------------------------------------------- Q3 truthfulness

func TestCommandExternalTruthfulness(t *testing.T) {
	f := newCommandFixture(t)
	// No income and no allocation: the purchase is underfunded on both sides.
	op, err := f.service.RecordDebitPurchase(f.envelope(t, "underfunded"), f.checking, f.spend, mustPositiveAmountForTest(t, 5_000, f.currency))
	if err != nil {
		t.Fatalf("RecordDebitPurchase should succeed for a factual event: %v", err)
	}
	if op.Type() != OpDebitPurchase {
		t.Errorf("type = %q, want %q", op.Type(), OpDebitPurchase)
	}

	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, -5_000)
	assertEnvelopeBalance(t, state, f.spend, -5_000)
	assertReadyToAssign(t, state, 0)
	assertInvariants(t, state)

	deficits, err := state.Deficits()
	if err != nil {
		t.Fatalf("Deficits: %v", err)
	}
	if len(deficits) != 1 || deficits[0].Kind != DeficitEnvelope || deficits[0].EnvelopeID != f.spend || deficits[0].Amount.MinorUnits() != 5_000 {
		t.Errorf("Deficits() = %+v, want one envelope deficit of 5000 on spend", deficits)
	}
	status, err := state.ResolutionStatus()
	if err != nil {
		t.Fatalf("ResolutionStatus: %v", err)
	}
	if status != ResolutionRequiresResolution {
		t.Errorf("ResolutionStatus() = %q, want %q", status, ResolutionRequiresResolution)
	}
}

// ------------------------------------------------------- refund remainders

func TestCommandRefundRemainder(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate", f.spend, 5_000)
	original := f.mustDebitPurchase(t, "purchase", f.spend, 5_000)

	if _, err := f.service.RecordCashRefund(f.envelope(t, "refund-1"), f.checking, original.ID(), f.spend, mustPositiveAmountForTest(t, 2_000, f.currency)); err != nil {
		t.Fatalf("first refund: %v", err)
	}
	if _, err := f.service.RecordCashRefund(f.envelope(t, "refund-2"), f.checking, original.ID(), f.spend, mustPositiveAmountForTest(t, 3_000, f.currency)); err != nil {
		t.Fatalf("second refund within remainder: %v", err)
	}
	versionBefore := f.service.Version()
	before := f.ledger(t)
	beforeAccount, err := before.PostedAccountBalance(f.checking)
	if err != nil {
		t.Fatalf("PostedAccountBalance: %v", err)
	}

	_, err = f.service.RecordCashRefund(f.envelope(t, "refund-3"), f.checking, original.ID(), f.spend, mustPositiveAmountForTest(t, 1, f.currency))
	if !HasCode(err, CodeRefundExceedsRemainder) {
		t.Fatalf("third refund error = %v, want REFUND_EXCEEDS_REMAINDER", err)
	}
	if f.service.Version() != versionBefore {
		t.Errorf("Version() changed from %d to %d on rejected refund", versionBefore, f.service.Version())
	}
	after := f.ledger(t)
	afterAccount, err := after.PostedAccountBalance(f.checking)
	if err != nil {
		t.Fatalf("PostedAccountBalance: %v", err)
	}
	if beforeAccount.MinorUnits() != afterAccount.MinorUnits() {
		t.Errorf("checking balance changed from %d to %d on rejected refund", beforeAccount.MinorUnits(), afterAccount.MinorUnits())
	}
	assertEnvelopeBalance(t, after, f.spend, 5_000)
	assertInvariants(t, after)

	t.Run("unknown original", func(t *testing.T) {
		g := newCommandFixture(t)
		unknown := mustJournalOpIDForTest(t, "op-does-not-exist")
		_, err := g.service.RecordCashRefund(g.envelope(t, "unknown"), g.checking, unknown, g.spend, mustPositiveAmountForTest(t, 100, g.currency))
		if !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
			t.Fatalf("refund of unknown original error = %v, want ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD", err)
		}
	})

	t.Run("non-purchase original", func(t *testing.T) {
		g := newCommandFixture(t)
		income := g.mustRecordIncome(t, "income-only", 1_000)
		_, err := g.service.RecordCashRefund(g.envelope(t, "not-purchase"), g.checking, income.ID(), g.spend, mustPositiveAmountForTest(t, 100, g.currency))
		if !HasCode(err, CodeInvalidValue) {
			t.Fatalf("refund of non-purchase error = %v, want INVALID_VALUE", err)
		}
	})
}

// ----------------------------------------------------- reversal remainders

func TestCommandReversalRemainder(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate", f.spend, 5_000)
	original := f.mustDebitPurchase(t, "purchase", f.spend, 5_000)

	if _, err := f.service.ReverseOperation(f.envelope(t, "reverse-1"), original.ID(), mustPositiveAmountForTest(t, 2_000, f.currency), "partial"); err != nil {
		t.Fatalf("first reversal: %v", err)
	}
	versionBefore := f.service.Version()
	_, err := f.service.ReverseOperation(f.envelope(t, "reverse-too-much"), original.ID(), mustPositiveAmountForTest(t, 4_000, f.currency), "too much")
	if !HasCode(err, CodeReversalExceedsRemainder) {
		t.Fatalf("over-reversal error = %v, want REVERSAL_EXCEEDS_REMAINDER", err)
	}
	if f.service.Version() != versionBefore {
		t.Errorf("Version() changed from %d to %d on rejected reversal", versionBefore, f.service.Version())
	}

	if _, err := f.service.ReverseOperation(f.envelope(t, "reverse-2"), original.ID(), mustPositiveAmountForTest(t, 3_000, f.currency), "remaining"); err != nil {
		t.Fatalf("second reversal within remainder: %v", err)
	}
	_, err = f.service.ReverseOperation(f.envelope(t, "reverse-3"), original.ID(), mustPositiveAmountForTest(t, 1, f.currency), "over")
	if !HasCode(err, CodeReversalExceedsRemainder) {
		t.Fatalf("reversal after full reversal error = %v, want REVERSAL_EXCEEDS_REMAINDER", err)
	}

	_, err = f.service.ReverseOperation(f.envelope(t, "reverse-blank-reason"), original.ID(), mustPositiveAmountForTest(t, 1, f.currency), "  ")
	if !HasCode(err, CodeInvalidValue) {
		t.Fatalf("reversal blank reason error = %v, want INVALID_VALUE", err)
	}

	state := f.ledger(t)
	assertAccountBalance(t, state, f.checking, 10_000)
	assertEnvelopeBalance(t, state, f.spend, 5_000)
	assertInvariants(t, state)
}

// ------------------------------------------- idempotency across handlers

func TestCommandIdempotencyAcrossHandlers(t *testing.T) {
	f := newCommandFixture(t)
	env := f.envelope(t, "income-key")
	amount := mustPositiveAmountForTest(t, 1_000, f.currency)

	first, err := f.service.RecordIncome(env, f.checking, amount)
	if err != nil {
		t.Fatalf("first RecordIncome: %v", err)
	}
	second, err := f.service.RecordIncome(env, f.checking, amount)
	if err != nil {
		t.Fatalf("replayed RecordIncome: %v", err)
	}
	if first.ID() != second.ID() {
		t.Errorf("replayed op id = %q, want original %q", second.ID(), first.ID())
	}
	assertVersion(t, f, 1)
	assertAccountBalance(t, f.ledger(t), f.checking, 1_000)

	conflicting := f.envelopeWithFingerprint(t, "income-key", "fedcba9876543210")
	if _, err := f.service.RecordIncome(conflicting, f.checking, amount); !HasCode(err, CodeIdempotencyKeyPayloadMismatch) {
		t.Fatalf("mismatched fingerprint error = %v, want IDEMPOTENCY_KEY_PAYLOAD_MISMATCH", err)
	}
	assertVersion(t, f, 1)
	assertAccountBalance(t, f.ledger(t), f.checking, 1_000)

	t.Run("refund replay bypasses remainder", func(t *testing.T) {
		g := newCommandFixture(t)
		g.mustRecordIncome(t, "income", 10_000)
		g.mustAllocate(t, "allocate", g.spend, 5_000)
		original := g.mustDebitPurchase(t, "purchase", g.spend, 5_000)
		refundEnv := g.envelope(t, "refund-replay")
		refundAmount := mustPositiveAmountForTest(t, 5_000, g.currency)

		firstRefund, err := g.service.RecordCashRefund(refundEnv, g.checking, original.ID(), g.spend, refundAmount)
		if err != nil {
			t.Fatalf("first refund: %v", err)
		}
		replayedRefund, err := g.service.RecordCashRefund(refundEnv, g.checking, original.ID(), g.spend, refundAmount)
		if err != nil {
			t.Fatalf("replayed refund should not trip the remainder cap: %v", err)
		}
		if firstRefund.ID() != replayedRefund.ID() {
			t.Errorf("replayed refund id = %q, want %q", replayedRefund.ID(), firstRefund.ID())
		}
		assertEnvelopeBalance(t, g.ledger(t), g.spend, 5_000)
	})
}

// ------------------------------------------------------------ atomicity

func TestCommandAtomicityOnFundingFailure(t *testing.T) {
	f := newCommandFixture(t)
	f.mustRecordIncome(t, "income", 10_000)
	f.mustAllocate(t, "allocate", f.spend, 1_000)

	versionBefore := f.service.Version()
	before := f.ledger(t)
	beforeSpend, err := before.EnvelopeBalance(f.spend)
	if err != nil {
		t.Fatalf("EnvelopeBalance: %v", err)
	}
	beforeReady, err := before.ReadyToAssign()
	if err != nil {
		t.Fatalf("ReadyToAssign: %v", err)
	}

	failedEnv := f.envelope(t, "fails")
	_, err = f.service.AllocateFunds(failedEnv, f.spend, mustPositiveAmountForTest(t, 999_999, f.currency))
	if !HasCode(err, CodeInsufficientReadyToAssign) {
		t.Fatalf("AllocateFunds error = %v, want INSUFFICIENT_READY_TO_ASSIGN", err)
	}

	if f.service.Version() != versionBefore {
		t.Errorf("Version() = %d after failure, want %d", f.service.Version(), versionBefore)
	}
	after := f.ledger(t)
	afterSpend, err := after.EnvelopeBalance(f.spend)
	if err != nil {
		t.Fatalf("EnvelopeBalance: %v", err)
	}
	afterReady, err := after.ReadyToAssign()
	if err != nil {
		t.Fatalf("ReadyToAssign: %v", err)
	}
	if beforeSpend.MinorUnits() != afterSpend.MinorUnits() {
		t.Errorf("spend balance changed from %d to %d after failure", beforeSpend.MinorUnits(), afterSpend.MinorUnits())
	}
	if beforeReady.MinorUnits() != afterReady.MinorUnits() {
		t.Errorf("ReadyToAssign changed from %d to %d after failure", beforeReady.MinorUnits(), afterReady.MinorUnits())
	}
	if _, lookupErr := f.service.Idempotency().Lookup(failedEnv.Key); !HasCode(lookupErr, CodeEntityNotFoundOrNotInHousehold) {
		t.Errorf("failed command left an idempotency record: %v", lookupErr)
	}
}
