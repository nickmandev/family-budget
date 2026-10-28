// Package postgres implements the persistence ports with GORM and PostgreSQL.
//
// The models in this file are adapter-local mappings of
// migrations/0001_initial_schema.sql. That migration is the schema authority:
// the adapter never runs AutoMigrate, and the model tags only describe existing
// columns. Domain accounting rules never live in GORM hooks; every balance-
// changing operation is an explicit transaction in the repository methods.
package postgres

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// householdModel maps households.
type householdModel struct {
	ID               pgtype.UUID `gorm:"column:id;primaryKey"`
	Name             string      `gorm:"column:name"`
	CurrencyCode     string      `gorm:"column:currency_code"`
	CurrencyExponent int16       `gorm:"column:currency_exponent"`
	Timezone         string      `gorm:"column:timezone"`
	LedgerVersion    int64       `gorm:"column:ledger_version"`
	Status           string      `gorm:"column:status"`
	CreatedAt        time.Time   `gorm:"column:created_at"`
	UpdatedAt        time.Time   `gorm:"column:updated_at"`
}

func (householdModel) TableName() string { return "households" }

// userModel maps users. Users are global, not household-owned.
type userModel struct {
	ID           pgtype.UUID `gorm:"column:id;primaryKey"`
	Email        string      `gorm:"column:email"`
	PasswordHash string      `gorm:"column:password_hash"`
	CreatedAt    time.Time   `gorm:"column:created_at"`
	UpdatedAt    time.Time   `gorm:"column:updated_at"`
}

func (userModel) TableName() string { return "users" }

// sessionModel maps sessions. Sessions are global authentication state keyed
// by user; only the token digest is stored.
type sessionModel struct {
	ID        pgtype.UUID `gorm:"column:id;primaryKey"`
	UserID    pgtype.UUID `gorm:"column:user_id"`
	TokenHash string      `gorm:"column:token_hash"`
	CreatedAt time.Time   `gorm:"column:created_at"`
	ExpiresAt time.Time   `gorm:"column:expires_at"`
	RevokedAt *time.Time  `gorm:"column:revoked_at"`
}

func (sessionModel) TableName() string { return "sessions" }

// membershipModel maps household_memberships.
type membershipModel struct {
	HouseholdID pgtype.UUID `gorm:"column:household_id;primaryKey"`
	UserID      pgtype.UUID `gorm:"column:user_id;primaryKey"`
	Role        string      `gorm:"column:role"`
	Status      string      `gorm:"column:status"`
	JoinedAt    time.Time   `gorm:"column:joined_at"`
	RemovedAt   *time.Time  `gorm:"column:removed_at"`
}

func (membershipModel) TableName() string { return "household_memberships" }

// accountModel maps accounts.
type accountModel struct {
	ID             pgtype.UUID `gorm:"column:id;primaryKey"`
	HouseholdID    pgtype.UUID `gorm:"column:household_id"`
	Name           string      `gorm:"column:name"`
	Kind           string      `gorm:"column:kind"`
	BudgetEligible bool        `gorm:"column:budget_eligible"`
	Status         string      `gorm:"column:status"`
	CreatedAt      time.Time   `gorm:"column:created_at"`
	UpdatedAt      time.Time   `gorm:"column:updated_at"`
}

func (accountModel) TableName() string { return "accounts" }

// envelopeModel maps envelopes. The linked card account is nullable.
type envelopeModel struct {
	ID                  pgtype.UUID `gorm:"column:id;primaryKey"`
	HouseholdID         pgtype.UUID `gorm:"column:household_id"`
	Name                string      `gorm:"column:name"`
	Status              string      `gorm:"column:status"`
	LinkedCardAccountID pgtype.UUID `gorm:"column:linked_card_account_id"`
	CreatedAt           time.Time   `gorm:"column:created_at"`
	UpdatedAt           time.Time   `gorm:"column:updated_at"`
}

func (envelopeModel) TableName() string { return "envelopes" }

// planModel maps plans. Zero amounts are stored as 0; nullable authorization
// fields use NULL because the schema allows "not authorized yet" to be
// explicit, even though the domain constructor sets the authorized amount to
// the intended amount for drafts.
type planModel struct {
	ID                     pgtype.UUID `gorm:"column:id;primaryKey"`
	HouseholdID            pgtype.UUID `gorm:"column:household_id"`
	Kind                   string      `gorm:"column:kind"`
	State                  string      `gorm:"column:state"`
	TargetEnvelopeID       pgtype.UUID `gorm:"column:target_envelope_id"`
	TargetAccountID        pgtype.UUID `gorm:"column:target_account_id"`
	IntendedAmountMinor    int64       `gorm:"column:intended_amount_minor"`
	AuthorizedAmountMinor  *int64      `gorm:"column:authorized_amount_minor"`
	Mode                   *string     `gorm:"column:mode"`
	Reason                 *string     `gorm:"column:reason"`
	HeldAmountMinor        int64       `gorm:"column:held_amount_minor"`
	HeldOnAccountMinor     int64       `gorm:"column:held_on_account_minor"`
	ReleasedAmountMinor    int64       `gorm:"column:released_amount_minor"`
	ReleasedOnAccountMinor int64       `gorm:"column:released_on_account_minor"`
	FinalAmountMinor       *int64      `gorm:"column:final_amount_minor"`
	CreatedAt              time.Time   `gorm:"column:created_at"`
	UpdatedAt              time.Time   `gorm:"column:updated_at"`
}

