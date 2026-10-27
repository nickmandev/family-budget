// This file implements the pure planned-operation lifecycle state machine
// (STATE_TRANSITIONS §2 and POSTING_MATRIX §4.1). The aggregate validates
// transitions and mutates only its own state; it NEVER applies effects to a
// ledger. Callers obtain reserved effects from the authorization layer, apply
// them, and use the plan-change helpers to build the effects for later
// transitions.
//
// Invalid transitions are rejected with INVALID_ENTITY_STATE and leave the
// plan unchanged (STATE_TRANSITIONS §5). Every mutator validates completely
// before it touches a field.
package domain

// PlannedOperation is the aggregate for an authorized-before-it-happens
// purchase or repayment. The zero value is invalid: construct with
// NewPlannedOperation.
//
// A planned card repayment is a distinct PlanKind, not a purchase variant
// (IMPLEMENTATION_DECISIONS Q2): it shares these lifecycle states but has its
// own completion postings.
type PlannedOperation struct {
	id     PlanId
	kind   PlanKind
	state  PlanState
	target ReservationTarget

	intendedAmount   PositiveAmount
	authorizedAmount PositiveAmount

	mode   AuthorizationMode // zero until authorized
	reason OverrideReason    // recorded only for an override

	heldAmount        PositiveAmount // active Qe hold; zero when not reserved
	heldOnAccount     PositiveAmount // active Qa hold; zero when no account hold
	releasedAmount    PositiveAmount // Qe released at completion/cancellation
	releasedOnAccount PositiveAmount // Qa released at completion/cancellation

	finalAmount PositiveAmount // recorded when completed, for idempotency
}

// NewPlannedOperation creates a DRAFT plan. Drafts reserve nothing.
//
// Structural target rules:
//   - a repayment requires a non-zero AccountID: it reserves source-account
//     cash plus the payment envelope (Q2);
//   - a purchase may use a zero AccountID (card purchase, Q1) or a non-zero
//     AccountID (debit/cash purchase). PlanKind alone cannot distinguish those
//     two purchase payment methods, so both target shapes are accepted and the
//     shape is what records the difference.
//
// A zero target.PlanID is filled from id. A non-zero target.PlanID that
// disagrees with id is rejected because it would make the aggregate inconsistent.
func NewPlannedOperation(id PlanId, kind PlanKind, target ReservationTarget, amount PositiveAmount) (*PlannedOperation, error) {
	if id.IsZero() {
		return nil, NewError(CodeInvalidValue, "planned operation plan id must not be empty")
	}
	if !kind.IsValid() {
		return nil, NewErrorf(CodeInvalidValue, "unknown plan kind %q", kind)
	}
	if amount.MinorUnits() <= 0 {
		return nil, NewError(CodeInvalidAmount, "planned operation amount must be greater than zero")
	}
	if err := amount.Currency().validate(); err != nil {
		return nil, err
	}
	if target.EnvelopeID.IsZero() {
		return nil, NewError(CodeInvalidValue, "planned operation target envelope id must not be empty")
	}
	if !target.PlanID.IsZero() && target.PlanID != id {
		return nil, NewErrorf(CodeInvalidValue, "planned operation target plan id %q does not match plan id %q", target.PlanID, id)
	}
	if target.PlanID.IsZero() {
		target.PlanID = id
	}
	if kind == PlanKindRepayment && target.AccountID.IsZero() {
		return nil, NewError(CodeInvalidValue, "a planned repayment requires a source cash account")
	}

	return &PlannedOperation{
		id:               id,
		kind:             kind,
		state:            PlanStateDraft,
		target:           target,
		intendedAmount:   amount,
		authorizedAmount: amount,
	}, nil
}

// ID returns the plan identifier.
func (p *PlannedOperation) ID() PlanId {
	if p == nil {
		return ""
	}
	return p.id
}

// Kind returns the planned-operation kind (purchase or repayment).
func (p *PlannedOperation) Kind() PlanKind {
	if p == nil {
		return ""
	}
	return p.kind
}

// State returns the current lifecycle state.
func (p *PlannedOperation) State() PlanState {
	if p == nil {
		return ""
	}
	return p.state
}

// Target returns the reservation target held by the plan.
func (p *PlannedOperation) Target() ReservationTarget {
	if p == nil {
		return ReservationTarget{}
	}
	return p.target
}

// IntendedAmount returns the plan's current intended amount.
func (p *PlannedOperation) IntendedAmount() PositiveAmount {
	if p == nil {
		return PositiveAmount{}
	}
	return p.intendedAmount
}

// AuthorizedAmount returns the currently authorized amount. It equals the
// intended amount until an adjustment changes it.
func (p *PlannedOperation) AuthorizedAmount() PositiveAmount {
	if p == nil {
		return PositiveAmount{}
	}
	return p.authorizedAmount
}

