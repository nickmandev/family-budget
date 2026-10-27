// This file implements the suggestion lifecycle commands on top of
// CommandService (STATE_TRANSITIONS §4, POSTING_MATRIX §4.3). Suggestions are
// unconfirmed candidates: creating, editing and cancelling one has no financial
// effect. Confirmation is where a suggestion meets the ledger:
//
//   - an already-completed external purchase is recorded truthfully through the
//     shared posting path with NO funding checks (Q3), and an underfunded
//     result surfaces through Deficits/ResolutionStatus afterwards;
//   - a future planned purchase is converted into a DRAFT plan instead, so
//     confirmation of details alone never bypasses purchase approval
//     (STATE_TRANSITIONS §4).
//
// Atomicity and idempotency follow plan_commands.go: the external-transaction
// path appends a journal operation and therefore records IdempotencyStore keys.
// Create/edit/cancel and conversion append nothing and record no idempotency
// entry.
//
// The Suggestion aggregate has no fields for the posting account or envelopes,
// so the external-transaction command receives them explicitly. A CONFIRMED
// suggestion is terminal for the service: a retry must reuse the original
// idempotency key, and a second confirmation with a new key is
// INVALID_ENTITY_STATE rather than a second posting.
package domain

// CreateSuggestion stores a SUGGESTED suggestion. No ledger effect, no
// reservation, no journal append (POSTING_MATRIX draft/suggestion row).
func (s *CommandService) CreateSuggestion(actor UserId, id SuggestionId, source TransactionSource, amount PositiveAmount, merchant string) (*Suggestion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actor.IsZero() {
		return nil, NewError(CodeInvalidValue, "suggestion actor id must not be empty")
	}
	if _, exists := s.suggestions[id]; exists {
		return nil, NewErrorf(CodeInvalidValue, "suggestion %q already exists", id)
	}
	suggestion, err := NewSuggestion(id, source, amount, merchant)
	if err != nil {
		return nil, err
	}
	s.suggestions[id] = suggestion
	return cloneSuggestion(suggestion), nil
}

// EditSuggestion updates candidate fields (SUGGESTED/EDITED only).
func (s *CommandService) EditSuggestion(actor UserId, id SuggestionId, amount PositiveAmount, merchant string) (*Suggestion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actor.IsZero() {
		return nil, NewError(CodeInvalidValue, "suggestion actor id must not be empty")
	}
	current, err := s.requireSuggestionLocked(id)
	if err != nil {
		return nil, err
	}
	next := cloneSuggestion(current)
	if err := next.Edit(amount, merchant); err != nil {
		return nil, err
	}
	s.suggestions[id] = next
	return cloneSuggestion(next), nil
}

// CancelSuggestion dismisses a suggestion (preserves its fingerprint fields).
func (s *CommandService) CancelSuggestion(actor UserId, id SuggestionId, reason string) (*Suggestion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actor.IsZero() {
		return nil, NewError(CodeInvalidValue, "suggestion actor id must not be empty")
	}
	current, err := s.requireSuggestionLocked(id)
	if err != nil {
		return nil, err
	}
	next := cloneSuggestion(current)
	if err := next.Cancel(reason); err != nil {
		return nil, err
	}
	s.suggestions[id] = next
	return cloneSuggestion(next), nil
}

// ConfirmSuggestionAsExternalTransaction confirms a factual already-completed
// external transaction (Q3): it records it truthfully through the shared posting
// path WITHOUT funding checks, marks the suggestion CONFIRMED and appends the
// journal operation atomically. Underfunding is surfaced via Deficits() /
// ResolutionStatus afterwards, never rejected. purchaseKind selects the posting
// shape; REPAYMENT is invalid here (it belongs to the planned-repayment flow).
//
// account is the debit/cash account or the card account depending on
// purchaseKind, envelope is the spending envelope, and paymentEnvelope is the
// card-payment envelope for CARD postings.
func (s *CommandService) ConfirmSuggestionAsExternalTransaction(env CommandEnvelope, id SuggestionId, purchaseKind PurchaseKind, account AccountId, envelope, paymentEnvelope EnvelopeId) (*Suggestion, JournalOperation, error) {
	return executeLifecycle(s, env, lifecycleCommand{
		opType: OpExternalTransaction,
		prepare: func(_ *LedgerState) (lifecycleOutcome, error) {
			current, err := s.requireSuggestionLocked(id)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			if current.State() == SuggestionStateConfirmed {
				return lifecycleOutcome{}, NewError(CodeInvalidEntityState, "suggestion is already confirmed; replay requires the original idempotency key")
			}
			amount := current.Amount()
			effects, err := suggestionExternalEffects(amount, purchaseKind, account, envelope, paymentEnvelope)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			next := cloneSuggestion(current)
			if err := next.Confirm(); err != nil {
				return lifecycleOutcome{}, err
			}
			return lifecycleOutcome{
				effects: effects,
				amount:  &amount,
				source:  current.Source(),
				commit:  func() { s.suggestions[id] = next },
			}, nil
		},
	}, func() *Suggestion { return cloneSuggestion(s.suggestions[id]) })
}

