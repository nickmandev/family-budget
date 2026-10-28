// Package application implements the household resource use cases that sit
// between the HTTP transport and the domain/persistence ports.
//
// The package owns household authorization (membership and role checks), the
// atomic unit-of-work boundary for every mutation, durable idempotency
// orchestration, and the translation of transport input into domain values. It
// never constructs ledger postings by hand: financial commands call the pure
// domain effect builders and checks, and every committed operation goes through
// domain.NewJournalOperation, so the domain remains the accounting authority.
//
// HTTP handlers remain thin (STACK_DECISIONS §5): decode, authenticate, call
// one application method, map the outcome.
package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// Dependencies is the explicit wiring for the application service.
type Dependencies struct {
	UnitOfWork ports.UnitOfWork
	Clock      ports.Clock
	IDs        ports.IDGenerator
}

// Service implements household, budget, transaction, plan and suggestion use
// cases.
type Service struct {
	uow   ports.UnitOfWork
	clock ports.Clock
	ids   ports.IDGenerator
}

// New validates dependencies and returns the service.
func New(deps Dependencies) (*Service, error) {
	switch {
	case deps.UnitOfWork == nil:
		return nil, errors.New("application: UnitOfWork is required")
	case deps.Clock == nil:
		return nil, errors.New("application: Clock is required")
	case deps.IDs == nil:
		return nil, errors.New("application: IDGenerator is required")
	}
	return &Service{uow: deps.UnitOfWork, clock: deps.Clock, ids: deps.IDs}, nil
}

// Mutation carries the audit and idempotency metadata every financial command
// needs. The transport fills it from the authenticated principal, the explicit
// effective date and the Idempotency-Key header.
type Mutation struct {
	Actor         domain.UserId
	Household     domain.HouseholdId
	EffectiveDate domain.EffectiveDate
	Key           domain.IdempotencyKey
	Fingerprint   domain.RequestFingerprint
	Reason        string
}

// AccountBalanceView is one account's derived balance state.
type AccountBalanceView struct {
	Account        ports.AccountRecord
	Posted         domain.Money
	Spendable      domain.Money
	Reserved       domain.Money
	CardLiability  bool
	BudgetEligible bool
}

// EnvelopeBalanceView is one envelope's derived balance state.
type EnvelopeBalanceView struct {
	Envelope  ports.EnvelopeRecord
	Balance   domain.Money
	Reserved  domain.Money
	Spendable domain.Money
}

// BalanceReport is the derived budgets view. It contains no stored balance
// columns; every value is derived from the append-only journal.
type BalanceReport struct {
	BudgetableCash domain.Money
	ReadyToAssign  domain.Money
	Accounts       []AccountBalanceView
	Envelopes      []EnvelopeBalanceView
	Resolution     domain.ResolutionStatus
	Deficits       []domain.BudgetDeficit
}

