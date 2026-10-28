package application

import (
	"context"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// CreateSuggestionInput is the transport-independent suggestion creation
// request.
type CreateSuggestionInput struct {
	Source      domain.TransactionSource
	AmountMinor int64
	Merchant    string
}

// ConfirmSuggestionInput supplies the posting shape for confirming a factual
// external transaction.
type ConfirmSuggestionInput struct {
	PurchaseKind      domain.PurchaseKind
	AccountID         domain.AccountId
	EnvelopeID        domain.EnvelopeId
	PaymentEnvelopeID domain.EnvelopeId
}

// CreateSuggestion stores a non-financial candidate.
func (s *Service) CreateSuggestion(ctx context.Context, actor domain.UserId, household domain.HouseholdId, input CreateSuggestionInput) (*domain.Suggestion, error) {
	var suggestion *domain.Suggestion
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		snapshot, err := loadLedger(ctx, repos, household)
		if err != nil {
			return err
		}
		amount, err := positiveFromMinor(snapshot.household.Currency, input.AmountMinor)
		if err != nil {
			return err
		}
		rawID, err := s.newID()
		if err != nil {
			return err
		}
		suggestionID, err := domain.NewSuggestionId(rawID)
		if err != nil {
			return err
		}
		created, err := domain.NewSuggestion(suggestionID, input.Source, amount, input.Merchant)
		if err != nil {
			return err
		}
		if err := repos.Suggestions.Create(ctx, household, created); err != nil {
			return err
		}
		suggestion = created
		return nil
	})
	return suggestion, err
}

// ListSuggestions returns suggestions in deterministic id order.
func (s *Service) ListSuggestions(ctx context.Context, actor domain.UserId, household domain.HouseholdId) ([]*domain.Suggestion, error) {
	var suggestions []*domain.Suggestion
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		got, err := repos.Suggestions.List(ctx, household)
		if err != nil {
			return err
		}
		suggestions = got
		return nil
	})
	return suggestions, err
}

// EditSuggestion updates candidate fields before confirmation.
func (s *Service) EditSuggestion(ctx context.Context, actor domain.UserId, household domain.HouseholdId, id domain.SuggestionId, amountMinor int64, merchant *string) (*domain.Suggestion, error) {
	var suggestion *domain.Suggestion
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		current, err := repos.Suggestions.Get(ctx, household, id)
		if err != nil {
			return err
		}
		expected := current.State()
		snapshot, err := loadLedger(ctx, repos, household)
		if err != nil {
			return err
		}
		amount, err := positiveFromMinor(snapshot.household.Currency, amountMinor)
		if err != nil {
			return err
		}
		name := current.Merchant()
		if merchant != nil {
			name = *merchant
		}
		if err := current.Edit(amount, name); err != nil {
			return err
		}
		if err := repos.Suggestions.Save(ctx, household, current, expected); err != nil {
			return err
		}
		suggestion = current
		return nil
	})
	return suggestion, err
}

// CancelSuggestion dismisses a candidate.
func (s *Service) CancelSuggestion(ctx context.Context, actor domain.UserId, household domain.HouseholdId, id domain.SuggestionId, reason string) (*domain.Suggestion, error) {
	var suggestion *domain.Suggestion
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		current, err := repos.Suggestions.Get(ctx, household, id)
		if err != nil {
			return err
		}
		expected := current.State()
		if err := current.Cancel(reason); err != nil {
			return err
		}
		if err := repos.Suggestions.Save(ctx, household, current, expected); err != nil {
			return err
		}
		suggestion = current
		return nil
	})
	return suggestion, err
}

