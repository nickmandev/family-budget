// Package ports defines the persistence, transaction, clock and identifier
// seams between the domain/application core and its adapters (STACK_DECISIONS
// §7). It is a pure interface layer: the package imports only the standard
// library and internal/domain, never GORM, database/sql or any PostgreSQL
// driver. The GORM adapter in internal/adapters/postgres implements these
// interfaces; PostgreSQL integration tests arrive with that adapter.
//
// # Household isolation
//
// Every financial repository method takes a household id explicitly, and the
// caller must only ever see that household's rows. Implementations must put
// household_id into every query (or rely on an equivalent household-scoped
// key space) and must translate "row absent" and "row belongs to another
// household" to the same domain error,
// domain.CodeEntityNotFoundOrNotInHousehold. This keeps cross-household
// existence from leaking through the API. Metadata repositories (users) are the
// only global exception: users are the authentication identity shared across
// households.
//
// # Append-only journal
//
// JournalRepository has no update or delete methods. Journal operations and
// their posting/reservation/link rows are immutable after commit. Append must
// insert the operation, its postings, reservation changes, related links and
// the household ledger-version increment atomically; adapters must not expose
// a "fix up" path for committed rows.
//
// # Error translation
//
// Repository methods return the domain errors defined in internal/domain so
// the HTTP layer can map every persistence outcome uniformly. Adapters must
// translate driver errors rather than leak them:
//
//   - missing row, or a row in another household: ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD;
//   - an idempotency key reused with a different fingerprint:
//     IDEMPOTENCY_KEY_PAYLOAD_MISMATCH;
//   - stale ledger version, lost optimistic update, serialization failure or a
//     lock that cannot be acquired in the retry budget: CONCURRENCY_CONFLICT;
//   - duplicate operation id or malformed persisted values: INVALID_VALUE (or
//     the more specific domain code the value violates).
//
// Only CONCURRENCY_CONFLICT is retryable; other errors must surface unchanged.
//
// # Transaction boundary and lock order
//
// One command maps to exactly one UnitOfWork.WithinTransaction call
// (DOMAIN_MODEL §2). The domain CommandService's validate-on-copy-then-commit
// discipline becomes: lock/read the household ledger-version row, load the
// rows needed to revalidate, apply the validated effects, append the journal
// operation, and record idempotency — all inside the same database transaction.
//
// To avoid deadlocks, all adapters use one lock order within a transaction:
//
//  1. the household row that owns ledger_version (SELECT ... FOR UPDATE);
//  2. account rows, ordered deterministically by ascending account UUID;
//  3. envelope rows, ordered deterministically by ascending envelope UUID;
//  4. plan rows, ordered by ascending plan UUID.
//
// Never lock rows from two households in one transaction and never acquire
// these locks in a different order. PostgreSQL-specific locking belongs in the
// adapter, but the order is part of this port contract.
//
// # Cursor pagination
//
// JournalRepository.List uses committed_at ascending with the operation UUID as
// a unique tie-breaker, matching API_CONTRACT cursor pagination. A cursor is
// only a position inside the caller's household; adapters must never let a
// cursor read outside the authorized household and must cap the page size.
package ports

import (
	"context"
	"time"

	"family-budget/internal/domain"
)

// LifecycleStatus is the ACTIVE/ARCHIVED state shared by households, accounts
// and envelopes.
type LifecycleStatus string

const (
	// LifecycleActive marks a usable entity.
	LifecycleActive LifecycleStatus = "ACTIVE"
	// LifecycleArchived marks an entity kept for history but closed to new use.
	LifecycleArchived LifecycleStatus = "ARCHIVED"
)

// MembershipRole is the household-level role of a member.
type MembershipRole string

const (
	// MembershipRoleOwner may administer the household.
	MembershipRoleOwner MembershipRole = "OWNER"
	// MembershipRoleMember may use the shared budget.
	MembershipRoleMember MembershipRole = "MEMBER"
)

// MembershipStatus is the membership lifecycle state.
type MembershipStatus string

const (
	// MembershipStatusActive is a current member.
	MembershipStatusActive MembershipStatus = "ACTIVE"
	// MembershipStatusRemoved is a former member whose history remains.
	MembershipStatusRemoved MembershipStatus = "REMOVED"
)

