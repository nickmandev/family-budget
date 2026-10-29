package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"gorm.io/gorm"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// This suite exercises the concurrency contract at the port/transaction layer.
// Step 5's CommandService is still an in-memory reference implementation, so
// there is no persistence-backed application service to drive yet. Each scenario
// therefore composes the repository ports and the domain's pure effects exactly
// as a command handler would: lock the household, re-read state, validate,
// append atomically. The full command-over-database wiring lands in 6C/7.

// The concurrency tests skip without the TEST_POSTGRES_* components through
// testStore.

func TestConcurrentSpendingCannotDoubleSpend(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "spend")
	ctx := t.Context()

	// Fund the envelope with 100 minor units.
	fundingDate, err := domain.ParseEffectiveDate("2026-10-28")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}
	fundingAmount := testPositiveAmount(t, 100, a.currency)
	fundingEffects := domain.Effects{Postings: domain.PostingSet{
		Accounts:  []domain.AccountPosting{{AccountID: a.accountID, Delta: testSignedDelta(t, 100, a.currency)}},
		Envelopes: []domain.EnvelopePosting{{EnvelopeID: a.envelopeID, Delta: testSignedDelta(t, 100, a.currency)}},
	}}
	funding := newJournalOperation(
		t, a, domain.JournalOperationId(mustID(t)),
		domain.IdempotencyKey("spend-funding"), domain.RequestFingerprint("f001"),
		domain.OpIncome, fundingDate, &fundingAmount, fundingEffects,
	)
	if err := repos.Journal.Append(ctx, a.householdID, funding); err != nil {
		t.Fatalf("fund envelope: %v", err)
	}

	const (
		spenders   = 8
		spendMinor = 30
		wantPosted = 3 // floor(100/30)
	)
	spendOps := make([]domain.JournalOperation, spenders)
	for i := 0; i < spenders; i++ {
		amount := testPositiveAmount(t, spendMinor, a.currency)
		effects := domain.Effects{Postings: domain.PostingSet{
			Accounts:  []domain.AccountPosting{{AccountID: a.accountID, Delta: testSignedDelta(t, -spendMinor, a.currency)}},
			Envelopes: []domain.EnvelopePosting{{EnvelopeID: a.envelopeID, Delta: testSignedDelta(t, -spendMinor, a.currency)}},
		}}
		spendOps[i] = newJournalOperation(
			t, a, domain.JournalOperationId(mustID(t)),
			domain.IdempotencyKey("spend-"+mustID(t)), domain.RequestFingerprint("f002"),
			domain.OpDebitPurchase, fundingDate, &amount, effects,
		)
	}

	householdUUID, err := uuidFromDomain(a.householdID.String())
	if err != nil {
		t.Fatalf("household uuid: %v", err)
	}
	envelopeUUID, err := uuidFromDomain(a.envelopeID.String())
	if err != nil {
		t.Fatalf("envelope uuid: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, spenders)
	var wg sync.WaitGroup
	for i := range spendOps {
		wg.Add(1)
		go func(operation domain.JournalOperation) {
			defer wg.Done()
			<-start
			errs <- store.WithinTransaction(ctx, func(ctx context.Context, txRepos ports.Repositories) error {
				tx := txRepos.Journal.(*journalRepository).db
				if err := lockHousehold(ctx, tx, householdUUID); err != nil {
					return err
				}
				balance, err := envelopeBalanceMinor(ctx, tx, householdUUID, envelopeUUID)
				if err != nil {
					return err
				}
				if balance < spendMinor {
					return domain.NewErrorf(domain.CodeInsufficientEnvelopeFunds, "envelope has %d minor units available", balance)
				}
				return txRepos.Journal.Append(ctx, a.householdID, operation)
			})
		}(spendOps[i])
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !domain.HasCode(err, domain.CodeInsufficientEnvelopeFunds) && !domain.HasCode(err, domain.CodeConcurrencyConflict) {
			t.Fatalf("spend failed with non-retryable, non-domain outcome: %v", err)
		}
	}
	if successes != wantPosted {
		t.Fatalf("successful spends = %d, want %d (funded amount must never be exceeded)", successes, wantPosted)
	}

	balance, err := envelopeBalanceMinor(ctx, store.DB(), householdUUID, envelopeUUID)
	if err != nil {
		t.Fatalf("final envelope balance: %v", err)
	}
	if balance != 10 {
		t.Fatalf("final envelope balance = %d, want 10 (no double spend)", balance)
	}
	spendCount, err := countJournalOperations(ctx, store.DB(), a.householdID, domain.OpDebitPurchase)
	if err != nil {
		t.Fatalf("count spend operations: %v", err)
	}
	if spendCount != wantPosted {
		t.Fatalf("spend operation count = %d, want %d", spendCount, wantPosted)
	}
}

func TestConcurrentIdempotentRetriesInsertOnce(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "retry")
	ctx := t.Context()

	date, err := domain.ParseEffectiveDate("2026-10-28")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}
	amount := testPositiveAmount(t, 450, a.currency)
	effects := domain.Effects{Postings: domain.PostingSet{Accounts: []domain.AccountPosting{{
		AccountID: a.accountID,
		Delta:     testSignedDelta(t, 450, a.currency),
	}}}}
	opID := domain.JournalOperationId(mustID(t))
	key := domain.IdempotencyKey("retry-key")
	fingerprint := domain.RequestFingerprint("f00d")
	operation := newJournalOperation(t, a, opID, key, fingerprint, domain.OpIncome, date, &amount, effects)
	now := journalTime()

	const retriers = 8
	type retryResult struct {
		createdNew bool
		err        error
	}
	results := make(chan retryResult, retriers)
	start := make(chan struct{})

	var wg sync.WaitGroup
	for i := 0; i < retriers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for attempt := 0; attempt < 40; attempt++ {
				createdNew := false
				err := store.WithinTransaction(ctx, func(ctx context.Context, txRepos ports.Repositories) error {
					record, isNew, err := txRepos.Idempotency.Begin(ctx, a.householdID, key, fingerprint, opID, now)
					if err != nil {
						return err
					}
					if !isNew {
						if record.OperationID() != opID {
							return domain.NewErrorf(domain.CodeInvalidValue, "replay returned operation %q, want %q", record.OperationID(), opID)
						}
						return nil
					}
					createdNew = true
					return txRepos.Journal.Append(ctx, a.householdID, operation)
				})
				if err == nil {
					results <- retryResult{createdNew: createdNew}
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			results <- retryResult{err: domain.NewError(domain.CodeConcurrencyConflict, "idempotent retry did not converge")}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	created := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("idempotent retry failed: %v", result.err)
		}
		if result.createdNew {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("new inserts = %d, want exactly 1", created)
	}

	journalCount, err := countJournalOperationsByKey(ctx, store.DB(), a.householdID, key)
	if err != nil {
		t.Fatalf("count journal operations: %v", err)
	}
	if journalCount != 1 {
		t.Fatalf("journal operations with key = %d, want 1", journalCount)
	}
	idempotencyCount, err := countIdempotencyRecords(ctx, store.DB(), a.householdID, key)
	if err != nil {
		t.Fatalf("count idempotency records: %v", err)
	}
	if idempotencyCount != 1 {
		t.Fatalf("idempotency records with key = %d, want 1", idempotencyCount)
	}
	var postingCount int64
	if err := store.DB().WithContext(ctx).Model(&accountPostingModel{}).
		Where("household_id = ? AND journal_operation_id = ?", mustUUID(t, a.householdID.String()), mustUUID(t, opID.String())).
		Count(&postingCount).Error; err != nil {
		t.Fatalf("count postings: %v", err)
	}
	if postingCount != 1 {
		t.Fatalf("posting count for operation = %d, want 1", postingCount)
	}
}

