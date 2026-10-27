// This file computes the pure reservation-hold effects of the planned-operation
// lifecycle from the posting matrix: reservation created, amount increased or
// decreased, cancelled, and reserved purchase completed. Reservations are hold
// changes only — they never post financial balances. It performs NO funding,
// liquidity, or authorization checks — those belong to the authorization layer
// built later. Every function returns the hold transitions (plus, for
// completion, the purchase postings); the caller applies them to a LedgerState,
// which reports unknown ids and rejects holds that would go negative.
package domain

// ReservationTarget describes what a planned operation holds against.
//
// AccountID is optional: the zero value means "no cash-account hold". That is
// the correct shape for a planned card purchase, which holds only the spending
// envelope (IMPLEMENTATION_DECISIONS Q1).
type ReservationTarget struct {
	PlanID     PlanId
	EnvelopeID EnvelopeId // envelope being held (spending envelope, or payment envelope for repayments)
	AccountID  AccountId  // optional: cash account held (zero value = no account hold, e.g. card purchases per Q1)
}

// CreateReservationEffects builds hold changes for authorizing a planned
// operation: Qe[envelope] +P and, when AccountID is set, Qa[account] +P
// (matrix row "Purchase reservation created"). For a planned card repayment
// pass the card-payment envelope as EnvelopeID and the source cash account as
// AccountID (Q2). For a planned debit/cash purchase pass the spending envelope
// and the source account. For a planned card purchase pass the spending
// envelope and a zero AccountID (Q1).
func CreateReservationEffects(target ReservationTarget, amount PositiveAmount) (Effects, error) {
	if amount.MinorUnits() <= 0 {
		return Effects{}, NewError(CodeInvalidAmount, "reservation amount must be greater than zero")
	}
	increase, err := positiveDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	var accountDelta *SignedMoneyDelta
	if !target.AccountID.IsZero() {
		accountDelta = &increase
	}
	return newReservationEffects(target, increase, accountDelta)
}

// ResizeReservationEffects builds hold deltas for changing a plan's amount
// after authorization: increase adds a positive delta to the same holds,
// decrease releases with a negative delta (matrix rows "Reservation amount
// increased/decreased"). The signed delta must be nonzero (INVALID_AMOUNT) and
// targets the same hold set as creation. Final hold validation (never below
// zero) is enforced when applied to LedgerState.
func ResizeReservationEffects(target ReservationTarget, delta SignedMoneyDelta) (Effects, error) {
	if delta.MinorUnits() == 0 {
		return Effects{}, NewError(CodeInvalidAmount, "reservation delta must be nonzero")
	}
	if _, err := delta.Money(); err != nil {
		return Effects{}, err
	}
	var accountDelta *SignedMoneyDelta
	if !target.AccountID.IsZero() {
		accountDelta = &delta
	}
	return newReservationEffects(target, delta, accountDelta)
}

// CancelReservationEffects releases the active holds for a plan (matrix row
// "Reservation cancelled"): Qe[envelope] -held and, when AccountID is set,
// Qa[account] -held. The caller passes the currently held amounts; idempotent
// cancellation semantics are the command layer's concern.
func CancelReservationEffects(target ReservationTarget, held PositiveAmount, heldOnAccount PositiveAmount) (Effects, error) {
	release, err := negativeDelta(held)
	if err != nil {
		return Effects{}, err
	}
	var accountRelease *SignedMoneyDelta
	if !target.AccountID.IsZero() {
		releaseAccount, err := negativeDelta(heldOnAccount)
		if err != nil {
			return Effects{}, err
		}
		accountRelease = &releaseAccount
	}
	return newReservationEffects(target, release, accountRelease)
}

// CompleteReservedPurchaseEffects consumes/releases the plan's reservation and
// merges the actual purchase postings in one atomic effect set (matrix row
// "Reserved purchase completed"): purchaseEffects (from DebitPurchaseEffects /
// CreditCardPurchaseEffects / CreditCardPaymentEffects) plus negative hold
// deltas releasing exactly the held amounts. The final amount may be lower
// than held (release the excess) or equal; higher requires reauthorization —
// that comparison is the command layer's job, here you simply release the
// held amounts given.
//
// The plan link is not attached here: RelatedRefPlan carries a
// JournalOperationId while the target carries a PlanId, so the command layer
// attaches that link when it knows the committed journal operation.
func CompleteReservedPurchaseEffects(target ReservationTarget, purchase Effects, held PositiveAmount, heldOnAccount PositiveAmount) (Effects, error) {
	release, err := CancelReservationEffects(target, held, heldOnAccount)
	if err != nil {
		return Effects{}, err
	}
	return MergeEffects(purchase, release)
}

// newReservationEffects assembles one validated ReservationChange for a target.
// A nil accountDelta means the target holds no cash account.
func newReservationEffects(target ReservationTarget, envelopeDelta SignedMoneyDelta, accountDelta *SignedMoneyDelta) (Effects, error) {
	envelopeHold, err := NewEnvelopeHold(target.EnvelopeID, envelopeDelta)
	if err != nil {
		return Effects{}, err
	}
	var accountHold *AccountHold
	if accountDelta != nil {
		hold, err := NewAccountHold(target.AccountID, *accountDelta)
		if err != nil {
			return Effects{}, err
		}
		accountHold = &hold
	}
	change, err := NewReservationChange(target.PlanID, envelopeHold, accountHold)
	if err != nil {
		return Effects{}, err
	}
	return Effects{Reservations: []ReservationChange{change}}, nil
}
