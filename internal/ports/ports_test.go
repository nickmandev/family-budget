package ports_test

// These are intentionally light compile-time and contract smoke tests for the
// port layer. The ports have no logic to unit test; the important persistence
// behavior (constraints, transactions, locking, migrations) is exercised by the
// PostgreSQL adapter integration tests in the next task.
//
// The fakes below prove that each interface is satisfiable by ordinary Go
// values and that a unit-of-work implementation can hand the complete
// repository set to a command callback. The smoke tests pin the documented
// error-code translation contract: adapters return domain errors, not driver
// errors.

import (
	"context"
	"errors"
	"testing"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

var (
	_ ports.HouseholdRepository   = (*fakeHouseholdRepository)(nil)
	_ ports.UserRepository        = (*fakeUserRepository)(nil)
	_ ports.MembershipRepository  = (*fakeMembershipRepository)(nil)
	_ ports.AccountRepository     = (*fakeAccountRepository)(nil)
	_ ports.EnvelopeRepository    = (*fakeEnvelopeRepository)(nil)
	_ ports.JournalRepository     = (*fakeJournalRepository)(nil)
	_ ports.ReservationRepository = (*fakeReservationRepository)(nil)
	_ ports.PlanRepository        = (*fakePlanRepository)(nil)
	_ ports.SuggestionRepository  = (*fakeSuggestionRepository)(nil)
	_ ports.IdempotencyRepository = (*fakeIdempotencyRepository)(nil)
	_ ports.UnitOfWork            = (*fakeUnitOfWork)(nil)
	_ ports.Clock                 = fakeClock{}
	_ ports.IDGenerator           = fakeIDGenerator{}
)

type fakeHouseholdRepository struct{}

func (*fakeHouseholdRepository) Create(context.Context, ports.HouseholdRecord) error { return nil }
func (*fakeHouseholdRepository) Get(context.Context, domain.HouseholdId) (ports.HouseholdRecord, error) {
	return ports.HouseholdRecord{}, domain.NewError(domain.CodeEntityNotFoundOrNotInHousehold, "fake household")
}
func (*fakeHouseholdRepository) SaveSettings(context.Context, ports.HouseholdRecord) error {
	return nil
}

type fakeUserRepository struct{}

func (*fakeUserRepository) Create(context.Context, ports.UserRecord) error { return nil }
func (*fakeUserRepository) Get(context.Context, domain.UserId) (ports.UserRecord, error) {
	return ports.UserRecord{}, domain.NewError(domain.CodeEntityNotFoundOrNotInHousehold, "fake user")
}
func (*fakeUserRepository) FindByEmail(context.Context, string) (ports.UserRecord, error) {
	return ports.UserRecord{}, domain.NewError(domain.CodeEntityNotFoundOrNotInHousehold, "fake user")
}

type fakeMembershipRepository struct{}

func (*fakeMembershipRepository) Add(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}
func (*fakeMembershipRepository) Get(context.Context, domain.HouseholdId, domain.UserId) (ports.MembershipRecord, error) {
	return ports.MembershipRecord{}, domain.NewError(domain.CodeEntityNotFoundOrNotInHousehold, "fake membership")
}
func (*fakeMembershipRepository) List(context.Context, domain.HouseholdId) ([]ports.MembershipRecord, error) {
	return nil, nil
}
func (*fakeMembershipRepository) Save(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}

type fakeAccountRepository struct{}

func (*fakeAccountRepository) Create(context.Context, domain.HouseholdId, ports.AccountRecord) error {
	return nil
}
func (*fakeAccountRepository) Get(context.Context, domain.HouseholdId, domain.AccountId) (ports.AccountRecord, error) {
	return ports.AccountRecord{}, domain.NewError(domain.CodeEntityNotFoundOrNotInHousehold, "fake account")
}
func (*fakeAccountRepository) List(context.Context, domain.HouseholdId, ports.AccountFilter) ([]ports.AccountRecord, error) {
	return nil, nil
}
func (*fakeAccountRepository) Save(context.Context, domain.HouseholdId, ports.AccountRecord) error {
	return nil
}
func (*fakeAccountRepository) HasActiveReservations(context.Context, domain.HouseholdId, domain.AccountId) (bool, error) {
	return false, nil
}

type fakeEnvelopeRepository struct{}

func (*fakeEnvelopeRepository) Create(context.Context, domain.HouseholdId, ports.EnvelopeRecord) error {
	return nil
}
func (*fakeEnvelopeRepository) Get(context.Context, domain.HouseholdId, domain.EnvelopeId) (ports.EnvelopeRecord, error) {
	return ports.EnvelopeRecord{}, domain.NewError(domain.CodeEntityNotFoundOrNotInHousehold, "fake envelope")
}
func (*fakeEnvelopeRepository) List(context.Context, domain.HouseholdId, ports.EnvelopeFilter) ([]ports.EnvelopeRecord, error) {
	return nil, nil
}
func (*fakeEnvelopeRepository) Save(context.Context, domain.HouseholdId, ports.EnvelopeRecord) error {
	return nil
}
func (*fakeEnvelopeRepository) HasActiveReservations(context.Context, domain.HouseholdId, domain.EnvelopeId) (bool, error) {
	return false, nil
}

type fakeJournalRepository struct{}

func (*fakeJournalRepository) Append(context.Context, domain.HouseholdId, domain.JournalOperation) error {
	return nil
}
func (*fakeJournalRepository) Get(context.Context, domain.HouseholdId, domain.JournalOperationId) (domain.JournalOperation, error) {
	return domain.JournalOperation{}, domain.NewError(domain.CodeEntityNotFoundOrNotInHousehold, "fake journal operation")
}
func (*fakeJournalRepository) List(context.Context, domain.HouseholdId, ports.JournalFilter) ([]domain.JournalOperation, error) {
	return nil, nil
}
func (*fakeJournalRepository) Version(context.Context, domain.HouseholdId) (uint64, error) {
	return 0, nil
}

type fakeReservationRepository struct{}

func (*fakeReservationRepository) ActiveEnvelopeHolds(context.Context, domain.HouseholdId, domain.EnvelopeId) (domain.Money, error) {
	return domain.Money{}, nil
}
func (*fakeReservationRepository) ActiveAccountHolds(context.Context, domain.HouseholdId, domain.AccountId) (domain.Money, error) {
	return domain.Money{}, nil
}
func (*fakeReservationRepository) HoldsForPlan(context.Context, domain.HouseholdId, domain.PlanId) (domain.Money, domain.Money, error) {
	return domain.Money{}, domain.Money{}, nil
}

type fakePlanRepository struct{}

func (*fakePlanRepository) Create(context.Context, domain.HouseholdId, *domain.PlannedOperation) error {
	return nil
}
func (*fakePlanRepository) Get(context.Context, domain.HouseholdId, domain.PlanId) (*domain.PlannedOperation, error) {
	return nil, domain.NewError(domain.CodeEntityNotFoundOrNotInHousehold, "fake plan")
}
func (*fakePlanRepository) Save(context.Context, domain.HouseholdId, *domain.PlannedOperation, domain.PlanState) error {
	return nil
}

type fakeSuggestionRepository struct{}

func (*fakeSuggestionRepository) Create(context.Context, domain.HouseholdId, *domain.Suggestion) error {
	return nil
}
func (*fakeSuggestionRepository) Get(context.Context, domain.HouseholdId, domain.SuggestionId) (*domain.Suggestion, error) {
	return nil, domain.NewError(domain.CodeEntityNotFoundOrNotInHousehold, "fake suggestion")
}
func (*fakeSuggestionRepository) Save(context.Context, domain.HouseholdId, *domain.Suggestion, domain.SuggestionState) error {
	return nil
}

type fakeIdempotencyRepository struct{}

func (*fakeIdempotencyRepository) Begin(context.Context, domain.HouseholdId, domain.IdempotencyKey, domain.RequestFingerprint, domain.JournalOperationId, time.Time) (domain.IdempotencyRecord, bool, error) {
	return domain.IdempotencyRecord{}, false, domain.NewError(
		domain.CodeIdempotencyKeyPayloadMismatch,
		"fake idempotency mismatch",
	)
}
func (*fakeIdempotencyRepository) Lookup(context.Context, domain.HouseholdId, domain.IdempotencyKey) (domain.IdempotencyRecord, error) {
	return domain.IdempotencyRecord{}, domain.NewError(domain.CodeEntityNotFoundOrNotInHousehold, "fake idempotency record")
}

type fakeUnitOfWork struct {
	repos ports.Repositories
}

func (u *fakeUnitOfWork) WithinTransaction(ctx context.Context, fn func(context.Context, ports.Repositories) error) error {
	if u.repos.Journal == nil {
		u.repos = ports.Repositories{
			Households:   &fakeHouseholdRepository{},
			Users:        &fakeUserRepository{},
			Memberships:  &fakeMembershipRepository{},
			Accounts:     &fakeAccountRepository{},
			Envelopes:    &fakeEnvelopeRepository{},
			Journal:      &fakeJournalRepository{},
			Reservations: &fakeReservationRepository{},
			Plans:        &fakePlanRepository{},
			Suggestions:  &fakeSuggestionRepository{},
			Idempotency:  &fakeIdempotencyRepository{},
		}
	}
	return fn(ctx, u.repos)
}

type fakeClock struct{}

func (fakeClock) Now() time.Time {
	return time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
}

type fakeIDGenerator struct{}

func (fakeIDGenerator) NewID() (string, error) {
	return "00000000-0000-7000-8000-000000000001", nil
}

// TestUnitOfWorkPropagatesDomainErrors proves the callback's domain error
// survives the transaction boundary; adapters must not replace it with a
// driver-specific error.
func TestUnitOfWorkPropagatesDomainErrors(t *testing.T) {
	uow := &fakeUnitOfWork{}
	household := domain.HouseholdId("household-1")
	operation := domain.JournalOperationId("operation-1")

	err := uow.WithinTransaction(context.Background(), func(ctx context.Context, repos ports.Repositories) error {
		_, getErr := repos.Journal.Get(ctx, household, operation)
		return getErr
	})
	if !domain.HasCode(err, domain.CodeEntityNotFoundOrNotInHousehold) {
		t.Fatalf("WithinTransaction error = %v, want code %s", err, domain.CodeEntityNotFoundOrNotInHousehold)
	}
	if !errors.Is(err, domain.ErrEntityNotFoundOrNotInHousehold) {
		t.Fatalf("WithinTransaction error = %v, want errors.Is domain.ErrEntityNotFoundOrNotInHousehold", err)
	}
}

// TestIdempotencyMismatchContract pins the mismatch code adapters must return
// when a household key is reused with a different request fingerprint.
func TestIdempotencyMismatchContract(t *testing.T) {
	repo := &fakeIdempotencyRepository{}
	_, _, err := repo.Begin(
		context.Background(),
		domain.HouseholdId("household-1"),
		domain.IdempotencyKey("key-1"),
		domain.RequestFingerprint("abcdef0123456789"),
		domain.JournalOperationId("operation-1"),
		time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC),
	)
	if !domain.HasCode(err, domain.CodeIdempotencyKeyPayloadMismatch) {
		t.Fatalf("Begin error = %v, want code %s", err, domain.CodeIdempotencyKeyPayloadMismatch)
	}
}

// TestClockAndIDGeneratorSeams checks the small infrastructure ports are
// usable without a database or a UUID dependency in this package.
func TestClockAndIDGeneratorSeams(t *testing.T) {
	var clock ports.Clock = fakeClock{}
	if clock.Now().IsZero() {
		t.Fatal("Clock.Now returned the zero time")
	}

	var ids ports.IDGenerator = fakeIDGenerator{}
	id, err := ids.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if len(id) != 36 {
		t.Fatalf("NewID length = %d, want a 36-character UUID string", len(id))
	}
}