// authorizeMember verifies that actor is an active member of household.
// Non-membership is reported as ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD (404) so
// the API never reveals that another household exists.
func authorizeMember(ctx context.Context, repos ports.Repositories, household domain.HouseholdId, actor domain.UserId) (ports.MembershipRecord, error) {
	if household.IsZero() || actor.IsZero() {
		return ports.MembershipRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	membership, err := repos.Memberships.Get(ctx, household, actor)
	if err != nil {
		if domain.HasCode(err, domain.CodeEntityNotFoundOrNotInHousehold) {
			return ports.MembershipRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
		}
		return ports.MembershipRecord{}, err
	}
	if membership.Status != ports.MembershipStatusActive {
		return ports.MembershipRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return membership, nil
}

// authorizeOwner additionally requires the household OWNER role.
func authorizeOwner(ctx context.Context, repos ports.Repositories, household domain.HouseholdId, actor domain.UserId) (ports.MembershipRecord, error) {
	membership, err := authorizeMember(ctx, repos, household, actor)
	if err != nil {
		return ports.MembershipRecord{}, err
	}
	if membership.Role != ports.MembershipRoleOwner {
		return ports.MembershipRecord{}, domain.ErrHouseholdAccessDenied
	}
	return membership, nil
}

func (s *Service) newRecordedAt() domain.RecordedAt {
	return domain.NewRecordedAt(s.clock.Now().UTC())
}

func (s *Service) newID() (string, error) {
	id, err := s.ids.NewID()
	if err != nil {
		return "", err
	}
	if !isUUIDText(id) {
		return "", domain.NewError(domain.CodeInvalidValue, "generated id is not a UUID")
	}
	return strings.ToLower(id), nil
}

// isUUIDText mirrors the transport's UUID check without importing net/http.
func isUUIDText(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if value[i] != '-' {
				return false
			}
			continue
		}
		c := value[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// CreateHousehold creates a household and makes the creator its OWNER. This is
// the household bootstrap deferred from authentication.
func (s *Service) CreateHousehold(ctx context.Context, actor domain.UserId, name string, currency domain.Currency, timezone string) (ports.HouseholdRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ports.HouseholdRecord{}, domain.NewError(domain.CodeInvalidValue, "household name must not be empty")
	}
	if currency.IsZero() {
		return ports.HouseholdRecord{}, domain.NewError(domain.CodeInvalidValue, "household currency must be set")
	}
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil || location == nil {
		return ports.HouseholdRecord{}, domain.NewError(domain.CodeInvalidValue, "household timezone must be a valid IANA timezone")
	}
	rawID, err := s.newID()
	if err != nil {
		return ports.HouseholdRecord{}, err
	}
	householdID, err := domain.NewHouseholdId(rawID)
	if err != nil {
		return ports.HouseholdRecord{}, err
	}
	now := s.clock.Now().UTC()
	record := ports.HouseholdRecord{
		ID:            householdID,
		Name:          name,
		Currency:      currency,
		Timezone:      location.String(),
		Status:        ports.LifecycleActive,
		LedgerVersion: 0,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	err = s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if err := repos.Households.Create(ctx, record); err != nil {
			return err
		}
		return repos.Memberships.Add(ctx, householdID, ports.MembershipRecord{
			UserID:   actor,
			Role:     ports.MembershipRoleOwner,
			Status:   ports.MembershipStatusActive,
			JoinedAt: now,
		})
	})
	if err != nil {
		return ports.HouseholdRecord{}, err
	}
	return record, nil
}

// ListHouseholds returns the households the actor actively belongs to.
func (s *Service) ListHouseholds(ctx context.Context, actor domain.UserId) ([]ports.HouseholdRecord, error) {
	var records []ports.HouseholdRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		memberships, err := repos.Memberships.ListForUser(ctx, actor)
		if err != nil {
			return err
		}
		for _, membership := range memberships {
			if membership.Status != ports.MembershipStatusActive {
				continue
			}
			household, err := repos.Households.Get(ctx, membership.HouseholdID)
			if err != nil {
				return err
			}
			records = append(records, household)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

// GetHousehold returns one household the actor belongs to.
func (s *Service) GetHousehold(ctx context.Context, actor domain.UserId, household domain.HouseholdId) (ports.HouseholdRecord, error) {
	var record ports.HouseholdRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		got, err := repos.Households.Get(ctx, household)
		if err != nil {
			return err
		}
		record = got
		return nil
	})
	return record, err
}

// AddMember adds an existing user to the household. Owner-only.
func (s *Service) AddMember(ctx context.Context, actor domain.UserId, household domain.HouseholdId, email string, role ports.MembershipRole) (ports.MembershipRecord, error) {
	if role != ports.MembershipRoleOwner && role != ports.MembershipRoleMember {
		return ports.MembershipRecord{}, domain.NewErrorf(domain.CodeInvalidValue, "unknown membership role %q", role)
	}
	var record ports.MembershipRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeOwner(ctx, repos, household, actor); err != nil {
			return err
		}
		user, err := repos.Users.FindByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
		if err != nil {
			return err
		}
		record = ports.MembershipRecord{
			UserID:   user.ID,
			Role:     role,
			Status:   ports.MembershipStatusActive,
			JoinedAt: s.clock.Now().UTC(),
		}
		return repos.Memberships.Add(ctx, household, record)
	})
	return record, err
}

// RemoveMember marks a membership removed. Owner-only.
func (s *Service) RemoveMember(ctx context.Context, actor domain.UserId, household domain.HouseholdId, target domain.UserId) (ports.MembershipRecord, error) {
	var record ports.MembershipRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeOwner(ctx, repos, household, actor); err != nil {
			return err
		}
		current, err := repos.Memberships.Get(ctx, household, target)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		current.Status = ports.MembershipStatusRemoved
		current.RemovedAt = &now
		if err := repos.Memberships.Save(ctx, household, current); err != nil {
			return err
		}
		record = current
		return nil
	})
	return record, err
}

