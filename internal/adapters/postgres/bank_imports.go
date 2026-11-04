package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

type bankImportRepository struct {
	db *gorm.DB
}

func (r *bankImportRepository) CreateBatch(ctx context.Context, record ports.BankImportBatchRecord) error {
	row, err := bankImportBatchToModel(record)
	if err != nil {
		return err
	}
	return toDomainErr(r.db.WithContext(ctx).Create(&row).Error)
}

func (r *bankImportRepository) UpdateBatchCounts(ctx context.Context, household domain.HouseholdId, id string, imported, duplicate, failed int) error {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return err
	}
	batchID, err := uuidFromDomain(id)
	if err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&bankImportBatchModel{}).
		Where("household_id = ? AND id = ?", householdID, batchID).
		Updates(map[string]any{"rows_imported": imported, "rows_duplicate": duplicate, "rows_failed": failed})
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	return nil
}

func (r *bankImportRepository) InsertEntry(ctx context.Context, record ports.BankImportEntryRecord) (bool, error) {
	row, err := bankImportEntryToModel(record)
	if err != nil {
		return false, err
	}
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "household_id"}, {Name: "account_id"}, {Name: "fingerprint"}},
		DoNothing: true,
	}).Create(&row)
	if result.Error != nil {
		return false, toDomainErr(result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *bankImportRepository) AttachSuggestion(ctx context.Context, household domain.HouseholdId, entryID, suggestionID string) error {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return err
	}
	entryUUID, err := uuidFromDomain(entryID)
	if err != nil {
		return err
	}
	suggestionUUID, err := uuidFromDomain(suggestionID)
	if err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&bankImportEntryModel{}).
		Where("household_id = ? AND id = ?", householdID, entryUUID).
		Update("suggestion_id", suggestionUUID)
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	return nil
}

func (r *bankImportRepository) ListBatches(ctx context.Context, household domain.HouseholdId) ([]ports.BankImportBatchRecord, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return nil, err
	}
	var rows []bankImportBatchModel
	if err := r.db.WithContext(ctx).Where("household_id = ?", householdID).
		Order("created_at DESC").Order("id DESC").Find(&rows).Error; err != nil {
		return nil, toDomainErr(err)
	}
	records := make([]ports.BankImportBatchRecord, 0, len(rows))
	for _, row := range rows {
		record, err := bankImportBatchFromModel(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (r *bankImportRepository) GetBatch(ctx context.Context, household domain.HouseholdId, id string) (ports.BankImportBatchRecord, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return ports.BankImportBatchRecord{}, err
	}
	batchID, err := uuidFromDomain(id)
	if err != nil {
		return ports.BankImportBatchRecord{}, err
	}
	var row bankImportBatchModel
	if err := r.db.WithContext(ctx).Where("household_id = ? AND id = ?", householdID, batchID).Take(&row).Error; err != nil {
		return ports.BankImportBatchRecord{}, toDomainErr(err)
	}
	return bankImportBatchFromModel(row)
}

func bankImportBatchToModel(record ports.BankImportBatchRecord) (bankImportBatchModel, error) {
	id, err := uuidFromDomain(record.ID)
	if err != nil {
		return bankImportBatchModel{}, err
	}
	householdID, err := uuidFromDomain(record.HouseholdID.String())
	if err != nil {
		return bankImportBatchModel{}, err
	}
	accountID, err := uuidFromDomain(record.AccountID.String())
	if err != nil {
		return bankImportBatchModel{}, err
	}
	createdBy, err := uuidFromDomain(record.CreatedBy.String())
	if err != nil {
		return bankImportBatchModel{}, err
	}
	return bankImportBatchModel{
		ID: id, HouseholdID: householdID, AccountID: accountID,
		BankCode: record.BankCode, Format: record.Format, FileName: record.FileName,
		FileSHA256: record.FileSHA256, PeriodFrom: record.PeriodFrom, PeriodTo: record.PeriodTo,
		RowsTotal: record.RowsTotal, RowsImported: record.RowsImported, RowsDuplicate: record.RowsDuplicate,
		RowsFailed: record.RowsFailed, CreatedBy: createdBy, CreatedAt: record.CreatedAt,
	}, nil
}

func bankImportBatchFromModel(row bankImportBatchModel) (ports.BankImportBatchRecord, error) {
	return ports.BankImportBatchRecord{
		ID: row.ID.String(), HouseholdID: domain.HouseholdId(row.HouseholdID.String()),
		AccountID: domain.AccountId(row.AccountID.String()), BankCode: row.BankCode,
		Format: row.Format, FileName: row.FileName, FileSHA256: row.FileSHA256,
		PeriodFrom: row.PeriodFrom, PeriodTo: row.PeriodTo, RowsTotal: row.RowsTotal,
		RowsImported: row.RowsImported, RowsDuplicate: row.RowsDuplicate, RowsFailed: row.RowsFailed,
		CreatedBy: domain.UserId(row.CreatedBy.String()), CreatedAt: row.CreatedAt,
	}, nil
}

func bankImportEntryToModel(record ports.BankImportEntryRecord) (bankImportEntryModel, error) {
	id, err := uuidFromDomain(record.ID)
	if err != nil {
		return bankImportEntryModel{}, err
	}
	batchID, err := uuidFromDomain(record.BatchID)
	if err != nil {
		return bankImportEntryModel{}, err
	}
	householdID, err := uuidFromDomain(record.HouseholdID.String())
	if err != nil {
		return bankImportEntryModel{}, err
	}
	accountID, err := uuidFromDomain(record.AccountID.String())
	if err != nil {
		return bankImportEntryModel{}, err
	}
	suggestionID, err := nullableUUIDPointer(record.SuggestionID)
	if err != nil {
		return bankImportEntryModel{}, err
	}
	return bankImportEntryModel{
		ID: id, BatchID: batchID, HouseholdID: householdID, AccountID: accountID,
		Fingerprint: record.Fingerprint, ExternalID: record.ExternalID, EffectiveDate: dateAtMidnight(record.EffectiveDate),
		ValueDate: datePointerAtMidnight(record.ValueDate), Direction: string(record.Direction),
		AmountMinor: record.AmountMinor, Currency: record.Currency, Merchant: record.Merchant,
		Description: record.Description, Reference: optionalString(record.Reference),
		TransactionCode: optionalString(record.TransactionCode), Status: record.Status,
		SuggestionID: suggestionID, CreatedAt: record.CreatedAt,
	}, nil
}

func nullableUUIDPointer(value *string) (pgtype.UUID, error) {
	if value == nil {
		return pgtype.UUID{}, nil
	}
	id, err := uuidFromDomain(*value)
	return id, err
}

func dateAtMidnight(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func datePointerAtMidnight(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	date := dateAtMidnight(*value)
	return &date
}
