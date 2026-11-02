package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// bankConnectionRepository implements ports.BankConnectionRepository.
type bankConnectionRepository struct {
	db *gorm.DB
}

func (r *bankConnectionRepository) Create(ctx context.Context, household domain.HouseholdId, record ports.BankConnectionRecord) error {
	row, err := bankConnectionToModel(household, record)
	if err != nil {
		return err
	}
	return toDomainErr(r.db.WithContext(ctx).Create(&row).Error)
}

func (r *bankConnectionRepository) Get(ctx context.Context, household domain.HouseholdId, id string) (ports.BankConnectionRecord, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return ports.BankConnectionRecord{}, err
	}
	connectionID, err := uuidFromDomain(id)
	if err != nil {
		return ports.BankConnectionRecord{}, err
	}
	var row BankConnectionModel
	result := r.db.WithContext(ctx).Where("household_id = ? AND id = ?", householdID, connectionID).Take(&row)
	if result.Error != nil {
		return ports.BankConnectionRecord{}, toDomainErr(result.Error)
	}
	return bankConnectionFromModel(row), nil
}

func (r *bankConnectionRepository) ListByHousehold(ctx context.Context, household domain.HouseholdId) ([]ports.BankConnectionRecord, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return nil, err
	}
	var rows []BankConnectionModel
	result := r.db.WithContext(ctx).Where("household_id = ?", householdID).
		Order("created_at DESC, id DESC").Find(&rows)
	if result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	records := make([]ports.BankConnectionRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, bankConnectionFromModel(row))
	}
	return records, nil
}

func (r *bankConnectionRepository) ClaimPendingState(ctx context.Context, stateHash string, now time.Time) (ports.BankConnectionRecord, error) {
	var row BankConnectionModel
	result := r.db.WithContext(ctx).Raw(`
		UPDATE bank_connections
		SET status = 'AUTHORIZED', updated_at = ?
		WHERE state_hash = ? AND status = 'PENDING' AND state_expires_at > ?
		RETURNING *`, now, stateHash, now).Scan(&row)
	if result.Error != nil {
		return ports.BankConnectionRecord{}, toDomainErr(result.Error)
	}
	if result.RowsAffected == 0 {
		return ports.BankConnectionRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return bankConnectionFromModel(row), nil
}

// Save updates mutable connection state without rewriting its ownership or
// single-use state hash.
func (r *bankConnectionRepository) Save(ctx context.Context, record ports.BankConnectionRecord) error {
	householdID, err := uuidFromDomain(record.HouseholdID.String())
	if err != nil {
		return err
	}
	connectionID, err := uuidFromDomain(record.ID)
	if err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&BankConnectionModel{}).
		Where("household_id = ? AND id = ?", householdID, connectionID).
		Updates(map[string]any{
			"status":                string(record.Status),
			"authorization_id":      record.AuthorizationID,
			"provider_redirect_url": record.ProviderRedirectURL,
			"session_id_encrypted":  record.SessionIDEncrypted,
			"session_expires_at":    utcTimePointer(record.SessionExpiresAt),
			"failure_summary":       record.FailureSummary,
			"updated_at":            record.UpdatedAt.UTC(),
		})
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	return nil
}

func bankConnectionToModel(household domain.HouseholdId, record ports.BankConnectionRecord) (BankConnectionModel, error) {
	id, err := uuidFromDomain(record.ID)
	if err != nil {
		return BankConnectionModel{}, err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return BankConnectionModel{}, err
	}
	initiatedBy, err := uuidFromDomain(record.InitiatedBy.String())
	if err != nil {
		return BankConnectionModel{}, err
	}
	return BankConnectionModel{
		ID:                  id,
		HouseholdID:         householdID,
		ASPSPName:           record.ASPSPName,
		ASPSPCountry:        record.ASPSPCountry,
		Status:              string(record.Status),
		StateHash:           record.StateHash,
		StateExpiresAt:      record.StateExpiresAt.UTC(),
		InitiatedBy:         initiatedBy,
		AuthorizationID:     record.AuthorizationID,
		ProviderRedirectURL: record.ProviderRedirectURL,
		SessionIDEncrypted:  record.SessionIDEncrypted,
		SessionExpiresAt:    utcTimePointer(record.SessionExpiresAt),
		FailureSummary:      record.FailureSummary,
		CreatedAt:           record.CreatedAt.UTC(),
		UpdatedAt:           record.UpdatedAt.UTC(),
	}, nil
}

func bankConnectionFromModel(row BankConnectionModel) ports.BankConnectionRecord {
	return ports.BankConnectionRecord{
		ID:                  row.ID.String(),
		HouseholdID:         domain.HouseholdId(row.HouseholdID.String()),
		ASPSPName:           row.ASPSPName,
		ASPSPCountry:        row.ASPSPCountry,
		Status:              ports.BankConnectionStatus(row.Status),
		StateHash:           row.StateHash,
		StateExpiresAt:      row.StateExpiresAt.UTC(),
		InitiatedBy:         domain.UserId(row.InitiatedBy.String()),
		AuthorizationID:     row.AuthorizationID,
		ProviderRedirectURL: row.ProviderRedirectURL,
		SessionIDEncrypted:  row.SessionIDEncrypted,
		SessionExpiresAt:    utcTimePointer(row.SessionExpiresAt),
		FailureSummary:      row.FailureSummary,
		CreatedAt:           row.CreatedAt.UTC(),
		UpdatedAt:           row.UpdatedAt.UTC(),
	}
}
