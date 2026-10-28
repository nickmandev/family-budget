package postgres

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// userRepository implements ports.UserRepository. Users are global (the
// authentication identity), so these methods are not household-scoped.
type userRepository struct {
	db *gorm.DB
}

func (r *userRepository) Create(ctx context.Context, user ports.UserRecord) error {
	now := time.Now().UTC()
	if user.CreatedAt.IsZero() {
		user.CreatedAt = now
	}
	if user.UpdatedAt.IsZero() {
		user.UpdatedAt = user.CreatedAt
	}
	row, err := userToModel(user)
	if err != nil {
		return err
	}
	return toDomainErr(r.db.WithContext(ctx).Create(&row).Error)
}

func (r *userRepository) Get(ctx context.Context, id domain.UserId) (ports.UserRecord, error) {
	uuid, err := uuidFromDomain(id.String())
	if err != nil {
		return ports.UserRecord{}, err
	}
	var row userModel
	result := r.db.WithContext(ctx).Where("id = ?", uuid).Take(&row)
	if result.Error != nil {
		return ports.UserRecord{}, toDomainErr(result.Error)
	}
	return userFromModel(row), nil
}

// FindByEmail performs the same case-insensitive lookup as the
// users_email_lower_key unique index.
func (r *userRepository) FindByEmail(ctx context.Context, email string) (ports.UserRecord, error) {
	var row userModel
	result := r.db.WithContext(ctx).Where("lower(email) = lower(?)", strings.TrimSpace(email)).Take(&row)
	if result.Error != nil {
		return ports.UserRecord{}, toDomainErr(result.Error)
	}
	return userFromModel(row), nil
}
