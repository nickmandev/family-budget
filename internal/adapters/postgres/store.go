package postgres

import (
	"context"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// Store is the PostgreSQL adapter root. It owns the GORM handle and implements
// the ports.UnitOfWork contract.
//
// Open does not run migrations or AutoMigrate: the reviewed SQL migration in
// migrations/ is the schema authority (STACK_DECISIONS §6).
type Store struct {
	db *gorm.DB
}

// Open connects to PostgreSQL using the supplied DSN and returns the adapter
// root.
func Open(dsn string) (*Store, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}
	return New(db), nil
}

// New wraps an existing GORM handle. It is primarily useful for tests and for
// composing the adapter with an application-owned connection pool.
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// DB exposes the underlying handle for adapter tests and wiring. Application
// code must use the repository ports, not this escape hatch.
func (s *Store) DB() *gorm.DB {
	return s.db
}

// Repositories returns the repository set bound to the store's non-transaction
// handle. Read-only queries may use it directly; every command must run through
// WithinTransaction.
func (s *Store) Repositories() ports.Repositories {
	return newRepositories(s.db)
}

// WithinTransaction runs fn with repositories bound to one database
// transaction. Returning a non-nil error rolls the transaction back; returning
// nil commits it. Nested calls become savepoints through GORM, but the port
// contract is one command = one top-level WithinTransaction call.
func (s *Store) WithinTransaction(ctx context.Context, fn func(ctx context.Context, repos ports.Repositories) error) error {
	// The transaction-scoped pending set lets IdempotencyRepository.Begin run
	// before JournalRepository.Append (the reference in-memory order) even
	// though the database foreign key only permits the record after the
	// operation exists. Append flushes the pending record inside the same
	// transaction.
	pending := newPendingIdempotencySet()
	ctx = context.WithValue(ctx, pendingIdempotencyContextKey{}, pending)
	return toDomainErr(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(ctx, newRepositories(tx))
	}))
}

// newRepositories binds every repository to one GORM handle (pool or tx).
func newRepositories(db *gorm.DB) ports.Repositories {
	return ports.Repositories{
		Households:   &householdRepository{db: db},
		Users:        &userRepository{db: db},
		Memberships:  &membershipRepository{db: db},
		Accounts:     &accountRepository{db: db},
		Envelopes:    &envelopeRepository{db: db},
		Journal:      &journalRepository{db: db},
		Reservations: &reservationRepository{db: db},
		Plans:        &planRepository{db: db},
		Suggestions:  &suggestionRepository{db: db},
		Idempotency:  &idempotencyRepository{db: db},
	}
}

// Compile-time port conformance checks.
var (
	_ ports.UnitOfWork            = (*Store)(nil)
	_ ports.HouseholdRepository   = (*householdRepository)(nil)
	_ ports.UserRepository        = (*userRepository)(nil)
	_ ports.MembershipRepository  = (*membershipRepository)(nil)
	_ ports.AccountRepository     = (*accountRepository)(nil)
	_ ports.EnvelopeRepository    = (*envelopeRepository)(nil)
	_ ports.JournalRepository     = (*journalRepository)(nil)
	_ ports.ReservationRepository = (*reservationRepository)(nil)
	_ ports.PlanRepository        = (*planRepository)(nil)
	_ ports.SuggestionRepository  = (*suggestionRepository)(nil)
	_ ports.IdempotencyRepository = (*idempotencyRepository)(nil)
)

// currencyForHousehold reads the owning household's currency. Plans,
// suggestions and reservations have no currency columns of their own and derive
// it here. A missing household is ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD.
func currencyForHousehold(ctx context.Context, db *gorm.DB, household string) (domain.Currency, error) {
	id, err := uuidFromDomain(household)
	if err != nil {
		return domain.Currency{}, err
	}
	var row struct {
		CurrencyCode     string
		CurrencyExponent int16
	}
	result := db.WithContext(ctx).Model(&householdModel{}).
		Select("currency_code, currency_exponent").
		Where("id = ?", id).
		Take(&row)
	if result.Error != nil {
		return domain.Currency{}, toDomainErr(result.Error)
	}
	return currencyFromRow(row.CurrencyCode, row.CurrencyExponent)
}
