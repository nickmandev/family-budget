// This file implements reauthorization of an already-authorized plan: the
// amount-change path (matrix rows "Reservation amount increased/decreased" and
// §4.1) and the completion guard. Both are pure: they return the hold and
// posting effects for the caller to commit and never mutate the ledger.
package domain

// ReauthorizeReservation handles a plan amount change after authorization
// (matrix rows "Reservation amount increased/decreased" and §4.1: "If final
// amount is lower than reserved amount, release the excess. If final amount is
// higher, reject completion until the delta has been reauthorized").
//
// It compares the currently held amount for the target against the new
// intended amount and returns the hold adjustment (ResizeReservationEffects
// shape) together with any funding result:
//   - increase (new > held): the additional amount is revalidated against
//     current spendable funds exactly like a fresh authorization (normal mode)
//     or covered by a fresh reasoned override for the envelope part only
//     (account liquidity still enforced). Outcome.Effects carry a POSITIVE
//     hold delta.
//   - decrease (new < held): the excess hold is released with a NEGATIVE hold
//     delta; no funding checks needed.
//   - equal: returns an empty Effects set and a zero Shortfall (no-op).
//
// held is the amount currently held for this target (the caller knows it from
// the plan's reservation record); the function does not read holds back out of
// the ledger. The new reservation state after applying the returned effects
// must equal the new amount: the effect is a delta of (new − held).
func ReauthorizeReservation(ledger *LedgerState, target ReservationTarget, held PositiveAmount, newAmount PositiveAmount, mode AuthorizationMode, reason OverrideReason) (AuthorizationOutcome, error) {
	if err := validateAuthorizationModeAndTarget(target, mode); err != nil {
		return AuthorizationOutcome{}, err
	}
	if err := validateAuthorizationAmount(held); err != nil {
		return AuthorizationOutcome{}, err
	}
	if err := validateAuthorizationAmount(newAmount); err != nil {
		return AuthorizationOutcome{}, err
	}
	if mode == AuthorizationModeEmergencyOverride {
		if err := requireOverrideReason(reason); err != nil {
			return AuthorizationOutcome{}, err
		}
	}

	zero, err := ZeroMoney(ledger.Currency())
	if err != nil {
		return AuthorizationOutcome{}, err
	}

	outcome := AuthorizationOutcome{Mode: mode, Shortfall: zero}
	if mode == AuthorizationModeEmergencyOverride {
		outcome.Reason = reason
	}

	comparison, err := newAmount.Money().Compare(held.Money())
	if err != nil {
		return AuthorizationOutcome{}, err
	}
	if comparison == 0 {
		// No-op: equal amounts never reach ResizeReservationEffects, whose
		// nonzero-delta rule would otherwise reject them with INVALID_AMOUNT.
		return outcome, nil
	}

	deltaMoney, err := newAmount.Money().Sub(held.Money())
	if err != nil {
		return AuthorizationOutcome{}, err
	}

	if comparison > 0 {
		// Revalidate only the additional amount: the existing hold has already
		// reduced spendable balances, so the delta is what must be funded.
		deltaAmount, err := NewPositiveAmount(deltaMoney)
		if err != nil {
			return AuthorizationOutcome{}, err
		}
		if mode == AuthorizationModeNormal {
			if err := ledger.RequireEnvelopeFunds(target.EnvelopeID, deltaAmount); err != nil {
				return AuthorizationOutcome{}, err
			}
		}
		// Account liquidity is enforced in both modes; the override bypasses
		// only the envelope test (§3.3).
		if !target.AccountID.IsZero() {
			if err := ledger.RequireAccountFunds(target.AccountID, deltaAmount); err != nil {
				return AuthorizationOutcome{}, err
			}
		}
		if mode == AuthorizationModeEmergencyOverride {
			shortfall, err := ledger.EnvelopeShortfall(target.EnvelopeID, deltaAmount)
			if err != nil {
				return AuthorizationOutcome{}, err
			}
			outcome.Shortfall = shortfall
		}
	}

	delta, err := NewSignedMoneyDelta(deltaMoney.MinorUnits(), deltaMoney.Currency())
	if err != nil {
		return AuthorizationOutcome{}, err
	}
	effects, err := ResizeReservationEffects(target, delta)
	if err != nil {
		return AuthorizationOutcome{}, err
	}
	outcome.Effects = effects
	return outcome, nil
}

// AuthorizeCompletion validates a completion attempt against the authorized
// amount (matrix §4.1: final amount cannot exceed authorization without
// revalidation; lower final amounts release the excess — the caller does that
// via CompleteReservedPurchaseEffects with the actual held/final amounts).
//
// It returns the posting effects for the completed purchase when
// final <= authorized (built by the caller-supplied purchaseEffects, which
// this function passes through unchanged so the payment-method shape stays in
// the effects layer) and CodeAmountExceedsAuthorization when final > authorized.
// A completion against a target whose holds do not match the recorded
// authorization is CodeReservationMismatch: heldOnAccount must be > 0 exactly
// when target.AccountID is set, and held must be > 0 always.
//
// The pure function releases exactly the held amounts it is given. When the
// final amount is lower than held the caller should pass the full held amount
// to release the excess while posting only the final purchase amount.
func AuthorizeCompletion(ledger *LedgerState, target ReservationTarget, held, heldOnAccount PositiveAmount, authorized, final PositiveAmount, purchaseEffects Effects) (Effects, error) {
	if target.PlanID.IsZero() {
		return Effects{}, NewError(CodeInvalidValue, "completion target plan id must not be empty")
	}
	if target.EnvelopeID.IsZero() {
		return Effects{}, NewError(CodeInvalidValue, "completion target envelope id must not be empty")
	}
	if held.MinorUnits() <= 0 {
		return Effects{}, NewError(CodeReservationMismatch, "completion requires a positive envelope reservation")
	}
	if heldOnAccount.MinorUnits() <= 0 && !target.AccountID.IsZero() {
		return Effects{}, NewError(CodeReservationMismatch, "completion target holds an account but reports no account reservation")
	}
	if heldOnAccount.MinorUnits() > 0 && target.AccountID.IsZero() {
		return Effects{}, NewError(CodeReservationMismatch, "completion reports an account reservation for a target without an account")
	}

	comparison, err := final.Money().Compare(authorized.Money())
	if err != nil {
		return Effects{}, err
	}
	if comparison > 0 {
		return Effects{}, NewErrorf(
			CodeAmountExceedsAuthorization,
			"final amount %d minor units exceeds authorized amount %d minor units",
			final.MinorUnits(),
			authorized.MinorUnits(),
		)
	}

	release, err := CancelReservationEffects(target, held, heldOnAccount)
	if err != nil {
		return Effects{}, err
	}
	completed, err := MergeEffects(purchaseEffects, release)
	if err != nil {
		return Effects{}, err
	}
	_ = ledger // read-only input reserved for future funding rechecks
	return completed, nil
}
