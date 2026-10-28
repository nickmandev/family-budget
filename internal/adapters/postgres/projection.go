package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// This file implements the projection-rebuild verification required by
// STACK_DECISIONS §8 and POSTING_MATRIX §6/§7 test 12: every materialized or
// derived balance must equal the balance replayed from the append-only journal.
//
// The adapter does not store account or envelope balances. Posting rows are the
// projection, and this file derives them two independent ways:
//
//   - RebuildLedgerFromJournal loads the household's journal operations through
//     the port and replays them with the domain's reference implementation
//     (domain.Journal.RebuildLedger), which applies postings and reservation
//     holds in commit order and checks the accounting invariants after every
//     operation.
//   - the live side of VerifyProjections aggregates the same posting and
//     reservation rows directly in SQL and reads the materialized
//     households.ledger_version through the port.
//
// VerifyProjections compares every derived balance (posted account balances,
// budgetable cash, envelope balances and their sum, Ready-to-assign, spendable
// balances, card liability, Qe/Qa) and treats any mismatch as
// ACCOUNTING_INVARIANT_VIOLATION, never as a user reconciliation event
// (POSTING_MATRIX §6).

// projectionShape is the static household shape a ledger replay needs: the
// currency, account metadata and envelope ids. The records keep the lifecycle
// status so archived rows are included exactly like the domain model does.
type projectionShape struct {
	household       domain.HouseholdId
	currency        domain.Currency
	accountRecords  []ports.AccountRecord
	accounts        []domain.AccountMeta
	envelopeRecords []ports.EnvelopeRecord
	envelopes       []domain.EnvelopeId
}

// RebuildLedgerFromJournal loads the household's journal through the
// JournalRepository port in deterministic (committed_at, id) order and replays
// it into a fresh domain.LedgerState exactly like domain.Journal.RebuildLedger:
// postings first, then reservation changes, then CheckInvariants after every
// operation.
//
// An empty journal produces an empty ledger at zero balances rather than the
// domain journal's INVALID_VALUE, because persistence verification must be
// able to check a household that has not committed anything yet. An unknown
// household, malformed row or invariant failure surfaces unchanged.
func (s *Store) RebuildLedgerFromJournal(ctx context.Context, household domain.HouseholdId) (*domain.LedgerState, error) {
	shape, err := s.loadProjectionShape(ctx, household)
	if err != nil {
		return nil, err
	}
	operations, err := s.loadAllJournalOperations(ctx, household)
	if err != nil {
		return nil, err
	}
	return rebuildLedgerFrom(shape, operations)
}

