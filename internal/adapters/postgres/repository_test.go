package postgres

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

func TestRepositoryCRUDRoundTripAndHouseholdIsolation(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "a")
	b := seedHousehold(t, repos, "b")
	ctx := t.Context()

	// Household metadata.
	household, err := repos.Households.Get(ctx, a.householdID)
	if err != nil {
		t.Fatalf("get household: %v", err)
	}
	if household.ID != a.householdID || household.Currency.Code() != "USD" || household.LedgerVersion != 0 {
		t.Fatalf("unexpected household: %+v", household)
	}
	household.Name = "Renamed " + household.Name
	household.Status = ports.LifecycleArchived
	if err := repos.Households.SaveSettings(ctx, household); err != nil {
		t.Fatalf("save household settings: %v", err)
	}
	reloadedHousehold, err := repos.Households.Get(ctx, a.householdID)
	if err != nil {
		t.Fatalf("reload household: %v", err)
	}
	if reloadedHousehold.Name != household.Name || reloadedHousehold.Status != ports.LifecycleArchived {
		t.Fatalf("household settings did not round-trip: %+v", reloadedHousehold)
	}
	if reloadedHousehold.LedgerVersion != 0 {
		t.Fatalf("SaveSettings must not change ledger_version, got %d", reloadedHousehold.LedgerVersion)
	}

	// Users are global and email lookup is case-insensitive.
	user, err := repos.Users.Get(ctx, a.userID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.ID != a.userID {
		t.Fatalf("unexpected user: %+v", user)
	}
	byEmail, err := repos.Users.FindByEmail(ctx, "OWNER-A@EXAMPLE.TEST")
	if err != nil {
		t.Fatalf("find user by email: %v", err)
	}
	if byEmail.ID != a.userID {
		t.Fatalf("email lookup returned %s, want %s", byEmail.ID, a.userID)
	}

	// Memberships.
	members, err := repos.Memberships.List(ctx, a.householdID)
	if err != nil {
		t.Fatalf("list memberships: %v", err)
	}
	if len(members) != 1 || members[0].UserID != a.userID || members[0].Role != ports.MembershipRoleOwner {
		t.Fatalf("unexpected memberships: %+v", members)
	}
	removedAt := time.Now().UTC().Truncate(time.Microsecond)
	membership := members[0]
	membership.Status = ports.MembershipStatusRemoved
	membership.RemovedAt = &removedAt
	if err := repos.Memberships.Save(ctx, a.householdID, membership); err != nil {
		t.Fatalf("save membership: %v", err)
	}
	gotMembership, err := repos.Memberships.Get(ctx, a.householdID, a.userID)
	if err != nil {
		t.Fatalf("get membership: %v", err)
	}
	if gotMembership.Status != ports.MembershipStatusRemoved || gotMembership.RemovedAt == nil {
		t.Fatalf("membership removal did not round-trip: %+v", gotMembership)
	}

	// Accounts: CRUD, filters and household isolation.
	account, err := repos.Accounts.Get(ctx, a.householdID, a.accountID)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.Kind != domain.AccountKindChecking || !account.BudgetEligible {
		t.Fatalf("unexpected account: %+v", account)
	}
	accounts, err := repos.Accounts.List(ctx, a.householdID, ports.AccountFilter{Kinds: []domain.AccountKind{domain.AccountKindChecking}})
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != a.accountID {
		t.Fatalf("unexpected account list: %+v", accounts)
	}
	archived, err := repos.Accounts.List(ctx, a.householdID, ports.AccountFilter{IncludeArchived: true})
	if err != nil || len(archived) != 1 {
		t.Fatalf("include-archived account list: %v %+v", err, archived)
	}
	account.Name = "Renamed checking"
	account.Status = ports.LifecycleArchived
	if err := repos.Accounts.Save(ctx, a.householdID, account); err != nil {
		t.Fatalf("save account: %v", err)
	}
	activeAccounts, err := repos.Accounts.List(ctx, a.householdID, ports.AccountFilter{})
	if err != nil {
		t.Fatalf("list active accounts: %v", err)
	}
	if len(activeAccounts) != 0 {
		t.Fatalf("archived account still listed as active: %+v", activeAccounts)
	}
	_, err = repos.Accounts.Get(ctx, b.householdID, a.accountID)
	wantCode(t, err, domain.CodeEntityNotFoundOrNotInHousehold)

	// Envelopes: CRUD, filters and household isolation.
	envelope, err := repos.Envelopes.Get(ctx, a.householdID, a.envelopeID)
	if err != nil {
		t.Fatalf("get envelope: %v", err)
	}
	envelope.Name = "Renamed groceries"
	if err := repos.Envelopes.Save(ctx, a.householdID, envelope); err != nil {
		t.Fatalf("save envelope: %v", err)
	}
	envelopes, err := repos.Envelopes.List(ctx, a.householdID, ports.EnvelopeFilter{})
	if err != nil || len(envelopes) != 1 {
		t.Fatalf("list envelopes: %v %+v", err, envelopes)
	}
	if envelopes[0].Name != "Renamed groceries" {
		t.Fatalf("envelope rename did not round-trip: %+v", envelopes[0])
	}
	_, err = repos.Envelopes.Get(ctx, b.householdID, a.envelopeID)
	wantCode(t, err, domain.CodeEntityNotFoundOrNotInHousehold)

	// Plans: create, get, expected-state guard, household isolation.
	planID := domain.PlanId(mustID(t))
	target := domain.ReservationTarget{PlanID: planID, EnvelopeID: a.envelopeID, AccountID: a.accountID}
	plan, err := domain.NewPlannedOperation(planID, domain.PlanKindPurchase, target, testPositiveAmount(t, 5_000, a.currency))
	if err != nil {
		t.Fatalf("new plan: %v", err)
	}
	if err := repos.Plans.Create(ctx, a.householdID, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	gotPlan, err := repos.Plans.Get(ctx, a.householdID, planID)
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if gotPlan.ID() != planID || gotPlan.State() != domain.PlanStateDraft || gotPlan.IntendedAmount().MinorUnits() != 5_000 {
		t.Fatalf("unexpected plan: %+v", gotPlan)
	}
	if err := repos.Plans.Save(ctx, a.householdID, gotPlan, domain.PlanStateDraft); err != nil {
		t.Fatalf("save draft plan with expected DRAFT: %v", err)
	}
	err = repos.Plans.Save(ctx, a.householdID, gotPlan, domain.PlanStateReserved)
	wantCode(t, err, domain.CodeConcurrencyConflict)
	_, err = repos.Plans.Get(ctx, b.householdID, planID)
	wantCode(t, err, domain.CodeEntityNotFoundOrNotInHousehold)

	// Suggestions: create, get, expected-state guard, household isolation.
	suggestionID := domain.SuggestionId(mustID(t))
	suggestion, err := domain.NewSuggestion(suggestionID, domain.TransactionSourceManual, testPositiveAmount(t, 1_200, a.currency), "Corner Store")
	if err != nil {
		t.Fatalf("new suggestion: %v", err)
	}
	if err := repos.Suggestions.Create(ctx, a.householdID, suggestion); err != nil {
		t.Fatalf("create suggestion: %v", err)
	}
	gotSuggestion, err := repos.Suggestions.Get(ctx, a.householdID, suggestionID)
	if err != nil {
		t.Fatalf("get suggestion: %v", err)
	}
	if gotSuggestion.Merchant() != "Corner Store" || gotSuggestion.State() != domain.SuggestionStateSuggested {
		t.Fatalf("unexpected suggestion: %+v", gotSuggestion)
	}
	if err := repos.Suggestions.Save(ctx, a.householdID, gotSuggestion, domain.SuggestionStateSuggested); err != nil {
		t.Fatalf("save suggestion with expected SUGGESTED: %v", err)
	}
	err = repos.Suggestions.Save(ctx, a.householdID, gotSuggestion, domain.SuggestionStateConfirmed)
	wantCode(t, err, domain.CodeConcurrencyConflict)
	_, err = repos.Suggestions.Get(ctx, b.householdID, suggestionID)
	wantCode(t, err, domain.CodeEntityNotFoundOrNotInHousehold)
}

func TestJournalAppendRoundTripPreservesEffects(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "journal")
	b := seedHousehold(t, repos, "journal-b")
	ctx := t.Context()

	// A plan is needed because reservation_changes has a plan foreign key.
	planID := domain.PlanId(mustID(t))
	target := domain.ReservationTarget{PlanID: planID, EnvelopeID: a.envelopeID, AccountID: a.accountID}
	plan, err := domain.NewPlannedOperation(planID, domain.PlanKindPurchase, target, testPositiveAmount(t, 10_000, a.currency))
	if err != nil {
		t.Fatalf("new plan: %v", err)
	}
	if err := repos.Plans.Create(ctx, a.householdID, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	date, err := domain.ParseEffectiveDate("2026-10-28")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}

	originalID := domain.JournalOperationId(mustID(t))
	originalEffects := domain.Effects{
		Postings: domain.PostingSet{
			Accounts: []domain.AccountPosting{{
				AccountID: a.accountID,
				Delta:     testSignedDelta(t, 10_000, a.currency),
			}},
			Envelopes: []domain.EnvelopePosting{{
				EnvelopeID: a.envelopeID,
				Delta:      testSignedDelta(t, 10_000, a.currency),
			}},
		},
	}
	amount := testPositiveAmount(t, 10_000, a.currency)
	original := newJournalOperation(
		t, a, originalID,
		domain.IdempotencyKey("journal-key-1"),
		domain.RequestFingerprint("aa11"),
		domain.OpIncome, date, &amount, originalEffects,
	)
	if err := repos.Journal.Append(ctx, a.householdID, original); err != nil {
		t.Fatalf("append original operation: %v", err)
	}

	reversalID := domain.JournalOperationId(mustID(t))
	reversalEffects := domain.Effects{
		Postings: domain.PostingSet{
			Accounts: []domain.AccountPosting{
				{AccountID: a.accountID, Delta: testSignedDelta(t, -4_000, a.currency)},
				{AccountID: a.accountID, Delta: testSignedDelta(t, -1_000, a.currency)},
			},
			Envelopes: []domain.EnvelopePosting{
				{EnvelopeID: a.envelopeID, Delta: testSignedDelta(t, -5_000, a.currency)},
			},
		},
		Reservations: []domain.ReservationChange{{
			PlanID:   planID,
			Envelope: domain.EnvelopeHold{EnvelopeID: a.envelopeID, Delta: testSignedDelta(t, 2_000, a.currency)},
			Account:  &domain.AccountHold{AccountID: a.accountID, Delta: testSignedDelta(t, 2_000, a.currency)},
		}},
		Related: []domain.RelatedOperationRef{},
	}
	related, err := domain.NewRelatedOperationRef(domain.RelatedRefOriginal, originalID)
	if err != nil {
		t.Fatalf("related ref: %v", err)
	}
	reversalEffects.Related = []domain.RelatedOperationRef{related}

	reversalAmount := testPositiveAmount(t, 5_000, a.currency)
	reversal := newJournalOperation(
		t, a, reversalID,
		domain.IdempotencyKey("journal-key-2"),
		domain.RequestFingerprint("bb22"),
		domain.OpReversal, date, &reversalAmount, reversalEffects,
	)
	if err := repos.Journal.Append(ctx, a.householdID, reversal); err != nil {
		t.Fatalf("append reversal: %v", err)
	}

	got, err := repos.Journal.Get(ctx, a.householdID, reversalID)
	if err != nil {
		t.Fatalf("get reversal: %v", err)
	}
	assertJournalOperationEqual(t, reversal, got)

	version, err := repos.Journal.Version(ctx, a.householdID)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != 2 {
		t.Fatalf("ledger version = %d, want 2", version)
	}

	// Cross-household access is indistinguishable from absence.
	_, err = repos.Journal.Get(ctx, b.householdID, reversalID)
	wantCode(t, err, domain.CodeEntityNotFoundOrNotInHousehold)

	// List honors type filtering and ordering.
	listed, err := repos.Journal.List(ctx, a.householdID, ports.JournalFilter{Types: []domain.OperationType{domain.OpReversal}})
	if err != nil {
		t.Fatalf("list journal: %v", err)
	}
	if len(listed) != 1 || listed[0].ID() != reversalID {
		t.Fatalf("unexpected filtered journal: %+v", listed)
	}
	all, err := repos.Journal.List(ctx, a.householdID, ports.JournalFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("list all journal: %v %+v", err, all)
	}
	if all[0].ID() != originalID || all[1].ID() != reversalID {
		t.Fatalf("journal ordering is not committed_at/id ascending: %s, %s", all[0].ID(), all[1].ID())
	}

	// Reservation holds are derived from the appended reservation change.
	envelopeHold, err := repos.Reservations.ActiveEnvelopeHolds(ctx, a.householdID, a.envelopeID)
	if err != nil {
		t.Fatalf("active envelope holds: %v", err)
	}
	if envelopeHold.MinorUnits() != 2_000 {
		t.Fatalf("envelope hold = %d, want 2000", envelopeHold.MinorUnits())
	}
	accountHold, err := repos.Reservations.ActiveAccountHolds(ctx, a.householdID, a.accountID)
	if err != nil {
		t.Fatalf("active account holds: %v", err)
	}
	if accountHold.MinorUnits() != 2_000 {
		t.Fatalf("account hold = %d, want 2000", accountHold.MinorUnits())
	}
	planEnvelopeHold, planAccountHold, err := repos.Reservations.HoldsForPlan(ctx, a.householdID, planID)
	if err != nil {
		t.Fatalf("holds for plan: %v", err)
	}
	if planEnvelopeHold.MinorUnits() != 2_000 || planAccountHold.MinorUnits() != 2_000 {
		t.Fatalf("plan holds = %d/%d, want 2000/2000", planEnvelopeHold.MinorUnits(), planAccountHold.MinorUnits())
	}
	active, err := repos.Accounts.HasActiveReservations(ctx, a.householdID, a.accountID)
	if err != nil || !active {
		t.Fatalf("account has active reservations = %v, err %v", active, err)
	}
	active, err = repos.Envelopes.HasActiveReservations(ctx, a.householdID, a.envelopeID)
	if err != nil || !active {
		t.Fatalf("envelope has active reservations = %v, err %v", active, err)
	}
}

