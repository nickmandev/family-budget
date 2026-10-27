// This file computes the pure posting effects of transfers from the posting
// matrix: eligible-to-eligible, excluded-to-eligible, eligible-to-excluded, and
// wholly-excluded. It performs NO funding, liquidity, or authorization checks —
// those belong to the authorization layer built later. Every function returns
// the postings only; the caller applies them to a LedgerState, which reports
// unknown ids.
package domain

// TransferEligibleToEligibleEffects moves value between two eligible cash
// accounts: source -P, destination +P. No budget effect — budgetable cash is
// unchanged (matrix row "Transfer: eligible cash account to eligible cash
// account"). Source and destination must be distinct (INVALID_VALUE).
func TransferEligibleToEligibleEffects(source, destination AccountId, amount PositiveAmount) (Effects, error) {
	if source == destination {
		return Effects{}, NewError(CodeInvalidValue, "transfer source and destination accounts must be distinct")
	}
	decrease, err := negativeDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	increase, err := positiveDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddAccount(source, decrease); err != nil {
		return Effects{}, err
	}
	if err := postings.AddAccount(destination, increase); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}

// TransferExcludedToEligibleEffects brings outside money into the budget:
// excluded source -P, eligible destination +P. Derived Ready-to-assign rises
// by P. It is a transfer, never income (matrix row "Transfer: excluded cash to
// eligible cash"). Source and destination must be distinct (INVALID_VALUE).
func TransferExcludedToEligibleEffects(source, destination AccountId, amount PositiveAmount) (Effects, error) {
	if source == destination {
		return Effects{}, NewError(CodeInvalidValue, "transfer source and destination accounts must be distinct")
	}
	decrease, err := negativeDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	increase, err := positiveDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddAccount(source, decrease); err != nil {
		return Effects{}, err
	}
	if err := postings.AddAccount(destination, increase); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}

// FundingSource names where budget value comes from when money leaves the
// budget boundary: from derived Ready-to-assign and/or from explicit envelopes.
//
// Callers build a FundingSource from already-validated Money values;
// TransferEligibleOutToExcludedEffects re-validates it. When no
// Ready-to-assign portion is taken, FromReady must still be a valid zero Money
// in the household currency (use ZeroMoney); a zero-value Money is rejected as
// an unbuilt value.
type FundingSource struct {
	// FromReady is the portion taken from derived Ready-to-assign.
	FromReady Money // may be zero
	// FromEnvelopes lists explicit envelope contributions (each > 0).
	FromEnvelopes []EnvelopeFunding
}

// EnvelopeFunding is one explicit envelope contribution to a budget-boundary
// transfer.
type EnvelopeFunding struct {
	EnvelopeID EnvelopeId
	Amount     Money // strictly positive
}

// TransferEligibleOutToExcludedEffects sends value out of the budget:
// eligible source -P, excluded destination +P, and explicit budget-removal
// postings so that A = R + sum(E) still holds: the funding sources must total
// exactly P (mismatch → INVALID_AMOUNT), each envelope contribution posts
// E[x] -amount, and the Ready-to-assign portion is removed from derived R by
// the account-side change (matrix row "Transfer: eligible cash to excluded
// cash" and IMPLEMENTATION_DECISIONS Q5). Reserved envelope funds are not
// consumed here — the authorization layer resolves reservations first.
//
// Source and destination must be distinct (INVALID_VALUE). Funding entries
// must have non-empty, distinct envelope ids and strictly positive amounts
// (INVALID_VALUE). A real external transfer may leave Ready-to-assign negative;
// that is a legitimate recorded funding shortfall and is not rejected here.
func TransferEligibleOutToExcludedEffects(source, destination AccountId, amount PositiveAmount, funding FundingSource) (Effects, error) {
	if source == destination {
		return Effects{}, NewError(CodeInvalidValue, "transfer source and destination accounts must be distinct")
	}
	decrease, err := negativeDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	increase, err := positiveDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	if err := validateFundingSource(funding, amount); err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddAccount(source, decrease); err != nil {
		return Effects{}, err
	}
	if err := postings.AddAccount(destination, increase); err != nil {
		return Effects{}, err
	}
	for _, contribution := range funding.FromEnvelopes {
		delta, err := negativeDeltaFromMoney(contribution.Amount)
		if err != nil {
			return Effects{}, err
		}
		if err := postings.AddEnvelope(contribution.EnvelopeID, delta); err != nil {
			return Effects{}, err
		}
	}
	return Effects{Postings: postings}, nil
}

// TransferExcludedToExcludedEffects moves value wholly outside the budget:
// source -P, destination +P. Outside the budget identity (matrix row "Transfer
// wholly within excluded accounts"). Source and destination must be distinct
// (INVALID_VALUE).
func TransferExcludedToExcludedEffects(source, destination AccountId, amount PositiveAmount) (Effects, error) {
	if source == destination {
		return Effects{}, NewError(CodeInvalidValue, "transfer source and destination accounts must be distinct")
	}
	decrease, err := negativeDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	increase, err := positiveDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddAccount(source, decrease); err != nil {
		return Effects{}, err
	}
	if err := postings.AddAccount(destination, increase); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}

// validateFundingSource checks the structural shape of a budget-boundary
// funding plan and that its contributions total exactly amount. It does not
// check whether the source account or envelopes actually hold the funds.
func validateFundingSource(funding FundingSource, amount PositiveAmount) error {
	if funding.FromReady.Currency().IsZero() {
		return NewError(CodeInvalidValue, "funding ready-to-assign portion must be a valid Money (use ZeroMoney when none is taken)")
	}
	if funding.FromReady.IsNegative() {
		return NewError(CodeInvalidAmount, "funding ready-to-assign portion must not be negative")
	}

	total := funding.FromReady
	seen := make(map[EnvelopeId]struct{}, len(funding.FromEnvelopes))
	for _, contribution := range funding.FromEnvelopes {
		if contribution.EnvelopeID.IsZero() {
			return NewError(CodeInvalidValue, "funding envelope id must not be empty")
		}
		if _, duplicate := seen[contribution.EnvelopeID]; duplicate {
			return NewErrorf(CodeInvalidValue, "funding envelope %q is listed more than once", contribution.EnvelopeID)
		}
		seen[contribution.EnvelopeID] = struct{}{}
		if contribution.Amount.Currency().IsZero() {
			return NewErrorf(CodeInvalidValue, "funding amount for envelope %q must be a valid Money", contribution.EnvelopeID)
		}
		if !contribution.Amount.IsPositive() {
			return NewErrorf(CodeInvalidValue, "funding amount for envelope %q must be greater than zero", contribution.EnvelopeID)
		}
		sum, err := total.Add(contribution.Amount)
		if err != nil {
			return err
		}
		total = sum
	}

	comparison, err := total.Compare(amount.Money())
	if err != nil {
		return err
	}
	if comparison != 0 {
		return NewErrorf(CodeInvalidAmount, "funding sources total %d minor units, want %d", total.MinorUnits(), amount.MinorUnits())
	}
	return nil
}

// negativeDeltaFromMoney negates an already-validated Money through checked
// arithmetic and the validating delta constructor.
func negativeDeltaFromMoney(m Money) (SignedMoneyDelta, error) {
	negated, err := m.Neg()
	if err != nil {
		return SignedMoneyDelta{}, err
	}
	return NewSignedMoneyDelta(negated.MinorUnits(), negated.Currency())
}
