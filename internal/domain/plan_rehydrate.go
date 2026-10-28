// This file adds the persistence-facing rehydration seam for PlannedOperation
// and the release/final amount accessors the adapter needs. The lifecycle
// mutators in plan.go deliberately cannot express "load this aggregate from a
// stored snapshot", so the repository adapter uses RehydratePlannedOperation to
// rebuild a plan without replaying its transition history.
//
// Rehydration validates the same structural invariants the lifecycle mutators
// enforce; it does NOT re-run funding or authorization checks, which belong to
// the command layer. The production GORM adapter is the only caller.

package domain

// PlannedOperationSnapshot is the full persisted state of a planned operation.
//
// Zero amounts are represented by the zero value of PositiveAmount (the same
// representation PlannedOperation uses internally for "not held"). FinalAmount
// is nil unless the plan is COMPLETED.
type PlannedOperationSnapshot struct {
	ID     PlanId
	Kind   PlanKind
	State  PlanState
	Target ReservationTarget

	IntendedAmount   PositiveAmount
	AuthorizedAmount PositiveAmount

	Mode   AuthorizationMode
	Reason OverrideReason

	HeldAmount        PositiveAmount
	HeldOnAccount     PositiveAmount
	ReleasedAmount    PositiveAmount
	ReleasedOnAccount PositiveAmount
	FinalAmount       *PositiveAmount
}

// RehydratePlannedOperation rebuilds a PlannedOperation from persisted state.
// It rejects structurally inconsistent snapshots with INVALID_VALUE,
// INVALID_AMOUNT, or RESERVATION_MISMATCH, mirroring the constraints of the
// lifecycle mutators and the database CHECK constraints.
func RehydratePlannedOperation(snapshot PlannedOperationSnapshot) (*PlannedOperation, error) {
	if snapshot.ID.IsZero() {
		return nil, NewError(CodeInvalidValue, "planned operation plan id must not be empty")
	}
	if !snapshot.Kind.IsValid() {
		return nil, NewErrorf(CodeInvalidValue, "unknown plan kind %q", snapshot.Kind)
	}
	if !snapshot.State.IsValid() {
		return nil, NewErrorf(CodeInvalidValue, "unknown plan state %q", snapshot.State)
	}
	if snapshot.Target.EnvelopeID.IsZero() {
		return nil, NewError(CodeInvalidValue, "planned operation target envelope id must not be empty")
	}
	if !snapshot.Target.PlanID.IsZero() && snapshot.Target.PlanID != snapshot.ID {
		return nil, NewErrorf(CodeInvalidValue, "planned operation target plan id %q does not match plan id %q", snapshot.Target.PlanID, snapshot.ID)
	}
	if snapshot.Target.PlanID.IsZero() {
		snapshot.Target.PlanID = snapshot.ID
	}
	if snapshot.Kind == PlanKindRepayment && snapshot.Target.AccountID.IsZero() {
		return nil, NewError(CodeInvalidValue, "a planned repayment requires a source cash account")
	}

	intended, err := validateRehydratedAmount("intended amount", snapshot.IntendedAmount)
	if err != nil {
		return nil, err
	}
	authorized, err := validateRehydratedAmount("authorized amount", snapshot.AuthorizedAmount)
	if err != nil {
		return nil, err
	}
	if _, err := intended.Money().Compare(authorized.Money()); err != nil {
		return nil, err
	}

	if snapshot.Mode != "" && !snapshot.Mode.IsValid() {
		return nil, NewErrorf(CodeInvalidValue, "unknown authorization mode %q", snapshot.Mode)
	}
	if err := validateRehydratedMode(snapshot.State, snapshot.Mode, snapshot.Reason); err != nil {
		return nil, err
	}

	if err := validateZeroAmount("released amount", snapshot.ReleasedAmount); err != nil {
		return nil, err
	}
	if err := validateZeroAmount("released on account", snapshot.ReleasedOnAccount); err != nil {
		return nil, err
	}

	switch snapshot.State {
	case PlanStateDraft:
		if err := requireSnapshotZero("envelope hold", snapshot.HeldAmount); err != nil {
			return nil, err
		}
		if err := requireSnapshotZero("account hold", snapshot.HeldOnAccount); err != nil {
			return nil, err
		}
		if snapshot.FinalAmount != nil {
			return nil, NewError(CodeInvalidValue, "a draft plan cannot record a final amount")
		}
	case PlanStateReserved, PlanStateReservedException:
		if err := validatePlanHoldShape(snapshot.Target, snapshot.HeldAmount, snapshot.HeldOnAccount); err != nil {
			return nil, err
		}
		if snapshot.FinalAmount != nil {
			return nil, NewError(CodeInvalidValue, "a reserved plan cannot record a final amount")
		}
	case PlanStateCompleted:
		if snapshot.FinalAmount == nil {
			return nil, NewError(CodeInvalidValue, "a completed plan must record a final amount")
		}
		final, err := validateRehydratedAmount("final amount", *snapshot.FinalAmount)
		if err != nil {
			return nil, err
		}
		comparison, err := final.Money().Compare(authorized.Money())
		if err != nil {
			return nil, err
		}
		if comparison > 0 {
			return nil, NewErrorf(
				CodeAmountExceedsAuthorization,
				"final amount %d minor units exceeds authorized amount %d minor units",
				final.MinorUnits(),
				authorized.MinorUnits(),
			)
		}
		if err := requireSnapshotZero("envelope hold", snapshot.HeldAmount); err != nil {
			return nil, err
		}
		if err := requireSnapshotZero("account hold", snapshot.HeldOnAccount); err != nil {
			return nil, err
		}
	case PlanStateCancelled:
		if snapshot.FinalAmount != nil {
			return nil, NewError(CodeInvalidValue, "a cancelled plan cannot record a final amount")
		}
		if err := requireSnapshotZero("envelope hold", snapshot.HeldAmount); err != nil {
			return nil, err
		}
		if err := requireSnapshotZero("account hold", snapshot.HeldOnAccount); err != nil {
			return nil, err
		}
	}

	return &PlannedOperation{
		id:                snapshot.ID,
		kind:              snapshot.Kind,
		state:             snapshot.State,
		target:            snapshot.Target,
		intendedAmount:    intended,
		authorizedAmount:  authorized,
		mode:              snapshot.Mode,
		reason:            snapshot.Reason,
		heldAmount:        snapshot.HeldAmount,
		heldOnAccount:     snapshot.HeldOnAccount,
		releasedAmount:    snapshot.ReleasedAmount,
		releasedOnAccount: snapshot.ReleasedOnAccount,
		finalAmount:       derefOptionalAmount(snapshot.FinalAmount),
	}, nil
}

