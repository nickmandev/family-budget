package postgres

import (
	"math/rand"
	"testing"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// This suite exercises the projection-rebuild contract at the database level:
// every balance the adapter derives from persisted posting/reservation rows
// must equal the ledger replayed from the journal (POSTING_MATRIX §6, §7 test
// 12). All tests skip without TEST_POSTGRES_DSN/DATABASE_URL through testStore.

const projectionSeed int64 = 0x5EED_C0FFEE

// projectionBaseTime is the fixed commit clock for deterministic journal order.
var projectionBaseTime = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// projectionFixture extends the standard seed with a card liability account and
// the extra envelopes a realistic card/refund/reversal sequence needs.
type projectionFixture struct {
	seed
	cardID      domain.AccountId
	savingsID   domain.AccountId
	groceriesID domain.EnvelopeId
	paymentID   domain.EnvelopeId
	travelID    domain.EnvelopeId
	date        domain.EffectiveDate
}

func newProjectionFixture(t *testing.T, store *Store) projectionFixture {
	t.Helper()

	repos := store.Repositories()
	base := seedHousehold(t, repos, "projection")
	ctx := t.Context()

	cardID := domain.AccountId(mustID(t))
	if err := repos.Accounts.Create(ctx, base.householdID, ports.AccountRecord{
		ID:             cardID,
		Name:           "Card",
		Kind:           domain.AccountKindCardLiability,
		BudgetEligible: false,
		Status:         ports.LifecycleActive,
	}); err != nil {
		t.Fatalf("create card account: %v", err)
	}

	savingsID := domain.AccountId(mustID(t))
	if err := repos.Accounts.Create(ctx, base.householdID, ports.AccountRecord{
		ID:             savingsID,
		Name:           "Savings",
		Kind:           domain.AccountKindSavings,
		BudgetEligible: true,
		Status:         ports.LifecycleActive,
	}); err != nil {
		t.Fatalf("create savings account: %v", err)
	}

	paymentID := domain.EnvelopeId(mustID(t))
	if err := repos.Envelopes.Create(ctx, base.householdID, ports.EnvelopeRecord{
		ID:                  paymentID,
		Name:                "Card payment",
		Status:              ports.LifecycleActive,
		LinkedCardAccountID: &cardID,
	}); err != nil {
		t.Fatalf("create payment envelope: %v", err)
	}

	travelID := domain.EnvelopeId(mustID(t))
	if err := repos.Envelopes.Create(ctx, base.householdID, ports.EnvelopeRecord{
		ID:     travelID,
		Name:   "Travel",
		Status: ports.LifecycleActive,
	}); err != nil {
		t.Fatalf("create travel envelope: %v", err)
	}

	date, err := domain.ParseEffectiveDate("2026-10-01")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}

	return projectionFixture{
		seed:        base,
		cardID:      cardID,
		savingsID:   savingsID,
		groceriesID: base.envelopeID,
		paymentID:   paymentID,
		travelID:    travelID,
		date:        date,
	}
}

// mustEffects returns a builder wrapper that fails the test when an effect
// builder rejects the scenario. Returning a function lets call sites pass the
// (Effects, error) pair directly: mustEffects(t)(domain.XEffects(...)).
func mustEffects(t *testing.T) func(domain.Effects, error) domain.Effects {
	t.Helper()
	return func(effects domain.Effects, err error) domain.Effects {
		t.Helper()
		if err != nil {
			t.Fatalf("build effects: %v", err)
		}
		return effects
	}
}

// amountPointer returns a pointer to a positive amount of minor units.
func amountPointer(t *testing.T, minor int64, currency domain.Currency) *domain.PositiveAmount {
	t.Helper()
	amount := testPositiveAmount(t, minor, currency)
	return &amount
}

// newProjectionOperation builds one committed operation at an explicit instant
// so the journal's (committed_at, id) ordering matches append order.
func newProjectionOperation(
	t *testing.T,
	s seed,
	id domain.JournalOperationId,
	key domain.IdempotencyKey,
	fingerprint domain.RequestFingerprint,
	opType domain.OperationType,
	date domain.EffectiveDate,
	amount *domain.PositiveAmount,
	effects domain.Effects,
	committed time.Time,
) domain.JournalOperation {
	t.Helper()

	committed = committed.UTC().Truncate(time.Microsecond)
	operation, err := domain.NewJournalOperation(domain.JournalOperationParams{
		ID:                 id,
		Household:          s.householdID,
		Actor:              s.userID,
		Type:               opType,
		Source:             domain.TransactionSourceManual,
		EffectiveDate:      date,
		Currency:           s.currency,
		IdempotencyKey:     key,
		RequestFingerprint: fingerprint,
		Amount:             amount,
		CreatedAt:          domain.NewRecordedAt(committed.Add(-time.Millisecond)),
		CommittedAt:        domain.NewRecordedAt(committed),
		Effects:            effects,
	})
	if err != nil {
		t.Fatalf("new journal operation: %v", err)
	}
	return operation
}