// ConfirmSuggestionAsExternalTransaction records the factual transaction and
// marks the suggestion confirmed in one atomic unit.
func (s *Service) ConfirmSuggestionAsExternalTransaction(ctx context.Context, mutation Mutation, id domain.SuggestionId, input ConfirmSuggestionInput) (*domain.Suggestion, domain.JournalOperation, error) {
	var suggestion *domain.Suggestion
	var operation domain.JournalOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		current, err := repos.Suggestions.Get(ctx, mutation.Household, id)
		if err != nil {
			return err
		}
		expected := current.State()
		var next *domain.Suggestion
		op, err := s.executeFinancial(ctx, repos, mutation, func(_ *ledgerSnapshot) (builtOperation, error) {
			amount := current.Amount()
			effects, err := suggestionExternalEffects(amount, input.PurchaseKind, input.AccountID, input.EnvelopeID, input.PaymentEnvelopeID)
			if err != nil {
				return builtOperation{}, err
			}
			next = current
			if err := next.Confirm(); err != nil {
				return builtOperation{}, err
			}
			return builtOperation{
				opType:  domain.OpExternalTransaction,
				source:  current.Source(),
				amount:  &amount,
				effects: effects,
			}, nil
		}, func() error {
			return repos.Suggestions.Save(ctx, mutation.Household, next, expected)
		})
		if err != nil {
			return err
		}
		operation = op
		saved, err := repos.Suggestions.Get(ctx, mutation.Household, id)
		if err != nil {
			return err
		}
		suggestion = saved
		return nil
	})
	return suggestion, operation, err
}

// ConvertSuggestionToPlannedPurchase routes a future purchase into the DRAFT
// plan lifecycle. It posts nothing and records no idempotency entry.
func (s *Service) ConvertSuggestionToPlannedPurchase(ctx context.Context, actor domain.UserId, household domain.HouseholdId, id domain.SuggestionId, kind domain.PlanKind, target PlanTargetInput) (*domain.Suggestion, *domain.PlannedOperation, error) {
	var suggestion *domain.Suggestion
	var plan *domain.PlannedOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		current, err := repos.Suggestions.Get(ctx, household, id)
		if err != nil {
			return err
		}
		if err := validateTarget(ctx, repos, household, target); err != nil {
			return err
		}
		expected := current.State()
		if expected != domain.SuggestionStateSuggested && expected != domain.SuggestionStateEdited {
			return domain.NewErrorf(domain.CodeInvalidEntityState, "cannot convert a suggestion in state %s", expected)
		}
		planID, err := domain.NewPlanId(current.ID().String())
		if err != nil {
			return err
		}
		created, err := domain.NewPlannedOperation(planID, kind, domain.ReservationTarget{
			PlanID:     planID,
			EnvelopeID: target.EnvelopeID,
			AccountID:  target.AccountID,
		}, current.Amount())
		if err != nil {
			return err
		}
		if err := current.Confirm(); err != nil {
			return err
		}
		if err := repos.Plans.Create(ctx, household, created); err != nil {
			return err
		}
		if err := repos.Suggestions.Save(ctx, household, current, expected); err != nil {
			return err
		}
		suggestion = current
		plan = created
		return nil
	})
	return suggestion, plan, err
}

func suggestionExternalEffects(amount domain.PositiveAmount, kind domain.PurchaseKind, account domain.AccountId, envelope, paymentEnvelope domain.EnvelopeId) (domain.Effects, error) {
	if !kind.IsValid() {
		return domain.Effects{}, domain.NewErrorf(domain.CodeInvalidValue, "unknown purchase kind %q", kind)
	}
	if envelope.IsZero() {
		return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "suggestion confirmation requires a spending envelope")
	}
	switch kind {
	case domain.PurchaseKindDebit, domain.PurchaseKindCash:
		if account.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "debit/cash suggestion confirmation requires a cash account")
		}
		if !paymentEnvelope.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "debit/cash suggestion confirmation does not use a payment envelope")
		}
		return domain.DebitPurchaseEffects(account, envelope, amount)
	case domain.PurchaseKindCard:
		if account.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "card suggestion confirmation requires a card account")
		}
		if paymentEnvelope.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "card suggestion confirmation requires a card-payment envelope")
		}
		return domain.CreditCardPurchaseEffects(account, envelope, paymentEnvelope, amount)
	case domain.PurchaseKindRepayment:
		return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "a suggestion cannot confirm a repayment")
	default:
		return domain.Effects{}, domain.NewErrorf(domain.CodeInvalidValue, "unknown purchase kind %q", kind)
	}
}