// HouseholdRecord is the persistence-neutral household shape. The domain has
// no separate Household aggregate yet, so this port record carries the fields
// DOMAIN_MODEL §3.1 defines.
type HouseholdRecord struct {
	ID            domain.HouseholdId
	Name          string
	Currency      domain.Currency
	Timezone      string // IANA timezone name, validated by the application/config layer
	Status        LifecycleStatus
	LedgerVersion uint64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// UserRecord is the authentication identity of a household member. Users are
// global, not household-owned; DOMAIN_MODEL §3.2 keeps passwords and auth
// secrets out of the domain and stores only stable user references there.
type UserRecord struct {
	ID           domain.UserId
	Email        string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// MembershipRecord binds a user to a household. The household id is a method
// argument on MembershipRepository, not a field here.
type MembershipRecord struct {
	UserID    domain.UserId
	Role      MembershipRole
	Status    MembershipStatus
	JoinedAt  time.Time
	RemovedAt *time.Time
}

// UserMembershipRecord is a membership projected from the user's side. The
// authenticated-user endpoint (/api/v1/auth/me) needs every household a user
// belongs to, so unlike MembershipRecord it carries the household id. It is a
// read-only projection: membership administration is household-scoped.
type UserMembershipRecord struct {
	HouseholdID domain.HouseholdId
	Role        MembershipRole
	Status      MembershipStatus
	JoinedAt    time.Time
	RemovedAt   *time.Time
}

// SessionRecord is one opaque, revocable authentication session.
//
// TokenHash is the SHA-256 digest (hex) of the raw bearer token; the raw token
// is returned to the client once at login and never persisted or logged.
// ExpiresAt is an instant; RevokedAt is nil while the session is live. Sessions
// are global (a user may have several), so they carry user_id rather than a
// household id.
type SessionRecord struct {
	ID        string
	UserID    domain.UserId
	TokenHash string
	CreatedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// AccountRecord is the metadata needed for an account; balances are derived
// from account_postings and reservations, never stored here.
type AccountRecord struct {
	ID             domain.AccountId
	Name           string
	Kind           domain.AccountKind
	BudgetEligible bool
	Status         LifecycleStatus
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// EnvelopeRecord is the metadata needed for an envelope. There is no kind
// field because the domain core exposes no EnvelopeKind; envelope behavior is
// carried by its postings and, for a card-payment envelope, its optional
// linked liability account (DOMAIN_MODEL §3.4).
type EnvelopeRecord struct {
	ID                  domain.EnvelopeId
	Name                string
	Status              LifecycleStatus
	LinkedCardAccountID *domain.AccountId
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// AccountFilter narrows an account listing. The zero value lists active
// accounts of every kind.
type AccountFilter struct {
	Kinds           []domain.AccountKind
	IncludeArchived bool
}

// EnvelopeFilter narrows an envelope listing. The zero value lists active
// envelopes. A nil LinkedCardAccountID means every envelope.
type EnvelopeFilter struct {
	IncludeArchived     bool
	LinkedCardAccountID *domain.AccountId
}

// JournalFilter narrows an append-only journal listing. Empty/zero fields mean
// "no restriction". From and To are inclusive effective-date bounds. The zero
// Limit leaves the page size to the adapter's documented default; adapters
// enforce a maximum page size. Results are ordered by
// (committed_at, id) ascending.
type JournalFilter struct {
	Types  []domain.OperationType
	Source domain.TransactionSource
	From   *domain.EffectiveDate
	To     *domain.EffectiveDate
	Cursor *JournalCursor
	Limit  int
}

// JournalCursor is a deterministic journal position: the last returned
// operation's committed instant plus its unique operation id tie-breaker.
type JournalCursor struct {
	CommittedAt time.Time
	ID          domain.JournalOperationId
}

// HouseholdRepository stores household metadata. The ledger version is owned
// by JournalRepository.Append; SaveSettings must not change it, and changing
// currency/timezone requires the explicit application policy and audit that
// DOMAIN_MODEL §3.1 requires.
type HouseholdRepository interface {
	Create(ctx context.Context, household HouseholdRecord) error
	Get(ctx context.Context, id domain.HouseholdId) (HouseholdRecord, error)
	SaveSettings(ctx context.Context, household HouseholdRecord) error
}

// UserRepository stores global authentication identities.
type UserRepository interface {
	Create(ctx context.Context, user UserRecord) error
	Get(ctx context.Context, id domain.UserId) (UserRecord, error)
	FindByEmail(ctx context.Context, email string) (UserRecord, error)
}

// SessionRepository stores authentication sessions. Lookups are by token
// hash, never by raw token. Revoke is idempotent: revoking an already-revoked
// session is not an error. RevokeAllForUser revokes every live session for the
// user (used after a credential change or owner recovery).
type SessionRepository interface {
	Create(ctx context.Context, session SessionRecord) error
	FindByTokenHash(ctx context.Context, tokenHash string) (SessionRecord, error)
	Revoke(ctx context.Context, id string) error
	RevokeAllForUser(ctx context.Context, user domain.UserId, now time.Time) error
}

// MembershipRepository stores household membership and role state.
//
// ListForUser is the user-scoped projection used by /auth/me. Like every other
// read it must not expose memberships the caller cannot see; callers pass the
// authenticated user's id, which they obtained from a validated session.
type MembershipRepository interface {
	Add(ctx context.Context, household domain.HouseholdId, membership MembershipRecord) error
	Get(ctx context.Context, household domain.HouseholdId, user domain.UserId) (MembershipRecord, error)
	List(ctx context.Context, household domain.HouseholdId) ([]MembershipRecord, error)
	ListForUser(ctx context.Context, user domain.UserId) ([]UserMembershipRecord, error)
	Save(ctx context.Context, household domain.HouseholdId, membership MembershipRecord) error
}

// AccountRepository stores account metadata. Archiving an account with active
// reservations is a domain/service rule (DOMAIN_MODEL §3.3); the repository
// exposes HasActiveReservations for that check. Save must not rewrite financial
// history or balances.
type AccountRepository interface {
	Create(ctx context.Context, household domain.HouseholdId, account AccountRecord) error
	Get(ctx context.Context, household domain.HouseholdId, id domain.AccountId) (AccountRecord, error)
	List(ctx context.Context, household domain.HouseholdId, filter AccountFilter) ([]AccountRecord, error)
	Save(ctx context.Context, household domain.HouseholdId, account AccountRecord) error
	HasActiveReservations(ctx context.Context, household domain.HouseholdId, id domain.AccountId) (bool, error)
}

// EnvelopeRepository stores envelope metadata. Archiving is subject to the
// same domain/service rules as accounts (DOMAIN_MODEL §3.4: no active
// reservations and the balance policy is satisfied).
type EnvelopeRepository interface {
	Create(ctx context.Context, household domain.HouseholdId, envelope EnvelopeRecord) error
	Get(ctx context.Context, household domain.HouseholdId, id domain.EnvelopeId) (EnvelopeRecord, error)
	List(ctx context.Context, household domain.HouseholdId, filter EnvelopeFilter) ([]EnvelopeRecord, error)
	Save(ctx context.Context, household domain.HouseholdId, envelope EnvelopeRecord) error
	HasActiveReservations(ctx context.Context, household domain.HouseholdId, id domain.EnvelopeId) (bool, error)
}

// JournalRepository is the append-only committed-operation store.
//
// Append must insert exactly one operation and all of its postings,
// reservation changes and related links, and increment the household's
// ledger_version, in one database transaction. It must reject a duplicate
// operation id or idempotency key without changing state. A call to Append for
// an operation in another household must fail with
// ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD (or INVALID_VALUE for a household
// mismatch, matching domain.Journal.Append).
//
// List ordering and cursor rules are documented on JournalFilter.
type JournalRepository interface {
	Append(ctx context.Context, household domain.HouseholdId, op domain.JournalOperation) error
	Get(ctx context.Context, household domain.HouseholdId, id domain.JournalOperationId) (domain.JournalOperation, error)
	List(ctx context.Context, household domain.HouseholdId, filter JournalFilter) ([]domain.JournalOperation, error)
	Version(ctx context.Context, household domain.HouseholdId) (uint64, error)
}

// ReservationRepository exposes the active reservation holds Qe and Qa derived
// from append-only reservation_changes rows. Returned Money values are in the
// household currency and are zero when no hold is active. These are the reads
// spendable-balance queries and projection rebuilds need (Q1/Q2/Q6).
type ReservationRepository interface {
	ActiveEnvelopeHolds(ctx context.Context, household domain.HouseholdId, envelope domain.EnvelopeId) (domain.Money, error)
	ActiveAccountHolds(ctx context.Context, household domain.HouseholdId, account domain.AccountId) (domain.Money, error)
	HoldsForPlan(ctx context.Context, household domain.HouseholdId, plan domain.PlanId) (envelopeHold domain.Money, accountHold domain.Money, err error)
}

// PlanRepository loads and saves planned-operation aggregates.
//
// Save is guarded against concurrent lifecycle changes by the expected state:
// the adapter updates the plan only when its stored state still equals
// expected, and returns CONCURRENCY_CONFLICT when no row matches. This is a
// state guard rather than a row-version token because financial command
// serialization already happens on the household ledger-version row. Create
// inserts a new aggregate and must reject an existing id.
type PlanRepository interface {
	Create(ctx context.Context, household domain.HouseholdId, plan *domain.PlannedOperation) error
	Get(ctx context.Context, household domain.HouseholdId, id domain.PlanId) (*domain.PlannedOperation, error)
	Save(ctx context.Context, household domain.HouseholdId, plan *domain.PlannedOperation, expected domain.PlanState) error
}

// SuggestionRepository loads and saves suggestion aggregates. Save uses the
// same expected-state guard as PlanRepository; a suggestion has no ledger
// effects.
type SuggestionRepository interface {
	Create(ctx context.Context, household domain.HouseholdId, suggestion *domain.Suggestion) error
	Get(ctx context.Context, household domain.HouseholdId, id domain.SuggestionId) (*domain.Suggestion, error)
	Save(ctx context.Context, household domain.HouseholdId, suggestion *domain.Suggestion, expected domain.SuggestionState) error
}

// IdempotencyRepository mirrors domain.IdempotencyStore with an explicit
// household scope (the database key is (household_id, idempotency_key)).
//
// Begin is an atomic check-and-insert: a missing key stores the record and
// returns isNew=true; an existing key with the same fingerprint returns the
// existing record and isNew=false; an existing key with a different
// fingerprint returns IDEMPOTENCY_KEY_PAYLOAD_MISMATCH. Lookup returns
// ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD when absent.
type IdempotencyRepository interface {
	Begin(ctx context.Context, household domain.HouseholdId, key domain.IdempotencyKey, fingerprint domain.RequestFingerprint, operationID domain.JournalOperationId, now time.Time) (record domain.IdempotencyRecord, isNew bool, err error)
	Lookup(ctx context.Context, household domain.HouseholdId, key domain.IdempotencyKey) (domain.IdempotencyRecord, error)
}

// Repositories is the per-transaction repository set. An adapter constructs
// these over one transaction handle; no repository value may be used after the
// transaction callback returns.
type Repositories struct {
	Households   HouseholdRepository
	Users        UserRepository
	Sessions     SessionRepository
	Memberships  MembershipRepository
	Accounts     AccountRepository
	Envelopes    EnvelopeRepository
	Journal      JournalRepository
	Reservations ReservationRepository
	Plans        PlanRepository
	Suggestions  SuggestionRepository
	Idempotency  IdempotencyRepository
}

// UnitOfWork is the atomicity boundary (DOMAIN_MODEL §2). One command is one
// WithinTransaction call. The callback receives repositories bound to the
// active transaction; returning a non-nil error rolls the transaction back,
// and returning nil commits it. A CONCURRENCY_CONFLICT may be retried by the
// caller; other errors must not be.
type UnitOfWork interface {
	WithinTransaction(ctx context.Context, fn func(ctx context.Context, repos Repositories) error) error
}

// Clock is the test seam for recorded/committed timestamps. Adapters should
// return UTC instants; the application converts them to domain.RecordedAt.
type Clock interface {
	Now() time.Time
}

// IDGenerator is the test seam for new public resource identifiers. NewID must
// return a canonical UUID string; adapters wrap a UUID library and convert the
// result to the domain's string-backed strong id types at the boundary.
type IDGenerator interface {
	NewID() (string, error)
}