func TestDeadlockAvoidanceAcrossOverlappingPostingOrders(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "lockorder")
	ctx := t.Context()

	secondAccount := domain.AccountId(mustID(t))
	if err := repos.Accounts.Create(ctx, a.householdID, ports.AccountRecord{
		ID:             secondAccount,
		Name:           "Savings",
		Kind:           domain.AccountKindSavings,
		BudgetEligible: true,
		Status:         ports.LifecycleActive,
	}); err != nil {
		t.Fatalf("create second account: %v", err)
	}
	secondEnvelope := domain.EnvelopeId(mustID(t))
	if err := repos.Envelopes.Create(ctx, a.householdID, ports.EnvelopeRecord{
		ID:     secondEnvelope,
		Name:   "Travel",
		Status: ports.LifecycleActive,
	}); err != nil {
		t.Fatalf("create second envelope: %v", err)
	}

	date, err := domain.ParseEffectiveDate("2026-10-28")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}

	const writers = 8
	operations := make([]domain.JournalOperation, writers)
	for i := 0; i < writers; i++ {
		amount := testPositiveAmount(t, 50, a.currency)
		firstAccount, secondAccountID := a.accountID, secondAccount
		firstEnvelope, secondEnvelopeID := a.envelopeID, secondEnvelope
		if i%2 == 1 {
			firstAccount, secondAccountID = secondAccount, a.accountID
			firstEnvelope, secondEnvelopeID = secondEnvelope, a.envelopeID
		}
		effects := domain.Effects{Postings: domain.PostingSet{
			Accounts: []domain.AccountPosting{
				{AccountID: firstAccount, Delta: testSignedDelta(t, -20, a.currency)},
				{AccountID: secondAccountID, Delta: testSignedDelta(t, -30, a.currency)},
			},
			Envelopes: []domain.EnvelopePosting{
				{EnvelopeID: firstEnvelope, Delta: testSignedDelta(t, -20, a.currency)},
				{EnvelopeID: secondEnvelopeID, Delta: testSignedDelta(t, -30, a.currency)},
			},
		}}
		operations[i] = newJournalOperation(
			t, a, domain.JournalOperationId(mustID(t)),
			domain.IdempotencyKey("lockorder-"+mustID(t)), domain.RequestFingerprint("f00e"),
			domain.OpDebitPurchase, date, &amount, effects,
		)
	}

	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range operations {
		wg.Add(1)
		go func(operation domain.JournalOperation) {
			defer wg.Done()
			<-start
			errs <- store.WithinTransaction(ctx, func(ctx context.Context, txRepos ports.Repositories) error {
				return txRepos.Journal.Append(ctx, a.householdID, operation)
			})
		}(operations[i])
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("overlapping-order append failed permanently: %v", err)
		}
	}

	version, err := repos.Journal.Version(ctx, a.householdID)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != writers {
		t.Fatalf("ledger version = %d, want %d", version, writers)
	}
}

