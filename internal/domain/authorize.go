// This file implements the authorization decisions for planned operations
// (POSTING_MATRIX §3.1 normal approval and §3.3 emergency override). It returns
// a decision plus the reservation effects to commit and never mutates the
// ledger: callers apply the returned effects and record the audit event.
//
// The pure funding primitives live in funding.go and the pure hold transitions
// in effects_reservation.go; this file only decides which checks apply.
package domain

// AuthorizationOutcome records how a planned operation was authorized and the
// hold changes to commit for it. Shortfall is nonzero only for an emergency
// override that deliberately authorizes an underfunded envelope: the resulting
// envelope deficit is visible and traceable (Q4), never hidden.
type AuthorizationOutcome struct {
	Mode      AuthorizationMode
	Shortfall Money          // envelope shortfall authorized by override; zero when fully funded
	Effects   Effects        // reservation holds to apply (CreateReservationEffects shape)
	Reason    OverrideReason // recorded only for override mode; zero value for normal
}

// AuthorizePurchase authorizes a planned purchase or repayment and creates its
// reservation holds in one step (matrix rows "Purchase reservation created"
// and §3.1–3.3).
//
// Normal mode requires P <= S[envelope] and, when target sets AccountID
// (debit/cash purchase or planned card repayment), P <= SA[account].
//
// Emergency override requires a nonblank Reason and bypasses the envelope test
// only: account liquidity is still enforced (CodeInsufficientAccountFunds).
// The envelope shortfall is reported in Outcome.Shortfall.
//
// Target semantics per IMPLEMENTATION_DECISIONS:
//   - planned debit/cash purchase: EnvelopeID = spending envelope, AccountID = source cash account (both holds, Q1/Q2 base rule);
//   - planned card purchase: EnvelopeID = spending envelope, AccountID = zero (envelope hold only, Q1);
//   - planned card repayment: EnvelopeID = card-payment envelope, AccountID = source cash account (both holds, Q2).
//
// AuthorizePurchase is payment-method agnostic: it only knows the target hold
// set. Payment-specific validation (e.g. repayment must not exceed outstanding
// liability) belongs to the command layer.
func AuthorizePurchase(ledger *LedgerState, target ReservationTarget, amount PositiveAmount, mode AuthorizationMode, reason OverrideReason) (AuthorizationOutcome, error) {
	if err := validateAuthorizationModeAndTarget(target, mode); err != nil {
		return AuthorizationOutcome{}, err
	}
	if err := validateAuthorizationAmount(amount); err != nil {
		return AuthorizationOutcome{}, err
	}

	zero, err := ZeroMoney(ledger.Currency())
	if err != nil {
		return AuthorizationOutcome{}, err
	}

	outcome := AuthorizationOutcome{Mode: mode}
	switch mode {
	case AuthorizationModeNormal:
		if err := ledger.RequireEnvelopeFunds(target.EnvelopeID, amount); err != nil {
			return AuthorizationOutcome{}, err
		}
		if !target.AccountID.IsZero() {
			if err := ledger.RequireAccountFunds(target.AccountID, amount); err != nil {
				return AuthorizationOutcome{}, err
			}
		}
		outcome.Shortfall = zero
	case AuthorizationModeEmergencyOverride:
		if err := requireOverrideReason(reason); err != nil {
			return AuthorizationOutcome{}, err
		}
		// The override never bypasses account liquidity (§3.3).
		if !target.AccountID.IsZero() {
			if err := ledger.RequireAccountFunds(target.AccountID, amount); err != nil {
				return AuthorizationOutcome{}, err
			}
		}
		shortfall, err := ledger.EnvelopeShortfall(target.EnvelopeID, amount)
		if err != nil {
			return AuthorizationOutcome{}, err
		}
		outcome.Shortfall = shortfall
		outcome.Reason = reason
	}

	holds, err := CreateReservationEffects(target, amount)
	if err != nil {
		return AuthorizationOutcome{}, err
	}
	outcome.Effects = holds
	return outcome, nil
}

// validateAuthorizationModeAndTarget rejects an unknown mode and a target with
// a zero plan or envelope id. A zero AccountID is valid: it means "no account
// hold" (planned card purchase, Q1).
func validateAuthorizationModeAndTarget(target ReservationTarget, mode AuthorizationMode) error {
	if !mode.IsValid() {
		return NewErrorf(CodeInvalidValue, "unknown authorization mode %q", mode)
	}
	if target.PlanID.IsZero() {
		return NewError(CodeInvalidValue, "authorization target plan id must not be empty")
	}
	if target.EnvelopeID.IsZero() {
		return NewError(CodeInvalidValue, "authorization target envelope id must not be empty")
	}
	return nil
}

// validateAuthorizationAmount rejects a zero-value or non-positive amount and a
// malformed currency. Currency mismatch against the ledger is reported by the
// funding checks.
func validateAuthorizationAmount(amount PositiveAmount) error {
	if amount.MinorUnits() <= 0 {
		return NewError(CodeInvalidAmount, "authorization amount must be greater than zero")
	}
	return amount.Currency().validate()
}

// requireOverrideReason enforces the mandatory, nonblank reason for an
// emergency override (POSTING_MATRIX §3.3).
func requireOverrideReason(reason OverrideReason) error {
	if reason.IsZero() {
		return NewError(CodeOverrideReasonRequired, "an emergency override requires a nonblank reason")
	}
	return nil
}
