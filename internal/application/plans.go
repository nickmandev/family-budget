package application

import (
	"context"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// PlanTargetInput identifies the envelope (and optional cash account) a plan
// holds against.
type PlanTargetInput struct {
	EnvelopeID domain.EnvelopeId
	AccountID  domain.AccountId
}

// CreatePlanInput is the transport-independent plan creation request.
type CreatePlanInput struct {
	Kind        domain.PlanKind
	Target      PlanTargetInput
	AmountMinor int64
}

// ReallocationInput authorizes a plan with a same-operation reallocation from
// another envelope.
type ReallocationInput struct {
	Source      domain.EnvelopeId
	AmountMinor int64
}

// AuthorizePlanInput selects normal or emergency-override authorization.
type AuthorizePlanInput struct {
	Mode         domain.AuthorizationMode
	Reallocation *ReallocationInput
}

// CompletePlanInput supplies the final amount and payment-method shape.
type CompletePlanInput struct {
	FinalMinor        int64
	PurchaseKind      domain.PurchaseKind
	CardAccountID     domain.AccountId
	PaymentEnvelopeID domain.EnvelopeId
}

func validateTarget(ctx context.Context, repos ports.Repositories, household domain.HouseholdId, target PlanTargetInput) error {
	if target.EnvelopeID.IsZero() {
		return domain.NewError(domain.CodeInvalidValue, "plan target envelope id must not be empty")
	}
	if _, err := repos.Envelopes.Get(ctx, household, target.EnvelopeID); err != nil {
		return err
	}
	if !target.AccountID.IsZero() {
		if _, err := repos.Accounts.Get(ctx, household, target.AccountID); err != nil {
			return err
		}
	}
	return nil
}

// CreatePurchasePlan creates a DRAFT plan.
func (s *Service) CreatePurchasePlan(ctx context.Context, actor domain.UserId, household domain.HouseholdId, input CreatePlanInput) (*domain.PlannedOperation, error) {
	var plan *domain.PlannedOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		if err := validateTarget(ctx, repos, household, input.Target); err != nil {
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
		planID, err := domain.NewPlanId(rawID)
		if err != nil {
			return err
		}
		created, err := domain.NewPlannedOperation(planID, input.Kind, domain.ReservationTarget{
			PlanID:     planID,
			EnvelopeID: input.Target.EnvelopeID,
			AccountID:  input.Target.AccountID,
		}, amount)
		if err != nil {
			return err
		}
		if err := repos.Plans.Create(ctx, household, created); err != nil {
			return err
		}
		plan = created
		return nil
	})
	return plan, err
}

// ListPurchasePlans returns every plan in deterministic id order.
func (s *Service) ListPurchasePlans(ctx context.Context, actor domain.UserId, household domain.HouseholdId) ([]*domain.PlannedOperation, error) {
	var plans []*domain.PlannedOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		got, err := repos.Plans.List(ctx, household)
		if err != nil {
			return err
		}
		plans = got
		return nil
	})
	return plans, err
}

// GetPurchasePlan returns one plan.
func (s *Service) GetPurchasePlan(ctx context.Context, actor domain.UserId, household domain.HouseholdId, id domain.PlanId) (*domain.PlannedOperation, error) {
	var plan *domain.PlannedOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		got, err := repos.Plans.Get(ctx, household, id)
		if err != nil {
			return err
		}
		plan = got
		return nil
	})
	return plan, err
}