func (planModel) TableName() string { return "plans" }

// suggestionModel maps suggestions.
type suggestionModel struct {
	ID                pgtype.UUID `gorm:"column:id;primaryKey"`
	HouseholdID       pgtype.UUID `gorm:"column:household_id"`
	Source            string      `gorm:"column:source"`
	AmountMinor       int64       `gorm:"column:amount_minor"`
	Merchant          string      `gorm:"column:merchant"`
	State             string      `gorm:"column:state"`
	CancelReason      *string     `gorm:"column:cancel_reason"`
	ConfirmationCount int         `gorm:"column:confirmation_count"`
	CreatedAt         time.Time   `gorm:"column:created_at"`
	UpdatedAt         time.Time   `gorm:"column:updated_at"`
}

func (suggestionModel) TableName() string { return "suggestions" }

// journalOperationModel maps journal_operations. It deliberately has no
// UpdatedAt column: the table is append-only.
type journalOperationModel struct {
	ID                 pgtype.UUID `gorm:"column:id;primaryKey"`
	HouseholdID        pgtype.UUID `gorm:"column:household_id"`
	ActorID            pgtype.UUID `gorm:"column:actor_id"`
	OpType             string      `gorm:"column:op_type"`
	Source             string      `gorm:"column:source"`
	EffectiveDate      time.Time   `gorm:"column:effective_date;type:date"`
	CurrencyCode       string      `gorm:"column:currency_code"`
	CurrencyExponent   int16       `gorm:"column:currency_exponent"`
	AmountMinor        *int64      `gorm:"column:amount_minor"`
	Reason             *string     `gorm:"column:reason"`
	IdempotencyKey     string      `gorm:"column:idempotency_key"`
	RequestFingerprint string      `gorm:"column:request_fingerprint"`
	CreatedAt          time.Time   `gorm:"column:created_at"`
	CommittedAt        time.Time   `gorm:"column:committed_at"`
}

func (journalOperationModel) TableName() string { return "journal_operations" }

// accountPostingModel maps account_postings. ID is a generated identity key.
type accountPostingModel struct {
	ID                 int64       `gorm:"column:id;primaryKey;autoIncrement"`
	HouseholdID        pgtype.UUID `gorm:"column:household_id"`
	JournalOperationID pgtype.UUID `gorm:"column:journal_operation_id"`
	AccountID          pgtype.UUID `gorm:"column:account_id"`
	DeltaMinor         int64       `gorm:"column:delta_minor"`
}

func (accountPostingModel) TableName() string { return "account_postings" }

// envelopePostingModel maps envelope_postings. ID is a generated identity key.
type envelopePostingModel struct {
	ID                 int64       `gorm:"column:id;primaryKey;autoIncrement"`
	HouseholdID        pgtype.UUID `gorm:"column:household_id"`
	JournalOperationID pgtype.UUID `gorm:"column:journal_operation_id"`
	EnvelopeID         pgtype.UUID `gorm:"column:envelope_id"`
	DeltaMinor         int64       `gorm:"column:delta_minor"`
}

func (envelopePostingModel) TableName() string { return "envelope_postings" }

// reservationChangeModel maps reservation_changes. ID is a generated identity
// key; account_id and account_delta_minor are both NULL or both set.
type reservationChangeModel struct {
	ID                 int64       `gorm:"column:id;primaryKey;autoIncrement"`
	HouseholdID        pgtype.UUID `gorm:"column:household_id"`
	JournalOperationID pgtype.UUID `gorm:"column:journal_operation_id"`
	PlanID             pgtype.UUID `gorm:"column:plan_id"`
	EnvelopeID         pgtype.UUID `gorm:"column:envelope_id"`
	AccountID          pgtype.UUID `gorm:"column:account_id"`
	EnvelopeDeltaMinor int64       `gorm:"column:envelope_delta_minor"`
	AccountDeltaMinor  *int64      `gorm:"column:account_delta_minor"`
}

func (reservationChangeModel) TableName() string { return "reservation_changes" }

// relatedLinkModel maps related_links. Exactly one target column is set; the
// current domain only represents journal-operation targets, so the adapter
// writes target_operation_id and rejects plan-target rows on read.
type relatedLinkModel struct {
	ID                 int64       `gorm:"column:id;primaryKey;autoIncrement"`
	HouseholdID        pgtype.UUID `gorm:"column:household_id"`
	JournalOperationID pgtype.UUID `gorm:"column:journal_operation_id"`
	Kind               string      `gorm:"column:kind"`
	TargetOperationID  pgtype.UUID `gorm:"column:target_operation_id"`
	TargetPlanID       pgtype.UUID `gorm:"column:target_plan_id"`
}

func (relatedLinkModel) TableName() string { return "related_links" }

// idempotencyRecordModel maps idempotency_records. The composite primary key is
// (household_id, idempotency_key).
type idempotencyRecordModel struct {
	HouseholdID        pgtype.UUID `gorm:"column:household_id;primaryKey"`
	IdempotencyKey     string      `gorm:"column:idempotency_key;primaryKey"`
	RequestFingerprint string      `gorm:"column:request_fingerprint"`
	OperationID        pgtype.UUID `gorm:"column:operation_id"`
	CreatedAt          time.Time   `gorm:"column:created_at"`
}

func (idempotencyRecordModel) TableName() string { return "idempotency_records" }