// ListMembers returns the household's memberships to any active member.
func (s *Service) ListMembers(ctx context.Context, actor domain.UserId, household domain.HouseholdId) ([]ports.MembershipRecord, error) {
	var records []ports.MembershipRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		got, err := repos.Memberships.List(ctx, household)
		if err != nil {
			return err
		}
		records = got
		return nil
	})
	return records, err
}

// CreateAccount creates account metadata. Balances are always derived from
// postings, so no balance column is written here.
func (s *Service) CreateAccount(ctx context.Context, actor domain.UserId, household domain.HouseholdId, name string, kind domain.AccountKind, eligible bool) (ports.AccountRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ports.AccountRecord{}, domain.NewError(domain.CodeInvalidValue, "account name must not be empty")
	}
	rawID, err := s.newID()
	if err != nil {
		return ports.AccountRecord{}, err
	}
	accountID, err := domain.NewAccountId(rawID)
	if err != nil {
		return ports.AccountRecord{}, err
	}
	if _, err := domain.NewAccountMeta(accountID, kind, eligible); err != nil {
		return ports.AccountRecord{}, err
	}
	now := s.clock.Now().UTC()
	record := ports.AccountRecord{
		ID:             accountID,
		Name:           name,
		Kind:           kind,
		BudgetEligible: eligible,
		Status:         ports.LifecycleActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	err = s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		return repos.Accounts.Create(ctx, household, record)
	})
	return record, err
}

// ListAccounts returns account metadata for the household.
func (s *Service) ListAccounts(ctx context.Context, actor domain.UserId, household domain.HouseholdId, includeArchived bool) ([]ports.AccountRecord, error) {
	var records []ports.AccountRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		got, err := repos.Accounts.List(ctx, household, ports.AccountFilter{IncludeArchived: includeArchived})
		if err != nil {
			return err
		}
		records = got
		return nil
	})
	return records, err
}

// UpdateAccount renames or archives account metadata. Eligibility is never
// changed here: changing budget eligibility is an explicit funding-plan flow
// (transfers across the eligibility boundary), not a metadata edit.
func (s *Service) UpdateAccount(ctx context.Context, actor domain.UserId, household domain.HouseholdId, id domain.AccountId, name *string, status *ports.LifecycleStatus, eligibility *bool) (ports.AccountRecord, error) {
	var record ports.AccountRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		current, err := repos.Accounts.Get(ctx, household, id)
		if err != nil {
			return err
		}
		if eligibility != nil && *eligibility != current.BudgetEligible {
			return domain.ErrAccountScopeChangeRequiresFundingPlan
		}
		if name != nil {
			trimmed := strings.TrimSpace(*name)
			if trimmed == "" {
				return domain.NewError(domain.CodeInvalidValue, "account name must not be empty")
			}
			current.Name = trimmed
		}
		if status != nil {
			if *status != ports.LifecycleActive && *status != ports.LifecycleArchived {
				return domain.NewErrorf(domain.CodeInvalidValue, "unknown lifecycle status %q", *status)
			}
			current.Status = *status
		}
		current.UpdatedAt = s.clock.Now().UTC()
		if err := repos.Accounts.Save(ctx, household, current); err != nil {
			return err
		}
		record = current
		return nil
	})
	return record, err
}

// CreateEnvelope creates envelope metadata.
func (s *Service) CreateEnvelope(ctx context.Context, actor domain.UserId, household domain.HouseholdId, name string, linkedCard *domain.AccountId) (ports.EnvelopeRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ports.EnvelopeRecord{}, domain.NewError(domain.CodeInvalidValue, "envelope name must not be empty")
	}
	rawID, err := s.newID()
	if err != nil {
		return ports.EnvelopeRecord{}, err
	}
	envelopeID, err := domain.NewEnvelopeId(rawID)
	if err != nil {
		return ports.EnvelopeRecord{}, err
	}
	now := s.clock.Now().UTC()
	record := ports.EnvelopeRecord{
		ID:                  envelopeID,
		Name:                name,
		Status:              ports.LifecycleActive,
		LinkedCardAccountID: linkedCard,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	err = s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		if linkedCard != nil && !linkedCard.IsZero() {
			account, err := repos.Accounts.Get(ctx, household, *linkedCard)
			if err != nil {
				return err
			}
			if account.Kind != domain.AccountKindCardLiability {
				return domain.NewError(domain.CodeInvalidValue, "linked card envelope requires a card-liability account")
			}
		}
		return repos.Envelopes.Create(ctx, household, record)
	})
	return record, err
}