func TestIdempotencyBeginReplayAndMismatch(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "idem")
	ctx := t.Context()

	date, err := domain.ParseEffectiveDate("2026-10-28")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}
	amount := testPositiveAmount(t, 1_500, a.currency)
	opID := domain.JournalOperationId(mustID(t))
	key := domain.IdempotencyKey("idem-key-1")
	fingerprint := domain.RequestFingerprint("cafe01")
	effects := domain.Effects{Postings: domain.PostingSet{Accounts: []domain.AccountPosting{{
		AccountID: a.accountID,
		Delta:     testSignedDelta(t, 1_500, a.currency),
	}}}}
	operation := newJournalOperation(t, a, opID, key, fingerprint, domain.OpIncome, date, &amount, effects)
	if err := repos.Journal.Append(ctx, a.householdID, operation); err != nil {
		t.Fatalf("append operation: %v", err)
	}

	now := journalTime()
	record, isNew, err := repos.Idempotency.Begin(ctx, a.householdID, key, fingerprint, opID, now)
	if err != nil {
		t.Fatalf("first begin: %v", err)
	}
	if !isNew || record.OperationID() != opID || record.Fingerprint() != fingerprint {
		t.Fatalf("unexpected first begin: isNew=%v record=%+v", isNew, record)
	}

	replayed, isNew, err := repos.Idempotency.Begin(ctx, a.householdID, key, fingerprint, opID, now.Add(time.Second))
	if err != nil {
		t.Fatalf("replay begin: %v", err)
	}
	if isNew || replayed.OperationID() != opID {
		t.Fatalf("unexpected replay: isNew=%v record=%+v", isNew, replayed)
	}

	_, _, err = repos.Idempotency.Begin(ctx, a.householdID, key, domain.RequestFingerprint("dead01"), opID, now)
	wantCode(t, err, domain.CodeIdempotencyKeyPayloadMismatch)

	lookedUp, err := repos.Idempotency.Lookup(ctx, a.householdID, key)
	if err != nil || lookedUp.OperationID() != opID {
		t.Fatalf("lookup: %v %+v", err, lookedUp)
	}
	_, err = repos.Idempotency.Lookup(ctx, a.householdID, domain.IdempotencyKey("missing"))
	wantCode(t, err, domain.CodeEntityNotFoundOrNotInHousehold)

	// Begin-before-Append inside one transaction is persisted by Append.
	secondOpID := domain.JournalOperationId(mustID(t))
	secondKey := domain.IdempotencyKey("idem-key-2")
	secondFingerprint := domain.RequestFingerprint("cafe02")
	secondEffects := domain.Effects{Postings: domain.PostingSet{Accounts: []domain.AccountPosting{{
		AccountID: a.accountID,
		Delta:     testSignedDelta(t, 700, a.currency),
	}}}}
	secondAmount := testPositiveAmount(t, 700, a.currency)
	second := newJournalOperation(t, a, secondOpID, secondKey, secondFingerprint, domain.OpIncome, date, &secondAmount, secondEffects)
	err = store.WithinTransaction(ctx, func(ctx context.Context, txRepos ports.Repositories) error {
		_, isNew, err := txRepos.Idempotency.Begin(ctx, a.householdID, secondKey, secondFingerprint, secondOpID, now)
		if err != nil {
			return err
		}
		if !isNew {
			t.Fatalf("Begin-before-Append should be new")
		}
		return txRepos.Journal.Append(ctx, a.householdID, second)
	})
	if err != nil {
		t.Fatalf("transactional begin+append: %v", err)
	}
	persisted, err := repos.Idempotency.Lookup(ctx, a.householdID, secondKey)
	if err != nil || persisted.OperationID() != secondOpID {
		t.Fatalf("pending idempotency record was not persisted: %v %+v", err, persisted)
	}
	replayed, isNew, err = repos.Idempotency.Begin(ctx, a.householdID, secondKey, secondFingerprint, secondOpID, now)
	if err != nil || isNew {
		t.Fatalf("expected replay after Begin-before-Append: isNew=%v err=%v", isNew, err)
	}
}