// AuthorizePurchasePlan authorizes a DRAFT plan, optionally with an atomic
// reallocation from another envelope.
func (s *Service) AuthorizePurchasePlan(ctx context.Context, mutation Mutation, id domain.PlanId, input AuthorizePlanInput) (*domain.PlannedOperation, domain.JournalOperation, error) {
	var plan *domain.PlannedOperation
	var operation domain.JournalOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		current, err := repos.Plans.Get(ctx, mutation.Household, id)
		if err != nil {
			return err
		}
		expected := current.State()
		var next *domain.PlannedOperation
		op, err := s.executeFinancial(ctx, repos, mutation, func(snapshot *ledgerSnapshot) (builtOperation, error) {
			if current.State() != domain.PlanStateDraft {
				return builtOperation{}, domain.NewErrorf(domain.CodeInvalidEntityState, "cannot authorize a plan in state %s", current.State())
			}
			amount := current.IntendedAmount()
			reason, err := overrideReasonFor(input.Mode, mutation.Reason)
			if err != nil {
				return builtOperation{}, err
			}
			var outcome domain.AuthorizationOutcome
			if input.Reallocation != nil {
				delta, err := positiveFromMinor(snapshot.household.Currency, input.Reallocation.AmountMinor)
				if err != nil {
					return builtOperation{}, err
				}
				outcome, err = domain.AuthorizeWithReallocation(snapshot.state, input.Reallocation.Source, current.Target(), delta, amount, input.Mode, reason)
				if err != nil {
					return builtOperation{}, err
				}
			} else {
				outcome, err = domain.AuthorizePurchase(snapshot.state, current.Target(), amount, input.Mode, reason)
				if err != nil {
					return builtOperation{}, err
				}
			}
			next = current
			if err := next.Authorize(outcome, amount, heldOnAccountFor(current.Target(), amount)); err != nil {
				return builtOperation{}, err
			}
			return builtOperation{
				opType:  domain.OpReservationCreate,
				source:  domain.TransactionSourcePlannedPurchase,
				amount:  &amount,
				reason:  mutation.Reason,
				effects: outcome.Effects,
			}, nil
		}, func() error {
			return repos.Plans.Save(ctx, mutation.Household, next, expected)
		})
		if err != nil {
			return err
		}
		operation = op
		saved, err := repos.Plans.Get(ctx, mutation.Household, id)
		if err != nil {
			return err
		}
		plan = saved
		return nil
	})
	return plan, operation, err
}

// ResizePurchasePlan adjusts an authorized plan's amount.
func (s *Service) ResizePurchasePlan(ctx context.Context, mutation Mutation, id domain.PlanId, newAmountMinor int64, mode domain.AuthorizationMode) (*domain.PlannedOperation, domain.JournalOperation, error) {
	var plan *domain.PlannedOperation
	var operation domain.JournalOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		current, err := repos.Plans.Get(ctx, mutation.Household, id)
		if err != nil {
			return err
		}
		expected := current.State()
		var next *domain.PlannedOperation
		op, err := s.executeFinancial(ctx, repos, mutation, func(snapshot *ledgerSnapshot) (builtOperation, error) {
			if current.State() != domain.PlanStateReserved && current.State() != domain.PlanStateReservedException {
				return builtOperation{}, domain.NewErrorf(domain.CodeInvalidEntityState, "cannot resize a plan in state %s", current.State())
			}
			newAmount, err := positiveFromMinor(snapshot.household.Currency, newAmountMinor)
			if err != nil {
				return builtOperation{}, err
			}
			comparison, err := newAmount.Money().Compare(current.HeldAmount().Money())
			if err != nil {
				return builtOperation{}, err
			}
			if comparison == 0 {
				return builtOperation{}, domain.NewError(domain.CodeInvalidAmount, "resize amount must differ from the currently held amount")
			}
			reason, err := overrideReasonFor(mode, mutation.Reason)
			if err != nil {
				return builtOperation{}, err
			}
			outcome, err := domain.ReauthorizeReservation(snapshot.state, current.Target(), current.HeldAmount(), newAmount, mode, reason)
			if err != nil {
				return builtOperation{}, err
			}
			newState := domain.PlanStateReserved
			if outcome.Mode == domain.AuthorizationModeEmergencyOverride {
				newState = domain.PlanStateReservedException
			}
			next = current
			if err := next.Adjust(outcome, newState, newAmount, newAmount, heldOnAccountFor(current.Target(), newAmount)); err != nil {
				return builtOperation{}, err
			}
			return builtOperation{
				opType:  domain.OpReservationResize,
				source:  domain.TransactionSourcePlannedPurchase,
				amount:  &newAmount,
				reason:  mutation.Reason,
				effects: outcome.Effects,
			}, nil
		}, func() error {
			return repos.Plans.Save(ctx, mutation.Household, next, expected)
		})
		if err != nil {
			return err
		}
		operation = op
		saved, err := repos.Plans.Get(ctx, mutation.Household, id)
		if err != nil {
			return err
		}
		plan = saved
		return nil
	})
	return plan, operation, err
}