// ReleasedAmount returns the envelope hold released at completion or
// cancellation. It is the zero value while the plan is still holding funds.
func (p *PlannedOperation) ReleasedAmount() PositiveAmount {
	if p == nil {
		return PositiveAmount{}
	}
	return p.releasedAmount
}

// ReleasedOnAccount returns the account hold released at completion or
// cancellation. It is the zero value when the plan holds no cash account.
func (p *PlannedOperation) ReleasedOnAccount() PositiveAmount {
	if p == nil {
		return PositiveAmount{}
	}
	return p.releasedOnAccount
}

// FinalAmount returns the amount recorded at completion. The second result is
// false unless the plan is COMPLETED.
func (p *PlannedOperation) FinalAmount() (PositiveAmount, bool) {
	if p == nil || p.finalAmount.MinorUnits() <= 0 {
		return PositiveAmount{}, false
	}
	return p.finalAmount, true
}

// validateRehydratedAmount requires a genuinely positive amount with a valid
// currency.
func validateRehydratedAmount(kind string, amount PositiveAmount) (PositiveAmount, error) {
	if amount.MinorUnits() <= 0 {
		return PositiveAmount{}, NewErrorf(CodeInvalidAmount, "planned operation %s must be greater than zero", kind)
	}
	if err := amount.Currency().validate(); err != nil {
		return PositiveAmount{}, err
	}
	return amount, nil
}

// validateZeroAmount accepts a structurally zero PositiveAmount and rejects a
// malformed negative value. PositiveAmount cannot itself hold a negative value,
// so this is a defensive check for struct-literal bypasses.
func validateZeroAmount(kind string, amount PositiveAmount) error {
	if amount.MinorUnits() < 0 {
		return NewErrorf(CodeInvalidValue, "planned operation %s must not be negative", kind)
	}
	if amount.MinorUnits() > 0 {
		if err := amount.Currency().validate(); err != nil {
			return err
		}
	}
	return nil
}

// requireSnapshotZero rejects a positive amount where the state requires none.
func requireSnapshotZero(kind string, amount PositiveAmount) error {
	if amount.MinorUnits() != 0 {
		return NewErrorf(CodeReservationMismatch, "planned operation in this state holds no %s", kind)
	}
	return nil
}

// validateRehydratedMode mirrors the plans_state_mode_consistent database
// constraint and the lifecycle mutators' mode/reason rules.
func validateRehydratedMode(state PlanState, mode AuthorizationMode, reason OverrideReason) error {
	switch mode {
	case "":
		if !reason.IsZero() {
			return NewError(CodeInvalidValue, "an override reason requires an authorization mode")
		}
	case AuthorizationModeNormal:
		if !reason.IsZero() {
			return NewError(CodeInvalidValue, "a normal authorization must not record an override reason")
		}
	case AuthorizationModeEmergencyOverride:
		if reason.IsZero() {
			return NewError(CodeOverrideReasonRequired, "an emergency override requires a nonblank reason")
		}
	default:
		return NewErrorf(CodeInvalidValue, "unknown authorization mode %q", mode)
	}

	switch state {
	case PlanStateDraft:
		if mode != "" {
			return NewError(CodeInvalidValue, "a draft plan must not have an authorization mode")
		}
	case PlanStateReserved:
		if mode != AuthorizationModeNormal {
			return NewError(CodeInvalidEntityState, "a reserved plan must have a normal authorization mode")
		}
	case PlanStateReservedException:
		if mode != AuthorizationModeEmergencyOverride {
			return NewError(CodeInvalidEntityState, "a reserved-exception plan must have an emergency override mode")
		}
	}
	return nil
}

// derefOptionalAmount returns the zero PositiveAmount for nil.
func derefOptionalAmount(amount *PositiveAmount) PositiveAmount {
	if amount == nil {
		return PositiveAmount{}
	}
	return *amount
}
