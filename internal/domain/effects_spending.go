// This file computes the pure posting effects of spending events from the
// posting matrix: debit/cash purchases, credit-card purchases, credit-card
// payments, and card fees/interest. It performs NO funding, liquidity,
// card-limit, or authorization checks — those belong to the authorization
// layer built later. Every function returns the postings only; the caller
// applies them to a LedgerState, which reports unknown ids.
package domain

// DebitPurchaseEffects posts a debit or cash-wallet purchase: eligible
// account -P and spending envelope -P. Planned purchases additionally require
// envelope and source-account checks in the authorization layer; an actual
// external purchase is recorded truthfully even when underfunded (flagged
// later, never dropped).
func DebitPurchaseEffects(account AccountId, spendingEnvelope EnvelopeId, amount PositiveAmount) (Effects, error) {
	decrease, err := negativeDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddAccount(account, decrease); err != nil {
		return Effects{}, err
	}
	if err := postings.AddEnvelope(spendingEnvelope, decrease); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}

// CreditCardPurchaseEffects posts a card purchase: card liability +P,
// spending envelope -P, card-payment envelope +P. No eligible cash moves at
// purchase time. Spending and payment envelopes must be distinct
// (INVALID_VALUE). Reserving funds for a planned card purchase holds the
// spending envelope only (IMPLEMENTATION_DECISIONS Q1) — that is a
// reservation-layer concern, not part of these postings.
func CreditCardPurchaseEffects(card AccountId, spendingEnvelope, paymentEnvelope EnvelopeId, amount PositiveAmount) (Effects, error) {
	if spendingEnvelope == paymentEnvelope {
		return Effects{}, NewError(CodeInvalidValue, "card purchase spending and payment envelopes must be distinct")
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
	if err := postings.AddAccount(card, increase); err != nil {
		return Effects{}, err
	}
	if err := postings.AddEnvelope(spendingEnvelope, decrease); err != nil {
		return Effects{}, err
	}
	if err := postings.AddEnvelope(paymentEnvelope, increase); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}

// CreditCardPaymentEffects posts a card repayment: eligible cash account -P,
// card liability -P, card-payment envelope -P. Repayment settles liability and
// is never a second expense. Cash account and card must be distinct
// (INVALID_VALUE). Planned repayment must not exceed the outstanding
// liability without explicit consent — authorization-layer concern.
func CreditCardPaymentEffects(cashAccount, card AccountId, paymentEnvelope EnvelopeId, amount PositiveAmount) (Effects, error) {
	if cashAccount == card {
		return Effects{}, NewError(CodeInvalidValue, "card payment cash account and card must be distinct")
	}
	decrease, err := negativeDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddAccount(cashAccount, decrease); err != nil {
		return Effects{}, err
	}
	if err := postings.AddAccount(card, decrease); err != nil {
		return Effects{}, err
	}
	if err := postings.AddEnvelope(paymentEnvelope, decrease); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}

// CardFeeOrInterestEffects posts a card fee or interest charge: card
// liability +P, fee envelope -P, card-payment envelope +P. Fee and payment
// envelopes must be distinct (INVALID_VALUE). Underfunded fee envelopes may go
// negative and are flagged at the command layer.
func CardFeeOrInterestEffects(card AccountId, feeEnvelope, paymentEnvelope EnvelopeId, amount PositiveAmount) (Effects, error) {
	if feeEnvelope == paymentEnvelope {
		return Effects{}, NewError(CodeInvalidValue, "card fee and payment envelopes must be distinct")
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
	if err := postings.AddAccount(card, increase); err != nil {
		return Effects{}, err
	}
	if err := postings.AddEnvelope(feeEnvelope, decrease); err != nil {
		return Effects{}, err
	}
	if err := postings.AddEnvelope(paymentEnvelope, increase); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings}, nil
}