func TestWithinTransactionRollbackLeavesNoPartialRows(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "rollback")
	ctx := t.Context()

	newAccountID := domain.AccountId(mustID(t))
	date, err := domain.ParseEffectiveDate("2026-10-28")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}
	opID := domain.JournalOperationId(mustID(t))
	amount := testPositiveAmount(t, 100, a.currency)
	effects := domain.Effects{Postings: domain.PostingSet{Accounts: []domain.AccountPosting{{
		AccountID: a.accountID,
		Delta:     testSignedDelta(t, 100, a.currency),
	}}}}
	operation := newJournalOperation(t, a, opID, domain.IdempotencyKey("rollback-key"), domain.RequestFingerprint("ab01"), domain.OpIncome, date, &amount, effects)

	sentinel := domain.NewError(domain.CodeAccountingInvariantViolation, "intentional rollback")
	err = store.WithinTransaction(ctx, func(ctx context.Context, txRepos ports.Repositories) error {
		if err := txRepos.Accounts.Create(ctx, a.householdID, ports.AccountRecord{
			ID:             newAccountID,
			Name:           "Rolled back",
			Kind:           domain.AccountKindSavings,
			BudgetEligible: true,
			Status:         ports.LifecycleActive,
		}); err != nil {
			return err
		}
		if err := txRepos.Journal.Append(ctx, a.householdID, operation); err != nil {
			return err
		}
		return sentinel
	})
	if !domain.HasCode(err, domain.CodeAccountingInvariantViolation) {
		t.Fatalf("expected sentinel rollback error, got %v", err)
	}

	_, err = repos.Accounts.Get(ctx, a.householdID, newAccountID)
	wantCode(t, err, domain.CodeEntityNotFoundOrNotInHousehold)
	_, err = repos.Journal.Get(ctx, a.householdID, opID)
	wantCode(t, err, domain.CodeEntityNotFoundOrNotInHousehold)
	version, err := repos.Journal.Version(ctx, a.householdID)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != 0 {
		t.Fatalf("ledger version after rollback = %d, want 0", version)
	}
}

