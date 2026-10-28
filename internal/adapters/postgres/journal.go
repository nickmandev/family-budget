package postgres

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

const (
	defaultJournalPageSize = 50
	maxJournalPageSize     = 200
)

// journalRepository implements ports.JournalRepository.
//
// # Lock order
//
// Every append takes the household row lock first (the row that owns
// ledger_version), then locks referenced account rows in ascending UUID order,
// then referenced envelope rows, then referenced plan rows. This is the port's
// documented order and prevents two transactions touching the same objects in
// different orders from deadlocking. The append inserts the operation, its
// typed child rows, its idempotency record and the ledger-version increment in
// the same transaction.
type journalRepository struct {
	db *gorm.DB
}

func (r *journalRepository) Append(ctx context.Context, household domain.HouseholdId, op domain.JournalOperation) error {
	if op.Household() != household {
		return domain.NewErrorf(domain.CodeInvalidValue, "journal operation %q belongs to a different household", op.ID())
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return err
	}

	return toDomainErr(r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockHousehold(ctx, tx, householdID); err != nil {
			return err
		}
		if err := lockEffectTargets(ctx, tx, householdID, op); err != nil {
			return err
		}

		row, err := journalOperationToModel(op)
		if err != nil {
			return err
		}
		if err := tx.Create(&row).Error; err != nil {
			return toDomainErr(err)
		}
		if err := insertJournalChildren(ctx, tx, row, op); err != nil {
			return err
		}
		if err := flushPendingIdempotencyRecord(ctx, tx, row, op); err != nil {
			return err
		}

		result := tx.Exec(
			"UPDATE households SET ledger_version = ledger_version + 1, updated_at = ? WHERE id = ?",
			time.Now().UTC(), householdID,
		)
		if result.Error != nil {
			return toDomainErr(result.Error)
		}
		if result.RowsAffected == 0 {
			return domain.ErrEntityNotFoundOrNotInHousehold
		}
		return nil
	}))
}

func (r *journalRepository) Get(ctx context.Context, household domain.HouseholdId, id domain.JournalOperationId) (domain.JournalOperation, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return domain.JournalOperation{}, err
	}
	operationID, err := uuidFromDomain(id.String())
	if err != nil {
		return domain.JournalOperation{}, err
	}

	var row journalOperationModel
	result := r.db.WithContext(ctx).
		Where("household_id = ? AND id = ?", householdID, operationID).
		Take(&row)
	if result.Error != nil {
		return domain.JournalOperation{}, toDomainErr(result.Error)
	}
	operations, err := hydrateJournalOperations(ctx, r.db, householdID, []journalOperationModel{row})
	if err != nil {
		return domain.JournalOperation{}, err
	}
	return operations[0], nil
}

func (r *journalRepository) List(ctx context.Context, household domain.HouseholdId, filter ports.JournalFilter) ([]domain.JournalOperation, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Model(&journalOperationModel{}).
		Where("household_id = ?", householdID)

	if len(filter.Types) > 0 {
		types := make([]string, 0, len(filter.Types))
		for _, opType := range filter.Types {
			types = append(types, opType.String())
		}
		query = query.Where("op_type IN ?", types)
	}
	if filter.Source != "" {
		query = query.Where("source = ?", filter.Source.String())
	}
	if filter.From != nil {
		query = query.Where("effective_date >= ?", effectiveDateValue(*filter.From))
	}
	if filter.To != nil {
		query = query.Where("effective_date <= ?", effectiveDateValue(*filter.To))
	}
	if filter.Cursor != nil {
		cursorID, err := uuidFromDomain(filter.Cursor.ID.String())
		if err != nil {
			return nil, err
		}
		committedAt := filter.Cursor.CommittedAt.UTC()
		query = query.Where(
			"(committed_at > ? OR (committed_at = ? AND id > ?))",
			committedAt, committedAt, cursorID,
		)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultJournalPageSize
	}
	if limit > maxJournalPageSize {
		limit = maxJournalPageSize
	}

	var rows []journalOperationModel
	if result := query.Order("committed_at ASC, id ASC").Limit(limit).Find(&rows); result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	return hydrateJournalOperations(ctx, r.db, householdID, rows)
}

func (r *journalRepository) Version(ctx context.Context, household domain.HouseholdId) (uint64, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return 0, err
	}
	var row householdModel
	result := r.db.WithContext(ctx).
		Select("ledger_version").
		Where("id = ?", householdID).
		Take(&row)
	if result.Error != nil {
		return 0, toDomainErr(result.Error)
	}
	return uint64(row.LedgerVersion), nil
}

// lockHousehold takes the household row lock that serializes every command for
// one household. It must be the first lock in an append transaction.
func lockHousehold(ctx context.Context, tx *gorm.DB, householdID pgtype.UUID) error {
	var row householdModel
	result := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("id = ?", householdID).
		Take(&row)
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	return nil
}

