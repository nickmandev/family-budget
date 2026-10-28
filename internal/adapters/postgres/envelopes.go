package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"gorm.io/gorm"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// envelopeRepository implements ports.EnvelopeRepository.
type envelopeRepository struct {
	db *gorm.DB
}

func (r *envelopeRepository) Create(ctx context.Context, household domain.HouseholdId, envelope ports.EnvelopeRecord) error {
	now := time.Now().UTC()
	if envelope.CreatedAt.IsZero() {
		envelope.CreatedAt = now
	}
	if envelope.UpdatedAt.IsZero() {
		envelope.UpdatedAt = envelope.CreatedAt
	}
	row, err := envelopeToModel(household, envelope)
	if err != nil {
		return err
	}
	return toDomainErr(r.db.WithContext(ctx).Create(&row).Error)
}

func (r *envelopeRepository) Get(ctx context.Context, household domain.HouseholdId, id domain.EnvelopeId) (ports.EnvelopeRecord, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return ports.EnvelopeRecord{}, err
	}
	envelopeID, err := uuidFromDomain(id.String())
	if err != nil {
		return ports.EnvelopeRecord{}, err
	}
	var row envelopeModel
	result := r.db.WithContext(ctx).
		Where("household_id = ? AND id = ?", householdID, envelopeID).
		Take(&row)
	if result.Error != nil {
		return ports.EnvelopeRecord{}, toDomainErr(result.Error)
	}
	return envelopeFromModel(row), nil
}

func (r *envelopeRepository) List(ctx context.Context, household domain.HouseholdId, filter ports.EnvelopeFilter) ([]ports.EnvelopeRecord, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Model(&envelopeModel{}).Where("household_id = ?", householdID)
	if !filter.IncludeArchived {
		query = query.Where("status = ?", string(ports.LifecycleActive))
	}
	if filter.LinkedCardAccountID != nil {
		linkedID, err := uuidFromDomain(filter.LinkedCardAccountID.String())
		if err != nil {
			return nil, err
		}
		query = query.Where("linked_card_account_id = ?", linkedID)
	}

	var rows []envelopeModel
	if result := query.Order("id ASC").Find(&rows); result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	records := make([]ports.EnvelopeRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, envelopeFromModel(row))
	}
	return records, nil
}

// Save updates metadata only: envelope balances and holds are derived from the
// journal and reservation rows.
func (r *envelopeRepository) Save(ctx context.Context, household domain.HouseholdId, envelope ports.EnvelopeRecord) error {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return err
	}
	envelopeID, err := uuidFromDomain(envelope.ID.String())
	if err != nil {
		return err
	}
	var linked pgtype.UUID
	if envelope.LinkedCardAccountID != nil {
		linked, err = uuidFromDomain(envelope.LinkedCardAccountID.String())
		if err != nil {
			return err
		}
	}
	result := r.db.WithContext(ctx).Model(&envelopeModel{}).
		Where("household_id = ? AND id = ?", householdID, envelopeID).
		Updates(map[string]any{
			"name":                   envelope.Name,
			"status":                 string(envelope.Status),
			"linked_card_account_id": linked,
			"updated_at":             time.Now().UTC(),
		})
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	return nil
}

// HasActiveReservations reports whether the envelope still has a non-zero Qe
// hold, derived from the append-only reservation_changes rows.
func (r *envelopeRepository) HasActiveReservations(ctx context.Context, household domain.HouseholdId, id domain.EnvelopeId) (bool, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return false, err
	}
	envelopeID, err := uuidFromDomain(id.String())
	if err != nil {
		return false, err
	}

	var envelope envelopeModel
	result := r.db.WithContext(ctx).
		Select("id").
		Where("household_id = ? AND id = ?", householdID, envelopeID).
		Take(&envelope)
	if result.Error != nil {
		return false, toDomainErr(result.Error)
	}

	var summary struct {
		Total int64
	}
	result = r.db.WithContext(ctx).Model(&reservationChangeModel{}).
		Select("COALESCE(SUM(envelope_delta_minor), 0) AS total").
		Where("household_id = ? AND envelope_id = ?", householdID, envelopeID).
		Scan(&summary)
	if result.Error != nil {
		return false, toDomainErr(result.Error)
	}
	return summary.Total != 0, nil
}
