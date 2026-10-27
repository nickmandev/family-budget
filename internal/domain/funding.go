// This file implements the primitive spendable-sufficiency checks the
// authorization layer builds on (POSTING_MATRIX §3.1 and §5). It decides
// nothing about authorization mode, overrides, or reallocation-first flows;
// it only reports whether current spendable balances cover a requested amount.
//
// Every comparison uses spendable amounts — S[x] = E[x] - Qe[x] for envelopes
// and SA[a] = posted - Qa[a] for cash accounts — never raw posted balances.
// Reservations therefore always reduce what a plan may consume.
package domain

// RequireReadyToAssign returns INSUFFICIENT_READY_TO_ASSIGN unless the derived
// Ready-to-assign covers amount (matrix: "Require R >= P" for allocations).
//
// An amount in another currency fails with CURRENCY_MISMATCH.
func (s *LedgerState) RequireReadyToAssign(amount PositiveAmount) error {
	ready, err := s.ReadyToAssign()
	if err != nil {
		return err
	}
	comparison, err := ready.Compare(amount.Money())
	if err != nil {
		return err
	}
	if comparison < 0 {
		return NewErrorf(
			CodeInsufficientReadyToAssign,
			"ready-to-assign %d minor units does not cover required %d minor units",
			ready.MinorUnits(),
			amount.MinorUnits(),
		)
	}
	return nil
}

// RequireEnvelopeFunds returns INSUFFICIENT_ENVELOPE_FUNDS unless the
// envelope's spendable balance covers amount (P <= S[x]).
//
// An unknown envelope id fails with ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD and an
// amount in another currency fails with CURRENCY_MISMATCH.
func (s *LedgerState) RequireEnvelopeFunds(envelope EnvelopeId, amount PositiveAmount) error {
	spendable, err := s.SpendableEnvelopeBalance(envelope)
	if err != nil {
		return err
	}
	comparison, err := spendable.Compare(amount.Money())
	if err != nil {
		return err
	}
	if comparison < 0 {
		return NewErrorf(
			CodeInsufficientEnvelopeFunds,
			"envelope %q spendable balance %d minor units does not cover required %d minor units",
			envelope,
			spendable.MinorUnits(),
			amount.MinorUnits(),
		)
	}
	return nil
}

// RequireAccountFunds returns INSUFFICIENT_ACCOUNT_FUNDS unless the eligible
// cash account's spendable balance covers amount (P <= SA[a]). The account
// must be a budget-eligible liquid cash account: a card-liability account or
// an excluded account is INVALID_VALUE here (a planned debit purchase or card
// repayment draws on eligible cash).
//
// An unknown account id fails with ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD and an
// amount in another currency fails with CURRENCY_MISMATCH.
func (s *LedgerState) RequireAccountFunds(account AccountId, amount PositiveAmount) error {
	spendable, err := s.spendableEligibleCash(account)
	if err != nil {
		return err
	}
	comparison, err := spendable.Compare(amount.Money())
	if err != nil {
		return err
	}
	if comparison < 0 {
		return NewErrorf(
			CodeInsufficientAccountFunds,
			"account %q spendable balance %d minor units does not cover required %d minor units",
			account,
			spendable.MinorUnits(),
			amount.MinorUnits(),
		)
	}
	return nil
}

// EnvelopeShortfall returns how much more the envelope needs to cover amount:
// zero when already covered, amount - S[x] otherwise. Never negative.
//
// An unknown envelope id fails with ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD and an
// amount in another currency fails with CURRENCY_MISMATCH.
func (s *LedgerState) EnvelopeShortfall(envelope EnvelopeId, amount PositiveAmount) (Money, error) {
	spendable, err := s.SpendableEnvelopeBalance(envelope)
	if err != nil {
		return Money{}, err
	}
	return shortfallFor(amount.Money(), spendable)
}

// AccountShortfall returns amount - SA[a] when positive, zero otherwise.
//
// The account must be a budget-eligible liquid cash account; a card-liability
// account or an excluded account is INVALID_VALUE. An unknown account id fails
// with ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD and an amount in another currency
// fails with CURRENCY_MISMATCH.
func (s *LedgerState) AccountShortfall(account AccountId, amount PositiveAmount) (Money, error) {
	spendable, err := s.spendableEligibleCash(account)
	if err != nil {
		return Money{}, err
	}
	return shortfallFor(amount.Money(), spendable)
}

// spendableEligibleCash validates that the account is budget-eligible liquid
// cash and returns SA[a] = posted balance - Qa[a].
func (s *LedgerState) spendableEligibleCash(id AccountId) (Money, error) {
	account, err := s.lookupAccount(id)
	if err != nil {
		return Money{}, err
	}
	if !account.meta.BudgetEligible || !account.meta.Kind.IsLiquidCash() {
		return Money{}, NewErrorf(CodeInvalidValue, "account %q is not a budget-eligible liquid cash account", id)
	}
	return account.posted.Sub(account.reserved)
}

// shortfallFor returns amount - spendable when that difference is positive and
// zero otherwise. A negative spendable balance naturally yields the full
// amount plus the deficit; Sub's checked arithmetic and currency validation
// apply.
func shortfallFor(amount, spendable Money) (Money, error) {
	shortfall, err := amount.Sub(spendable)
	if err != nil {
		return Money{}, err
	}
	if shortfall.IsNegative() {
		return ZeroMoney(amount.Currency())
	}
	return shortfall, nil
}