// Mode returns the authorization mode, or the zero value before authorization.
func (p *PlannedOperation) Mode() AuthorizationMode {
	if p == nil {
		return ""
	}
	return p.mode
}

// Reason returns the recorded override reason, or the zero value unless the
// plan was authorized/adjusted by emergency override.
func (p *PlannedOperation) Reason() OverrideReason {
	if p == nil {
		return ""
	}
	return p.reason
}

// HeldAmount returns the currently reserved envelope amount (Qe). It is zero
// when the plan holds nothing: draft, completed, or cancelled.
func (p *PlannedOperation) HeldAmount() PositiveAmount {
	if p == nil {
		return PositiveAmount{}
	}
	return p.heldAmount
}

// HeldOnAccount returns the currently reserved account amount (Qa). It is zero
// when the plan holds no cash account or after its holds are released.
func (p *PlannedOperation) HeldOnAccount() PositiveAmount {
	if p == nil {
		return PositiveAmount{}
	}
	return p.heldOnAccount
}

// Authorize moves DRAFT -> RESERVED for a normal outcome or DRAFT ->
// RESERVED_EXCEPTION for an override outcome with a recorded reason. Any other
// source state is INVALID_ENTITY_STATE.
//
// The caller supplies the held and heldOnAccount amounts explicitly so this
// method stays ledger-free; they must match the plan's intended amount and the
// target's hold shape. The outcome's Effects are not inspected or applied: the
// caller applies them and records the authorization audit.
func (p *PlannedOperation) Authorize(outcome AuthorizationOutcome, held, heldOnAccount PositiveAmount) error {
	if err := p.validateInitialized(); err != nil {
		return err
	}
	if p.state != PlanStateDraft {
		return NewErrorf(CodeInvalidEntityState, "cannot authorize a plan in state %s", p.state)
	}

	next, err := planStateAfterAuthorization(outcome)
	if err != nil {
		return err
	}
	if err := validatePlanHoldAmounts(p.target, held, heldOnAccount, p.intendedAmount); err != nil {
		return err
	}

	p.state = next
	p.mode = outcome.Mode
	p.reason = outcome.Reason
	if outcome.Mode == AuthorizationModeNormal {
		p.reason = ""
	}
	p.authorizedAmount = p.intendedAmount
	p.heldAmount = held
	p.heldOnAccount = heldOnAccount
	p.releasedAmount = PositiveAmount{}
	p.releasedOnAccount = PositiveAmount{}
	p.finalAmount = PositiveAmount{}
	return nil
}