// appendProjectionOperation commits one operation at a deterministic position.
func appendProjectionOperation(
	t *testing.T,
	store *Store,
	s seed,
	sequence int,
	opType domain.OperationType,
	date domain.EffectiveDate,
	amount *domain.PositiveAmount,
	effects domain.Effects,
) domain.JournalOperation {
	t.Helper()

	operation := newProjectionOperation(
		t,
		s,
		domain.JournalOperationId(mustID(t)),
		domain.IdempotencyKey("projection-"+mustID(t)),
		domain.RequestFingerprint("proj"),
		opType,
		date,
		amount,
		effects,
		projectionBaseTime.Add(time.Duration(sequence)*time.Second),
	)
	if err := store.Repositories().Journal.Append(t.Context(), s.householdID, operation); err != nil {
		t.Fatalf("append %s operation: %v", opType, err)
	}
	return operation
}

func TestVerifyProjectionsAfterRealisticSequence(t *testing.T) {
	store := testStore(t)
	fx := newProjectionFixture(t, store)
	ctx := t.Context()

	income := mustEffects(t)(domain.IncomeReceivedEffects(fx.accountID, testPositiveAmount(t, 100_000, fx.currency)))
	appendProjectionOperation(t, store, fx.seed, 0, domain.OpIncome, fx.date, amountPointer(t, 100_000, fx.currency), income)

	groceriesAllocation := mustEffects(t)(domain.AllocateFundsEffects(fx.groceriesID, testPositiveAmount(t, 30_000, fx.currency)))
	appendProjectionOperation(t, store, fx.seed, 1, domain.OpAllocate, fx.date, amountPointer(t, 30_000, fx.currency), groceriesAllocation)

	travelAllocation := mustEffects(t)(domain.AllocateFundsEffects(fx.travelID, testPositiveAmount(t, 20_000, fx.currency)))
	appendProjectionOperation(t, store, fx.seed, 2, domain.OpAllocate, fx.date, amountPointer(t, 20_000, fx.currency), travelAllocation)

	debitEffects := mustEffects(t)(domain.DebitPurchaseEffects(fx.accountID, fx.groceriesID, testPositiveAmount(t, 5_000, fx.currency)))
	debit := appendProjectionOperation(t, store, fx.seed, 3, domain.OpDebitPurchase, fx.date, amountPointer(t, 5_000, fx.currency), debitEffects)

	cardEffects := mustEffects(t)(domain.CreditCardPurchaseEffects(fx.cardID, fx.groceriesID, fx.paymentID, testPositiveAmount(t, 4_000, fx.currency)))
	appendProjectionOperation(t, store, fx.seed, 4, domain.OpCardPurchase, fx.date, amountPointer(t, 4_000, fx.currency), cardEffects)

	paymentEffects := mustEffects(t)(domain.CreditCardPaymentEffects(fx.accountID, fx.cardID, fx.paymentID, testPositiveAmount(t, 4_000, fx.currency)))
	appendProjectionOperation(t, store, fx.seed, 5, domain.OpCardPayment, fx.date, amountPointer(t, 4_000, fx.currency), paymentEffects)

	refundEffects := mustEffects(t)(domain.CashRefundEffects(fx.accountID, debit.ID(), fx.groceriesID, testPositiveAmount(t, 1_000, fx.currency)))
	appendProjectionOperation(t, store, fx.seed, 6, domain.OpCashRefund, fx.date, amountPointer(t, 1_000, fx.currency), refundEffects)

	reversalEffects := mustEffects(t)(domain.ReversalEffects(debit.Effects().Postings, debit.ID()))
	appendProjectionOperation(t, store, fx.seed, 7, domain.OpReversal, fx.date, amountPointer(t, 5_000, fx.currency), reversalEffects)

	if err := store.VerifyProjections(ctx, fx.householdID); err != nil {
		t.Fatalf("projections do not match the journal rebuild: %v", err)
	}

	state, err := store.RebuildLedgerFromJournal(ctx, fx.householdID)
	if err != nil {
		t.Fatalf("rebuild ledger: %v", err)
	}
	assertMinorUnits(t, "checking posted", state.PostedAccountBalance, fx.accountID, 97_000)
	assertMinorUnits(t, "card liability", state.CardLiabilityBalance, fx.cardID, 0)
	assertMinorUnits(t, "groceries balance", state.EnvelopeBalance, fx.groceriesID, 27_000)
	assertMinorUnits(t, "travel balance", state.EnvelopeBalance, fx.travelID, 20_000)
	assertMinorUnits(t, "payment envelope balance", state.EnvelopeBalance, fx.paymentID, 0)
	if budgetable, err := state.BudgetableCash(); err != nil || budgetable.MinorUnits() != 97_000 {
		t.Fatalf("budgetable cash = %v/%v, want 97000", budgetable.MinorUnits(), err)
	}
	if envelopeSum, err := state.SumEnvelopeBalances(); err != nil || envelopeSum.MinorUnits() != 47_000 {
		t.Fatalf("envelope sum = %v/%v, want 47000", envelopeSum.MinorUnits(), err)
	}
	if ready, err := state.ReadyToAssign(); err != nil || ready.MinorUnits() != 50_000 {
		t.Fatalf("ready-to-assign = %v/%v, want 50000", ready.MinorUnits(), err)
	}
}

