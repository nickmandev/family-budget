package domain

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// journalFixture is the shared household shape for journal-operation and
// journal tests: one eligible checking account, one excluded account, one card
// liability, and spending/payment envelopes.
type journalFixture struct {
	household HouseholdId
	actor     UserId
	currency  Currency
	checking  AccountId
	excluded  AccountId
	card      AccountId
	spend     EnvelopeId
	pay       EnvelopeId
	date      EffectiveDate
	created   RecordedAt
	committed RecordedAt
}

func newJournalFixture(t *testing.T) *journalFixture {
	t.Helper()
	household, err := NewHouseholdId("household-1")
	if err != nil {
		t.Fatalf("NewHouseholdId: %v", err)
	}
	actor, err := NewUserId("user-1")
	if err != nil {
		t.Fatalf("NewUserId: %v", err)
	}
	date, err := ParseEffectiveDate("2026-01-15")
	if err != nil {
		t.Fatalf("ParseEffectiveDate: %v", err)
	}
	return &journalFixture{
		household: household,
		actor:     actor,
		currency:  mustCurrency(t, "USD", 2),
		checking:  mustAccountIDForTest(t, "acct-checking"),
		excluded:  mustAccountIDForTest(t, "acct-excluded"),
		card:      mustAccountIDForTest(t, "acct-card"),
		spend:     mustEnvelopeIDForTest(t, "env-spend"),
		pay:       mustEnvelopeIDForTest(t, "env-pay"),
		date:      date,
		created:   NewRecordedAt(time.Date(2026, time.January, 15, 9, 0, 0, 0, time.UTC)),
		committed: NewRecordedAt(time.Date(2026, time.January, 15, 9, 0, 1, 0, time.UTC)),
	}
}

// newLedger builds an empty ledger matching the fixture's accounts and
// envelopes.
func (f *journalFixture) newLedger(t *testing.T) *LedgerState {
	t.Helper()
	return mustLedgerForTest(t, f.currency,
		[]AccountMeta{
			mustAccountMetaForTest(t, f.checking, AccountKindChecking, true),
			mustAccountMetaForTest(t, f.excluded, AccountKindSavings, false),
			mustAccountMetaForTest(t, f.card, AccountKindCardLiability, false),
		},
		[]EnvelopeId{f.spend, f.pay},
	)
}

// params builds an otherwise-valid envelope for an operation with the given id,
// type, and effects.
func (f *journalFixture) params(t *testing.T, id string, opType OperationType, effects Effects) JournalOperationParams {
	t.Helper()
	opID, err := NewJournalOperationId(id)
	if err != nil {
		t.Fatalf("NewJournalOperationId(%q): %v", id, err)
	}
	key, err := NewIdempotencyKey("key-" + id)
	if err != nil {
		t.Fatalf("NewIdempotencyKey: %v", err)
	}
	fingerprint, err := NewRequestFingerprint("abcdef0123456789")
	if err != nil {
		t.Fatalf("NewRequestFingerprint: %v", err)
	}
	return JournalOperationParams{
		ID:                 opID,
		Household:          f.household,
		Actor:              f.actor,
		Type:               opType,
		Source:             TransactionSourceManual,
		EffectiveDate:      f.date,
		Currency:           f.currency,
		IdempotencyKey:     key,
		RequestFingerprint: fingerprint,
		CreatedAt:          f.created,
		CommittedAt:        f.committed,
		Effects:            effects,
	}
}

// operation builds and validates an operation, failing the test on error.
func (f *journalFixture) operation(t *testing.T, id string, opType OperationType, effects Effects) JournalOperation {
	t.Helper()
	op, err := NewJournalOperation(f.params(t, id, opType, effects))
	if err != nil {
		t.Fatalf("NewJournalOperation(%q): %v", id, err)
	}
	return op
}