func TestConcurrentAppendsSerializeOnHouseholdLock(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "locking")
	ctx := t.Context()

	const writers = 4
	date, err := domain.ParseEffectiveDate("2026-10-28")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}

	operations := make([]domain.JournalOperation, writers)
	for i := 0; i < writers; i++ {
		opID := domain.JournalOperationId(mustID(t))
		amount := testPositiveAmount(t, int64(100+i), a.currency)
		effects := domain.Effects{Postings: domain.PostingSet{Accounts: []domain.AccountPosting{{
			AccountID: a.accountID,
			Delta:     testSignedDelta(t, int64(100+i), a.currency),
		}}}}
		operations[i] = newJournalOperation(
			t, a, opID,
			domain.IdempotencyKey("lock-key-"+opID.String()),
			domain.RequestFingerprint("ef01"),
			domain.OpIncome, date, &amount, effects,
		)
	}

	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
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
			t.Fatalf("concurrent append failed: %v", err)
		}
	}

	version, err := repos.Journal.Version(ctx, a.householdID)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != writers {
		t.Fatalf("ledger version = %d, want %d", version, writers)
	}
	listed, err := repos.Journal.List(ctx, a.householdID, ports.JournalFilter{Limit: maxJournalPageSize})
	if err != nil {
		t.Fatalf("list journal: %v", err)
	}
	if len(listed) != writers {
		t.Fatalf("journal row count = %d, want %d", len(listed), writers)
	}
}