// ListEnvelopes returns envelope metadata for the household.
func (s *Service) ListEnvelopes(ctx context.Context, actor domain.UserId, household domain.HouseholdId, includeArchived bool) ([]ports.EnvelopeRecord, error) {
	var records []ports.EnvelopeRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		got, err := repos.Envelopes.List(ctx, household, ports.EnvelopeFilter{IncludeArchived: includeArchived})
		if err != nil {
			return err
		}
		records = got
		return nil
	})
	return records, err
}

// UpdateEnvelope renames or archives envelope metadata.
func (s *Service) UpdateEnvelope(ctx context.Context, actor domain.UserId, household domain.HouseholdId, id domain.EnvelopeId, name *string, status *ports.LifecycleStatus) (ports.EnvelopeRecord, error) {
	var record ports.EnvelopeRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		current, err := repos.Envelopes.Get(ctx, household, id)
		if err != nil {
			return err
		}
		if name != nil {
			trimmed := strings.TrimSpace(*name)
			if trimmed == "" {
				return domain.NewError(domain.CodeInvalidValue, "envelope name must not be empty")
			}
			current.Name = trimmed
		}
		if status != nil {
			if *status != ports.LifecycleActive && *status != ports.LifecycleArchived {
				return domain.NewErrorf(domain.CodeInvalidValue, "unknown lifecycle status %q", *status)
			}
			current.Status = *status
		}
		current.UpdatedAt = s.clock.Now().UTC()
		if err := repos.Envelopes.Save(ctx, household, current); err != nil {
			return err
		}
		record = current
		return nil
	})
	return record, err
}

// Balances derives the full budget view from the append-only journal.
func (s *Service) Balances(ctx context.Context, actor domain.UserId, household domain.HouseholdId) (BalanceReport, error) {
	var report BalanceReport
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		snapshot, err := loadLedger(ctx, repos, household)
		if err != nil {
			return err
		}
		state := snapshot.state

		budgetable, err := state.BudgetableCash()
		if err != nil {
			return err
		}
		ready, err := state.ReadyToAssign()
		if err != nil {
			return err
		}
		resolution, err := state.ResolutionStatus()
		if err != nil {
			return err
		}
		deficits, err := state.Deficits()
		if err != nil {
			return err
		}
		report = BalanceReport{
			BudgetableCash: budgetable,
			ReadyToAssign:  ready,
			Resolution:     resolution,
			Deficits:       deficits,
		}

		for _, account := range snapshot.accounts {
			posted, err := state.PostedAccountBalance(account.ID)
			if err != nil {
				return err
			}
			spendable, err := state.SpendableAccountBalance(account.ID)
			if err != nil {
				return err
			}
			reserved, err := state.ActiveAccountReservations(account.ID)
			if err != nil {
				return err
			}
			report.Accounts = append(report.Accounts, AccountBalanceView{
				Account:        account,
				Posted:         posted,
				Spendable:      spendable,
				Reserved:       reserved,
				CardLiability:  account.Kind == domain.AccountKindCardLiability,
				BudgetEligible: account.BudgetEligible,
			})
		}
		for _, envelope := range snapshot.envelopes {
			balance, err := state.EnvelopeBalance(envelope.ID)
			if err != nil {
				return err
			}
			reserved, err := state.ActiveEnvelopeReservations(envelope.ID)
			if err != nil {
				return err
			}
			spendable, err := state.SpendableEnvelopeBalance(envelope.ID)
			if err != nil {
				return err
			}
			report.Envelopes = append(report.Envelopes, EnvelopeBalanceView{
				Envelope:  envelope,
				Balance:   balance,
				Reserved:  reserved,
				Spendable: spendable,
			})
		}
		return nil
	})
	return report, err
}

