package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	"family-budget/internal/domain"
)

// planRepository implements ports.PlanRepository.
type planRepository struct {
	db *gorm.DB
}

func (r *planRepository) Create(ctx context.Context, household domain.HouseholdId, plan *domain.PlannedOperation) error {
	if plan == nil {
		return domain.NewError(domain.CodeInvalidValue, "planned operation must not be nil")
	}
	if _, err := currencyForHousehold(ctx, r.db, household.String()); err != nil {
		return err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return err
	}
	row, err := planToModel(plan, time.Now().UTC())
	if err != nil {
		return err
	}
	row.HouseholdID = householdID
	return toDomainErr(r.db.WithContext(ctx).Create(&row).Error)
}

func (r *planRepository) Get(ctx context.Context, household domain.HouseholdId, id domain.PlanId) (*domain.PlannedOperation, error) {
	currency, err := currencyForHousehold(ctx, r.db, household.String())
	if err != nil {
		return nil, err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return nil, err
	}
	planID, err := uuidFromDomain(id.String())
	if err != nil {
		return nil, err
	}
	var row planModel
	result := r.db.WithContext(ctx).
		Where("household_id = ? AND id = ?", householdID, planID).
		Take(&row)
	if result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	return planFromModel(row, currency)
}

// List returns every plan in the household in deterministic id order.
func (r *planRepository) List(ctx context.Context, household domain.HouseholdId) ([]*domain.PlannedOperation, error) {
	currency, err := currencyForHousehold(ctx, r.db, household.String())
	if err != nil {
		return nil, err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return nil, err
	}
	var rows []planModel
	if result := r.db.WithContext(ctx).
		Where("household_id = ?", householdID).
		Order("id ASC").
		Find(&rows); result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	plans := make([]*domain.PlannedOperation, 0, len(rows))
	for _, row := range rows {
		plan, err := planFromModel(row, currency)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// Save updates the whole aggregate only while the stored state still equals
// expected. A mismatch is CONCURRENCY_CONFLICT; a missing row is
// ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD.
func (r *planRepository) Save(ctx context.Context, household domain.HouseholdId, plan *domain.PlannedOperation, expected domain.PlanState) error {
	if plan == nil {
		return domain.NewError(domain.CodeInvalidValue, "planned operation must not be nil")
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return err
	}
	planID, err := uuidFromDomain(plan.ID().String())
	if err != nil {
		return err
	}
	row, err := planToModel(plan, time.Now().UTC())
	if err != nil {
		return err
	}

	result := r.db.WithContext(ctx).Model(&planModel{}).
		Where("household_id = ? AND id = ? AND state = ?", householdID, planID, expected.String()).
		Updates(map[string]any{
			"kind":                      row.Kind,
			"state":                     row.State,
			"target_envelope_id":        row.TargetEnvelopeID,
			"target_account_id":         row.TargetAccountID,
			"intended_amount_minor":     row.IntendedAmountMinor,
			"authorized_amount_minor":   row.AuthorizedAmountMinor,
			"mode":                      row.Mode,
			"reason":                    row.Reason,
			"held_amount_minor":         row.HeldAmountMinor,
			"held_on_account_minor":     row.HeldOnAccountMinor,
			"released_amount_minor":     row.ReleasedAmountMinor,
			"released_on_account_minor": row.ReleasedOnAccountMinor,
			"final_amount_minor":        row.FinalAmountMinor,
			"updated_at":                row.UpdatedAt,
		})
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	if result.RowsAffected == 0 {
		// Distinguish a stale-state guard failure from a missing row without
		// leaking cross-household existence: the existence probe is itself
		// household-scoped.
		var count int64
		if err := r.db.WithContext(ctx).Model(&planModel{}).
			Where("household_id = ? AND id = ?", householdID, planID).
			Count(&count).Error; err != nil {
			return toDomainErr(err)
		}
		if count == 0 {
			return domain.ErrEntityNotFoundOrNotInHousehold
		}
		return domain.WrapError(domain.CodeConcurrencyConflict, "plan changed concurrently", nil)
	}
	return nil
}