// assertJournalOperationEqual compares the domain-visible surface plus the full
// effect set (including posting order) of two operations.
func assertJournalOperationEqual(t *testing.T, want, got domain.JournalOperation) {
	t.Helper()

	if got.ID() != want.ID() ||
		got.Household() != want.Household() ||
		got.Actor() != want.Actor() ||
		got.Type() != want.Type() ||
		got.Source() != want.Source() ||
		got.EffectiveDate() != want.EffectiveDate() ||
		got.Currency().Code() != want.Currency().Code() ||
		got.Currency().Exponent() != want.Currency().Exponent() ||
		got.IdempotencyKey() != want.IdempotencyKey() ||
		got.RequestFingerprint() != want.RequestFingerprint() ||
		got.Reason() != want.Reason() {
		t.Fatalf("journal metadata mismatch:\nwant %+v\ngot  %+v", want, got)
	}
	if !got.CreatedAt().Time().Equal(want.CreatedAt().Time()) {
		t.Fatalf("created_at mismatch: want %s got %s", want.CreatedAt(), got.CreatedAt())
	}
	if !got.CommittedAt().Time().Equal(want.CommittedAt().Time()) {
		t.Fatalf("committed_at mismatch: want %s got %s", want.CommittedAt(), got.CommittedAt())
	}
	wantAmount, wantHas := want.Amount()
	gotAmount, gotHas := got.Amount()
	if wantHas != gotHas || (wantHas && wantAmount.MinorUnits() != gotAmount.MinorUnits()) {
		t.Fatalf("amount mismatch: want %v/%v got %v/%v", wantAmount, wantHas, gotAmount, gotHas)
	}
	if !reflect.DeepEqual(want.Effects(), got.Effects()) {
		t.Fatalf("effects mismatch:\nwant %#v\ngot  %#v", want.Effects(), got.Effects())
	}
}
