package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	"family-budget/internal/domain"
)

// suggestionRepository implements ports.SuggestionRepository.
type suggestionRepository struct {
	db *gorm.DB
}

func (r *suggestionRepository) Create(ctx context.Context, household domain.HouseholdId, suggestion *domain.Suggestion) error {
	if suggestion == nil {
		return domain.NewError(domain.CodeInvalidValue, "suggestion must not be nil")
	}
	if _, err := currencyForHousehold(ctx, r.db, household.String()); err != nil {
		return err
	}
	row, err := suggestionToModel(household, suggestion, time.Now().UTC())
	if err != nil {
		return err
	}
	return toDomainErr(r.db.WithContext(ctx).Create(&row).Error)
}

func (r *suggestionRepository) Get(ctx context.Context, household domain.HouseholdId, id domain.SuggestionId) (*domain.Suggestion, error) {
	currency, err := currencyForHousehold(ctx, r.db, household.String())
	if err != nil {
		return nil, err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return nil, err
	}
	suggestionID, err := uuidFromDomain(id.String())
	if err != nil {
		return nil, err
	}
	var row suggestionModel
	result := r.db.WithContext(ctx).
		Where("household_id = ? AND id = ?", householdID, suggestionID).
		Take(&row)
	if result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	return suggestionFromModel(row, currency)
}

// List returns every suggestion in the household in deterministic id order.
func (r *suggestionRepository) List(ctx context.Context, household domain.HouseholdId) ([]*domain.Suggestion, error) {
	currency, err := currencyForHousehold(ctx, r.db, household.String())
	if err != nil {
		return nil, err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return nil, err
	}
	var rows []suggestionModel
	if result := r.db.WithContext(ctx).
		Where("household_id = ?", householdID).
		Order("id ASC").
		Find(&rows); result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	suggestions := make([]*domain.Suggestion, 0, len(rows))
	for _, row := range rows {
		suggestion, err := suggestionFromModel(row, currency)
		if err != nil {
			return nil, err
		}
		suggestions = append(suggestions, suggestion)
	}
	return suggestions, nil
}

// Save updates the aggregate only while the stored state still equals expected
// (the same optimistic state guard as plans).
func (r *suggestionRepository) Save(ctx context.Context, household domain.HouseholdId, suggestion *domain.Suggestion, expected domain.SuggestionState) error {
	if suggestion == nil {
		return domain.NewError(domain.CodeInvalidValue, "suggestion must not be nil")
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return err
	}
	suggestionID, err := uuidFromDomain(suggestion.ID().String())
	if err != nil {
		return err
	}
	row, err := suggestionToModel(household, suggestion, time.Now().UTC())
	if err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&suggestionModel{}).
		Where("household_id = ? AND id = ? AND state = ?", householdID, suggestionID, expected.String()).
		Updates(map[string]any{
			"source":             row.Source,
			"amount_minor":       row.AmountMinor,
			"merchant":           row.Merchant,
			"state":              row.State,
			"cancel_reason":      row.CancelReason,
			"confirmation_count": row.ConfirmationCount,
			"updated_at":         row.UpdatedAt,
		})
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := r.db.WithContext(ctx).Model(&suggestionModel{}).
			Where("household_id = ? AND id = ?", householdID, suggestionID).
			Count(&count).Error; err != nil {
			return toDomainErr(err)
		}
		if count == 0 {
			return domain.ErrEntityNotFoundOrNotInHousehold
		}
		return domain.WrapError(domain.CodeConcurrencyConflict, "suggestion changed concurrently", nil)
	}
	return nil
}