func TestConcurrentPlanCompletionPostsExactlyOnce(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "completion")
	ctx := t.Context()

	planID := domain.PlanId(mustID(t))
	target := domain.ReservationTarget{PlanID: planID, EnvelopeID: a.envelopeID, AccountID: a.accountID}
	plan, err := domain.NewPlannedOperation(planID, domain.PlanKindPurchase, target, testPositiveAmount(t, 5_000, a.currency))
	if err != nil {
		t.Fatalf("new plan: %v", err)
	}
	if err := plan.Authorize(domain.AuthorizationOutcome{Mode: domain.AuthorizationModeNormal}, testPositiveAmount(t, 5_000, a.currency), testPositiveAmount(t, 5_000, a.currency)); err != nil {
		t.Fatalf("authorize plan: %v", err)
	}
	if err := repos.Plans.Create(ctx, a.householdID, plan); err != nil {
		t.Fatalf("create reserved plan: %v", err)
	}

	date, err := domain.ParseEffectiveDate("2026-10-28")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}
	completionEffects := func() domain.Effects {
		return domain.Effects{
			Postings: domain.PostingSet{
				Accounts:  []domain.AccountPosting{{AccountID: a.accountID, Delta: testSignedDelta(t, -5_000, a.currency)}},
				Envelopes: []domain.EnvelopePosting{{EnvelopeID: a.envelopeID, Delta: testSignedDelta(t, -5_000, a.currency)}},
			},
			Reservations: []domain.ReservationChange{{
				PlanID:   planID,
				Envelope: domain.EnvelopeHold{EnvelopeID: a.envelopeID, Delta: testSignedDelta(t, -5_000, a.currency)},
				Account:  &domain.AccountHold{AccountID: a.accountID, Delta: testSignedDelta(t, -5_000, a.currency)},
			}},
		}
	}
	completions := make([]domain.JournalOperation, 2)
	for i := range completions {
		amount := testPositiveAmount(t, 5_000, a.currency)
		completions[i] = newJournalOperation(
			t, a, domain.JournalOperationId(mustID(t)),
			domain.IdempotencyKey("complete-"+mustID(t)), domain.RequestFingerprint("f00f"),
			domain.OpPurchaseComplete, date, &amount, completionEffects(),
		)
	}

	householdUUID, err := uuidFromDomain(a.householdID.String())
	if err != nil {
		t.Fatalf("household uuid: %v", err)
	}
	finalAmount := testPositiveAmount(t, 5_000, a.currency)

	type completionResult struct {
		posted bool
		err    error
	}
	start := make(chan struct{})
	results := make(chan completionResult, len(completions))
	var wg sync.WaitGroup
	for i := range completions {
		wg.Add(1)
		go func(operation domain.JournalOperation) {
			defer wg.Done()
			<-start
			posted := false
			err := store.WithinTransaction(ctx, func(ctx context.Context, txRepos ports.Repositories) error {
				tx := txRepos.Journal.(*journalRepository).db
				if err := lockHousehold(ctx, tx, householdUUID); err != nil {
					return err
				}
				if err := lockUUIDRows(ctx, tx, "plans", householdUUID, []string{planID.String()}); err != nil {
					return err
				}
				current, err := txRepos.Plans.Get(ctx, a.householdID, planID)
				if err != nil {
					return err
				}
				if current.State() == domain.PlanStateCompleted {
					return nil // already completed: replay is a successful no-op
				}
				if current.State() != domain.PlanStateReserved {
					return domain.NewErrorf(domain.CodeInvalidEntityState, "cannot complete a plan in state %s", current.State())
				}
				if err := current.Complete(finalAmount, current.HeldAmount(), current.HeldOnAccount()); err != nil {
					return err
				}
				if err := txRepos.Plans.Save(ctx, a.householdID, current, domain.PlanStateReserved); err != nil {
					return err
				}
				if err := txRepos.Journal.Append(ctx, a.householdID, operation); err != nil {
					return err
				}
				posted = true
				return nil
			})
			results <- completionResult{posted: posted, err: err}
		}(completions[i])
	}
	close(start)
	wg.Wait()
	close(results)

	posted := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("plan completion failed: %v", result.err)
		}
		if result.posted {
			posted++
		}
	}
	if posted != 1 {
		t.Fatalf("plan completions posted = %d, want exactly 1", posted)
	}
	completionCount, err := countJournalOperations(ctx, store.DB(), a.householdID, domain.OpPurchaseComplete)
	if err != nil {
		t.Fatalf("count completion operations: %v", err)
	}
	if completionCount != 1 {
		t.Fatalf("completion operation count = %d, want 1", completionCount)
	}
	finalPlan, err := repos.Plans.Get(ctx, a.householdID, planID)
	if err != nil {
		t.Fatalf("get final plan: %v", err)
	}
	if finalPlan.State() != domain.PlanStateCompleted {
		t.Fatalf("final plan state = %s, want COMPLETED", finalPlan.State())
	}
}

