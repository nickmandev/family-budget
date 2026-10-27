package domain

// VerifyBudgetIdentity asserts the fundamental accounting identity
// A = R + sum(E[i]) over independently computed values (POSTING_MATRIX §1).
// A mismatch is an internal integrity error (IMPLEMENTATION_DECISIONS Q4),
// never a user-facing budget condition.
func VerifyBudgetIdentity(budgetableCash, readyToAssign, envelopeSum Money) error {
	identity, err := readyToAssign.Add(envelopeSum)
	if err != nil {
		return err
	}
	comparison, err := budgetableCash.Compare(identity)
	if err != nil {
		return err
	}
	if comparison != 0 {
		return NewErrorf(
			CodeAccountingInvariantViolation,
			"budget identity violated: budgetable cash %d minor units != ready-to-assign %d + envelope sum %d",
			budgetableCash.MinorUnits(),
			readyToAssign.MinorUnits(),
			envelopeSum.MinorUnits(),
		)
	}
	return nil
}

// CheckInvariants validates structural soundness of the ledger projection:
// reservations (Qe/Qa) are never negative, card-liability accounts are never
// budget-eligible, and the derived identity holds. Negative envelope balances
// and negative Ready-to-assign are legitimate recorded outcomes (Q4) and must
// NOT trigger an error here.
func (s *LedgerState) CheckInvariants() error {
	for id, account := range s.accounts {
		if account.reserved.IsNegative() {
			return NewErrorf(CodeAccountingInvariantViolation, "account %q has a negative reservation", id)
		}
		if account.meta.Kind == AccountKindCardLiability && account.meta.BudgetEligible {
			return NewErrorf(CodeAccountingInvariantViolation, "card liability account %q is budget eligible", id)
		}
	}
	for id, envelope := range s.envelopes {
		if envelope.reserved.IsNegative() {
			return NewErrorf(CodeAccountingInvariantViolation, "envelope %q has a negative reservation", id)
		}
	}

	budgetable, err := s.BudgetableCash()
	if err != nil {
		return err
	}
	envelopeSum, err := s.SumEnvelopeBalances()
	if err != nil {
		return err
	}
	readyToAssign, err := s.ReadyToAssign()
	if err != nil {
		return err
	}
	return VerifyBudgetIdentity(budgetable, readyToAssign, envelopeSum)
}