// Resolution returns the derived budget-resolution state (Q4 visibility).
func (s *Service) Resolution(ctx context.Context, actor domain.UserId, household domain.HouseholdId) (domain.ResolutionStatus, []domain.BudgetDeficit, error) {
	var status domain.ResolutionStatus
	var deficits []domain.BudgetDeficit
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		snapshot, err := loadLedger(ctx, repos, household)
		if err != nil {
			return err
		}
		status, err = snapshot.state.ResolutionStatus()
		if err != nil {
			return err
		}
		deficits, err = snapshot.state.Deficits()
		return err
	})
	return status, deficits, err
}

// loadLedger rebuilds the derived ledger from the append-only journal plus
// current account and envelope metadata. The household currency comes from the
// household record, so an empty journal is a valid starting state.
func loadLedger(ctx context.Context, repos ports.Repositories, household domain.HouseholdId) (*ledgerSnapshot, error) {
	record, err := repos.Households.Get(ctx, household)
	if err != nil {
		return nil, err
	}
	accounts, err := repos.Accounts.List(ctx, household, ports.AccountFilter{IncludeArchived: true})
	if err != nil {
		return nil, err
	}
	envelopes, err := repos.Envelopes.List(ctx, household, ports.EnvelopeFilter{IncludeArchived: true})
	if err != nil {
		return nil, err
	}
	metas := make([]domain.AccountMeta, 0, len(accounts))
	for _, account := range accounts {
		metas = append(metas, domain.AccountMeta{
			ID:             account.ID,
			Kind:           account.Kind,
			BudgetEligible: account.BudgetEligible,
		})
	}
	ids := make([]domain.EnvelopeId, 0, len(envelopes))
	for _, envelope := range envelopes {
		ids = append(ids, envelope.ID)
	}
	state, err := domain.NewLedgerState(record.Currency, metas, ids)
	if err != nil {
		return nil, err
	}
	operations, err := listAllJournal(ctx, repos, household)
	if err != nil {
		return nil, err
	}
	for _, op := range operations {
		if err := state.ApplyPostings(op.Postings()); err != nil {
			return nil, err
		}
		if err := state.ApplyReservationChanges(op.ReservationChanges()...); err != nil {
			return nil, err
		}
		if err := state.CheckInvariants(); err != nil {
			return nil, err
		}
	}
	return &ledgerSnapshot{
		household:  record,
		accounts:   accounts,
		envelopes:  envelopes,
		operations: operations,
		state:      state,
	}, nil
}

type ledgerSnapshot struct {
	household  ports.HouseholdRecord
	accounts   []ports.AccountRecord
	envelopes  []ports.EnvelopeRecord
	operations []domain.JournalOperation
	state      *domain.LedgerState
}

const journalRebuildPageSize = 200

// listAllJournal walks the cursor-paginated journal so rebuilding the ledger
// honours the adapter's maximum page size instead of issuing an unbounded read.
func listAllJournal(ctx context.Context, repos ports.Repositories, household domain.HouseholdId) ([]domain.JournalOperation, error) {
	var all []domain.JournalOperation
	var cursor *ports.JournalCursor
	for {
		page, err := repos.Journal.List(ctx, household, ports.JournalFilter{
			Cursor: cursor,
			Limit:  journalRebuildPageSize,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < journalRebuildPageSize {
			return all, nil
		}
		last := page[len(page)-1]
		cursor = &ports.JournalCursor{CommittedAt: last.CommittedAt().Time(), ID: last.ID()}
	}
}

// positiveFromMinor builds a positive domain amount in the household currency.
func positiveFromMinor(currency domain.Currency, minor int64) (domain.PositiveAmount, error) {
	if minor <= 0 {
		return domain.PositiveAmount{}, domain.NewErrorf(domain.CodeInvalidAmount, "amount must be greater than zero, got %d minor units", minor)
	}
	money, err := domain.NewMoney(minor, currency)
	if err != nil {
		return domain.PositiveAmount{}, err
	}
	return domain.NewPositiveAmount(money)
}

func signedFromMinor(currency domain.Currency, minor int64) (domain.SignedMoneyDelta, error) {
	money, err := domain.NewMoney(minor, currency)
	if err != nil {
		return domain.SignedMoneyDelta{}, err
	}
	return domain.NewSignedMoneyDelta(money.MinorUnits(), currency)
}

func zeroMoney(currency domain.Currency) (domain.Money, error) {
	return domain.ZeroMoney(currency)
}

func formatUnknownError(step string, err error) error {
	return fmt.Errorf("application: %s: %w", step, err)
}