// CancelPurchasePlan cancels a plan and releases any holds. Cancelling a DRAFT
// appends no journal operation.
func (s *Service) CancelPurchasePlan(ctx context.Context, mutation Mutation, id domain.PlanId) (*domain.PlannedOperation, domain.JournalOperation, error) {
	var plan *domain.PlannedOperation
	var operation domain.JournalOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, mutation.Household, mutation.Actor); err != nil {
			return err
		}
		current, err := repos.Plans.Get(ctx, mutation.Household, id)
		if err != nil {
			return err
		}
		if record, err := repos.Idempotency.Lookup(ctx, mutation.Household, mutation.Key); err == nil {
			if record.Fingerprint() != mutation.Fingerprint {
				return domain.NewError(domain.CodeIdempotencyKeyPayloadMismatch, "idempotency key was reused with a different request")
			}
			op, err := repos.Journal.Get(ctx, mutation.Household, record.OperationID())
			if err != nil {
				return err
			}
			operation = op
			saved, err := repos.Plans.Get(ctx, mutation.Household, id)
			if err != nil {
				return err
			}
			plan = saved
			return nil
		} else if !domain.HasCode(err, domain.CodeEntityNotFoundOrNotInHousehold) {
			return err
		}
		expected := current.State()
		next := current
		if err := next.Cancel(); err != nil {
			return err
		}
		effects, err := domain.PlanCancelEffects(next)
		if err != nil {
			return err
		}
		if effects.IsEmpty() {
			if err := repos.Plans.Save(ctx, mutation.Household, next, expected); err != nil {
				return err
			}
			plan = next
			return nil
		}
		var amount *domain.PositiveAmount
		if held := current.HeldAmount(); held.MinorUnits() > 0 {
			amount = &held
		}
		op, err := s.executeFinancial(ctx, repos, mutation, func(_ *ledgerSnapshot) (builtOperation, error) {
			return builtOperation{
				opType:  domain.OpReservationCancel,
				source:  domain.TransactionSourcePlannedPurchase,
				amount:  amount,
				effects: effects,
			}, nil
		}, func() error {
			return repos.Plans.Save(ctx, mutation.Household, next, expected)
		})
		if err != nil {
			return err
		}
		operation = op
		saved, err := repos.Plans.Get(ctx, mutation.Household, id)
		if err != nil {
			return err
		}
		plan = saved
		return nil
	})
	return plan, operation, err
}

// CompletePurchasePlan completes an authorized plan exactly once.
func (s *Service) CompletePurchasePlan(ctx context.Context, mutation Mutation, id domain.PlanId, input CompletePlanInput) (*domain.PlannedOperation, domain.JournalOperation, error) {
	var plan *domain.PlannedOperation
	var operation domain.JournalOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		current, err := repos.Plans.Get(ctx, mutation.Household, id)
		if err != nil {
			return err
		}
		expected := current.State()
		var next *domain.PlannedOperation
		op, err := s.executeFinancial(ctx, repos, mutation, func(snapshot *ledgerSnapshot) (builtOperation, error) {
			if current.State() != domain.PlanStateReserved && current.State() != domain.PlanStateReservedException {
				return builtOperation{}, domain.NewErrorf(domain.CodeInvalidEntityState, "cannot complete a plan in state %s", current.State())
			}
			final, err := positiveFromMinor(snapshot.household.Currency, input.FinalMinor)
			if err != nil {
				return builtOperation{}, err
			}
			purchase, err := completionPostings(current, final, input.PurchaseKind, input.CardAccountID, input.PaymentEnvelopeID)
			if err != nil {
				return builtOperation{}, err
			}
			effects, err := domain.AuthorizeCompletion(
				snapshot.state, current.Target(), current.HeldAmount(), current.HeldOnAccount(),
				current.AuthorizedAmount(), final, purchase,
			)
			if err != nil {
				return builtOperation{}, err
			}
			next = current
			if err := next.Complete(final, current.HeldAmount(), current.HeldOnAccount()); err != nil {
				return builtOperation{}, err
			}
			return builtOperation{
				opType:  domain.OpPurchaseComplete,
				source:  domain.TransactionSourcePlannedPurchase,
				amount:  &final,
				effects: effects,
			}, nil
		}, func() error {
			return repos.Plans.Save(ctx, mutation.Household, next, expected)
		})
		if err != nil {
			return err
		}
		operation = op
		saved, err := repos.Plans.Get(ctx, mutation.Household, id)
		if err != nil {
			return err
		}
		plan = saved
		return nil
	})
	return plan, operation, err
}