// ConvertSuggestionToPlannedPurchase confirms a future planned purchase: it
// routes into CreatePurchasePlan + DRAFT state instead of committing spending
// (STATE_TRANSITIONS §4: "confirmation of details alone must not bypass purchase
// approval"). The suggestion becomes CONFIRMED and the plan id is the
// suggestion id, so a repeated conversion cannot silently create a second plan.
func (s *CommandService) ConvertSuggestionToPlannedPurchase(actor UserId, id SuggestionId, kind PlanKind, target ReservationTarget) (*Suggestion, *PlannedOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actor.IsZero() {
		return nil, nil, NewError(CodeInvalidValue, "suggestion actor id must not be empty")
	}
	current, err := s.requireSuggestionLocked(id)
	if err != nil {
		return nil, nil, err
	}
	switch current.State() {
	case SuggestionStateSuggested, SuggestionStateEdited:
	default:
		return nil, nil, NewErrorf(CodeInvalidEntityState, "cannot convert a suggestion in state %s", current.State())
	}
	planID, err := NewPlanId(string(id))
	if err != nil {
		return nil, nil, err
	}
	if _, exists := s.plans[planID]; exists {
		return nil, nil, NewErrorf(CodeInvalidValue, "plan %q already exists", planID)
	}
	if err := s.validatePlanTargetLocked(target); err != nil {
		return nil, nil, err
	}
	plan, err := NewPlannedOperation(planID, kind, target, current.Amount())
	if err != nil {
		return nil, nil, err
	}
	next := cloneSuggestion(current)
	if err := next.Confirm(); err != nil {
		return nil, nil, err
	}
	s.plans[planID] = plan
	s.suggestions[id] = next
	return cloneSuggestion(next), clonePlan(plan), nil
}

// suggestionExternalEffects selects the factual posting shape for a confirmed
// external suggestion. It performs no funding check: the point of Q3 is to
// record the real event and flag any deficit afterwards.
func suggestionExternalEffects(amount PositiveAmount, purchaseKind PurchaseKind, account AccountId, envelope, paymentEnvelope EnvelopeId) (Effects, error) {
	if !purchaseKind.IsValid() {
		return Effects{}, NewErrorf(CodeInvalidValue, "unknown purchase kind %q", purchaseKind)
	}
	if envelope.IsZero() {
		return Effects{}, NewError(CodeInvalidValue, "suggestion confirmation requires a spending envelope")
	}
	switch purchaseKind {
	case PurchaseKindDebit, PurchaseKindCash:
		if account.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "debit/cash suggestion confirmation requires a cash account")
		}
		if !paymentEnvelope.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "debit/cash suggestion confirmation does not use a payment envelope")
		}
		return DebitPurchaseEffects(account, envelope, amount)
	case PurchaseKindCard:
		if account.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "card suggestion confirmation requires a card account")
		}
		if paymentEnvelope.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "card suggestion confirmation requires a card-payment envelope")
		}
		return CreditCardPurchaseEffects(account, envelope, paymentEnvelope, amount)
	case PurchaseKindRepayment:
		return Effects{}, NewError(CodeInvalidValue, "a suggestion cannot confirm a repayment; use the planned-repayment flow")
	default:
		return Effects{}, NewErrorf(CodeInvalidValue, "unknown purchase kind %q", purchaseKind)
	}
}

// requireSuggestionLocked returns the stored suggestion or a not-found domain
// error. The caller must hold the service mutex.
func (s *CommandService) requireSuggestionLocked(id SuggestionId) (*Suggestion, error) {
	suggestion, known := s.suggestions[id]
	if !known {
		return nil, NewErrorf(CodeEntityNotFoundOrNotInHousehold, "suggestion %q is not part of this household", id)
	}
	return suggestion, nil
}

// cloneSuggestion returns an independent snapshot of a suggestion aggregate.
func cloneSuggestion(suggestion *Suggestion) *Suggestion {
	if suggestion == nil {
		return nil
	}
	copied := *suggestion
	return &copied
}
