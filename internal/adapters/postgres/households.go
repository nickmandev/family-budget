package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// householdRepository implements ports.HouseholdRepository.
type householdRepository struct {
	db *gorm.DB
}

func (r *householdRepository) Create(ctx context.Context, household ports.HouseholdRecord) error {
	now := time.Now().UTC()
	if household.CreatedAt.IsZero() {
		household.CreatedAt = now
	}
	if household.UpdatedAt.IsZero() {
		household.UpdatedAt = household.CreatedAt
	}
	row, err := householdToModel(household)
	if err != nil {
		return err
	}
	return toDomainErr(r.db.WithContext(ctx).Create(&row).Error)
}

func (r *householdRepository) Get(ctx context.Context, id domain.HouseholdId) (ports.HouseholdRecord, error) {
	uuid, err := uuidFromDomain(id.String())
	if err != nil {
		return ports.HouseholdRecord{}, err
	}
	var row householdModel
	result := r.db.WithContext(ctx).Where("id = ?", uuid).Take(&row)
	if result.Error != nil {
		return ports.HouseholdRecord{}, toDomainErr(result.Error)
	}
	return householdFromModel(row)
}

// SaveSettings updates metadata only. ledger_version is owned by
// JournalRepository.Append and is never touched here.
func (r *householdRepository) SaveSettings(ctx context.Context, household ports.HouseholdRecord) error {
	uuid, err := uuidFromDomain(household.ID.String())
	if err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&householdModel{}).
		Where("id = ?", uuid).
		Updates(map[string]any{
			"name":              household.Name,
			"currency_code":     household.Currency.Code(),
			"currency_exponent": int16(household.Currency.Exponent()),
			"timezone":          household.Timezone,
			"status":            string(household.Status),
			"updated_at":        time.Now().UTC(),
		})
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	return nil
}
