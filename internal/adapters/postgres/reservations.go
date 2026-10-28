package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"gorm.io/gorm"

	"family-budget/internal/domain"
)

// reservationRepository derives the active Qe/Qa holds from the append-only
// reservation_changes rows. A hold is simply the signed sum of all its deltas;
// release rows cancel earlier holds, so no mutable reservation table exists.
type reservationRepository struct {
	db *gorm.DB
}

func (r *reservationRepository) ActiveEnvelopeHolds(ctx context.Context, household domain.HouseholdId, envelope domain.EnvelopeId) (domain.Money, error) {
	currency, err := currencyForHousehold(ctx, r.db, household.String())
	if err != nil {
		return domain.Money{}, err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return domain.Money{}, err
	}
	envelopeID, err := uuidFromDomain(envelope.String())
	if err != nil {
		return domain.Money{}, err
	}
	if err := ensureEnvelopeExists(ctx, r.db, householdID, envelopeID); err != nil {
		return domain.Money{}, err
	}

	var summary struct {
		Total int64
	}
	result := r.db.WithContext(ctx).Model(&reservationChangeModel{}).
		Select("COALESCE(SUM(envelope_delta_minor), 0) AS total").
		Where("household_id = ? AND envelope_id = ?", householdID, envelopeID).
		Scan(&summary)
	if result.Error != nil {
		return domain.Money{}, toDomainErr(result.Error)
	}
	return domain.NewMoney(summary.Total, currency)
}

func (r *reservationRepository) ActiveAccountHolds(ctx context.Context, household domain.HouseholdId, account domain.AccountId) (domain.Money, error) {
	currency, err := currencyForHousehold(ctx, r.db, household.String())
	if err != nil {
		return domain.Money{}, err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return domain.Money{}, err
	}
	accountID, err := uuidFromDomain(account.String())
	if err != nil {
		return domain.Money{}, err
	}
	if err := ensureAccountExists(ctx, r.db, householdID, accountID); err != nil {
		return domain.Money{}, err
	}

	var summary struct {
		Total int64
	}
	result := r.db.WithContext(ctx).Model(&reservationChangeModel{}).
		Select("COALESCE(SUM(account_delta_minor), 0) AS total").
		Where("household_id = ? AND account_id = ?", householdID, accountID).
		Scan(&summary)
	if result.Error != nil {
		return domain.Money{}, toDomainErr(result.Error)
	}
	return domain.NewMoney(summary.Total, currency)
}

func (r *reservationRepository) HoldsForPlan(ctx context.Context, household domain.HouseholdId, plan domain.PlanId) (domain.Money, domain.Money, error) {
	currency, err := currencyForHousehold(ctx, r.db, household.String())
	if err != nil {
		return domain.Money{}, domain.Money{}, err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return domain.Money{}, domain.Money{}, err
	}
	planID, err := uuidFromDomain(plan.String())
	if err != nil {
		return domain.Money{}, domain.Money{}, err
	}
	if err := ensurePlanExists(ctx, r.db, householdID, planID); err != nil {
		return domain.Money{}, domain.Money{}, err
	}

	var summary struct {
		EnvelopeTotal int64
		AccountTotal  int64
	}
	result := r.db.WithContext(ctx).Model(&reservationChangeModel{}).
		Select("COALESCE(SUM(envelope_delta_minor), 0) AS envelope_total, COALESCE(SUM(account_delta_minor), 0) AS account_total").
		Where("household_id = ? AND plan_id = ?", householdID, planID).
		Scan(&summary)
	if result.Error != nil {
		return domain.Money{}, domain.Money{}, toDomainErr(result.Error)
	}

	envelopeHold, err := domain.NewMoney(summary.EnvelopeTotal, currency)
	if err != nil {
		return domain.Money{}, domain.Money{}, err
	}
	accountHold, err := domain.NewMoney(summary.AccountTotal, currency)
	if err != nil {
		return domain.Money{}, domain.Money{}, err
	}
	return envelopeHold, accountHold, nil
}

// ensureEnvelopeExists verifies household-scoped existence before a derived
// hold query, so a cross-household envelope id is indistinguishable from an
// absent one.
func ensureEnvelopeExists(ctx context.Context, db *gorm.DB, householdID, envelopeID pgtype.UUID) error {
	var row envelopeModel
	result := db.WithContext(ctx).
		Select("id").
		Where("household_id = ? AND id = ?", householdID, envelopeID).
		Take(&row)
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	return nil
}

// ensureAccountExists verifies household-scoped account existence.
func ensureAccountExists(ctx context.Context, db *gorm.DB, householdID, accountID pgtype.UUID) error {
	var row accountModel
	result := db.WithContext(ctx).
		Select("id").
		Where("household_id = ? AND id = ?", householdID, accountID).
		Take(&row)
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	return nil
}

// ensurePlanExists verifies household-scoped plan existence.
func ensurePlanExists(ctx context.Context, db *gorm.DB, householdID, planID pgtype.UUID) error {
	var row planModel
	result := db.WithContext(ctx).
		Select("id").
		Where("household_id = ? AND id = ?", householdID, planID).
		Take(&row)
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	return nil
}