// VerifyProjections rebuilds the ledger from the journal and compares it with
// every balance the adapter derives from persisted rows. It returns
// ACCOUNTING_INVARIANT_VIOLATION on the first mismatch; a mismatch means the
// projection and its journal disagree, which is an internal consistency error
// (POSTING_MATRIX §6), not a reconciliation input.
//
// The comparison covers:
//
//   - households.ledger_version against the number of committed operations;
//   - PostedAccountBalance and ActiveAccountReservations per account;
//   - SpendableAccountBalance per account;
//   - CardLiabilityBalance per card-liability account;
//   - EnvelopeBalance and ActiveEnvelopeReservations per envelope;
//   - SpendableEnvelopeBalance per envelope;
//   - BudgetableCash, SumEnvelopeBalances and ReadyToAssign;
//   - the ReservationRepository's ActiveEnvelopeHolds and
//     ActiveAccountHolds derived-query methods.
func (s *Store) VerifyProjections(ctx context.Context, household domain.HouseholdId) error {
	shape, err := s.loadProjectionShape(ctx, household)
	if err != nil {
		return err
	}
	operations, err := s.loadAllJournalOperations(ctx, household)
	if err != nil {
		return err
	}
	state, err := rebuildLedgerFrom(shape, operations)
	if err != nil {
		return err
	}
	live, err := s.loadLiveProjection(ctx, household, shape.currency)
	if err != nil {
		return err
	}

	repos := s.Repositories()
	version, err := repos.Journal.Version(ctx, household)
	if err != nil {
		return err
	}
	if version != uint64(len(operations)) {
		return projectionMismatch(
			"household %q ledger version is %d but the journal holds %d committed operations",
			household, version, len(operations),
		)
	}

	for _, record := range shape.accountRecords {
		wantPosted, err := state.PostedAccountBalance(record.ID)
		if err != nil {
			return err
		}
		gotPosted, err := live.postedAccount(record.ID)
		if err != nil {
			return err
		}
		if !sameMoney(wantPosted, gotPosted) {
			return projectionMismatch(
				"account %q posted balance is %d minor units but the journal rebuild gives %d",
				record.ID, gotPosted.MinorUnits(), wantPosted.MinorUnits(),
			)
		}

		wantReserved, err := state.ActiveAccountReservations(record.ID)
		if err != nil {
			return err
		}
		gotReserved, err := live.reservedAccount(record.ID)
		if err != nil {
			return err
		}
		if !sameMoney(wantReserved, gotReserved) {
			return projectionMismatch(
				"account %q active reservation is %d minor units but the journal rebuild gives %d",
				record.ID, gotReserved.MinorUnits(), wantReserved.MinorUnits(),
			)
		}

		queriedReserved, err := repos.Reservations.ActiveAccountHolds(ctx, household, record.ID)
		if err != nil {
			return err
		}
		if !sameMoney(wantReserved, queriedReserved) {
			return projectionMismatch(
				"account %q reservation query returns %d minor units but the journal rebuild gives %d",
				record.ID, queriedReserved.MinorUnits(), wantReserved.MinorUnits(),
			)
		}

		wantSpendable, err := state.SpendableAccountBalance(record.ID)
		if err != nil {
			return err
		}
		gotSpendable, err := gotPosted.Sub(gotReserved)
		if err != nil {
			return err
		}
		if !sameMoney(wantSpendable, gotSpendable) {
			return projectionMismatch(
				"account %q spendable balance is %d minor units but the journal rebuild gives %d",
				record.ID, gotSpendable.MinorUnits(), wantSpendable.MinorUnits(),
			)
		}

		if record.Kind == domain.AccountKindCardLiability {
			wantLiability, err := state.CardLiabilityBalance(record.ID)
			if err != nil {
				return err
			}
			if !sameMoney(wantLiability, gotPosted) {
				return projectionMismatch(
					"card liability %q is %d minor units but the journal rebuild gives %d",
					record.ID, gotPosted.MinorUnits(), wantLiability.MinorUnits(),
				)
			}
		}
	}

	for _, id := range shape.envelopes {
		wantBalance, err := state.EnvelopeBalance(id)
		if err != nil {
			return err
		}
		gotBalance, err := live.envelopeBalance(id)
		if err != nil {
			return err
		}
		if !sameMoney(wantBalance, gotBalance) {
			return projectionMismatch(
				"envelope %q balance is %d minor units but the journal rebuild gives %d",
				id, gotBalance.MinorUnits(), wantBalance.MinorUnits(),
			)
		}

		wantReserved, err := state.ActiveEnvelopeReservations(id)
		if err != nil {
			return err
		}
		gotReserved, err := live.reservedEnvelope(id)
		if err != nil {
			return err
		}
		if !sameMoney(wantReserved, gotReserved) {
			return projectionMismatch(
				"envelope %q active reservation is %d minor units but the journal rebuild gives %d",
				id, gotReserved.MinorUnits(), wantReserved.MinorUnits(),
			)
		}

		queriedReserved, err := repos.Reservations.ActiveEnvelopeHolds(ctx, household, id)
		if err != nil {
			return err
		}
		if !sameMoney(wantReserved, queriedReserved) {
			return projectionMismatch(
				"envelope %q reservation query returns %d minor units but the journal rebuild gives %d",
				id, queriedReserved.MinorUnits(), wantReserved.MinorUnits(),
			)
		}

		wantSpendable, err := state.SpendableEnvelopeBalance(id)
		if err != nil {
			return err
		}
		gotSpendable, err := gotBalance.Sub(gotReserved)
		if err != nil {
			return err
		}
		if !sameMoney(wantSpendable, gotSpendable) {
			return projectionMismatch(
				"envelope %q spendable balance is %d minor units but the journal rebuild gives %d",
				id, gotSpendable.MinorUnits(), wantSpendable.MinorUnits(),
			)
		}
	}

	wantBudgetable, err := state.BudgetableCash()
	if err != nil {
		return err
	}
	gotBudgetable, err := live.budgetableCash(shape)
	if err != nil {
		return err
	}
	if !sameMoney(wantBudgetable, gotBudgetable) {
		return projectionMismatch(
			"budgetable cash is %d minor units but the journal rebuild gives %d",
			gotBudgetable.MinorUnits(), wantBudgetable.MinorUnits(),
		)
	}

	wantEnvelopeSum, err := state.SumEnvelopeBalances()
	if err != nil {
		return err
	}
	gotEnvelopeSum, err := live.sumEnvelopeBalances(shape)
	if err != nil {
		return err
	}
	if !sameMoney(wantEnvelopeSum, gotEnvelopeSum) {
		return projectionMismatch(
			"envelope balance sum is %d minor units but the journal rebuild gives %d",
			gotEnvelopeSum.MinorUnits(), wantEnvelopeSum.MinorUnits(),
		)
	}

	wantReady, err := state.ReadyToAssign()
	if err != nil {
		return err
	}
	gotReady, err := gotBudgetable.Sub(gotEnvelopeSum)
	if err != nil {
		return err
	}
	if !sameMoney(wantReady, gotReady) {
		return projectionMismatch(
			"ready-to-assign is %d minor units but the journal rebuild gives %d",
			gotReady.MinorUnits(), wantReady.MinorUnits(),
		)
	}

	return nil
}