// lockEffectTargets locks referenced account, envelope and plan rows in the
// port's documented order. It also turns a reference to a missing or
// cross-household row into ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD before the
// insert can produce a less specific foreign-key error.
func lockEffectTargets(ctx context.Context, tx *gorm.DB, householdID pgtype.UUID, op domain.JournalOperation) error {
	effects := op.Effects()

	accountIDs := make([]string, 0)
	seenAccounts := make(map[string]struct{})
	addAccount := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seenAccounts[id]; ok {
			return
		}
		seenAccounts[id] = struct{}{}
		accountIDs = append(accountIDs, id)
	}
	for _, posting := range effects.Postings.Accounts {
		addAccount(posting.AccountID.String())
	}
	for _, change := range effects.Reservations {
		if change.Account != nil {
			addAccount(change.Account.AccountID.String())
		}
	}

	envelopeIDs := make([]string, 0)
	seenEnvelopes := make(map[string]struct{})
	addEnvelope := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seenEnvelopes[id]; ok {
			return
		}
		seenEnvelopes[id] = struct{}{}
		envelopeIDs = append(envelopeIDs, id)
	}
	for _, posting := range effects.Postings.Envelopes {
		addEnvelope(posting.EnvelopeID.String())
	}
	for _, change := range effects.Reservations {
		addEnvelope(change.Envelope.EnvelopeID.String())
	}

	planIDs := make([]string, 0)
	seenPlans := make(map[string]struct{})
	for _, change := range effects.Reservations {
		id := change.PlanID.String()
		if id == "" {
			continue
		}
		if _, ok := seenPlans[id]; ok {
			continue
		}
		seenPlans[id] = struct{}{}
		planIDs = append(planIDs, id)
	}

	if err := lockUUIDRows(ctx, tx, "accounts", householdID, accountIDs); err != nil {
		return err
	}
	if err := lockUUIDRows(ctx, tx, "envelopes", householdID, envelopeIDs); err != nil {
		return err
	}
	return lockUUIDRows(ctx, tx, "plans", householdID, planIDs)
}

// lockUUIDRows locks the named table's rows in ascending UUID order. table is
// always one of the adapter's fixed table names, never user input.
func lockUUIDRows(ctx context.Context, tx *gorm.DB, table string, householdID pgtype.UUID, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	uuids := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		uuid, err := uuidValue(id)
		if err != nil {
			return err
		}
		uuids = append(uuids, uuid)
	}

	var rows []struct {
		ID pgtype.UUID
	}
	result := tx.WithContext(ctx).
		Table(table).
		Select("id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("household_id = ? AND id IN ?", householdID, uuids).
		Order("id ASC").
		Find(&rows)
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	if len(rows) != len(uuids) {
		return domain.NewErrorf(
			domain.CodeEntityNotFoundOrNotInHousehold,
			"journal operation references a %s row that is not in this household",
			table,
		)
	}
	return nil
}

// insertJournalChildren writes the typed posting, reservation and related rows
// for one already-inserted operation.
func insertJournalChildren(ctx context.Context, tx *gorm.DB, row journalOperationModel, op domain.JournalOperation) error {
	effects := op.Effects()

	if len(effects.Postings.Accounts) > 0 {
		rows := make([]accountPostingModel, 0, len(effects.Postings.Accounts))
		for _, posting := range effects.Postings.Accounts {
			accountID, err := uuidFromDomain(posting.AccountID.String())
			if err != nil {
				return err
			}
			rows = append(rows, accountPostingModel{
				HouseholdID:        row.HouseholdID,
				JournalOperationID: row.ID,
				AccountID:          accountID,
				DeltaMinor:         posting.Delta.MinorUnits(),
			})
		}
		if err := tx.WithContext(ctx).Create(&rows).Error; err != nil {
			return toDomainErr(err)
		}
	}

	if len(effects.Postings.Envelopes) > 0 {
		rows := make([]envelopePostingModel, 0, len(effects.Postings.Envelopes))
		for _, posting := range effects.Postings.Envelopes {
			envelopeID, err := uuidFromDomain(posting.EnvelopeID.String())
			if err != nil {
				return err
			}
			rows = append(rows, envelopePostingModel{
				HouseholdID:        row.HouseholdID,
				JournalOperationID: row.ID,
				EnvelopeID:         envelopeID,
				DeltaMinor:         posting.Delta.MinorUnits(),
			})
		}
		if err := tx.WithContext(ctx).Create(&rows).Error; err != nil {
			return toDomainErr(err)
		}
	}

	if len(effects.Reservations) > 0 {
		rows := make([]reservationChangeModel, 0, len(effects.Reservations))
		for _, change := range effects.Reservations {
			planID, err := uuidFromDomain(change.PlanID.String())
			if err != nil {
				return err
			}
			envelopeID, err := uuidFromDomain(change.Envelope.EnvelopeID.String())
			if err != nil {
				return err
			}
			model := reservationChangeModel{
				HouseholdID:        row.HouseholdID,
				JournalOperationID: row.ID,
				PlanID:             planID,
				EnvelopeID:         envelopeID,
				EnvelopeDeltaMinor: change.Envelope.Delta.MinorUnits(),
			}
			if change.Account != nil {
				accountID, err := uuidFromDomain(change.Account.AccountID.String())
				if err != nil {
					return err
				}
				delta := change.Account.Delta.MinorUnits()
				model.AccountID = accountID
				model.AccountDeltaMinor = &delta
			}
			rows = append(rows, model)
		}
		if err := tx.WithContext(ctx).Create(&rows).Error; err != nil {
			return toDomainErr(err)
		}
	}

	if len(effects.Related) > 0 {
		rows := make([]relatedLinkModel, 0, len(effects.Related))
		for _, ref := range effects.Related {
			targetID, err := uuidFromDomain(ref.OperationID.String())
			if err != nil {
				return err
			}
			rows = append(rows, relatedLinkModel{
				HouseholdID:        row.HouseholdID,
				JournalOperationID: row.ID,
				Kind:               ref.Kind.String(),
				TargetOperationID:  targetID,
			})
		}
		if err := tx.WithContext(ctx).Create(&rows).Error; err != nil {
			return toDomainErr(err)
		}
	}
	return nil
}

