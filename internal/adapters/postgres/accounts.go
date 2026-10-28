package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// accountRepository implements ports.AccountRepository.
type accountRepository struct {
	db *gorm.DB
}

func (r *accountRepository) Create(ctx context.Context, household domain.HouseholdId, account ports.AccountRecord) error {
	now := time.Now().UTC()
	if account.CreatedAt.IsZero() {
		account.CreatedAt = now
	}
	if account.UpdatedAt.IsZero() {
		account.UpdatedAt = account.CreatedAt
	}
	row, err := accountToModel(household, account)
	if err != nil {
		return err
	}
	return toDomainErr(r.db.WithContext(ctx).Create(&row).Error)
}

func (r *accountRepository) Get(ctx context.Context, household domain.HouseholdId, id domain.AccountId) (ports.AccountRecord, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return ports.AccountRecord{}, err
	}
	accountID, err := uuidFromDomain(id.String())
	if err != nil {
		return ports.AccountRecord{}, err
	}
	var row accountModel
	result := r.db.WithContext(ctx).
		Where("household_id = ? AND id = ?", householdID, accountID).
		Take(&row)
	if result.Error != nil {
		return ports.AccountRecord{}, toDomainErr(result.Error)
	}
	return accountFromModel(row)
}

func (r *accountRepository) List(ctx context.Context, household domain.HouseholdId, filter ports.AccountFilter) ([]ports.AccountRecord, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Model(&accountModel{}).Where("household_id = ?", householdID)
	if !filter.IncludeArchived {
		query = query.Where("status = ?", string(ports.LifecycleActive))
	}
	if len(filter.Kinds) > 0 {
		kinds := make([]string, 0, len(filter.Kinds))
		for _, kind := range filter.Kinds {
			kinds = append(kinds, kind.String())
		}
		query = query.Where("kind IN ?", kinds)
	}

	var rows []accountModel
	if result := query.Order("id ASC").Find(&rows); result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	records := make([]ports.AccountRecord, 0, len(rows))
	for _, row := range rows {
		record, err := accountFromModel(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// Save updates metadata only. Posted balances and reservations are derived from
// the append-only journal and are never rewritten here.
func (r *accountRepository) Save(ctx context.Context, household domain.HouseholdId, account ports.AccountRecord) error {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return err
	}
	accountID, err := uuidFromDomain(account.ID.String())
	if err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&accountModel{}).
		Where("household_id = ? AND id = ?", householdID, accountID).
		Updates(map[string]any{
			"name":            account.Name,
			"kind":            account.Kind.String(),
			"budget_eligible": account.BudgetEligible,
			"status":          string(account.Status),
			"updated_at":      time.Now().UTC(),
		})
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	return nil
}

// HasActiveReservations reports whether the account still has a non-zero Qa
// hold, derived from the append-only reservation_changes rows.
func (r *accountRepository) HasActiveReservations(ctx context.Context, household domain.HouseholdId, id domain.AccountId) (bool, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return false, err
	}
	accountID, err := uuidFromDomain(id.String())
	if err != nil {
		return false, err
	}

	var account accountModel
	result := r.db.WithContext(ctx).
		Select("id").
		Where("household_id = ? AND id = ?", householdID, accountID).
		Take(&account)
	if result.Error != nil {
		return false, toDomainErr(result.Error)
	}

	var summary struct {
		Total int64
	}
	result = r.db.WithContext(ctx).Model(&reservationChangeModel{}).
		Select("COALESCE(SUM(account_delta_minor), 0) AS total").
		Where("household_id = ? AND account_id = ?", householdID, accountID).
		Scan(&summary)
	if result.Error != nil {
		return false, toDomainErr(result.Error)
	}
	return summary.Total != 0, nil
}