func TestVerifyProjectionsAtEveryCommit(t *testing.T) {
	store := testStore(t)
	fx := newProjectionFixture(t, store)
	ctx := t.Context()

	income := mustEffects(t)(domain.IncomeReceivedEffects(fx.accountID, testPositiveAmount(t, 10_000, fx.currency)))
	appendProjectionOperation(t, store, fx.seed, 0, domain.OpIncome, fx.date, amountPointer(t, 10_000, fx.currency), income)
	if err := store.VerifyProjections(ctx, fx.householdID); err != nil {
		t.Fatalf("verify after income: %v", err)
	}

	allocate := mustEffects(t)(domain.AllocateFundsEffects(fx.groceriesID, testPositiveAmount(t, 8_000, fx.currency)))
	appendProjectionOperation(t, store, fx.seed, 1, domain.OpAllocate, fx.date, amountPointer(t, 8_000, fx.currency), allocate)
	if err := store.VerifyProjections(ctx, fx.householdID); err != nil {
		t.Fatalf("verify after allocation: %v", err)
	}

	debitEffects := mustEffects(t)(domain.DebitPurchaseEffects(fx.accountID, fx.groceriesID, testPositiveAmount(t, 1_000, fx.currency)))
	debit := appendProjectionOperation(t, store, fx.seed, 2, domain.OpDebitPurchase, fx.date, amountPointer(t, 1_000, fx.currency), debitEffects)
	if err := store.VerifyProjections(ctx, fx.householdID); err != nil {
		t.Fatalf("verify after debit purchase: %v", err)
	}

	cardEffects := mustEffects(t)(domain.CreditCardPurchaseEffects(fx.cardID, fx.groceriesID, fx.paymentID, testPositiveAmount(t, 500, fx.currency)))
	appendProjectionOperation(t, store, fx.seed, 3, domain.OpCardPurchase, fx.date, amountPointer(t, 500, fx.currency), cardEffects)
	if err := store.VerifyProjections(ctx, fx.householdID); err != nil {
		t.Fatalf("verify after card purchase: %v", err)
	}

	refundEffects := mustEffects(t)(domain.CashRefundEffects(fx.accountID, debit.ID(), fx.groceriesID, testPositiveAmount(t, 200, fx.currency)))
	appendProjectionOperation(t, store, fx.seed, 4, domain.OpCashRefund, fx.date, amountPointer(t, 200, fx.currency), refundEffects)
	if err := store.VerifyProjections(ctx, fx.householdID); err != nil {
		t.Fatalf("verify after refund: %v", err)
	}

	travelAllocation := mustEffects(t)(domain.AllocateFundsEffects(fx.travelID, testPositiveAmount(t, 3_000, fx.currency)))
	appendProjectionOperation(t, store, fx.seed, 5, domain.OpAllocate, fx.date, amountPointer(t, 3_000, fx.currency), travelAllocation)
	if err := store.VerifyProjections(ctx, fx.householdID); err != nil {
		t.Fatalf("verify after second allocation: %v", err)
	}
}

