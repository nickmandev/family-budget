package domain

// EnvelopeHold changes the active reservation Qe against one envelope.
type EnvelopeHold struct {
	EnvelopeID EnvelopeId
	Delta      SignedMoneyDelta // positive holds more, negative releases
}

// NewEnvelopeHold validates the envelope id and delta, then builds an
// EnvelopeHold.
func NewEnvelopeHold(id EnvelopeId, delta SignedMoneyDelta) (EnvelopeHold, error) {
	if id.IsZero() {
		return EnvelopeHold{}, NewError(CodeInvalidValue, "envelope hold envelope id must not be empty")
	}
	if err := validateSignedDelta("envelope hold", delta); err != nil {
		return EnvelopeHold{}, err
	}
	return EnvelopeHold{EnvelopeID: id, Delta: delta}, nil
}

// AccountHold changes the active reservation Qa against one eligible cash
// account.
type AccountHold struct {
	AccountID AccountId
	Delta     SignedMoneyDelta
}

// NewAccountHold validates the account id and delta, then builds an
// AccountHold.
func NewAccountHold(id AccountId, delta SignedMoneyDelta) (AccountHold, error) {
	if id.IsZero() {
		return AccountHold{}, NewError(CodeInvalidValue, "account hold account id must not be empty")
	}
	if err := validateSignedDelta("account hold", delta); err != nil {
		return AccountHold{}, err
	}
	return AccountHold{AccountID: id, Delta: delta}, nil
}

// ReservationChange is the hold effect of one planned-operation event.
//
// Account is nil when the plan holds no cash-account funds — e.g. a credit-card
// purchase reserves only the spending envelope (IMPLEMENTATION_DECISIONS Q1);
// a planned card repayment sets both (Q2). When Account is non-nil its
// AccountID and Delta must both be present.
type ReservationChange struct {
	PlanID   PlanId
	Envelope EnvelopeHold
	Account  *AccountHold
}

// NewReservationChange validates the plan, envelope hold, and optional account
// hold, then builds a ReservationChange. A nil Account is valid; a
// partially-populated AccountHold is rejected with INVALID_VALUE.
func NewReservationChange(plan PlanId, envelope EnvelopeHold, account *AccountHold) (ReservationChange, error) {
	if plan.IsZero() {
		return ReservationChange{}, NewError(CodeInvalidValue, "reservation change plan id must not be empty")
	}
	if envelope.EnvelopeID.IsZero() {
		return ReservationChange{}, NewError(CodeInvalidValue, "reservation change envelope id must not be empty")
	}
	if err := validateSignedDelta("reservation change envelope hold", envelope.Delta); err != nil {
		return ReservationChange{}, err
	}

	var accountCopy *AccountHold
	if account != nil {
		if account.AccountID.IsZero() {
			return ReservationChange{}, NewError(CodeInvalidValue, "reservation change account id must not be empty")
		}
		if err := validateSignedDelta("reservation change account hold", account.Delta); err != nil {
			return ReservationChange{}, err
		}
		held := *account
		accountCopy = &held
	}

	return ReservationChange{
		PlanID:   plan,
		Envelope: envelope,
		Account:  accountCopy,
	}, nil
}