// Adjust updates amount/category/account after authorization per
// POSTING_MATRIX §4.1:
//
//	RESERVED           -> RESERVED (revalidate deltas, adjust holds) or DRAFT
//	RESERVED_EXCEPTION -> RESERVED (reauthorized normally),
//	                      RESERVED_EXCEPTION (new/revalidated override), or DRAFT
//
// The new hold amounts come from ReauthorizeReservation's outcome. A normal
// outcome may land in RESERVED (or DRAFT per caller choice); an override
// outcome must land in RESERVED_EXCEPTION with a recorded reason. DRAFT,
// COMPLETED, and CANCELLED sources are INVALID_ENTITY_STATE, as are target
// states the contract does not allow.
//
// Adjusting to DRAFT holds nothing; the caller must release the old
// reservation explicitly using the HeldAmount/HeldOnAccount values read before
// the call (the transition contract permits the RESERVED -> DRAFT -> RESERVED
// cancel-and-recreate path for category/account changes).
func (p *PlannedOperation) Adjust(outcome AuthorizationOutcome, newState PlanState, newAmount PositiveAmount, held, heldOnAccount PositiveAmount) error {
	if err := p.validateInitialized(); err != nil {
		return err
	}
	switch p.state {
	case PlanStateReserved, PlanStateReservedException:
	default:
		return NewErrorf(CodeInvalidEntityState, "cannot adjust a plan in state %s", p.state)
	}
	if !newState.IsValid() {
		return NewErrorf(CodeInvalidValue, "unknown plan state %q", newState)
	}
	if !planTransitionAllowed(p.state, newState) {
		return NewErrorf(CodeInvalidEntityState, "cannot adjust a plan from %s to %s", p.state, newState)
	}
	if newAmount.MinorUnits() <= 0 {
		return NewError(CodeInvalidAmount, "adjusted plan amount must be greater than zero")
	}
	if err := newAmount.Currency().validate(); err != nil {
		return err
	}

	switch newState {
	case PlanStateDraft:
		if held.MinorUnits() != 0 || heldOnAccount.MinorUnits() != 0 {
			return NewError(CodeReservationMismatch, "a draft plan holds no reservations")
		}
	case PlanStateReserved:
		if outcome.Mode != AuthorizationModeNormal {
			if outcome.Mode == AuthorizationModeEmergencyOverride {
				return NewError(CodeInvalidEntityState, "an override outcome cannot produce a normally reserved plan")
			}
			return NewErrorf(CodeInvalidValue, "unknown authorization mode %q", outcome.Mode)
		}
		if err := validatePlanHoldAmounts(p.target, held, heldOnAccount, newAmount); err != nil {
			return err
		}
	case PlanStateReservedException:
		if outcome.Mode != AuthorizationModeEmergencyOverride {
			if outcome.Mode == AuthorizationModeNormal {
				return NewError(CodeInvalidEntityState, "a normal outcome cannot produce a reserved-exception plan")
			}
			return NewErrorf(CodeInvalidValue, "unknown authorization mode %q", outcome.Mode)
		}
		if outcome.Reason.IsZero() {
			return NewError(CodeOverrideReasonRequired, "an emergency override requires a nonblank reason")
		}
		if err := validatePlanHoldAmounts(p.target, held, heldOnAccount, newAmount); err != nil {
			return err
		}
	default:
		// COMPLETED and CANCELLED are not adjust targets; reaching here means
		// the transition table grew without this switch being revisited.
		return NewErrorf(CodeInvalidEntityState, "cannot adjust a plan into state %s", newState)
	}

	p.state = newState
	p.intendedAmount = newAmount
	p.authorizedAmount = newAmount
	p.releasedAmount = PositiveAmount{}
	p.releasedOnAccount = PositiveAmount{}
	p.finalAmount = PositiveAmount{}
	switch newState {
	case PlanStateDraft:
		p.mode = ""
		p.reason = ""
		p.heldAmount = PositiveAmount{}
		p.heldOnAccount = PositiveAmount{}
	case PlanStateReserved:
		p.mode = AuthorizationModeNormal
		p.reason = ""
		p.heldAmount = held
		p.heldOnAccount = heldOnAccount
	case PlanStateReservedException:
		p.mode = AuthorizationModeEmergencyOverride
		p.reason = outcome.Reason
		p.heldAmount = held
		p.heldOnAccount = heldOnAccount
	}
	return nil
}

// Complete moves RESERVED or RESERVED_EXCEPTION -> COMPLETED. The caller has
// already checked the final amount against the authorization through
// AuthorizeCompletion; this method rechecks the bound anyway, enforces the
// source state, and releases the plan's holds.
//
// Idempotency (POSTING_MATRIX §4.1): completing an already COMPLETED plan with
// the same final amount is a successful no-op returning nil. Completing it
// with a different amount is INVALID_ENTITY_STATE — the caller must use a
// linked reversal/correction rather than mutate committed history.
//
// held and heldOnAccount must be the plan's current held amounts; they are
// recorded as the released amounts so PlanCompleteEffects can rebuild the
// completion effect set. The final amount may be lower than the held amount,
// releasing the excess.
func (p *PlannedOperation) Complete(final PositiveAmount, held, heldOnAccount PositiveAmount) error {
	if err := p.validateInitialized(); err != nil {
		return err
	}
	if final.MinorUnits() <= 0 {
		return NewError(CodeInvalidAmount, "completion amount must be greater than zero")
	}
	if err := final.Currency().validate(); err != nil {
		return err
	}

	if p.state == PlanStateCompleted {
		if samePlanAmount(final, p.finalAmount) {
			return nil
		}
		return NewErrorf(
			CodeInvalidEntityState,
			"plan is already completed for %d minor units and cannot be completed for %d",
			p.finalAmount.MinorUnits(),
			final.MinorUnits(),
		)
	}
	if p.state != PlanStateReserved && p.state != PlanStateReservedException {
		return NewErrorf(CodeInvalidEntityState, "cannot complete a plan in state %s", p.state)
	}

	comparison, err := final.Money().Compare(p.authorizedAmount.Money())
	if err != nil {
		return err
	}
	if comparison > 0 {
		return NewErrorf(
			CodeAmountExceedsAuthorization,
			"final amount %d minor units exceeds authorized amount %d minor units",
			final.MinorUnits(),
			p.authorizedAmount.MinorUnits(),
		)
	}
	if err := validatePlanHoldShape(p.target, held, heldOnAccount); err != nil {
		return err
	}
	if err := requirePlanAmount("completion envelope release", held, p.heldAmount); err != nil {
		return err
	}
	if !p.target.AccountID.IsZero() {
		if err := requirePlanAmount("completion account release", heldOnAccount, p.heldOnAccount); err != nil {
			return err
		}
	}

	p.state = PlanStateCompleted
	p.finalAmount = final
	p.releasedAmount = held
	p.releasedOnAccount = heldOnAccount
	p.heldAmount = PositiveAmount{}
	p.heldOnAccount = PositiveAmount{}
	return nil
}