func overrideReasonFor(mode domain.AuthorizationMode, raw string) (domain.OverrideReason, error) {
	if mode != domain.AuthorizationModeEmergencyOverride {
		if !mode.IsValid() {
			return "", domain.NewErrorf(domain.CodeInvalidValue, "unknown authorization mode %q", mode)
		}
		return "", nil
	}
	return domain.NewOverrideReason(raw)
}

func heldOnAccountFor(target domain.ReservationTarget, amount domain.PositiveAmount) domain.PositiveAmount {
	if target.AccountID.IsZero() {
		return domain.PositiveAmount{}
	}
	return amount
}

func completionPostings(plan *domain.PlannedOperation, final domain.PositiveAmount, kind domain.PurchaseKind, card domain.AccountId, paymentEnvelope domain.EnvelopeId) (domain.Effects, error) {
	if !kind.IsValid() {
		return domain.Effects{}, domain.NewErrorf(domain.CodeInvalidValue, "unknown purchase kind %q", kind)
	}
	target := plan.Target()
	switch plan.Kind() {
	case domain.PlanKindRepayment:
		if kind != domain.PurchaseKindRepayment {
			return domain.Effects{}, domain.NewErrorf(domain.CodeInvalidValue, "a repayment plan must complete with REPAYMENT postings")
		}
	case domain.PlanKindPurchase:
		if kind == domain.PurchaseKindRepayment {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "a purchase plan cannot complete with REPAYMENT postings")
		}
	default:
		return domain.Effects{}, domain.NewErrorf(domain.CodeInvalidValue, "unknown plan kind %q", plan.Kind())
	}

	switch kind {
	case domain.PurchaseKindDebit, domain.PurchaseKindCash:
		if target.AccountID.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "debit/cash completion requires a source cash account")
		}
		if !card.IsZero() || !paymentEnvelope.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "debit/cash completion does not use a card account or payment envelope")
		}
		return domain.DebitPurchaseEffects(target.AccountID, target.EnvelopeID, final)
	case domain.PurchaseKindCard:
		if !target.AccountID.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "a card purchase plan holds only its spending envelope")
		}
		if card.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "card completion requires a card account")
		}
		if paymentEnvelope.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "card completion requires a card-payment envelope")
		}
		return domain.CreditCardPurchaseEffects(card, target.EnvelopeID, paymentEnvelope, final)
	case domain.PurchaseKindRepayment:
		if target.AccountID.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "a repayment plan requires a source cash account")
		}
		if card.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "repayment completion requires a card account")
		}
		if !paymentEnvelope.IsZero() && paymentEnvelope != target.EnvelopeID {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "repayment payment envelope must match the plan target")
		}
		return domain.CreditCardPaymentEffects(target.AccountID, card, target.EnvelopeID, final)
	default:
		return domain.Effects{}, domain.NewErrorf(domain.CodeInvalidValue, "unknown purchase kind %q", kind)
	}
}