func TestJournalVersionStartsAtZero(t *testing.T) {
	f := newJournalFixture(t)
	journal := NewJournal(f.household)
	if journal.Household() != f.household {
		t.Errorf("Household = %q, want %q", journal.Household(), f.household)
	}
	if got := journal.Version(); got != 0 {
		t.Errorf("Version = %d, want 0", got)
	}
}

func TestJournalAppendAndLookup(t *testing.T) {
	f := newJournalFixture(t)
	journal := NewJournal(f.household)

	income, err := IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 10_000, f.currency))
	if err != nil {
		t.Fatalf("IncomeReceivedEffects: %v", err)
	}
	allocate, err := AllocateFundsEffects(f.spend, mustPositiveAmountForTest(t, 4_000, f.currency))
	if err != nil {
		t.Fatalf("AllocateFundsEffects: %v", err)
	}
	first := f.operation(t, "op-income", OpIncome, income)
	second := f.operation(t, "op-allocate", OpAllocate, allocate)

	if err := journal.Append(first); err != nil {
		t.Fatalf("Append(first): %v", err)
	}
	if got := journal.Version(); got != 1 {
		t.Fatalf("Version after first append = %d, want 1", got)
	}
	if err := journal.Append(second); err != nil {
		t.Fatalf("Append(second): %v", err)
	}
	if got := journal.Version(); got != 2 {
		t.Fatalf("Version after second append = %d, want 2", got)
	}

	ops := journal.Operations()
	if len(ops) != 2 {
		t.Fatalf("Operations length = %d, want 2", len(ops))
	}
	if ops[0].ID() != first.ID() || ops[1].ID() != second.ID() {
		t.Errorf("Operations order = [%q, %q], want [%q, %q]", ops[0].ID(), ops[1].ID(), first.ID(), second.ID())
	}

	got, err := journal.Operation(second.ID())
	if err != nil {
		t.Fatalf("Operation(second): %v", err)
	}
	if got.ID() != second.ID() {
		t.Errorf("Operation round-trip ID = %q, want %q", got.ID(), second.ID())
	}

	if err := journal.Append(second); !HasCode(err, CodeInvalidValue) {
		t.Errorf("Append(duplicate) error = %v, want INVALID_VALUE", err)
	}
	if got := journal.Version(); got != 2 {
		t.Errorf("Version after duplicate append = %d, want 2", got)
	}
	if ops := journal.Operations(); len(ops) != 2 {
		t.Errorf("Operations length after duplicate append = %d, want 2", len(ops))
	}

	if _, err := journal.Operation("missing"); !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
		t.Errorf("Operation(missing) error = %v, want ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD", err)
	}

	otherHousehold, err := NewHouseholdId("household-2")
	if err != nil {
		t.Fatalf("NewHouseholdId: %v", err)
	}
	foreignParams := f.params(t, "op-foreign", OpIncome, income)
	foreignParams.Household = otherHousehold
	foreign, err := NewJournalOperation(foreignParams)
	if err != nil {
		t.Fatalf("NewJournalOperation(foreign): %v", err)
	}
	if err := journal.Append(foreign); !HasCode(err, CodeInvalidValue) {
		t.Errorf("Append(foreign household) error = %v, want INVALID_VALUE", err)
	}
	if got := journal.Version(); got != 2 {
		t.Errorf("Version after foreign append = %d, want 2", got)
	}
}

