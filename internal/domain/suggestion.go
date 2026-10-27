// This file implements the pure suggestion/draft lifecycle state machine
// (STATE_TRANSITIONS §4 and POSTING_MATRIX §4.3). A suggestion is an
// unconfirmed candidate transaction: a manual draft or a future
// notification-derived candidate.
//
// A suggestion has NO financial postings and NO reservations — by construction
// it exposes no effects and no posting set, so it cannot touch the ledger. The
// posting path runs only after confirmation, and it belongs to the command
// layer (an already-completed purchase goes through the shared posting
// implementation; a future planned purchase enters the planned-purchase
// authorization flow instead). Confirmation here records only that the user
// confirmed the candidate details.
//
// Invalid transitions are rejected with INVALID_ENTITY_STATE and leave the
// suggestion unchanged (STATE_TRANSITIONS §5). Every mutator validates
// completely before it touches a field.
package domain

// SuggestionState is the unconfirmed-transaction lifecycle
// (STATE_TRANSITIONS §4).
type SuggestionState string

const (
	// SuggestionStateSuggested is a newly ingested or manually created
	// candidate. It has no ledger effect at all.
	SuggestionStateSuggested SuggestionState = "SUGGESTED"
	// SuggestionStateEdited is a suggestion whose candidate fields the user
	// has corrected at least once.
	SuggestionStateEdited SuggestionState = "EDITED"
	// SuggestionStateConfirmed records that the user confirmed the candidate
	// details. Confirmation itself posts nothing; the command layer invokes
	// the appropriate shared path.
	SuggestionStateConfirmed SuggestionState = "CONFIRMED"
	// SuggestionStateCancelled is a dismissed suggestion. It is suppressed
	// from the active inbox, but its source fingerprint is preserved so
	// ingestion does not recreate it on every sync (Q10).
	SuggestionStateCancelled SuggestionState = "CANCELLED"
)

// ParseSuggestionState validates a raw suggestion state.
func ParseSuggestionState(raw string) (SuggestionState, error) {
	state := SuggestionState(raw)
	if !state.IsValid() {
		return "", NewErrorf(CodeInvalidValue, "unknown suggestion state %q", raw)
	}
	return state, nil
}

// IsValid reports whether the state is one of the defined values.
func (s SuggestionState) IsValid() bool {
	switch s {
	case SuggestionStateSuggested,
		SuggestionStateEdited,
		SuggestionStateConfirmed,
		SuggestionStateCancelled:
		return true
	default:
		return false
	}
}

// String returns the stored state text.
func (s SuggestionState) String() string {
	return string(s)
}

// suggestionTransitions is the complete allowed-transition table from
// STATE_TRANSITIONS §4. CONFIRMED and CANCELLED are terminal: they have no
// outbound transitions. An EDITED -> EDITED self-edge represents a further
// edit.
var suggestionTransitions = map[SuggestionState][]SuggestionState{
	SuggestionStateSuggested: {
		SuggestionStateEdited,
		SuggestionStateConfirmed,
		SuggestionStateCancelled,
	},
	SuggestionStateEdited: {
		SuggestionStateEdited,
		SuggestionStateConfirmed,
		SuggestionStateCancelled,
	},
	SuggestionStateConfirmed: {},
	SuggestionStateCancelled: {},
}

// AllowedSuggestionTransitions returns the full allowed transition set for
// documentation and conformance tests. The returned map and its slices are
// copies, so callers cannot mutate the package-level table.
func AllowedSuggestionTransitions() map[SuggestionState][]SuggestionState {
	allowed := make(map[SuggestionState][]SuggestionState, len(suggestionTransitions))
	for from, targets := range suggestionTransitions {
		copied := make([]SuggestionState, len(targets))
		copy(copied, targets)
		allowed[from] = copied
	}
	return allowed
}

