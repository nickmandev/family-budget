package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// membershipRepository implements ports.MembershipRepository.
type membershipRepository struct {
	db *gorm.DB
}

func (r *membershipRepository) Add(ctx context.Context, household domain.HouseholdId, membership ports.MembershipRecord) error {
	if membership.JoinedAt.IsZero() {
		membership.JoinedAt = time.Now().UTC()
	}
	row, err := membershipToModel(household, membership)
	if err != nil {
		return err
	}
	return toDomainErr(r.db.WithContext(ctx).Create(&row).Error)
}

func (r *membershipRepository) Get(ctx context.Context, household domain.HouseholdId, user domain.UserId) (ports.MembershipRecord, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return ports.MembershipRecord{}, err
	}
	userID, err := uuidFromDomain(user.String())
	if err != nil {
		return ports.MembershipRecord{}, err
	}
	var row membershipModel
	result := r.db.WithContext(ctx).
		Where("household_id = ? AND user_id = ?", householdID, userID).
		Take(&row)
	if result.Error != nil {
		return ports.MembershipRecord{}, toDomainErr(result.Error)
	}
	return membershipFromModel(row), nil
}

func (r *membershipRepository) List(ctx context.Context, household domain.HouseholdId) ([]ports.MembershipRecord, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return nil, err
	}
	var rows []membershipModel
	result := r.db.WithContext(ctx).
		Where("household_id = ?", householdID).
		Order("joined_at ASC, user_id ASC").
		Find(&rows)
	if result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	records := make([]ports.MembershipRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, membershipFromModel(row))
	}
	return records, nil
}

// ListForUser returns every membership held by one user across households. It
// is the /auth/me projection; ordering is deterministic for stable responses.
func (r *membershipRepository) ListForUser(ctx context.Context, user domain.UserId) ([]ports.UserMembershipRecord, error) {
	userID, err := uuidFromDomain(user.String())
	if err != nil {
		return nil, err
	}
	var rows []membershipModel
	result := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("joined_at ASC, household_id ASC").
		Find(&rows)
	if result.Error != nil {
		return nil, toDomainErr(result.Error)
	}
	records := make([]ports.UserMembershipRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, ports.UserMembershipRecord{
			HouseholdID: domain.HouseholdId(row.HouseholdID.String()),
			Role:        ports.MembershipRole(row.Role),
			Status:      ports.MembershipStatus(row.Status),
			JoinedAt:    row.JoinedAt.UTC(),
			RemovedAt:   utcTimePointer(row.RemovedAt),
		})
	}
	return records, nil
}

// Save updates role/status/removal only; joined_at is immutable history.
func (r *membershipRepository) Save(ctx context.Context, household domain.HouseholdId, membership ports.MembershipRecord) error {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return err
	}
	userID, err := uuidFromDomain(membership.UserID.String())
	if err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&membershipModel{}).
		Where("household_id = ? AND user_id = ?", householdID, userID).
		Updates(map[string]any{
			"role":       string(membership.Role),
			"status":     string(membership.Status),
			"removed_at": utcTimePointer(membership.RemovedAt),
		})
	if result.Error != nil {
		return toDomainErr(result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	return nil
}