func TestJournalOperationsByType(t *testing.T) {
	f := newJournalFixture(t)
	journal := NewJournal(f.household)

	income, err := IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 5_000, f.currency))
	if err != nil {
		t.Fatalf("IncomeReceivedEffects: %v", err)
	}
	allocate, err := AllocateFundsEffects(f.spend, mustPositiveAmountForTest(t, 1_000, f.currency))
	if err != nil {
		t.Fatalf("AllocateFundsEffects: %v", err)
	}
	if err := journal.Append(f.operation(t, "op-income-1", OpIncome, income)); err != nil {
		t.Fatalf("Append(income 1): %v", err)
	}
	if err := journal.Append(f.operation(t, "op-allocate", OpAllocate, allocate)); err != nil {
		t.Fatalf("Append(allocate): %v", err)
	}
	if err := journal.Append(f.operation(t, "op-income-2", OpIncome, income)); err != nil {
		t.Fatalf("Append(income 2): %v", err)
	}

	incomes := journal.OperationsByType(OpIncome)
	if len(incomes) != 2 {
		t.Fatalf("OperationsByType(INCOME) length = %d, want 2", len(incomes))
	}
	if incomes[0].ID() != "op-income-1" || incomes[1].ID() != "op-income-2" {
		t.Errorf("OperationsByType(INCOME) order = [%q, %q]", incomes[0].ID(), incomes[1].ID())
	}
	if got := journal.OperationsByType(OpCardPayment); len(got) != 0 {
		t.Errorf("OperationsByType(CARD_PAYMENT) length = %d, want 0", len(got))
	}
}