func TestRollbackAtomicityUnderConcurrency(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "rollbackrace")
	ctx := t.Context()

	date, err := domain.ParseEffectiveDate("2026-10-28")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}

	const writers = 8
	operations := make([]domain.JournalOperation, writers)
	for i := 0; i < writers; i++ {
		amount := testPositiveAmount(t, int64(100+i), a.currency)
		effects := domain.Effects{Postings: domain.PostingSet{
			Accounts:  []domain.AccountPosting{{AccountID: a.accountID, Delta: testSignedDelta(t, int64(100+i), a.currency)}},
			Envelopes: []domain.EnvelopePosting{{EnvelopeID: a.envelopeID, Delta: testSignedDelta(t, int64(100+i), a.currency)}},
		}}
		operations[i] = newJournalOperation(
			t, a, domain.JournalOperationId(mustID(t)),
			domain.IdempotencyKey("rollback-race-"+mustID(t)), domain.RequestFingerprint("f010"),
			domain.OpIncome, date, &amount, effects,
		)
	}

	start := make(chan struct{})
	results := make(chan struct {
		index int
		err   error
	}, writers)
	var wg sync.WaitGroup
	for i := range operations {
		wg.Add(1)
		go func(index int, operation domain.JournalOperation) {
			defer wg.Done()
			<-start
			err := store.WithinTransaction(ctx, func(ctx context.Context, txRepos ports.Repositories) error {
				if err := txRepos.Journal.Append(ctx, a.householdID, operation); err != nil {
					return err
				}
				if index%2 == 1 {
					return domain.NewError(domain.CodeAccountingInvariantViolation, "intentional rollback")
				}
				return nil
			})
			results <- struct {
				index int
				err   error
			}{index: index, err: err}
		}(i, operations[i])
	}
	close(start)
	wg.Wait()
	close(results)

	wantSuccesses := writers / 2
	successes := 0
	for result := range results {
		if result.index%2 == 1 {
			if !domain.HasCode(result.err, domain.CodeAccountingInvariantViolation) {
				t.Fatalf("failing command %d returned %v, want intentional rollback", result.index, result.err)
			}
			continue
		}
		if result.err != nil {
			t.Fatalf("succeeding command %d failed: %v", result.index, result.err)
		}
		successes++
	}
	if successes != wantSuccesses {
		t.Fatalf("successful commands = %d, want %d", successes, wantSuccesses)
	}

	operationCount, err := countJournalOperations(ctx, store.DB(), a.householdID, domain.OpIncome)
	if err != nil {
		t.Fatalf("count journal operations: %v", err)
	}
	if operationCount != int64(wantSuccesses) {
		t.Fatalf("journal operations = %d, want %d (rolled-back operations must leave no rows)", operationCount, wantSuccesses)
	}
	version, err := repos.Journal.Version(ctx, a.householdID)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != uint64(wantSuccesses) {
		t.Fatalf("ledger version = %d, want %d", version, wantSuccesses)
	}
	var postingCount int64
	if err := store.DB().WithContext(ctx).Model(&accountPostingModel{}).
		Where("household_id = ?", mustUUID(t, a.householdID.String())).
		Count(&postingCount).Error; err != nil {
		t.Fatalf("count postings: %v", err)
	}
	if postingCount != int64(wantSuccesses) {
		t.Fatalf("account postings = %d, want %d", postingCount, wantSuccesses)
	}
}