// flushPendingIdempotencyRecord persists an idempotency record that was
// registered by Begin-before-Append in this transaction. Append-before-Begin
// callers have nothing pending; their Begin inserts the record directly. The
// ON CONFLICT clause keeps the append idempotent under a replay race.
func flushPendingIdempotencyRecord(ctx context.Context, tx *gorm.DB, row journalOperationModel, op domain.JournalOperation) error {
	pending, ok := pendingIdempotencyFromContext(ctx).take(row.HouseholdID, op.IdempotencyKey().String(), row.ID)
	if !ok {
		return nil
	}
	result := tx.WithContext(ctx).Exec(
		`INSERT INTO idempotency_records
			(household_id, idempotency_key, request_fingerprint, operation_id, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (household_id, idempotency_key) DO NOTHING`,
		pending.householdID,
		pending.key,
		pending.fingerprint,
		pending.operationID,
		pending.createdAt,
	)
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	return nil
}

// hydrateJournalOperations loads children in four batched queries and rebuilds
// each full domain operation. Child rows are ordered by identity id so the
// in-memory order matches the append order.
func hydrateJournalOperations(ctx context.Context, db *gorm.DB, householdID pgtype.UUID, rows []journalOperationModel) ([]domain.JournalOperation, error) {
	if len(rows) == 0 {
		return []domain.JournalOperation{}, nil
	}

	ids := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}

	accountsByOp := make(map[string][]accountPostingModel)
	var accountRows []accountPostingModel
	result := db.WithContext(ctx).
		Where("household_id = ? AND journal_operation_id IN ?", householdID, ids).
		Order("id ASC").
		Find(&accountRows)
	if result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	for _, row := range accountRows {
		key := uuidString(row.JournalOperationID)
		accountsByOp[key] = append(accountsByOp[key], row)
	}

	envelopesByOp := make(map[string][]envelopePostingModel)
	var envelopeRows []envelopePostingModel
	result = db.WithContext(ctx).
		Where("household_id = ? AND journal_operation_id IN ?", householdID, ids).
		Order("id ASC").
		Find(&envelopeRows)
	if result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	for _, row := range envelopeRows {
		key := uuidString(row.JournalOperationID)
		envelopesByOp[key] = append(envelopesByOp[key], row)
	}

	reservationsByOp := make(map[string][]reservationChangeModel)
	var reservationRows []reservationChangeModel
	result = db.WithContext(ctx).
		Where("household_id = ? AND journal_operation_id IN ?", householdID, ids).
		Order("id ASC").
		Find(&reservationRows)
	if result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	for _, row := range reservationRows {
		key := uuidString(row.JournalOperationID)
		reservationsByOp[key] = append(reservationsByOp[key], row)
	}

	relatedByOp := make(map[string][]relatedLinkModel)
	var relatedRows []relatedLinkModel
	result = db.WithContext(ctx).
		Where("household_id = ? AND journal_operation_id IN ?", householdID, ids).
		Order("id ASC").
		Find(&relatedRows)
	if result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	for _, row := range relatedRows {
		key := uuidString(row.JournalOperationID)
		relatedByOp[key] = append(relatedByOp[key], row)
	}

	operations := make([]domain.JournalOperation, 0, len(rows))
	for _, row := range rows {
		key := uuidString(row.ID)
		currency, err := currencyFromRow(row.CurrencyCode, row.CurrencyExponent)
		if err != nil {
			return nil, err
		}
		effects, err := effectsFromRows(
			accountsByOp[key],
			envelopesByOp[key],
			reservationsByOp[key],
			relatedByOp[key],
			currency,
		)
		if err != nil {
			return nil, err
		}
		operation, err := journalOperationFromModel(row, effects)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, nil
}
