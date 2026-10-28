package postgres

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"family-budget/internal/domain"
)

// idempotencyRepository implements ports.IdempotencyRepository.
//
// The database key is the composite primary key (household_id,
// idempotency_key) from the migration. Begin is an atomic check-and-insert:
//
//  1. lock and inspect any existing record for the key;
//  2. if absent, insert it when the referenced journal operation already
//     exists in this transaction.
//
// Step 2 has a schema-driven caveat: idempotency_records.operation_id has a
// foreign key to journal_operations. A caller that calls Begin before Append
// (the reference in-memory order) gets a validated record with isNew=true but
// no row yet; the subsequent Append in the same transaction persists the row.
// A caller that calls Begin after Append gets the row inserted immediately. Both
// orders are safe under concurrency: the journal_operations unique key on
// (household_id, idempotency_key) is the ultimate duplicate guard, and the
// ON CONFLICT branch here handles a racing insert of the record itself.
type idempotencyRepository struct {
	db *gorm.DB
}

// pendingIdempotencyContextKey identifies the per-transaction pending
// idempotency set created by Store.WithinTransaction.
type pendingIdempotencyContextKey struct{}

// pendingIdempotencyRecord is a Begin call that could not insert yet because
// the referenced journal operation does not exist in the transaction.
type pendingIdempotencyRecord struct {
	householdID pgtype.UUID
	key         string
	fingerprint string
	operationID pgtype.UUID
	createdAt   time.Time
}

// pendingIdempotencySet is scoped to one WithinTransaction callback.
type pendingIdempotencySet struct {
	mu      sync.Mutex
	records map[string]pendingIdempotencyRecord
}

func newPendingIdempotencySet() *pendingIdempotencySet {
	return &pendingIdempotencySet{records: make(map[string]pendingIdempotencyRecord)}
}

func pendingIdempotencyFromContext(ctx context.Context) *pendingIdempotencySet {
	set, _ := ctx.Value(pendingIdempotencyContextKey{}).(*pendingIdempotencySet)
	return set
}

func pendingIdempotencyKey(householdID pgtype.UUID, key string) string {
	return householdID.String() + "\x00" + key
}

func (s *pendingIdempotencySet) add(record pendingIdempotencyRecord) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[pendingIdempotencyKey(record.householdID, record.key)] = record
}

func (s *pendingIdempotencySet) take(householdID pgtype.UUID, key string, operationID pgtype.UUID) (pendingIdempotencyRecord, bool) {
	if s == nil {
		return pendingIdempotencyRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	recordKey := pendingIdempotencyKey(householdID, key)
	record, ok := s.records[recordKey]
	if !ok || record.operationID.String() != operationID.String() {
		return pendingIdempotencyRecord{}, false
	}
	delete(s.records, recordKey)
	return record, true
}

func (r *idempotencyRepository) Begin(ctx context.Context, household domain.HouseholdId, key domain.IdempotencyKey, fingerprint domain.RequestFingerprint, operationID domain.JournalOperationId, now time.Time) (record domain.IdempotencyRecord, isNew bool, err error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return domain.IdempotencyRecord{}, false, err
	}
	operationUUID, err := uuidFromDomain(operationID.String())
	if err != nil {
		return domain.IdempotencyRecord{}, false, err
	}

	// Validate locally before touching the database so malformed input fails
	// with the same domain code as the in-memory reference store.
	candidate, err := domain.NewIdempotencyRecord(key, fingerprint, operationID, domain.NewRecordedAt(now))
	if err != nil {
		return domain.IdempotencyRecord{}, false, err
	}

	txErr := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, found, err := lookupIdempotencyRow(ctx, tx, householdID, key.String(), true)
		if err != nil {
			return err
		}
		if found {
			existingRecord, err := idempotencyRecordFromModel(existing)
			if err != nil {
				return err
			}
			if existingRecord.Fingerprint() != fingerprint {
				return domain.NewErrorf(
					domain.CodeIdempotencyKeyPayloadMismatch,
					"idempotency key %q was reused with a different request fingerprint",
					key,
				)
			}
			record = existingRecord
			isNew = false
			return nil
		}

		// The foreign key requires the original operation to exist in this
		// transaction before the record can be inserted.
		var count int64
		if err := tx.Model(&journalOperationModel{}).
			Where("household_id = ? AND id = ?", householdID, operationUUID).
			Count(&count).Error; err != nil {
			return toDomainErr(err)
		}
		if count == 0 {
			// Begin-before-Append: Append flushes the record in this same
			// transaction. A direct (non-transaction) call still returns the
			// validated in-memory record; without an Append there is no
			// operation for the foreign key to reference.
			pendingIdempotencyFromContext(ctx).add(pendingIdempotencyRecord{
				householdID: householdID,
				key:         key.String(),
				fingerprint: fingerprint.String(),
				operationID: operationUUID,
				createdAt:   now.UTC(),
			})
			record = candidate
			isNew = true
			return nil
		}

		insert := tx.Exec(
			`INSERT INTO idempotency_records
				(household_id, idempotency_key, request_fingerprint, operation_id, created_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT (household_id, idempotency_key) DO NOTHING`,
			householdID, key.String(), fingerprint.String(), operationUUID, now.UTC(),
		)
		if insert.Error != nil {
			return toDomainErr(insert.Error)
		}
		if insert.RowsAffected == 1 {
			record = candidate
			isNew = true
			return nil
		}

		// A concurrent transaction inserted the same key first. Re-read it
		// under lock and apply the fingerprint rule.
		existing, found, err = lookupIdempotencyRow(ctx, tx, householdID, key.String(), true)
		if err != nil {
			return err
		}
		if !found {
			return domain.WrapError(domain.CodeConcurrencyConflict, "idempotency record disappeared concurrently", nil)
		}
		existingRecord, err := idempotencyRecordFromModel(existing)
		if err != nil {
			return err
		}
		if existingRecord.Fingerprint() != fingerprint {
			return domain.NewErrorf(
				domain.CodeIdempotencyKeyPayloadMismatch,
				"idempotency key %q was reused with a different request fingerprint",
				key,
			)
		}
		record = existingRecord
		isNew = false
		return nil
	})
	if txErr != nil {
		return domain.IdempotencyRecord{}, false, toDomainErr(txErr)
	}
	return record, isNew, nil
}

func (r *idempotencyRepository) Lookup(ctx context.Context, household domain.HouseholdId, key domain.IdempotencyKey) (domain.IdempotencyRecord, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return domain.IdempotencyRecord{}, err
	}
	row, found, err := lookupIdempotencyRow(ctx, r.db.WithContext(ctx), householdID, key.String(), false)
	if err != nil {
		return domain.IdempotencyRecord{}, err
	}
	if !found {
		return domain.IdempotencyRecord{}, domain.NewErrorf(
			domain.CodeEntityNotFoundOrNotInHousehold,
			"idempotency key %q has no record in this household",
			key,
		)
	}
	return idempotencyRecordFromModel(row)
}

// lookupIdempotencyRow fetches one record, optionally taking a row lock.
func lookupIdempotencyRow(ctx context.Context, db *gorm.DB, householdID pgtype.UUID, key string, forUpdate bool) (idempotencyRecordModel, bool, error) {
	query := db.WithContext(ctx).Where("household_id = ? AND idempotency_key = ?", householdID, key)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row idempotencyRecordModel
	result := query.Take(&row)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return idempotencyRecordModel{}, false, nil
		}
		return idempotencyRecordModel{}, false, toDomainErr(result.Error)
	}
	return row, true, nil
}