func TestVerifyProjectionsRandomizedJournal(t *testing.T) {
	store := testStore(t)
	fx := newProjectionFixture(t, store)
	ctx := t.Context()

	const operationCount = 50
	t.Logf("randomized projection seed = %d", projectionSeed)
	rng := rand.New(rand.NewSource(projectionSeed))

	type recordedOperation struct {
		operation domain.JournalOperation
		amount    int64
		envelope  domain.EnvelopeId
	}
	var all []recordedOperation
	var purchases []recordedOperation
	reversed := make(map[domain.JournalOperationId]bool)

	dateFor := func(index int) domain.EffectiveDate {
		date, err := domain.NewEffectiveDate(2026, 10, 1+index%28)
		if err != nil {
			t.Fatalf("effective date: %v", err)
		}
		return date
	}
	minor := func(minimum, maximum int64) int64 {
		return minimum + int64(rng.Int63n(maximum-minimum+1))
	}

	for index := 0; index < operationCount; index++ {
		date := dateFor(index)
		committed := projectionBaseTime.Add(time.Duration(index) * time.Minute)
		amount := int64(0)
		opType := domain.OpIncome
		var effects domain.Effects
		var envelope domain.EnvelopeId

		switch rng.Intn(9) {
		case 0:
			amount = minor(1, 20_000)
			opType = domain.OpIncome
			effects = mustEffects(t)(domain.IncomeReceivedEffects(fx.accountID, testPositiveAmount(t, amount, fx.currency)))
		case 1:
			amount = minor(1, 10_000)
			opType = domain.OpAllocate
			envelopes := []domain.EnvelopeId{fx.groceriesID, fx.travelID}
			effects = mustEffects(t)(domain.AllocateFundsEffects(envelopes[rng.Intn(len(envelopes))], testPositiveAmount(t, amount, fx.currency)))
		case 2:
			amount = minor(1, 5_000)
			opType = domain.OpReallocate
			source := fx.groceriesID
			destination := fx.travelID
			if rng.Intn(2) == 0 {
				source, destination = destination, source
			}
			effects = mustEffects(t)(domain.ReallocateFundsEffects(source, destination, testPositiveAmount(t, amount, fx.currency)))
		case 3:
			amount = minor(1, 5_000)
			opType = domain.OpDebitPurchase
			envelopes := []domain.EnvelopeId{fx.groceriesID, fx.travelID}
			envelope = envelopes[rng.Intn(len(envelopes))]
			effects = mustEffects(t)(domain.DebitPurchaseEffects(fx.accountID, envelope, testPositiveAmount(t, amount, fx.currency)))
		case 4:
			if len(purchases) == 0 {
				amount = minor(1, 5_000)
				opType = domain.OpIncome
				effects = mustEffects(t)(domain.IncomeReceivedEffects(fx.accountID, testPositiveAmount(t, amount, fx.currency)))
				break
			}
			original := purchases[rng.Intn(len(purchases))]
			amount = minor(1, original.amount)
			opType = domain.OpCashRefund
			effects = mustEffects(t)(domain.CashRefundEffects(fx.accountID, original.operation.ID(), original.envelope, testPositiveAmount(t, amount, fx.currency)))
		case 5:
			amount = minor(1, 5_000)
			opType = domain.OpTransferEligible
			effects = mustEffects(t)(domain.TransferEligibleToEligibleEffects(fx.accountID, fx.savingsID, testPositiveAmount(t, amount, fx.currency)))
		case 6:
			amount = minor(1, 3_000)
			opType = domain.OpCardPurchase
			envelopes := []domain.EnvelopeId{fx.groceriesID, fx.travelID}
			effects = mustEffects(t)(domain.CreditCardPurchaseEffects(fx.cardID, envelopes[rng.Intn(len(envelopes))], fx.paymentID, testPositiveAmount(t, amount, fx.currency)))
		case 7:
			amount = minor(1, 3_000)
			opType = domain.OpCardPayment
			effects = mustEffects(t)(domain.CreditCardPaymentEffects(fx.accountID, fx.cardID, fx.paymentID, testPositiveAmount(t, amount, fx.currency)))
		case 8:
			reversible := make([]recordedOperation, 0, len(all))
			for _, candidate := range all {
				if !reversed[candidate.operation.ID()] && !candidate.operation.Effects().Postings.IsEmpty() {
					reversible = append(reversible, candidate)
				}
			}
			if len(reversible) == 0 {
				amount = minor(1, 5_000)
				opType = domain.OpIncome
				effects = mustEffects(t)(domain.IncomeReceivedEffects(fx.accountID, testPositiveAmount(t, amount, fx.currency)))
				break
			}
			original := reversible[rng.Intn(len(reversible))]
			reversed[original.operation.ID()] = true
			amount = original.amount
			opType = domain.OpReversal
			effects = mustEffects(t)(domain.ReversalEffects(original.operation.Effects().Postings, original.operation.ID()))
		}

		operation := newProjectionOperation(
			t,
			fx.seed,
			domain.JournalOperationId(mustID(t)),
			domain.IdempotencyKey("random-"+mustID(t)),
			domain.RequestFingerprint("rand"),
			opType,
			date,
			amountPointer(t, amount, fx.currency),
			effects,
			committed,
		)
		if err := store.Repositories().Journal.Append(ctx, fx.householdID, operation); err != nil {
			t.Fatalf("append randomized operation %d (%s): %v", index, opType, err)
		}

		recorded := recordedOperation{operation: operation, amount: amount, envelope: envelope}
		all = append(all, recorded)
		if opType == domain.OpDebitPurchase {
			purchases = append(purchases, recorded)
		}
	}

	operations, err := store.loadAllJournalOperations(ctx, fx.householdID)
	if err != nil {
		t.Fatalf("load journal: %v", err)
	}
	if len(operations) != operationCount {
		t.Fatalf("journal holds %d operations, want %d", len(operations), operationCount)
	}

	if err := store.VerifyProjections(ctx, fx.householdID); err != nil {
		t.Fatalf("randomized journal projections do not match: %v", err)
	}
}

