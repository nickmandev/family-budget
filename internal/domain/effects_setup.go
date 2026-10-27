// This file computes the pure posting effects of setup and budget events from
// the posting matrix: opening balances, received income, unrelated
// reimbursements, allocation, and reallocation. It performs NO funding,
// liquidity, card-limit, or authorization checks — those belong to the
// authorization layer built later. Every function returns the postings only;
// the caller applies them to a LedgerState, which reports unknown ids.
package domain

// OpeningBalanceEffects posts an opening balance (or its actual signed
// balance) to one account. Matrix rows "Opening eligible cash balance",
// "Opening excluded cash balance", "Opening card liability" share this
// posting shape: a single account posting whose budget effect is determined
// by the account's kind/eligibility in LedgerState — eligible liquid cash
// raises derived Ready-to-assign (not income), excluded accounts and card
// liabilities have no budget effect.
func OpeningBalanceEffects(account AccountId, delta SignedMoneyDelta) (Effects, error) {
	postings := PostingSet{}
	if err := postings.AddAccount(account, delta); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}

// IncomeReceivedEffects posts received income to an account. Eligible
// accounts raise derived Ready-to-assign; excluded accounts do not. Income is
// only recognized when actually received (POSTING_MATRIX).
func IncomeReceivedEffects(account AccountId, amount PositiveAmount) (Effects, error) {
	delta, err := positiveDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddAccount(account, delta); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}

// ReimbursementReceivedEffects posts an unrelated reimbursement into an
// account. Default budget treatment: same as income (derived Ready-to-assign
// rises). An explicit alternate budget treatment is a command-layer choice,
// not a posting-shape change here.
func ReimbursementReceivedEffects(account AccountId, amount PositiveAmount) (Effects, error) {
	delta, err := positiveDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddAccount(account, delta); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}

// AllocateFundsEffects moves amount from derived Ready-to-assign into an
// envelope: E[x] +P. Ready-to-assign decreases as a derived consequence.
// Requires sufficient Ready-to-assign — enforced by the authorization layer,
// not here.
func AllocateFundsEffects(envelope EnvelopeId, amount PositiveAmount) (Effects, error) {
	delta, err := positiveDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddEnvelope(envelope, delta); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}

// ReallocateFundsEffects moves amount between envelopes: E[source] -P,
// E[destination] +P. Source and destination must be distinct (INVALID_VALUE).
// Requires sufficient source spendable balance — enforced by the
// authorization layer, not here.
func ReallocateFundsEffects(source, destination EnvelopeId, amount PositiveAmount) (Effects, error) {
	if source == destination {
		return Effects{}, NewError(CodeInvalidValue, "reallocation source and destination envelopes must be distinct")
	}
	increase, err := positiveDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	decrease, err := negativeDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddEnvelope(source, decrease); err != nil {
		return Effects{}, err
	}
	if err := postings.AddEnvelope(destination, increase); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}

// positiveDelta converts a strictly positive amount to a positive posting
// delta through the validating constructor.
func positiveDelta(amount PositiveAmount) (SignedMoneyDelta, error) {
	money := amount.Money()
	return NewSignedMoneyDelta(money.MinorUnits(), money.Currency())
}

// negativeDelta converts a strictly positive amount to its exact negation
// through checked arithmetic and the validating constructor.
func negativeDelta(amount PositiveAmount) (SignedMoneyDelta, error) {
	money := amount.Money()
	negated, err := money.Neg()
	if err != nil {
		return SignedMoneyDelta{}, err
	}
	return NewSignedMoneyDelta(negated.MinorUnits(), negated.Currency())
}
