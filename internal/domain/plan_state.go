// This file defines the planned-operation lifecycle vocabulary: the two plan
// kinds that share one lifecycle (IMPLEMENTATION_DECISIONS Q2) and the state
// machine from STATE_TRANSITIONS §2. Keeping the transition table in one place
// lets CanTransition and every mutator agree on the same contract.
package domain

// PlanKind distinguishes the two planned-operation semantics that share one
// lifecycle (IMPLEMENTATION_DECISIONS Q2): a planned purchase and a planned
// card repayment are NOT the same thing — the repayment settles a liability
// and must never look like a second expense.
//
// A purchase plan may target a card purchase (zero AccountID, Q1) or a
// debit/cash purchase (non-zero AccountID). A repayment plan always holds both
// the payment envelope and the source cash account (Q2).
type PlanKind string

const (
	// PlanKindPurchase is a planned purchase, either card (envelope hold only)
	// or debit/cash (envelope plus account holds).
	PlanKindPurchase PlanKind = "PURCHASE"
	// PlanKindRepayment is a planned card repayment. It is semantically
	// distinct from a purchase even though it reuses the lifecycle (Q2).
	PlanKindRepayment PlanKind = "REPAYMENT"
)

// ParsePlanKind validates a raw plan kind.
func ParsePlanKind(raw string) (PlanKind, error) {
	kind := PlanKind(raw)
	if !kind.IsValid() {
		return "", NewErrorf(CodeInvalidValue, "unknown plan kind %q", raw)
	}
	return kind, nil
}

// IsValid reports whether the kind is one of the defined values.
func (k PlanKind) IsValid() bool {
	switch k {
	case PlanKindPurchase, PlanKindRepayment:
		return true
	default:
		return false
	}
}

// String returns the stored kind text.
func (k PlanKind) String() string {
	return string(k)
}

// PlanState is the planned-operation lifecycle state (STATE_TRANSITIONS §2).
type PlanState string

const (
	// PlanStateDraft is freely editable and holds no funds.
	PlanStateDraft PlanState = "DRAFT"
	// PlanStateReserved passed normal authorization and holds its funds.
	PlanStateReserved PlanState = "RESERVED"
	// PlanStateReservedException passed a reasoned emergency override; the
	// envelope shortfall is visible and attributable.
	PlanStateReservedException PlanState = "RESERVED_EXCEPTION"
	// PlanStateCompleted posted the purchase exactly once and released its
	// holds.
	PlanStateCompleted PlanState = "COMPLETED"
	// PlanStateCancelled posted nothing and released all holds.
	PlanStateCancelled PlanState = "CANCELLED"
)

// ParsePlanState validates a raw plan state.
func ParsePlanState(raw string) (PlanState, error) {
	state := PlanState(raw)
	if !state.IsValid() {
		return "", NewErrorf(CodeInvalidValue, "unknown plan state %q", raw)
	}
	return state, nil
}

// IsValid reports whether the state is one of the defined values.
func (s PlanState) IsValid() bool {
	switch s {
	case PlanStateDraft,
		PlanStateReserved,
		PlanStateReservedException,
		PlanStateCompleted,
		PlanStateCancelled:
		return true
	default:
		return false
	}
}

// String returns the stored state text.
func (s PlanState) String() string {
	return string(s)
}

// planTransitions is the complete allowed-transition table from
// STATE_TRANSITIONS §2. COMPLETED and CANCELLED are terminal: they have no
// outbound transitions. A DRAFT -> DRAFT self-edge represents editing a draft.
var planTransitions = map[PlanState][]PlanState{
	PlanStateDraft: {
		PlanStateDraft,
		PlanStateReserved,
		PlanStateReservedException,
		PlanStateCancelled,
	},
	PlanStateReserved: {
		PlanStateReserved,
		PlanStateDraft,
		PlanStateCompleted,
		PlanStateCancelled,
	},
	PlanStateReservedException: {
		PlanStateReserved,
		PlanStateReservedException,
		PlanStateDraft,
		PlanStateCompleted,
		PlanStateCancelled,
	},
	PlanStateCompleted: {},
	PlanStateCancelled: {},
}

// AllowedPlanTransitions returns the full allowed transition set for
// documentation and conformance tests. The returned map and its slices are
// copies, so callers cannot mutate the package-level table.
func AllowedPlanTransitions() map[PlanState][]PlanState {
	allowed := make(map[PlanState][]PlanState, len(planTransitions))
	for from, targets := range planTransitions {
		copied := make([]PlanState, len(targets))
		copy(copied, targets)
		allowed[from] = copied
	}
	return allowed
}

// planTransitionAllowed reports whether from -> to is in the transition table.
func planTransitionAllowed(from, to PlanState) bool {
	if !from.IsValid() || !to.IsValid() {
		return false
	}
	for _, candidate := range planTransitions[from] {
		if candidate == to {
			return true
		}
	}
	return false
}
