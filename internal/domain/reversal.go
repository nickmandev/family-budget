// This file derives the display state of a committed financial transaction
// (STATE_TRANSITIONS §3 and POSTING_MATRIX §4.2) from linked compensating
// events. The status is never stored as a mutable column: it is a pure
// function of the original reversible amount and the linked reversal amount.
//
// Refunds and reversals are distinct. A refund is a genuine new event, not a
// correction, so refunded amounts are tracked separately and never reduce the
// reversible remainder or change ReversalStatus. A full refund may
// economically offset a purchase, but the transaction remains COMMITTED.
package domain

// ReversalStatus is the derived display state of a committed financial
// transaction (STATE_TRANSITIONS §3). It is computed from the original
// reversible amount and the linked compensating amounts — never stored as a
// mutable column and never derived from refunds (a refund is a genuine new
// event, not a correction).
type ReversalStatus string

const (
	// ReversalStatusCommitted means the original postings exist and nothing
	// has compensated them. Refunds do not change this status.
	ReversalStatusCommitted ReversalStatus = "COMMITTED"
	// ReversalStatusPartiallyReversed means linked compensating reversals have
	// reversed only part of the reversible amount.
	ReversalStatusPartiallyReversed ReversalStatus = "PARTIALLY_REVERSED"
	// ReversalStatusFullyReversed means linked compensating reversals have
	// reversed the full reversible effect.
	ReversalStatusFullyReversed ReversalStatus = "FULLY_REVERSED"
)

// ParseReversalStatus validates a raw reversal status.
func ParseReversalStatus(raw string) (ReversalStatus, error) {
	status := ReversalStatus(raw)
	if !status.IsValid() {
		return "", NewErrorf(CodeInvalidValue, "unknown reversal status %q", raw)
	}
	return status, nil
}

// IsValid reports whether the status is one of the defined values.
func (s ReversalStatus) IsValid() bool {
	switch s {
	case ReversalStatusCommitted,
		ReversalStatusPartiallyReversed,
		ReversalStatusFullyReversed:
		return true
	default:
		return false
	}
}

// String returns the stored status text.
func (s ReversalStatus) String() string {
	return string(s)
}

// ReversalLedger summarizes the linked compensation against one original
// operation: totalReversed accumulates REVERSAL-kind (and replacement-
// compensation) amounts only; totalRefunded accumulates REFUND-kind amounts
// separately and does NOT reduce the reversible remainder (POSTING_MATRIX §4.2:
// "A refund is a genuine new event, not a correction").
//
// The zero value is invalid: construct with NewReversalLedger.
type ReversalLedger struct {
	original      PositiveAmount
	totalReversed Money
	totalRefunded Money
	links         []RelatedOperationRef
}

// NewReversalLedger creates a ledger for original, the committed operation's
// reversible amount. The fresh ledger reports ReversalStatusCommitted with the
// full reversible and refundable remainders.
func NewReversalLedger(original PositiveAmount) (*ReversalLedger, error) {
	if original.MinorUnits() <= 0 {
		return nil, NewError(CodeInvalidAmount, "reversal original amount must be greater than zero")
	}
	if err := original.Currency().validate(); err != nil {
		return nil, err
	}
	zero, err := ZeroMoney(original.Currency())
	if err != nil {
		return nil, err
	}
	return &ReversalLedger{
		original:      original,
		totalReversed: zero,
		totalRefunded: zero,
	}, nil
}