func TestVerifyProjectionsDetectsCorruptedLedgerVersion(t *testing.T) {
	// This is the only test that writes directly to the schema outside the
	// adapter ports. households.ledger_version is the adapter's materialized
	// projection (posting rows are append-only inputs, not a mutable cache), so
	// corrupting it must surface as ACCOUNTING_INVARIANT_VIOLATION rather than
	// being silently trusted or treated as a reconciliation adjustment.
	cases := []struct {
		name  string
		query string
	}{
		{name: "version ahead of journal", query: "UPDATE households SET ledger_version = ledger_version + 3 WHERE id = ?"},
		{name: "version behind journal", query: "UPDATE households SET ledger_version = 0 WHERE id = ?"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testStore(t)
			fx := newProjectionFixture(t, store)
			ctx := t.Context()

			income := mustEffects(t)(domain.IncomeReceivedEffects(fx.accountID, testPositiveAmount(t, 5_000, fx.currency)))
			appendProjectionOperation(t, store, fx.seed, 0, domain.OpIncome, fx.date, amountPointer(t, 5_000, fx.currency), income)
			allocate := mustEffects(t)(domain.AllocateFundsEffects(fx.groceriesID, testPositiveAmount(t, 2_000, fx.currency)))
			appendProjectionOperation(t, store, fx.seed, 1, domain.OpAllocate, fx.date, amountPointer(t, 2_000, fx.currency), allocate)

			if err := store.VerifyProjections(ctx, fx.householdID); err != nil {
				t.Fatalf("precondition: healthy projections should verify: %v", err)
			}

			if err := store.DB().Exec(tc.query, mustUUID(t, fx.householdID.String())).Error; err != nil {
				t.Fatalf("corrupt ledger version: %v", err)
			}

			err := store.VerifyProjections(ctx, fx.householdID)
			wantCode(t, err, domain.CodeAccountingInvariantViolation)
		})
	}
}

// assertMinorUnits asserts a ledger accessor returns the wanted minor units.
func assertMinorUnits[T comparable](
	t *testing.T,
	label string,
	accessor func(T) (domain.Money, error),
	id T,
	want int64,
) {
	t.Helper()
	money, err := accessor(id)
	if err != nil {
		t.Fatalf("%s for %v: %v", label, id, err)
	}
	if money.MinorUnits() != want {
		t.Fatalf("%s for %v = %d minor units, want %d", label, id, money.MinorUnits(), want)
	}
}