func TestJournalConcurrentAppend(t *testing.T) {
	f := newJournalFixture(t)
	journal := NewJournal(f.household)

	const count = 64
	income, err := IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 1, f.currency))
	if err != nil {
		t.Fatalf("IncomeReceivedEffects: %v", err)
	}
	operations := make([]JournalOperation, count)
	for i := 0; i < count; i++ {
		operations[i] = f.operation(t, fmt.Sprintf("op-%02d", i), OpIncome, income)
	}

	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := range operations {
		wg.Add(1)
		go func(op JournalOperation) {
			defer wg.Done()
			if err := journal.Append(op); err != nil {
				errs <- err
			}
		}(operations[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Append: %v", err)
	}

	if got := journal.Version(); got != count {
		t.Errorf("Version = %d, want %d", got, count)
	}
	if got := len(journal.Operations()); got != count {
		t.Errorf("Operations length = %d, want %d", got, count)
	}
}

func TestJournalRebuildLedger(t *testing.T) {
	f := newJournalFixture(t)
	journal := NewJournal(f.household)

	income, err := IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 10_000, f.currency))
	if err != nil {
		t.Fatalf("IncomeReceivedEffects: %v", err)
	}
	allocate, err := AllocateFundsEffects(f.spend, mustPositiveAmountForTest(t, 4_000, f.currency))
	if err != nil {
		t.Fatalf("AllocateFundsEffects: %v", err)
	}
	debit, err := DebitPurchaseEffects(f.checking, f.spend, mustPositiveAmountForTest(t, 1_500, f.currency))
	if err != nil {
		t.Fatalf("DebitPurchaseEffects: %v", err)
	}
	cardPurchase, err := CreditCardPurchaseEffects(f.card, f.spend, f.pay, mustPositiveAmountForTest(t, 2_000, f.currency))
	if err != nil {
		t.Fatalf("CreditCardPurchaseEffects: %v", err)
	}
	cardPayment, err := CreditCardPaymentEffects(f.checking, f.card, f.pay, mustPositiveAmountForTest(t, 1_000, f.currency))
	if err != nil {
		t.Fatalf("CreditCardPaymentEffects: %v", err)
	}

	steps := []struct {
		id      string
		opType  OperationType
		effects Effects
	}{
		{id: "op-income", opType: OpIncome, effects: income},
		{id: "op-allocate", opType: OpAllocate, effects: allocate},
		{id: "op-debit", opType: OpDebitPurchase, effects: debit},
		{id: "op-card-purchase", opType: OpCardPurchase, effects: cardPurchase},
		{id: "op-card-payment", opType: OpCardPayment, effects: cardPayment},
	}

	direct := f.newLedger(t)
	for _, step := range steps {
		op := f.operation(t, step.id, step.opType, step.effects)
		if err := journal.Append(op); err != nil {
			t.Fatalf("Append(%q): %v", step.id, err)
		}
		if err := direct.ApplyPostings(step.effects.Postings); err != nil {
			t.Fatalf("ApplyPostings(%q): %v", step.id, err)
		}
		if err := direct.CheckInvariants(); err != nil {
			t.Fatalf("direct CheckInvariants(%q): %v", step.id, err)
		}
	}

	rebuilt, err := journal.RebuildLedger(
		[]AccountMeta{
			mustAccountMetaForTest(t, f.checking, AccountKindChecking, true),
			mustAccountMetaForTest(t, f.excluded, AccountKindSavings, false),
			mustAccountMetaForTest(t, f.card, AccountKindCardLiability, false),
		},
		[]EnvelopeId{f.spend, f.pay},
	)
	if err != nil {
		t.Fatalf("RebuildLedger: %v", err)
	}
	if err := rebuilt.CheckInvariants(); err != nil {
		t.Fatalf("rebuilt CheckInvariants: %v", err)
	}

	for _, id := range []AccountId{f.checking, f.excluded, f.card} {
		want, err := direct.PostedAccountBalance(id)
		if err != nil {
			t.Fatalf("direct PostedAccountBalance(%q): %v", id, err)
		}
		got, err := rebuilt.PostedAccountBalance(id)
		if err != nil {
			t.Fatalf("rebuilt PostedAccountBalance(%q): %v", id, err)
		}
		if !moneyEqual(got, want) {
			t.Errorf("account %q posted = %d, want %d", id, got.MinorUnits(), want.MinorUnits())
		}
	}
	for _, id := range []EnvelopeId{f.spend, f.pay} {
		want, err := direct.EnvelopeBalance(id)
		if err != nil {
			t.Fatalf("direct EnvelopeBalance(%q): %v", id, err)
		}
		got, err := rebuilt.EnvelopeBalance(id)
		if err != nil {
			t.Fatalf("rebuilt EnvelopeBalance(%q): %v", id, err)
		}
		if !moneyEqual(got, want) {
			t.Errorf("envelope %q balance = %d, want %d", id, got.MinorUnits(), want.MinorUnits())
		}
	}

	for _, check := range []struct {
		name string
		got  func(*LedgerState) (Money, error)
	}{
		{name: "budgetable cash", got: (*LedgerState).BudgetableCash},
		{name: "envelope sum", got: (*LedgerState).SumEnvelopeBalances},
		{name: "ready to assign", got: (*LedgerState).ReadyToAssign},
	} {
		want, err := check.got(direct)
		if err != nil {
			t.Fatalf("direct %s: %v", check.name, err)
		}
		got, err := check.got(rebuilt)
		if err != nil {
			t.Fatalf("rebuilt %s: %v", check.name, err)
		}
		if !moneyEqual(got, want) {
			t.Errorf("%s = %d, want %d", check.name, got.MinorUnits(), want.MinorUnits())
		}
	}

	wantCard, err := direct.CardLiabilityBalance(f.card)
	if err != nil {
		t.Fatalf("direct CardLiabilityBalance: %v", err)
	}
	gotCard, err := rebuilt.CardLiabilityBalance(f.card)
	if err != nil {
		t.Fatalf("rebuilt CardLiabilityBalance: %v", err)
	}
	if !moneyEqual(gotCard, wantCard) {
		t.Errorf("card liability = %d, want %d", gotCard.MinorUnits(), wantCard.MinorUnits())
	}
}

func TestJournalRebuildLedgerRejectsEmptyJournal(t *testing.T) {
	f := newJournalFixture(t)
	journal := NewJournal(f.household)
	if _, err := journal.RebuildLedger(nil, nil); !HasCode(err, CodeInvalidValue) {
		t.Errorf("RebuildLedger(empty) error = %v, want INVALID_VALUE", err)
	}
}

// moneyEqual compares two Money values by minor units and currency code.
func moneyEqual(a, b Money) bool {
	return a.MinorUnits() == b.MinorUnits() && a.Currency().Equal(b.Currency())
}