// RecordReversal credits a compensating reversal (or a partial reversal) of
// amount against the original. Cumulative reversals beyond the original amount
// are rejected with REVERSAL_EXCEEDS_REMAINDER (POSTING_MATRIX §5), and the
// ledger is left unchanged. The link is recorded as RelatedRefReversal.
func (r *ReversalLedger) RecordReversal(amount PositiveAmount, op JournalOperationId) error {
	if err := r.validateInitialized(); err != nil {
		return err
	}
	if amount.MinorUnits() <= 0 {
		return NewError(CodeInvalidAmount, "reversal amount must be greater than zero")
	}
	if err := amount.Currency().validate(); err != nil {
		return err
	}
	link, err := NewRelatedOperationRef(RelatedRefReversal, op)
	if err != nil {
		return err
	}
	next, err := r.totalReversed.Add(amount.Money())
	if err != nil {
		return err
	}
	comparison, err := next.Compare(r.original.Money())
	if err != nil {
		return err
	}
	if comparison > 0 {
		return NewErrorf(
			CodeReversalExceedsRemainder,
			"cumulative reversals of %d minor units exceed original amount of %d minor units",
			next.MinorUnits(),
			r.original.MinorUnits(),
		)
	}

	r.totalReversed = next
	r.links = append(r.links, link)
	return nil
}

// RecordRefund credits a genuine refund (cash or card) of amount linked to the
// original. Cumulative refunds beyond the original amount are rejected with
// REFUND_EXCEEDS_REMAINDER, and the ledger is left unchanged. Refunds never
// change ReversalStatus: they are tracked separately from reversals. The link
// is recorded as RelatedRefRefund.
func (r *ReversalLedger) RecordRefund(amount PositiveAmount, op JournalOperationId) error {
	if err := r.validateInitialized(); err != nil {
		return err
	}
	if amount.MinorUnits() <= 0 {
		return NewError(CodeInvalidAmount, "refund amount must be greater than zero")
	}
	if err := amount.Currency().validate(); err != nil {
		return err
	}
	link, err := NewRelatedOperationRef(RelatedRefRefund, op)
	if err != nil {
		return err
	}
	next, err := r.totalRefunded.Add(amount.Money())
	if err != nil {
		return err
	}
	comparison, err := next.Compare(r.original.Money())
	if err != nil {
		return err
	}
	if comparison > 0 {
		return NewErrorf(
			CodeRefundExceedsRemainder,
			"cumulative refunds of %d minor units exceed original amount of %d minor units",
			next.MinorUnits(),
			r.original.MinorUnits(),
		)
	}

	r.totalRefunded = next
	r.links = append(r.links, link)
	return nil
}

// Status derives the display state: COMMITTED when totalReversed == 0;
// FULLY_REVERSED when totalReversed >= original; PARTIALLY_REVERSED otherwise.
// Refund totals never affect the result.
func (r *ReversalLedger) Status() ReversalStatus {
	if r == nil || r.original.MinorUnits() <= 0 {
		return ""
	}
	if r.totalReversed.IsZero() {
		return ReversalStatusCommitted
	}
	comparison, err := r.totalReversed.Compare(r.original.Money())
	if err != nil {
		return ""
	}
	if comparison >= 0 {
		return ReversalStatusFullyReversed
	}
	return ReversalStatusPartiallyReversed
}

// ReversibleRemainder returns original − totalReversed (zero once fully
// reversed; never negative).
func (r *ReversalLedger) ReversibleRemainder() Money {
	if r == nil || r.original.MinorUnits() <= 0 {
		return Money{}
	}
	remaining, err := r.original.Money().Sub(r.totalReversed)
	if err != nil {
		return Money{}
	}
	return remaining
}

// RefundableRemainder returns original − totalRefunded (zero once fully
// refunded; never negative).
func (r *ReversalLedger) RefundableRemainder() Money {
	if r == nil || r.original.MinorUnits() <= 0 {
		return Money{}
	}
	remaining, err := r.original.Money().Sub(r.totalRefunded)
	if err != nil {
		return Money{}
	}
	return remaining
}

// Links returns the recorded related-operation references in insertion order.
// The returned slice is a copy, so callers cannot mutate the ledger's history.
func (r *ReversalLedger) Links() []RelatedOperationRef {
	if r == nil {
		return nil
	}
	copied := make([]RelatedOperationRef, len(r.links))
	copy(copied, r.links)
	return copied
}

// validateInitialized rejects nil and zero-value ledgers.
func (r *ReversalLedger) validateInitialized() error {
	if r == nil || r.original.MinorUnits() <= 0 {
		return NewError(CodeInvalidValue, "reversal ledger must be initialized")
	}
	return nil
}
