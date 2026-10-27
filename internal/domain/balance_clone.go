// This file adds a deep-copy operation to LedgerState without editing the
// original projection file. It exists so the command layer can validate a
// candidate effect set against a throwaway working copy before committing it
// (POSTING_MATRIX §5 atomicity).
package domain

// Clone returns a deep copy of the ledger projection: every account, envelope,
// and registered plan is copied, including posted and reserved balances.
//
// The clone is independent of the receiver; mutating it (ApplyPostings,
// ApplyReservationChanges) never changes the original. Clone returns
// INVALID_VALUE for a nil or otherwise unbuilt ledger so a caller cannot
// accidentally validate against an invalid projection.
func (s *LedgerState) Clone() (*LedgerState, error) {
	if s == nil {
		return nil, NewError(CodeInvalidValue, "cannot clone a nil ledger state")
	}
	if err := s.currency.validate(); err != nil {
		return nil, WrapError(CodeInvalidValue, "cannot clone an unbuilt ledger state", err)
	}

	cloned := &LedgerState{
		currency:  s.currency,
		accounts:  make(map[AccountId]*accountState, len(s.accounts)),
		envelopes: make(map[EnvelopeId]*envelopeState, len(s.envelopes)),
		plans:     make(map[PlanId]struct{}, len(s.plans)),
	}
	for id, account := range s.accounts {
		copied := *account
		cloned.accounts[id] = &copied
	}
	for id, envelope := range s.envelopes {
		copied := *envelope
		cloned.envelopes[id] = &copied
	}
	for id := range s.plans {
		cloned.plans[id] = struct{}{}
	}
	return cloned, nil
}
