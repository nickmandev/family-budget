package domain

// TransactionSource records the provenance of a journal operation. The five
// values below are the complete set documented in DOMAIN_MODEL.md §4. Future
// source kinds must not change posting semantics by themselves.
//
// A PLANNED_REPAYMENT extension is intentionally deferred: see the "Still
// open" section of IMPLEMENTATION_DECISIONS.md. Do not add it here until that
// mapping is confirmed.
type TransactionSource string

const (
	// TransactionSourceManual is a user-entered transaction.
	TransactionSourceManual TransactionSource = "MANUAL"
	// TransactionSourcePlannedPurchase is a completed planned purchase.
	TransactionSourcePlannedPurchase TransactionSource = "PLANNED_PURCHASE"
	// TransactionSourceExternalEntry is an externally sourced or imported
	// transaction that has been confirmed.
	TransactionSourceExternalEntry TransactionSource = "EXTERNAL_ENTRY"
	// TransactionSourceOpeningBalance establishes an opening account balance or
	// card liability during onboarding.
	TransactionSourceOpeningBalance TransactionSource = "OPENING_BALANCE"
	// TransactionSourceReconciliation is a reconciliation adjustment.
	TransactionSourceReconciliation TransactionSource = "RECONCILIATION"
)

// ParseTransactionSource validates a raw transaction source.
func ParseTransactionSource(value string) (TransactionSource, error) {
	source := TransactionSource(value)
	if !source.IsValid() {
		return "", NewErrorf(CodeInvalidValue, "unknown transaction source %q", value)
	}
	return source, nil
}

// IsValid reports whether the source is one of the five defined values.
func (s TransactionSource) IsValid() bool {
	switch s {
	case TransactionSourceManual,
		TransactionSourcePlannedPurchase,
		TransactionSourceExternalEntry,
		TransactionSourceOpeningBalance,
		TransactionSourceReconciliation:
		return true
	default:
		return false
	}
}

// String returns the stored source text.
func (s TransactionSource) String() string {
	return string(s)
}
