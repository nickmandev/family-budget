package domain

// RelatedRefKind classifies a typed link between journal operations
// (DOMAIN_MODEL §4 RelatedOperationRef).
type RelatedRefKind string

const (
	// RelatedRefOriginal marks a refund, reversal, or correction that points at
	// the operation it modifies.
	RelatedRefOriginal RelatedRefKind = "ORIGINAL"
	// RelatedRefReversal marks the compensating operation that reverses an
	// original operation.
	RelatedRefReversal RelatedRefKind = "REVERSAL"
	// RelatedRefRefund marks a genuine refund linked to its original purchase.
	RelatedRefRefund RelatedRefKind = "REFUND"
	// RelatedRefReplacement marks the corrected operation that replaces a
	// reversed one.
	RelatedRefReplacement RelatedRefKind = "REPLACEMENT"
	// RelatedRefPlan marks the planned operation a committed journal operation
	// originated from.
	RelatedRefPlan RelatedRefKind = "PLAN"
)

// ParseRelatedRefKind validates a raw related-reference kind.
func ParseRelatedRefKind(raw string) (RelatedRefKind, error) {
	kind := RelatedRefKind(raw)
	if !kind.IsValid() {
		return "", NewErrorf(CodeInvalidValue, "unknown related reference kind %q", raw)
	}
	return kind, nil
}

// IsValid reports whether the kind is one of the defined values.
func (k RelatedRefKind) IsValid() bool {
	switch k {
	case RelatedRefOriginal,
		RelatedRefReversal,
		RelatedRefRefund,
		RelatedRefReplacement,
		RelatedRefPlan:
		return true
	default:
		return false
	}
}

// String returns the stored kind text.
func (k RelatedRefKind) String() string {
	return string(k)
}

// RelatedOperationRef is a typed link from one journal operation to another.
type RelatedOperationRef struct {
	Kind        RelatedRefKind
	OperationID JournalOperationId
}

// NewRelatedOperationRef validates the kind and operation id, then builds a
// RelatedOperationRef.
func NewRelatedOperationRef(kind RelatedRefKind, opID JournalOperationId) (RelatedOperationRef, error) {
	if !kind.IsValid() {
		return RelatedOperationRef{}, NewErrorf(CodeInvalidValue, "invalid related reference kind %q", kind)
	}
	if opID.IsZero() {
		return RelatedOperationRef{}, NewError(CodeInvalidValue, "related reference operation id must not be empty")
	}
	return RelatedOperationRef{Kind: kind, OperationID: opID}, nil
}