// Cancel moves DRAFT, RESERVED, or RESERVED_EXCEPTION -> CANCELLED. Completed
// and cancelled plans are rejected with INVALID_ENTITY_STATE. Cancelling a
// reserved plan records the released holds so the caller can build
// CancelReservationEffects; no financial postings are produced.
func (p *PlannedOperation) Cancel() error {
	if err := p.validateInitialized(); err != nil {
		return err
	}
	switch p.state {
	case PlanStateDraft:
		p.state = PlanStateCancelled
		p.heldAmount = PositiveAmount{}
		p.heldOnAccount = PositiveAmount{}
		p.releasedAmount = PositiveAmount{}
		p.releasedOnAccount = PositiveAmount{}
	case PlanStateReserved, PlanStateReservedException:
		p.state = PlanStateCancelled
		p.releasedAmount = p.heldAmount
		p.releasedOnAccount = p.heldOnAccount
		p.heldAmount = PositiveAmount{}
		p.heldOnAccount = PositiveAmount{}
	default:
		return NewErrorf(CodeInvalidEntityState, "cannot cancel a plan in state %s", p.state)
	}
	return nil
}

// CanTransition reports whether a transition from the current state to to is
// allowed. It is a pure query and never mutates the plan.
func (p *PlannedOperation) CanTransition(to PlanState) bool {
	if p == nil || p.id.IsZero() {
		return false
	}
	return planTransitionAllowed(p.state, to)
}

// validateInitialized rejects nil and zero-value aggregates.
func (p *PlannedOperation) validateInitialized() error {
	if p == nil || p.id.IsZero() {
		return NewError(CodeInvalidValue, "planned operation must be initialized")
	}
	return nil
}

// planStateAfterAuthorization maps an authorization outcome to the lifecycle
// state it produces, enforcing the mandatory override reason.
func planStateAfterAuthorization(outcome AuthorizationOutcome) (PlanState, error) {
	switch outcome.Mode {
	case AuthorizationModeNormal:
		return PlanStateReserved, nil
	case AuthorizationModeEmergencyOverride:
		if outcome.Reason.IsZero() {
			return "", NewError(CodeOverrideReasonRequired, "an emergency override requires a nonblank reason")
		}
		return PlanStateReservedException, nil
	default:
		return "", NewErrorf(CodeInvalidValue, "unknown authorization mode %q", outcome.Mode)
	}
}

// validatePlanHoldAmounts validates the hold shape and requires the holds to
// equal expected, the plan's post-transition amount.
func validatePlanHoldAmounts(target ReservationTarget, held, heldOnAccount, expected PositiveAmount) error {
	if err := validatePlanHoldShape(target, held, heldOnAccount); err != nil {
		return err
	}
	if err := requirePlanAmount("envelope reservation", held, expected); err != nil {
		return err
	}
	if !target.AccountID.IsZero() {
		if err := requirePlanAmount("account reservation", heldOnAccount, expected); err != nil {
			return err
		}
	}
	return nil
}

// validatePlanHoldShape enforces the structural hold shape of a target: a
// positive envelope hold, and an account hold exactly when the target sets an
// account (Q1/Q2).
func validatePlanHoldShape(target ReservationTarget, held, heldOnAccount PositiveAmount) error {
	if held.MinorUnits() <= 0 {
		return NewError(CodeReservationMismatch, "planned operation requires a positive envelope reservation")
	}
	if target.AccountID.IsZero() {
		if heldOnAccount.MinorUnits() != 0 {
			return NewError(CodeReservationMismatch, "planned operation target holds no account but reports an account reservation")
		}
		return nil
	}
	if heldOnAccount.MinorUnits() <= 0 {
		return NewError(CodeReservationMismatch, "planned operation target holds an account but reports no account reservation")
	}
	return nil
}

// requirePlanAmount reports RESERVATION_MISMATCH when got and want differ.
func requirePlanAmount(kind string, got, want PositiveAmount) error {
	if samePlanAmount(got, want) {
		return nil
	}
	return NewErrorf(
		CodeReservationMismatch,
		"%s of %d minor units does not match recorded amount of %d minor units",
		kind,
		got.MinorUnits(),
		want.MinorUnits(),
	)
}

// samePlanAmount compares two positive amounts, returning false for malformed
// zero-value amounts.
func samePlanAmount(a, b PositiveAmount) bool {
	comparison, err := a.Money().Compare(b.Money())
	return err == nil && comparison == 0
}