// loadProjectionShape reads the household currency, account metadata and
// envelope ids, including archived rows.
func (s *Store) loadProjectionShape(ctx context.Context, household domain.HouseholdId) (projectionShape, error) {
	repos := s.Repositories()
	record, err := repos.Households.Get(ctx, household)
	if err != nil {
		return projectionShape{}, err
	}
	accountRecords, err := repos.Accounts.List(ctx, household, ports.AccountFilter{IncludeArchived: true})
	if err != nil {
		return projectionShape{}, err
	}
	accounts := make([]domain.AccountMeta, 0, len(accountRecords))
	for _, account := range accountRecords {
		meta, err := domain.NewAccountMeta(account.ID, account.Kind, account.BudgetEligible)
		if err != nil {
			return projectionShape{}, err
		}
		accounts = append(accounts, meta)
	}
	envelopeRecords, err := repos.Envelopes.List(ctx, household, ports.EnvelopeFilter{IncludeArchived: true})
	if err != nil {
		return projectionShape{}, err
	}
	envelopes := make([]domain.EnvelopeId, 0, len(envelopeRecords))
	for _, envelope := range envelopeRecords {
		envelopes = append(envelopes, envelope.ID)
	}
	return projectionShape{
		household:       household,
		currency:        record.Currency,
		accountRecords:  accountRecords,
		accounts:        accounts,
		envelopeRecords: envelopeRecords,
		envelopes:       envelopes,
	}, nil
}

// loadAllJournalOperations pages through the port's deterministic ordering
// until it has every committed operation for the household.
func (s *Store) loadAllJournalOperations(ctx context.Context, household domain.HouseholdId) ([]domain.JournalOperation, error) {
	repos := s.Repositories()
	operations := make([]domain.JournalOperation, 0)
	var cursor *ports.JournalCursor
	for {
		page, err := repos.Journal.List(ctx, household, ports.JournalFilter{
			Limit:  maxJournalPageSize,
			Cursor: cursor,
		})
		if err != nil {
			return nil, err
		}
		operations = append(operations, page...)
		if len(page) < maxJournalPageSize {
			return operations, nil
		}
		last := page[len(page)-1]
		cursor = &ports.JournalCursor{CommittedAt: last.CommittedAt().Time(), ID: last.ID()}
	}
}

// rebuildLedgerFrom replays operations into a fresh ledger using the domain's
// reference journal implementation.
func rebuildLedgerFrom(shape projectionShape, operations []domain.JournalOperation) (*domain.LedgerState, error) {
	if len(operations) == 0 {
		return domain.NewLedgerState(shape.currency, shape.accounts, shape.envelopes)
	}
	journal := domain.NewJournal(shape.household)
	for _, operation := range operations {
		if err := journal.Append(operation); err != nil {
			return nil, err
		}
	}
	return journal.RebuildLedger(shape.accounts, shape.envelopes)
}

// liveProjection is the adapter's independently derived view of the same rows,
// aggregated in SQL instead of replayed through the domain journal.
type liveProjection struct {
	currency         domain.Currency
	accountPosted    map[domain.AccountId]int64
	accountReserved  map[domain.AccountId]int64
	envelopeBalances map[domain.EnvelopeId]int64
	envelopeReserved map[domain.EnvelopeId]int64
}

