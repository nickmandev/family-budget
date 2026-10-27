// This file implements the reallocation-first authorization path
// (POSTING_MATRIX §3.2): recheck the source envelope, post the reallocation
// delta into the purchase envelope, and create the reservation holds as one
// atomic effect set. Again the function never mutates the ledger — it returns
// the combined effects for the caller to commit with the authorization audit
// event.
package domain

// AuthorizeWithReallocation implements the reallocation-first path
// (POSTING_MATRIX §3.2) as ONE atomic operation: recheck the source envelope
// against current committed state, post the reallocation delta into the
// purchase envelope, and create the reservation holds — all in a single
// Effects value so callers can commit them together with the authorization
// audit event.
//
// delta is the amount moved from source to target.EnvelopeID (must be > 0).
// Requirements: source != target.EnvelopeID (INVALID_VALUE); S[source] >= delta
// (CodeInsufficientEnvelopeFunds); after the move, the target envelope must
// cover the purchase amount S[target] + delta >= amount in NORMAL mode
// (CodeInsufficientEnvelopeFunds) — in OVERRIDE mode the envelope test is
// skipped exactly as in AuthorizePurchase and any remaining shortfall is
// reported; account liquidity is enforced in BOTH modes when target sets
// AccountID.
//
// The combined Effects contain the reallocation postings and the reservation
// holds; the caller applies them atomically and records the authorization
// audit event (a separate concern).
func AuthorizeWithReallocation(ledger *LedgerState, source EnvelopeId, target ReservationTarget, delta PositiveAmount, amount PositiveAmount, mode AuthorizationMode, reason OverrideReason) (AuthorizationOutcome, error) {
	if err := validateAuthorizationModeAndTarget(target, mode); err != nil {
		return AuthorizationOutcome{}, err
	}
	if err := validateAuthorizationAmount(delta); err != nil {
		return AuthorizationOutcome{}, err
	}
	if err := validateAuthorizationAmount(amount); err != nil {
		return AuthorizationOutcome{}, err
	}
	if source == target.EnvelopeID {
		return AuthorizationOutcome{}, NewError(CodeInvalidValue, "reallocation source and target envelopes must be distinct")
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

	// The source envelope's spendable balance is never overridable: the
	// emergency override skips only the target envelope test (§3.2/§3.3).
	if err := ledger.RequireEnvelopeFunds(source, delta); err != nil {
		return AuthorizationOutcome{}, err
	}
	if !target.AccountID.IsZero() {
		if err := ledger.RequireAccountFunds(target.AccountID, amount); err != nil {
			return AuthorizationOutcome{}, err
		}
	}

	shortfallBefore, err := ledger.EnvelopeShortfall(target.EnvelopeID, amount)
	if err != nil {
		return AuthorizationOutcome{}, err
	}
	remaining := zero
	if shortfallBefore.IsPositive() {
		covered, err := shortfallBefore.Sub(delta.Money())
		if err != nil {
			return AuthorizationOutcome{}, err
		}
		if covered.IsPositive() {
			remaining = covered
		}
	}
	if mode == AuthorizationModeNormal && remaining.IsPositive() {
		return AuthorizationOutcome{}, NewErrorf(
			CodeInsufficientEnvelopeFunds,
			"reallocation of %d minor units leaves the target envelope %d minor units short",
			delta.MinorUnits(),
			remaining.MinorUnits(),
		)
	}

	reallocation, err := ReallocateFundsEffects(source, target.EnvelopeID, delta)
	if err != nil {
		return AuthorizationOutcome{}, err
	}
	holds, err := CreateReservationEffects(target, amount)
	if err != nil {
		return AuthorizationOutcome{}, err
	}
	effects, err := MergeEffects(reallocation, holds)
	if err != nil {
		return AuthorizationOutcome{}, err
	}

	outcome := AuthorizationOutcome{
		Mode:      mode,
		Shortfall: remaining,
		Effects:   effects,
	}
	if mode == AuthorizationModeEmergencyOverride {
		outcome.Reason = reason
	}
	return outcome, nil
}
