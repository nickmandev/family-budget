// This file computes the pure posting effects of reviewed reconciliation
// adjustments and documents the zero-effect rows of the posting matrix. It
// performs NO funding, liquidity, authorization, or reconciliation reason/
// evidence checks — those belong to the command layer built later. Every
// function returns postings only; the caller applies them to a LedgerState,
// which reports unknown ids.
package domain

// ReconciliationAdjustmentEffects posts a reviewed reconciliation adjustment
// to an eligible cash account (matrix rows "Reconciliation increase in
// eligible cash" and "Reconciliation decrease in eligible cash"). delta is
// signed: positive when the confirmed balance is higher than the ledger,
// negative when lower.
//
// Budget treatment: by default the derived Ready-to-assign absorbs the change;
// alternatively the caller attributes the difference to explicit envelopes via
// funding (the same FundingSource shape as eligible-out transfers, but with
// signed envelope contributions here). Each envelope posting mirrors its
// signed contribution, so the envelope side always follows the sign of delta:
// an increase gives E[x] +amount and a decrease gives E[x] -amount. Funding
// must reconcile exactly: FromReady + sum(FromEnvelopes) == delta, where
// FromReady is the signed portion attributed to Ready-to-assign (use ZeroMoney
// when none is attributed).
//
// The adjustment requires an actor, reason and effective date at the command
// layer (CodeOverrideReasonRequired-style validation there) — this function
// only shapes the postings. A decrease may legitimately drive Ready-to-assign
// or an envelope negative: that is a visible reviewed discrepancy
// (IMPLEMENTATION_DECISIONS Q4), not an error here.
func ReconciliationAdjustmentEffects(account AccountId, delta SignedMoneyDelta, funding FundingSource) (Effects, error) {
	if account.IsZero() {
		return Effects{}, NewError(CodeInvalidValue, "reconciliation account id must not be empty")
	}
	if err := validateSignedDelta("reconciliation adjustment", delta); err != nil {
		return Effects{}, err
	}
	deltaMoney, err := delta.Money()
	if err != nil {
		return Effects{}, err
	}

	if funding.FromReady.Currency().IsZero() {
		return Effects{}, NewError(CodeInvalidValue, "reconciliation funding ready-to-assign portion must be a valid Money (use ZeroMoney when none is taken)")
	}
	if !funding.FromReady.IsZero() && funding.FromReady.IsNegative() != delta.IsNegative() {
		return Effects{}, NewError(CodeInvalidValue, "reconciliation ready-to-assign portion must follow the sign of the adjustment")
	}

	total := funding.FromReady
	seen := make(map[EnvelopeId]struct{}, len(funding.FromEnvelopes))
	for _, contribution := range funding.FromEnvelopes {
		if contribution.EnvelopeID.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "reconciliation funding envelope id must not be empty")
		}
		if _, duplicate := seen[contribution.EnvelopeID]; duplicate {
			return Effects{}, NewErrorf(CodeInvalidValue, "reconciliation funding envelope %q is listed more than once", contribution.EnvelopeID)
		}
		seen[contribution.EnvelopeID] = struct{}{}
		if contribution.Amount.Currency().IsZero() {
			return Effects{}, NewErrorf(CodeInvalidValue, "reconciliation funding amount for envelope %q must be a valid Money", contribution.EnvelopeID)
		}
		if contribution.Amount.IsZero() {
			return Effects{}, NewErrorf(CodeInvalidValue, "reconciliation funding amount for envelope %q must not be zero", contribution.EnvelopeID)
		}
		if contribution.Amount.IsNegative() != delta.IsNegative() {
			return Effects{}, NewErrorf(CodeInvalidValue, "reconciliation funding amount for envelope %q must follow the sign of the adjustment", contribution.EnvelopeID)
		}
		sum, err := total.Add(contribution.Amount)
		if err != nil {
			return Effects{}, err
		}
		total = sum
	}

	comparison, err := total.Compare(deltaMoney)
	if err != nil {
		return Effects{}, err
	}
	if comparison != 0 {
		return Effects{}, NewErrorf(CodeInvalidAmount, "reconciliation funding sources total %d minor units, want %d", total.MinorUnits(), delta.MinorUnits())
	}

	postings := PostingSet{}
	if err := postings.AddAccount(account, delta); err != nil {
		return Effects{}, err
	}
	for _, contribution := range funding.FromEnvelopes {
		envelopeDelta, err := NewSignedMoneyDelta(contribution.Amount.MinorUnits(), contribution.Amount.Currency())
		if err != nil {
			return Effects{}, err
		}
		if err := postings.AddEnvelope(contribution.EnvelopeID, envelopeDelta); err != nil {
			return Effects{}, err
		}
	}
	return Effects{Postings: postings}, nil
}

// MonthlyBoundaryEffects has no postings: the calendar month boundary changes
// reporting and target periods, not balances (matrix row "Monthly boundary /
// rollover"). Rollover is continuous balance carry-forward, so this row is a
// documented no-op rather than a transaction.
func MonthlyBoundaryEffects() Effects {
	return Effects{}
}

// SuggestionLifecycleEffects has no postings and no reservations: drafts and
// transaction suggestions never affect balances or spendable amounts (matrix
// row "Draft / transaction suggestion created, edited, or cancelled"). The
// suggestion lifecycle is a command-layer state machine; only confirmation
// enters the posting path.
func SuggestionLifecycleEffects() Effects {
	return Effects{}
}