// suggestionTransitionAllowed reports whether from -> to is in the transition
// table.
func suggestionTransitionAllowed(from, to SuggestionState) bool {
	if !from.IsValid() || !to.IsValid() {
		return false
	}
	for _, candidate := range suggestionTransitions[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// Suggestion is an unconfirmed candidate transaction — a manual draft or a
// future notification-derived candidate. It NEVER has ledger postings or
// reservations (POSTING_MATRIX "Draft / transaction suggestion" row). The zero
// value is invalid: construct with NewSuggestion.
//
// This type intentionally has no method returning Effects or PostingSet. Any
// financial effect comes from the command layer's confirmed-transaction path,
// never from the suggestion itself.
type Suggestion struct {
	id     SuggestionId
	source TransactionSource
	amount PositiveAmount
	// merchant is the candidate merchant text. It is preserved after
	// cancellation as part of the source fingerprint (Q10).
	merchant string

	state        SuggestionState
	cancelReason string

	// confirmationCount is zero until confirmed. It guards against a second
	// confirmation accidentally being treated as a fresh confirmation.
	confirmationCount int
}

// NewSuggestion creates a SUGGESTED suggestion from candidate fields. The
// amount must be a genuinely positive amount; the source must be a known
// transaction source. No ledger effect is created.
func NewSuggestion(id SuggestionId, source TransactionSource, amount PositiveAmount, merchant string) (*Suggestion, error) {
	if id.IsZero() {
		return nil, NewError(CodeInvalidValue, "suggestion id must not be empty")
	}
	if !source.IsValid() {
		return nil, NewErrorf(CodeInvalidValue, "unknown transaction source %q", source)
	}
	if amount.MinorUnits() <= 0 {
		return nil, NewError(CodeInvalidValue, "suggestion amount must be greater than zero")
	}
	if err := amount.Currency().validate(); err != nil {
		return nil, err
	}

	return &Suggestion{
		id:       id,
		source:   source,
		amount:   amount,
		merchant: merchant,
		state:    SuggestionStateSuggested,
	}, nil
}

// ID returns the suggestion identifier.
func (s *Suggestion) ID() SuggestionId {
	if s == nil {
		return ""
	}
	return s.id
}

// Source returns the candidate's transaction source. It is preserved after
// cancellation for ingestion fingerprinting.
func (s *Suggestion) Source() TransactionSource {
	if s == nil {
		return ""
	}
	return s.source
}

// Amount returns the candidate amount. It is preserved after cancellation for
// ingestion fingerprinting.
func (s *Suggestion) Amount() PositiveAmount {
	if s == nil {
		return PositiveAmount{}
	}
	return s.amount
}

// Merchant returns the candidate merchant text. It is preserved after
// cancellation for ingestion fingerprinting.
func (s *Suggestion) Merchant() string {
	if s == nil {
		return ""
	}
	return s.merchant
}

// State returns the current lifecycle state.
func (s *Suggestion) State() SuggestionState {
	if s == nil {
		return ""
	}
	return s.state
}

// CancelReason returns the dismissal reason, or the empty string when the
// suggestion was not cancelled with a recorded reason.
func (s *Suggestion) CancelReason() string {
	if s == nil {
		return ""
	}
	return s.cancelReason
}

// ConfirmationCount returns how many times the suggestion has been confirmed.
// It is zero until confirmation and guards double confirmation.
func (s *Suggestion) ConfirmationCount() int {
	if s == nil {
		return 0
	}
	return s.confirmationCount
}

// Edit updates candidate fields (SUGGESTED -> EDITED, EDITED -> EDITED).
// Editing a CONFIRMED or CANCELLED suggestion is INVALID_ENTITY_STATE and
// leaves the suggestion unchanged.
func (s *Suggestion) Edit(amount PositiveAmount, merchant string) error {
	if err := s.validateInitialized(); err != nil {
		return err
	}
	switch s.state {
	case SuggestionStateSuggested, SuggestionStateEdited:
	default:
		return NewErrorf(CodeInvalidEntityState, "cannot edit a suggestion in state %s", s.state)
	}
	if !suggestionTransitionAllowed(s.state, SuggestionStateEdited) {
		return NewErrorf(CodeInvalidEntityState, "cannot edit a suggestion from %s", s.state)
	}
	if amount.MinorUnits() <= 0 {
		return NewError(CodeInvalidAmount, "suggestion amount must be greater than zero")
	}
	if err := amount.Currency().validate(); err != nil {
		return err
	}

	s.amount = amount
	s.merchant = merchant
	s.state = SuggestionStateEdited
	return nil
}

// Confirm moves SUGGESTED|EDITED -> CONFIRMED. Confirmation of a factual
// already-completed transaction goes through the shared posting path at the
// command layer; confirmation of a future planned purchase routes to the plan
// authorization flow instead — this aggregate only records that the user
// confirmed the details (STATE_TRANSITIONS §4).
//
// Confirming an already CONFIRMED suggestion is a successful idempotent no-op
// returning nil: no candidate field can change between confirmations, so the
// fields always match. Confirming a CANCELLED suggestion is
// INVALID_ENTITY_STATE.
func (s *Suggestion) Confirm() error {
	if err := s.validateInitialized(); err != nil {
		return err
	}
	switch s.state {
	case SuggestionStateConfirmed:
		return nil
	case SuggestionStateSuggested, SuggestionStateEdited:
		if !suggestionTransitionAllowed(s.state, SuggestionStateConfirmed) {
			return NewErrorf(CodeInvalidEntityState, "cannot confirm a suggestion from %s", s.state)
		}
		s.state = SuggestionStateConfirmed
		s.confirmationCount++
		return nil
	default:
		return NewErrorf(CodeInvalidEntityState, "cannot confirm a suggestion in state %s", s.state)
	}
}

// Cancel moves SUGGESTED|EDITED -> CANCELLED with an optional reason. It
// suppresses the suggestion from the active inbox; a source fingerprint is
// preserved so ingestion does not recreate it on every sync (Q10: identical
// merchant/amount pairs are never assumed to be the same purchase). Cancelling
// a CONFIRMED or CANCELLED suggestion is INVALID_ENTITY_STATE.
func (s *Suggestion) Cancel(reason string) error {
	if err := s.validateInitialized(); err != nil {
		return err
	}
	switch s.state {
	case SuggestionStateSuggested, SuggestionStateEdited:
	default:
		return NewErrorf(CodeInvalidEntityState, "cannot cancel a suggestion in state %s", s.state)
	}
	if !suggestionTransitionAllowed(s.state, SuggestionStateCancelled) {
		return NewErrorf(CodeInvalidEntityState, "cannot cancel a suggestion from %s", s.state)
	}

	s.state = SuggestionStateCancelled
	s.cancelReason = reason
	return nil
}

// CanTransition reports whether a transition from the current state to to is
// allowed. It is a pure query and never mutates the suggestion.
func (s *Suggestion) CanTransition(to SuggestionState) bool {
	if s == nil || s.id.IsZero() {
		return false
	}
	return suggestionTransitionAllowed(s.state, to)
}

// validateInitialized rejects nil and zero-value aggregates.
func (s *Suggestion) validateInitialized() error {
	if s == nil || s.id.IsZero() {
		return NewError(CodeInvalidValue, "suggestion must be initialized")
	}
	return nil
}
