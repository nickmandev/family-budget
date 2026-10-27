// This file computes the pure posting effects of refunds from the posting
// matrix: a refund of a debit/cash purchase back to eligible cash and a refund
// of a credit-card purchase back onto the card. It performs NO
// funding, liquidity, refundable-remainder, or authorization checks — those
// belong to the command/authorization layer built later. Every function returns
// the postings and the link to the original operation; the caller applies them
// to a LedgerState, which reports unknown ids.
package domain

// CashRefundEffects posts a genuine refund of a debit/cash purchase back to
// eligible cash: account +P and original spending envelope +P (matrix row
// "Refund to eligible cash from debit/cash purchase"). It links to the
// original purchase via RelatedRefOriginal. Cumulative refunds cannot exceed
// the refundable amount — remainder enforcement belongs to the command layer
// (CodeRefundExceedsRemainder there).
//
// Because both cash and its envelope rise by P, derived Ready-to-assign is
// unchanged by the refund itself; it is restored relative to a state where the
// original purchase had reduced both sides.
func CashRefundEffects(refundAccount AccountId, original JournalOperationId, spendingEnvelope EnvelopeId, amount PositiveAmount) (Effects, error) {
	originalRef, err := NewRelatedOperationRef(RelatedRefOriginal, original)
	if err != nil {
		return Effects{}, err
	}
	increase, err := positiveDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddAccount(refundAccount, increase); err != nil {
		return Effects{}, err
	}
	if err := postings.AddEnvelope(spendingEnvelope, increase); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings, Related: []RelatedOperationRef{originalRef}}, nil
}

// CardRefundEffects posts a refund of a credit-card purchase: card liability
// -P, original spending envelope +P, card-payment envelope -P (matrix row
// "Refund to credit card from card purchase"). The card-payment envelope may
// become negative if the card was already repaid — that is a legitimate
// traceable condition and must NOT be rejected here. Links to the original via
// RelatedRefOriginal. Spending and payment envelopes must be distinct
// (INVALID_VALUE).
func CardRefundEffects(card AccountId, original JournalOperationId, spendingEnvelope, paymentEnvelope EnvelopeId, amount PositiveAmount) (Effects, error) {
	if spendingEnvelope == paymentEnvelope {
		return Effects{}, NewError(CodeInvalidValue, "card refund spending and payment envelopes must be distinct")
	}
	originalRef, err := NewRelatedOperationRef(RelatedRefOriginal, original)
	if err != nil {
		return Effects{}, err
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
	if err := postings.AddAccount(card, decrease); err != nil {
		return Effects{}, err
	}
	if err := postings.AddEnvelope(spendingEnvelope, increase); err != nil {
		return Effects{}, err
	}
	if err := postings.AddEnvelope(paymentEnvelope, decrease); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings, Related: []RelatedOperationRef{originalRef}}, nil
}