// loadLiveProjection aggregates posting and reservation rows per account and
// envelope for one household.
func (s *Store) loadLiveProjection(ctx context.Context, household domain.HouseholdId, currency domain.Currency) (liveProjection, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return liveProjection{}, err
	}
	db := s.db.WithContext(ctx)
	projection := liveProjection{
		currency:         currency,
		accountPosted:    make(map[domain.AccountId]int64),
		accountReserved:  make(map[domain.AccountId]int64),
		envelopeBalances: make(map[domain.EnvelopeId]int64),
		envelopeReserved: make(map[domain.EnvelopeId]int64),
	}

	type accountTotal struct {
		AccountID pgtype.UUID `gorm:"column:account_id"`
		Total     int64       `gorm:"column:total"`
	}
	var accountPostings []accountTotal
	result := db.Model(&accountPostingModel{}).
		Select("account_id, COALESCE(SUM(delta_minor), 0) AS total").
		Where("household_id = ?", householdID).
		Group("account_id").
		Scan(&accountPostings)
	if result.Error != nil {
		return liveProjection{}, toDomainErr(result.Error)
	}
	for _, row := range accountPostings {
		projection.accountPosted[domain.AccountId(uuidString(row.AccountID))] = row.Total
	}

	var accountReservations []accountTotal
	result = db.Model(&reservationChangeModel{}).
		Select("account_id, COALESCE(SUM(account_delta_minor), 0) AS total").
		Where("household_id = ? AND account_id IS NOT NULL", householdID).
		Group("account_id").
		Scan(&accountReservations)
	if result.Error != nil {
		return liveProjection{}, toDomainErr(result.Error)
	}
	for _, row := range accountReservations {
		projection.accountReserved[domain.AccountId(uuidString(row.AccountID))] = row.Total
	}

	type envelopeTotal struct {
		EnvelopeID pgtype.UUID `gorm:"column:envelope_id"`
		Total      int64       `gorm:"column:total"`
	}
	var envelopePostings []envelopeTotal
	result = db.Model(&envelopePostingModel{}).
		Select("envelope_id, COALESCE(SUM(delta_minor), 0) AS total").
		Where("household_id = ?", householdID).
		Group("envelope_id").
		Scan(&envelopePostings)
	if result.Error != nil {
		return liveProjection{}, toDomainErr(result.Error)
	}
	for _, row := range envelopePostings {
		projection.envelopeBalances[domain.EnvelopeId(uuidString(row.EnvelopeID))] = row.Total
	}

	var envelopeReservations []envelopeTotal
	result = db.Model(&reservationChangeModel{}).
		Select("envelope_id, COALESCE(SUM(envelope_delta_minor), 0) AS total").
		Where("household_id = ?", householdID).
		Group("envelope_id").
		Scan(&envelopeReservations)
	if result.Error != nil {
		return liveProjection{}, toDomainErr(result.Error)
	}
	for _, row := range envelopeReservations {
		projection.envelopeReserved[domain.EnvelopeId(uuidString(row.EnvelopeID))] = row.Total
	}

	return projection, nil
}

func (p liveProjection) postedAccount(id domain.AccountId) (domain.Money, error) {
	return domain.NewMoney(p.accountPosted[id], p.currency)
}

func (p liveProjection) reservedAccount(id domain.AccountId) (domain.Money, error) {
	return domain.NewMoney(p.accountReserved[id], p.currency)
}

func (p liveProjection) envelopeBalance(id domain.EnvelopeId) (domain.Money, error) {
	return domain.NewMoney(p.envelopeBalances[id], p.currency)
}

func (p liveProjection) reservedEnvelope(id domain.EnvelopeId) (domain.Money, error) {
	return domain.NewMoney(p.envelopeReserved[id], p.currency)
}

func (p liveProjection) budgetableCash(shape projectionShape) (domain.Money, error) {
	total, err := domain.ZeroMoney(p.currency)
	if err != nil {
		return domain.Money{}, err
	}
	for _, meta := range shape.accounts {
		if !meta.BudgetEligible || !meta.Kind.IsLiquidCash() {
			continue
		}
		posted, err := p.postedAccount(meta.ID)
		if err != nil {
			return domain.Money{}, err
		}
		total, err = total.Add(posted)
		if err != nil {
			return domain.Money{}, err
		}
	}
	return total, nil
}

func (p liveProjection) sumEnvelopeBalances(shape projectionShape) (domain.Money, error) {
	total, err := domain.ZeroMoney(p.currency)
	if err != nil {
		return domain.Money{}, err
	}
	for _, id := range shape.envelopes {
		balance, err := p.envelopeBalance(id)
		if err != nil {
			return domain.Money{}, err
		}
		total, err = total.Add(balance)
		if err != nil {
			return domain.Money{}, err
		}
	}
	return total, nil
}

// sameMoney compares two valid Money values by currency and minor units.
func sameMoney(a, b domain.Money) bool {
	return a.Currency().Equal(b.Currency()) && a.MinorUnits() == b.MinorUnits()
}

// projectionMismatch builds the internal consistency error POSTING_MATRIX §6
// requires.
func projectionMismatch(format string, args ...any) error {
	return domain.NewErrorf(domain.CodeAccountingInvariantViolation, format, args...)
}
