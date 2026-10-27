// This file derives the visible budget-resolution state from current balances
// (IMPLEMENTATION_DECISIONS Q4). Deficits are user-resolvable funding gaps the
// ledger has genuinely recorded — negative Ready-to-assign, negative envelope
// balances, and negative card-payment envelopes. An accounting identity
// mismatch is a separate internal integrity error (VerifyBudgetIdentity /
// CheckInvariants) and is never reported here.
//
// Acknowledgment is deliberately not modeled: the historical RESOLVED label
// comes from a separate command/audit action with no ledger effect.
package domain

import "slices"

// DeficitKind classifies a derived budget deficit (IMPLEMENTATION_DECISIONS Q4).
type DeficitKind string

const (
	// DeficitReadyToAssign is a negative derived Ready-to-assign.
	DeficitReadyToAssign DeficitKind = "READY_TO_ASSIGN"
	// DeficitEnvelope is a negative envelope balance, including a card-payment
	// envelope.
	DeficitEnvelope DeficitKind = "ENVELOPE"
)

// BudgetDeficit is a visible, derived shortfall — a real funding gap recorded
// by the ledger, never an internal inconsistency.
type BudgetDeficit struct {
	Kind       DeficitKind
	EnvelopeID EnvelopeId // zero value when Kind is READY_TO_ASSIGN
	Amount     Money      // positive magnitude of the deficit
}

// Deficits derives the currently visible budget deficits: negative
// Ready-to-assign and/or negative envelope balances (including card-payment
// envelopes). Results are sorted deterministically (ready-to-assign first,
// then envelopes by id). A negative balance is a legitimate recorded outcome
// (Q4) and must be surfaced, not rejected. An accounting identity mismatch is
// an internal integrity error (VerifyBudgetIdentity / CheckInvariants) and is
// NEVER reported here as a deficit.
func (s *LedgerState) Deficits() ([]BudgetDeficit, error) {
	ready, err := s.ReadyToAssign()
	if err != nil {
		return nil, err
	}

	var deficits []BudgetDeficit
	if ready.IsNegative() {
		amount, err := ready.Neg()
		if err != nil {
			return nil, err
		}
		deficits = append(deficits, BudgetDeficit{Kind: DeficitReadyToAssign, Amount: amount})
	}

	ids := make([]EnvelopeId, 0, len(s.envelopes))
	for id := range s.envelopes {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	for _, id := range ids {
		balance := s.envelopes[id].balance
		if !balance.IsNegative() {
			continue
		}
		amount, err := balance.Neg()
		if err != nil {
			return nil, err
		}
		deficits = append(deficits, BudgetDeficit{Kind: DeficitEnvelope, EnvelopeID: id, Amount: amount})
	}
	return deficits, nil
}

// ResolutionStatus is the derived budget-resolution state (Q4).
type ResolutionStatus string

const (
	// ResolutionClear means no visible deficit exists.
	ResolutionClear ResolutionStatus = "CLEAR"
	// ResolutionRequiresResolution means at least one visible deficit exists.
	ResolutionRequiresResolution ResolutionStatus = "REQUIRES_RESOLUTION"
)

// ResolutionStatus derives the current state from Deficits: requires-resolution
// while any deficit exists, clear otherwise. The third historical label
// RESOLVED comes from a user acknowledgment recorded as an audit action with no
// ledger effect — acknowledgment is deliberately NOT modeled in the domain
// ledger (Q4: "keep acknowledgment separate") and lives in the command/audit
// layer.
func (s *LedgerState) ResolutionStatus() (ResolutionStatus, error) {
	deficits, err := s.Deficits()
	if err != nil {
		return "", err
	}
	if len(deficits) > 0 {
		return ResolutionRequiresResolution, nil
	}
	return ResolutionClear, nil
}