// envelopeBalanceMinor sums committed envelope postings for one envelope.
func envelopeBalanceMinor(ctx context.Context, db *gorm.DB, householdID, envelopeID pgtype.UUID) (int64, error) {
	var summary struct {
		Total int64
	}
	result := db.WithContext(ctx).Model(&envelopePostingModel{}).
		Select("COALESCE(SUM(delta_minor), 0) AS total").
		Where("household_id = ? AND envelope_id = ?", householdID, envelopeID).
		Scan(&summary)
	if result.Error != nil {
		return 0, toDomainErr(result.Error)
	}
	return summary.Total, nil
}

// mustUUID converts a domain identifier for test-side aggregate queries.
func mustUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	id, err := uuidFromDomain(raw)
	if err != nil {
		t.Fatalf("uuid %q: %v", raw, err)
	}
	return id
}

// countJournalOperations counts committed operations of one type in a
// household.
func countJournalOperations(ctx context.Context, db *gorm.DB, household domain.HouseholdId, opType domain.OperationType) (int64, error) {
	var count int64
	result := db.WithContext(ctx).Model(&journalOperationModel{}).
		Where("household_id = ? AND op_type = ?", mustUUIDNoT(household.String()), opType.String()).
		Count(&count)
	if result.Error != nil {
		return 0, toDomainErr(result.Error)
	}
	return count, nil
}

// countJournalOperationsByKey counts operations bound to one idempotency key.
func countJournalOperationsByKey(ctx context.Context, db *gorm.DB, household domain.HouseholdId, key domain.IdempotencyKey) (int64, error) {
	var count int64
	result := db.WithContext(ctx).Model(&journalOperationModel{}).
		Where("household_id = ? AND idempotency_key = ?", mustUUIDNoT(household.String()), key.String()).
		Count(&count)
	if result.Error != nil {
		return 0, toDomainErr(result.Error)
	}
	return count, nil
}

// countIdempotencyRecords counts records for one household-scoped key.
func countIdempotencyRecords(ctx context.Context, db *gorm.DB, household domain.HouseholdId, key domain.IdempotencyKey) (int64, error) {
	var count int64
	result := db.WithContext(ctx).Model(&idempotencyRecordModel{}).
		Where("household_id = ? AND idempotency_key = ?", mustUUIDNoT(household.String()), key.String()).
		Count(&count)
	if result.Error != nil {
		return 0, toDomainErr(result.Error)
	}
	return count, nil
}

// mustUUIDNoT is the non-testing variant used by query helpers after the test
// has already validated the fixture identifiers.
func mustUUIDNoT(raw string) pgtype.UUID {
	id, err := uuidFromDomain(raw)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}
