package postgres

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// sessionRepository implements ports.SessionRepository. Sessions are global
// authentication state and are not household-scoped.
type sessionRepository struct {
	db *gorm.DB
}

func (r *sessionRepository) Create(ctx context.Context, session ports.SessionRecord) error {
	if strings.TrimSpace(session.TokenHash) == "" {
		return domain.NewError(domain.CodeInvalidValue, "session token hash must not be empty")
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}
	if session.ExpiresAt.IsZero() || !session.ExpiresAt.After(session.CreatedAt) {
		return domain.NewError(domain.CodeInvalidValue, "session expiry must be after creation")
	}
	id, err := uuidFromDomain(session.ID)
	if err != nil {
		return err
	}
	userID, err := uuidFromDomain(session.UserID.String())
	if err != nil {
		return err
	}
	row := sessionModel{
		ID:        id,
		UserID:    userID,
		TokenHash: session.TokenHash,
		CreatedAt: session.CreatedAt.UTC(),
		ExpiresAt: session.ExpiresAt.UTC(),
		RevokedAt: utcTimePointer(session.RevokedAt),
	}
	return toDomainErr(r.db.WithContext(ctx).Create(&row).Error)
}

// FindByTokenHash returns the session for a digest. Expiry and revocation are
// enforcement decisions for the auth service, which needs to distinguish a
// live session from an expired/revoked one for the same generic response.
func (r *sessionRepository) FindByTokenHash(ctx context.Context, tokenHash string) (ports.SessionRecord, error) {
	if strings.TrimSpace(tokenHash) == "" {
		return ports.SessionRecord{}, domain.NewError(domain.CodeInvalidValue, "session token hash must not be empty")
	}
	var row sessionModel
	result := r.db.WithContext(ctx).Where("token_hash = ?", tokenHash).Take(&row)
	if result.Error != nil {
		return ports.SessionRecord{}, toDomainErr(result.Error)
	}
	return sessionFromModel(row), nil
}

// Revoke marks exactly one session revoked. It is idempotent: a session that is
// already revoked (or an unknown id) leaves accepted state unchanged.
func (r *sessionRepository) Revoke(ctx context.Context, id string) error {
	sessionID, err := uuidFromDomain(id)
	if err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&sessionModel{}).
		Where("id = ? AND revoked_at IS NULL", sessionID).
		Update("revoked_at", time.Now().UTC())
	return toDomainErr(result.Error)
}

// RevokeAllForUser revokes every live session belonging to user. It is used
// after a credential change or owner-recovery action, not by logout.
func (r *sessionRepository) RevokeAllForUser(ctx context.Context, user domain.UserId, now time.Time) error {
	userID, err := uuidFromDomain(user.String())
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result := r.db.WithContext(ctx).Model(&sessionModel{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", now.UTC())
	return toDomainErr(result.Error)
}

func sessionFromModel(row sessionModel) ports.SessionRecord {
	return ports.SessionRecord{
		ID:        uuidString(row.ID),
		UserID:    domain.UserId(row.UserID.String()),
		TokenHash: row.TokenHash,
		CreatedAt: row.CreatedAt.UTC(),
		ExpiresAt: row.ExpiresAt.UTC(),
		RevokedAt: utcTimePointer(row.RevokedAt),
	}
}
