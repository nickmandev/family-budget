// This file builds the pure Effects for planned-operation transitions. It
// keeps the PlannedOperation aggregate itself effects-free: the state machine
// decides whether a transition is legal, and these helpers translate the
// current plan state and holds into the hold/postings shape the ledger needs.
//
// The helpers do not apply anything and do not mutate the plan.
package domain

// PlanAdjustmentEffects builds the hold-adjustment effects for an allowed
// Adjust transition (matrix rows "Reservation amount increased/decreased").
// delta is the signed difference between the new and old intended amounts and
// is applied to the plan's current holds through ResizeReservationEffects.
//
// A category or account change may need the old holds cancelled and new ones
// created; the transition contract permits that as RESERVED -> DRAFT ->
// RESERVED, with the caller releasing the old reservation explicitly.
func PlanAdjustmentEffects(p *PlannedOperation, delta SignedMoneyDelta) (Effects, error) {
	if err := requirePlanForEffects(p); err != nil {
		return Effects{}, err
	}
	switch p.state {
	case PlanStateReserved, PlanStateReservedException:
		return ResizeReservationEffects(p.target, delta)
	default:
		return Effects{}, NewErrorf(CodeInvalidEntityState, "cannot build adjustment effects for a plan in state %s", p.state)
	}
}

// PlanCancelEffects releases the plan's current holds (matrix rows "Reservation
// cancelled"). A DRAFT plan holds nothing and returns empty effects; a plan
// cancelled after it was reserved returns the release recorded by Cancel; a
// COMPLETED plan rejects because its holds were consumed by completion.
func PlanCancelEffects(p *PlannedOperation) (Effects, error) {
	if err := requirePlanForEffects(p); err != nil {
		return Effects{}, err
	}
	switch p.state {
	case PlanStateDraft:
		return Effects{}, nil
	case PlanStateReserved, PlanStateReservedException:
		return CancelReservationEffects(p.target, p.heldAmount, p.heldOnAccount)
	case PlanStateCancelled:
		if p.releasedAmount.MinorUnits() == 0 && p.releasedOnAccount.MinorUnits() == 0 {
			return Effects{}, nil
		}
		return CancelReservationEffects(p.target, p.releasedAmount, p.releasedOnAccount)
	default:
		return Effects{}, NewErrorf(CodeInvalidEntityState, "cannot build cancellation effects for a plan in state %s", p.state)
	}
}

// PlanCompleteEffects merges the actual purchase postings with the hold
// release for a plan that is being completed (CompleteReservedPurchaseEffects,
// matrix row "Reserved purchase completed"). purchase carries the
// payment-method-shaped effects from the effects layer: DebitPurchaseEffects or
// CreditCardPurchaseEffects for a purchase, CreditCardPaymentEffects for a Q2
// repayment.
//
// Call this while the plan is RESERVED or RESERVED_EXCEPTION, before
// Complete. Completing an already COMPLETED plan is idempotent on the
// aggregate, but rebuilding its postings here would make a second posting
// possible, so this helper rejects that state.
func PlanCompleteEffects(p *PlannedOperation, purchase Effects) (Effects, error) {
	if err := requirePlanForEffects(p); err != nil {
		return Effects{}, err
	}
	switch p.state {
	case PlanStateReserved, PlanStateReservedException:
		return CompleteReservedPurchaseEffects(p.target, purchase, p.heldAmount, p.heldOnAccount)
	default:
		return Effects{}, NewErrorf(CodeInvalidEntityState, "cannot build completion effects for a plan in state %s", p.state)
	}
}

// requirePlanForEffects rejects nil and zero-value plan aggregates.
func requirePlanForEffects(p *PlannedOperation) error {
	if p == nil || p.id.IsZero() {
		return NewError(CodeInvalidValue, "planned operation must be initialized")
	}
	return nil
}
