// This file computes the pure posting effects of corrections from the posting
// matrix: reversal of a committed operation, replacement after correction,
// category correction, account/source correction, and metadata-only
// correction. Corrections are compensating postings, never rewrites: the
// original journal operation and its postings stay untouched. It performs NO
// funding, liquidity, or authorization checks — those belong to the command
// layer built later. Reversal and refund remainder caps need the journal
// history, so CodeReversalExceedsRemainder / CodeRefundExceedsRemainder are
// enforced there, not here. Every function returns new postings only; the
// caller applies them to a LedgerState, which reports unknown ids.
package domain

// ReversalEffects builds compensating postings that reverse the original
// operation's financial effect exactly: every account and envelope posting is
// the negation of the original posting set (matrix row "Reversal of committed
// purchase/event"). It links to the original via RelatedRefReversal.
//
// The full original posting set is reversed. Partial reversals are built by
// the command layer constructing the partial original set first, because only
// the journal history knows the reversible remainder; that remainder cap
// (CodeReversalExceedsRemainder) is not enforced in this pure layer.
func ReversalEffects(original PostingSet, originalOp JournalOperationId) (Effects, error) {
	if originalOp.IsZero() {
		return Effects{}, NewError(CodeInvalidValue, "reversal original operation id must not be empty")
	}
	if original.IsEmpty() {
		return Effects{}, NewError(CodeInvalidValue, "reversal original posting set must not be empty")
	}
	link, err := NewRelatedOperationRef(RelatedRefReversal, originalOp)
	if err != nil {
		return Effects{}, err
	}
	compensating, err := original.Negate()
	if err != nil {
		return Effects{}, err
	}
	return Effects{Postings: compensating, Related: []RelatedOperationRef{link}}, nil
}

// ReplacementEffects posts a compensating reversal of the original followed
// by the corrected postings in one atomic effect set (matrix row "Replacement
// after correction"). Both the RelatedRefReversal and RelatedRefReplacement
// links point at the original operation id, preserving the audit trail.
//
// The corrected effects must contain at least one posting. Passing the
// original PostingSet by value and reversing it into a new set keeps the
// caller's original input unmodified.
func ReplacementEffects(original PostingSet, originalOp JournalOperationId, corrected Effects) (Effects, error) {
	if originalOp.IsZero() {
		return Effects{}, NewError(CodeInvalidValue, "replacement original operation id must not be empty")
	}
	if original.IsEmpty() {
		return Effects{}, NewError(CodeInvalidValue, "replacement original posting set must not be empty")
	}
	if corrected.Postings.IsEmpty() {
		return Effects{}, NewError(CodeInvalidValue, "replacement corrected postings must not be empty")
	}
	reversal, err := ReversalEffects(original, originalOp)
	if err != nil {
		return Effects{}, err
	}
	link, err := NewRelatedOperationRef(RelatedRefReplacement, originalOp)
	if err != nil {
		return Effects{}, err
	}
	replacementLink := Effects{Related: []RelatedOperationRef{link}}
	merged, err := MergeEffects(reversal, corrected, replacementLink)
	if err != nil {
		return Effects{}, err
	}
	return merged, nil
}

// CategoryCorrectionEffects reverses the original budget-category postings and
// posts the corrected category postings, leaving account postings untouched
// (matrix row "Category correction"): E[original] +P, E[corrected] -P, so
// sum(E) and derived Ready-to-assign are unchanged. The corrected category
// replaces the original budget posting, so the link is
// RelatedRefReplacement pointing at the original operation.
//
// The original and corrected envelopes must be distinct (INVALID_VALUE). The
// amount is the corrected amount being moved between envelopes; whether it
// matches the historical original is a command-layer check.
func CategoryCorrectionEffects(originalOp JournalOperationId, originalEnvelope, correctedEnvelope EnvelopeId, amount PositiveAmount) (Effects, error) {
	if originalOp.IsZero() {
		return Effects{}, NewError(CodeInvalidValue, "category correction original operation id must not be empty")
	}
	if originalEnvelope.IsZero() {
		return Effects{}, NewError(CodeInvalidValue, "category correction original envelope id must not be empty")
	}
	if correctedEnvelope.IsZero() {
		return Effects{}, NewError(CodeInvalidValue, "category correction corrected envelope id must not be empty")
	}
	if originalEnvelope == correctedEnvelope {
		return Effects{}, NewError(CodeInvalidValue, "category correction original and corrected envelopes must be distinct")
	}
	restore, err := positiveDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	corrected, err := negativeDelta(amount)
	if err != nil {
		return Effects{}, err
	}
	link, err := NewRelatedOperationRef(RelatedRefReplacement, originalOp)
	if err != nil {
		return Effects{}, err
	}
	postings := PostingSet{}
	if err := postings.AddEnvelope(originalEnvelope, restore); err != nil {
		return Effects{}, err
	}
	if err := postings.AddEnvelope(correctedEnvelope, corrected); err != nil {
		return Effects{}, err
	}
	return Effects{Postings: postings, Related: []RelatedOperationRef{link}}, nil
}

// AccountCorrectionEffects compensates the original account effect and posts
// the corrected account effect, compensating/reposting budget effects as
// needed so the identity holds after the whole correction commits (matrix row
// "Account/source correction"). It is the named entry point for the
// replacement shape, so it delegates to ReplacementEffects with the corrected
// effects supplied by the caller.
func AccountCorrectionEffects(original PostingSet, originalOp JournalOperationId, corrected Effects) (Effects, error) {
	return ReplacementEffects(original, originalOp, corrected)
}

// MetadataCorrectionEffects returns empty financial effects with an audit link
// only (matrix row "Metadata-only correction"): no account effect, no budget
// effect, and no reservation change. The audit record itself is the command
// layer's concern; the empty posting shape is asserted here so the row cannot
// silently gain financial postings. The link is RelatedRefOriginal because the
// correction points at the operation it modifies.
func MetadataCorrectionEffects(originalOp JournalOperationId) (Effects, error) {
	if originalOp.IsZero() {
		return Effects{}, NewError(CodeInvalidValue, "metadata correction original operation id must not be empty")
	}
	link, err := NewRelatedOperationRef(RelatedRefOriginal, originalOp)
	if err != nil {
		return Effects{}, err
	}
	return Effects{Related: []RelatedOperationRef{link}}, nil
}
