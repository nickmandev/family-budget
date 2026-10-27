package domain

import (
	"reflect"
	"testing"
)

// newPlanInState builds a plan directly in the requested lifecycle state for
// transition tests. All fixtures use a debit-purchase target so both envelope
// and account holds are exercised.
func newPlanInState(t *testing.T, state PlanState) *PlannedOperation {
	t.Helper()
	f := newFundingFixture(t)
	id := mustPlanIDForTest(t, "plan-"+string(state))
	target := ReservationTarget{PlanID: id, EnvelopeID: f.spend, AccountID: f.checking}
	amount := mustPositiveAmountForTest(t, 1_000, f.currency)
	plan, err := NewPlannedOperation(id, PlanKindPurchase, target, amount)
	if err != nil {
		t.Fatalf("NewPlannedOperation: %v", err)
	}

	reason := mustOverrideReasonForTest(t, "state fixture override")
	switch state {
	case PlanStateDraft:
		// Already in the requested state.
	case PlanStateReserved:
		if err := plan.Authorize(AuthorizationOutcome{Mode: AuthorizationModeNormal}, amount, amount); err != nil {
			t.Fatalf("Authorize(normal): %v", err)
		}
	case PlanStateReservedException:
		outcome := AuthorizationOutcome{Mode: AuthorizationModeEmergencyOverride, Reason: reason}
		if err := plan.Authorize(outcome, amount, amount); err != nil {
			t.Fatalf("Authorize(override): %v", err)
		}
	case PlanStateCompleted:
		if err := plan.Authorize(AuthorizationOutcome{Mode: AuthorizationModeNormal}, amount, amount); err != nil {
			t.Fatalf("Authorize(normal): %v", err)
		}
		if err := plan.Complete(amount, amount, amount); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	case PlanStateCancelled:
		if err := plan.Cancel(); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	default:
		t.Fatalf("newPlanInState: unsupported state %q", state)
	}
	return plan
}

// assertPlanState checks the lifecycle state and the two amount accessors.
func assertPlanState(t *testing.T, plan *PlannedOperation, want PlanState, wantIntended, wantAuthorized int64) {
	t.Helper()
	if got := plan.State(); got != want {
		t.Errorf("State = %s, want %s", got, want)
	}
	if got := plan.IntendedAmount().MinorUnits(); got != wantIntended {
		t.Errorf("IntendedAmount = %d, want %d", got, wantIntended)
	}
	if got := plan.AuthorizedAmount().MinorUnits(); got != wantAuthorized {
		t.Errorf("AuthorizedAmount = %d, want %d", got, wantAuthorized)
	}
}

// assertPlanUnchanged compares a snapshot with the plan's current value.
func assertPlanUnchanged(t *testing.T, before PlannedOperation, plan *PlannedOperation) {
	t.Helper()
	if !reflect.DeepEqual(before, *plan) {
		t.Errorf("plan changed after a rejected transition: before %+v, after %+v", before, *plan)
	}
}

func TestParsePlanKindAndState(t *testing.T) {
	kinds := []struct {
		raw   string
		valid bool
	}{
		{"PURCHASE", true},
		{"REPAYMENT", true},
		{"purchase", false},
		{"", false},
		{"UNKNOWN", false},
	}
	for _, tt := range kinds {
		kind, err := ParsePlanKind(tt.raw)
		if tt.valid {
			if err != nil {
				t.Errorf("ParsePlanKind(%q): %v", tt.raw, err)
				continue
			}
			if kind.String() != tt.raw {
				t.Errorf("ParsePlanKind(%q).String() = %q", tt.raw, kind.String())
			}
			if !kind.IsValid() {
				t.Errorf("ParsePlanKind(%q).IsValid() = false", tt.raw)
			}
			continue
		}
		if !HasCode(err, CodeInvalidValue) {
			t.Errorf("ParsePlanKind(%q) error = %v, want INVALID_VALUE", tt.raw, err)
		}
	}

	states := []struct {
		raw   string
		valid bool
	}{
		{"DRAFT", true},
		{"RESERVED", true},
		{"RESERVED_EXCEPTION", true},
		{"COMPLETED", true},
		{"CANCELLED", true},
		{"draft", false},
		{"", false},
		{"UNKNOWN", false},
	}
	for _, tt := range states {
		state, err := ParsePlanState(tt.raw)
		if tt.valid {
			if err != nil {
				t.Errorf("ParsePlanState(%q): %v", tt.raw, err)
				continue
			}
			if state.String() != tt.raw {
				t.Errorf("ParsePlanState(%q).String() = %q", tt.raw, state.String())
			}
			if !state.IsValid() {
				t.Errorf("ParsePlanState(%q).IsValid() = false", tt.raw)
			}
			continue
		}
		if !HasCode(err, CodeInvalidValue) {
			t.Errorf("ParsePlanState(%q) error = %v, want INVALID_VALUE", tt.raw, err)
		}
	}
}

func TestNewPlannedOperation(t *testing.T) {
	f := newFundingFixture(t)
	amount := mustPositiveAmountForTest(t, 1_000, f.currency)

	tests := []struct {
		name           string
		id             PlanId
		kind           PlanKind
		target         ReservationTarget
		amount         PositiveAmount
		wantCode       ErrorCode
		wantTargetPlan PlanId
	}{
		{
			name:           "valid card purchase",
			id:             mustPlanIDForTest(t, "plan-card"),
			kind:           PlanKindPurchase,
			target:         ReservationTarget{PlanID: mustPlanIDForTest(t, "plan-card"), EnvelopeID: f.spend},
			amount:         amount,
			wantTargetPlan: mustPlanIDForTest(t, "plan-card"),
		},
		{
			name:           "valid debit purchase",
			id:             mustPlanIDForTest(t, "plan-debit"),
			kind:           PlanKindPurchase,
			target:         ReservationTarget{PlanID: mustPlanIDForTest(t, "plan-debit"), EnvelopeID: f.spend, AccountID: f.checking},
			amount:         amount,
			wantTargetPlan: mustPlanIDForTest(t, "plan-debit"),
		},
		{
			name:           "valid repayment",
			id:             mustPlanIDForTest(t, "plan-repay"),
			kind:           PlanKindRepayment,
			target:         ReservationTarget{PlanID: mustPlanIDForTest(t, "plan-repay"), EnvelopeID: f.pay, AccountID: f.checking},
			amount:         amount,
			wantTargetPlan: mustPlanIDForTest(t, "plan-repay"),
		},
		{
			name:           "zero target plan id is filled from the aggregate",
			id:             mustPlanIDForTest(t, "plan-filled"),
			kind:           PlanKindPurchase,
			target:         ReservationTarget{EnvelopeID: f.spend},
			amount:         amount,
			wantTargetPlan: mustPlanIDForTest(t, "plan-filled"),
		},
		{
			name:     "repayment requires a source account",
			id:       mustPlanIDForTest(t, "plan-repay-no-account"),
			kind:     PlanKindRepayment,
			target:   ReservationTarget{PlanID: mustPlanIDForTest(t, "plan-repay-no-account"), EnvelopeID: f.pay},
			amount:   amount,
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero plan id",
			id:       "",
			kind:     PlanKindPurchase,
			target:   ReservationTarget{EnvelopeID: f.spend},
			amount:   amount,
			wantCode: CodeInvalidValue,
		},
		{
			name:     "unknown kind",
			id:       mustPlanIDForTest(t, "plan-bad-kind"),
			kind:     PlanKind("BOGUS"),
			target:   ReservationTarget{EnvelopeID: f.spend},
			amount:   amount,
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero amount",
			id:       mustPlanIDForTest(t, "plan-zero-amount"),
			kind:     PlanKindPurchase,
			target:   ReservationTarget{EnvelopeID: f.spend},
			amount:   PositiveAmount{},
			wantCode: CodeInvalidAmount,
		},
		{
			name:     "zero envelope id",
			id:       mustPlanIDForTest(t, "plan-no-envelope"),
			kind:     PlanKindPurchase,
			target:   ReservationTarget{},
			amount:   amount,
			wantCode: CodeInvalidValue,
		},
		{
			name:     "mismatched target plan id",
			id:       mustPlanIDForTest(t, "plan-one"),
			kind:     PlanKindPurchase,
			target:   ReservationTarget{PlanID: mustPlanIDForTest(t, "plan-two"), EnvelopeID: f.spend},
			amount:   amount,
			wantCode: CodeInvalidValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := NewPlannedOperation(tt.id, tt.kind, tt.target, tt.amount)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewPlannedOperation error = %v, want code %s", err, tt.wantCode)
				}
				if plan != nil {
					t.Fatalf("NewPlannedOperation returned a plan alongside error %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewPlannedOperation: %v", err)
			}
			if plan.ID() != tt.id {
				t.Errorf("ID = %q, want %q", plan.ID(), tt.id)
			}
			if plan.Kind() != tt.kind {
				t.Errorf("Kind = %q, want %q", plan.Kind(), tt.kind)
			}
			if plan.Target().PlanID != tt.wantTargetPlan {
				t.Errorf("Target.PlanID = %q, want %q", plan.Target().PlanID, tt.wantTargetPlan)
			}
			assertPlanState(t, plan, PlanStateDraft, amount.MinorUnits(), amount.MinorUnits())
			if plan.Mode() != "" {
				t.Errorf("Mode = %q, want zero", plan.Mode())
			}
			if !plan.Reason().IsZero() {
				t.Errorf("Reason = %q, want zero", plan.Reason())
			}
			if plan.HeldAmount().MinorUnits() != 0 || plan.HeldOnAccount().MinorUnits() != 0 {
				t.Errorf("draft holds = (%d, %d), want (0, 0)", plan.HeldAmount().MinorUnits(), plan.HeldOnAccount().MinorUnits())
			}
		})
	}
}

func containsPlanState(states []PlanState, target PlanState) bool {
	for _, state := range states {
		if state == target {
			return true
		}
	}
	return false
}

func TestPlannedOperationAllowedTransitions(t *testing.T) {
	// Every allowed edge is listed explicitly. If AllowedPlanTransitions grows,
	// the coverage check below fails until this list is extended.
	pairs := []struct {
		from PlanState
		to   PlanState
	}{
		{PlanStateDraft, PlanStateDraft},
		{PlanStateDraft, PlanStateReserved},
		{PlanStateDraft, PlanStateReservedException},
		{PlanStateDraft, PlanStateCancelled},
		{PlanStateReserved, PlanStateReserved},
		{PlanStateReserved, PlanStateDraft},
		{PlanStateReserved, PlanStateCompleted},
		{PlanStateReserved, PlanStateCancelled},
		{PlanStateReservedException, PlanStateReserved},
		{PlanStateReservedException, PlanStateReservedException},
		{PlanStateReservedException, PlanStateDraft},
		{PlanStateReservedException, PlanStateCompleted},
		{PlanStateReservedException, PlanStateCancelled},
	}

	covered := make(map[[2]PlanState]bool, len(pairs))
	for _, pair := range pairs {
		pair := pair
		t.Run(string(pair.from)+"->"+string(pair.to), func(t *testing.T) {
			plan := newPlanInState(t, pair.from)
			if !plan.CanTransition(pair.to) {
				t.Errorf("CanTransition(%s) = false, want true from %s", pair.to, pair.from)
			}
			covered[[2]PlanState{pair.from, pair.to}] = true
		})
	}

	allowed := AllowedPlanTransitions()
	for from, targets := range allowed {
		for _, to := range targets {
			if !covered[[2]PlanState{from, to}] {
				t.Errorf("allowed transition %s -> %s has no explicit coverage", from, to)
			}
		}
	}
	for _, pair := range pairs {
		if !containsPlanState(allowed[pair.from], pair.to) {
			t.Errorf("explicit transition %s -> %s is not in AllowedPlanTransitions", pair.from, pair.to)
		}
	}

	states := []PlanState{
		PlanStateDraft,
		PlanStateReserved,
		PlanStateReservedException,
		PlanStateCompleted,
		PlanStateCancelled,
	}
	for _, from := range states {
		plan := newPlanInState(t, from)
		for _, to := range states {
			want := containsPlanState(allowed[from], to)
			if got := plan.CanTransition(to); got != want {
				t.Errorf("CanTransition(%s -> %s) = %v, want %v", from, to, got, want)
			}
		}
	}

	var zero PlannedOperation
	if zero.CanTransition(PlanStateDraft) {
		t.Error("zero-value plan CanTransition(DRAFT) = true, want false")
	}
	if plan := newPlanInState(t, PlanStateDraft); plan.CanTransition(PlanState("BOGUS")) {
		t.Error("CanTransition(BOGUS) = true, want false")
	}
}

func TestPlannedOperationMutatorTransitions(t *testing.T) {
	reason := mustOverrideReasonForTest(t, "lifecycle override")
	normal := AuthorizationOutcome{Mode: AuthorizationModeNormal}
	override := AuthorizationOutcome{Mode: AuthorizationModeEmergencyOverride, Reason: reason}

	tests := []struct {
		name  string
		from  PlanState
		apply func(t *testing.T, plan *PlannedOperation)
		check func(t *testing.T, plan *PlannedOperation)
	}{
		{
			name: "DRAFT -> RESERVED",
			from: PlanStateDraft,
			apply: func(t *testing.T, plan *PlannedOperation) {
				amount := plan.IntendedAmount()
				if err := plan.Authorize(normal, amount, amount); err != nil {
					t.Fatalf("Authorize: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateReserved, 1_000, 1_000)
				if plan.Mode() != AuthorizationModeNormal {
					t.Errorf("Mode = %q, want NORMAL", plan.Mode())
				}
				if !plan.Reason().IsZero() {
					t.Errorf("Reason = %q, want zero", plan.Reason())
				}
				if plan.HeldAmount().MinorUnits() != 1_000 || plan.HeldOnAccount().MinorUnits() != 1_000 {
					t.Errorf("holds = (%d, %d), want (1000, 1000)", plan.HeldAmount().MinorUnits(), plan.HeldOnAccount().MinorUnits())
				}
			},
		},
		{
			name: "DRAFT -> RESERVED_EXCEPTION",
			from: PlanStateDraft,
			apply: func(t *testing.T, plan *PlannedOperation) {
				amount := plan.IntendedAmount()
				if err := plan.Authorize(override, amount, amount); err != nil {
					t.Fatalf("Authorize: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateReservedException, 1_000, 1_000)
				if plan.Mode() != AuthorizationModeEmergencyOverride {
					t.Errorf("Mode = %q, want EMERGENCY_OVERRIDE", plan.Mode())
				}
				if plan.Reason() != reason {
					t.Errorf("Reason = %q, want %q", plan.Reason(), reason)
				}
			},
		},
		{
			name: "DRAFT -> CANCELLED",
			from: PlanStateDraft,
			apply: func(t *testing.T, plan *PlannedOperation) {
				if err := plan.Cancel(); err != nil {
					t.Fatalf("Cancel: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateCancelled, 1_000, 1_000)
				if plan.HeldAmount().MinorUnits() != 0 || plan.HeldOnAccount().MinorUnits() != 0 {
					t.Errorf("cancelled draft holds = (%d, %d), want (0, 0)", plan.HeldAmount().MinorUnits(), plan.HeldOnAccount().MinorUnits())
				}
			},
		},
		{
			name: "RESERVED -> RESERVED",
			from: PlanStateReserved,
			apply: func(t *testing.T, plan *PlannedOperation) {
				larger := mustPositiveAmountForTest(t, 1_500, plan.IntendedAmount().Currency())
				if err := plan.Adjust(normal, PlanStateReserved, larger, larger, larger); err != nil {
					t.Fatalf("Adjust: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateReserved, 1_500, 1_500)
				if plan.HeldAmount().MinorUnits() != 1_500 || plan.HeldOnAccount().MinorUnits() != 1_500 {
					t.Errorf("holds = (%d, %d), want (1500, 1500)", plan.HeldAmount().MinorUnits(), plan.HeldOnAccount().MinorUnits())
				}
			},
		},
		{
			name: "RESERVED -> DRAFT",
			from: PlanStateReserved,
			apply: func(t *testing.T, plan *PlannedOperation) {
				smaller := mustPositiveAmountForTest(t, 800, plan.IntendedAmount().Currency())
				if err := plan.Adjust(AuthorizationOutcome{}, PlanStateDraft, smaller, PositiveAmount{}, PositiveAmount{}); err != nil {
					t.Fatalf("Adjust: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateDraft, 800, 800)
				if plan.HeldAmount().MinorUnits() != 0 || plan.HeldOnAccount().MinorUnits() != 0 {
					t.Errorf("draft holds = (%d, %d), want (0, 0)", plan.HeldAmount().MinorUnits(), plan.HeldOnAccount().MinorUnits())
				}
				if plan.Mode() != "" || !plan.Reason().IsZero() {
					t.Errorf("draft authorization = (%q, %q), want zero", plan.Mode(), plan.Reason())
				}
			},
		},
		{
			name: "RESERVED -> COMPLETED",
			from: PlanStateReserved,
			apply: func(t *testing.T, plan *PlannedOperation) {
				amount := plan.HeldAmount()
				if err := plan.Complete(amount, amount, amount); err != nil {
					t.Fatalf("Complete: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateCompleted, 1_000, 1_000)
				if plan.HeldAmount().MinorUnits() != 0 || plan.HeldOnAccount().MinorUnits() != 0 {
					t.Errorf("completed holds = (%d, %d), want (0, 0)", plan.HeldAmount().MinorUnits(), plan.HeldOnAccount().MinorUnits())
				}
				if plan.finalAmount.MinorUnits() != 1_000 {
					t.Errorf("finalAmount = %d, want 1000", plan.finalAmount.MinorUnits())
				}
			},
		},
		{
			name: "RESERVED -> CANCELLED",
			from: PlanStateReserved,
			apply: func(t *testing.T, plan *PlannedOperation) {
				if err := plan.Cancel(); err != nil {
					t.Fatalf("Cancel: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateCancelled, 1_000, 1_000)
				if plan.HeldAmount().MinorUnits() != 0 || plan.HeldOnAccount().MinorUnits() != 0 {
					t.Errorf("cancelled holds = (%d, %d), want (0, 0)", plan.HeldAmount().MinorUnits(), plan.HeldOnAccount().MinorUnits())
				}
				if plan.releasedAmount.MinorUnits() != 1_000 || plan.releasedOnAccount.MinorUnits() != 1_000 {
					t.Errorf("released = (%d, %d), want (1000, 1000)", plan.releasedAmount.MinorUnits(), plan.releasedOnAccount.MinorUnits())
				}
			},
		},
		{
			name: "RESERVED_EXCEPTION -> RESERVED",
			from: PlanStateReservedException,
			apply: func(t *testing.T, plan *PlannedOperation) {
				larger := mustPositiveAmountForTest(t, 1_500, plan.IntendedAmount().Currency())
				if err := plan.Adjust(normal, PlanStateReserved, larger, larger, larger); err != nil {
					t.Fatalf("Adjust: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateReserved, 1_500, 1_500)
				if plan.Mode() != AuthorizationModeNormal {
					t.Errorf("Mode = %q, want NORMAL", plan.Mode())
				}
				if !plan.Reason().IsZero() {
					t.Errorf("Reason = %q, want zero after normal reauthorization", plan.Reason())
				}
			},
		},
		{
			name: "RESERVED_EXCEPTION -> RESERVED_EXCEPTION",
			from: PlanStateReservedException,
			apply: func(t *testing.T, plan *PlannedOperation) {
				larger := mustPositiveAmountForTest(t, 1_500, plan.IntendedAmount().Currency())
				newReason := mustOverrideReasonForTest(t, "revalidated override")
				outcome := AuthorizationOutcome{Mode: AuthorizationModeEmergencyOverride, Reason: newReason}
				if err := plan.Adjust(outcome, PlanStateReservedException, larger, larger, larger); err != nil {
					t.Fatalf("Adjust: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateReservedException, 1_500, 1_500)
				if plan.Mode() != AuthorizationModeEmergencyOverride {
					t.Errorf("Mode = %q, want EMERGENCY_OVERRIDE", plan.Mode())
				}
				if plan.Reason() != OverrideReason("revalidated override") {
					t.Errorf("Reason = %q, want revalidated override", plan.Reason())
				}
			},
		},
		{
			name: "RESERVED_EXCEPTION -> DRAFT",
			from: PlanStateReservedException,
			apply: func(t *testing.T, plan *PlannedOperation) {
				smaller := mustPositiveAmountForTest(t, 800, plan.IntendedAmount().Currency())
				if err := plan.Adjust(AuthorizationOutcome{}, PlanStateDraft, smaller, PositiveAmount{}, PositiveAmount{}); err != nil {
					t.Fatalf("Adjust: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateDraft, 800, 800)
				if plan.Mode() != "" || !plan.Reason().IsZero() {
					t.Errorf("draft authorization = (%q, %q), want zero", plan.Mode(), plan.Reason())
				}
			},
		},
		{
			name: "RESERVED_EXCEPTION -> COMPLETED",
			from: PlanStateReservedException,
			apply: func(t *testing.T, plan *PlannedOperation) {
				amount := plan.HeldAmount()
				if err := plan.Complete(amount, amount, amount); err != nil {
					t.Fatalf("Complete: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateCompleted, 1_000, 1_000)
				if plan.Mode() != AuthorizationModeEmergencyOverride {
					t.Errorf("Mode = %q, want the override audit to remain", plan.Mode())
				}
			},
		},
		{
			name: "RESERVED_EXCEPTION -> CANCELLED",
			from: PlanStateReservedException,
			apply: func(t *testing.T, plan *PlannedOperation) {
				if err := plan.Cancel(); err != nil {
					t.Fatalf("Cancel: %v", err)
				}
			},
			check: func(t *testing.T, plan *PlannedOperation) {
				assertPlanState(t, plan, PlanStateCancelled, 1_000, 1_000)
				if plan.Mode() != AuthorizationModeEmergencyOverride {
					t.Errorf("Mode = %q, want the exception audit to remain", plan.Mode())
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := newPlanInState(t, tt.from)
			tt.apply(t, plan)
			tt.check(t, plan)
		})
	}
}

func TestPlannedOperationAuthorizeOutcomes(t *testing.T) {
	reason := mustOverrideReasonForTest(t, "recorded override")
	tests := []struct {
		name       string
		outcome    AuthorizationOutcome
		wantState  PlanState
		wantMode   AuthorizationMode
		wantReason OverrideReason
		wantCode   ErrorCode
	}{
		{
			name:      "normal outcome reserves",
			outcome:   AuthorizationOutcome{Mode: AuthorizationModeNormal},
			wantState: PlanStateReserved,
			wantMode:  AuthorizationModeNormal,
		},
		{
			name:       "reasoned override reserves exceptionally",
			outcome:    AuthorizationOutcome{Mode: AuthorizationModeEmergencyOverride, Reason: reason},
			wantState:  PlanStateReservedException,
			wantMode:   AuthorizationModeEmergencyOverride,
			wantReason: reason,
		},
		{
			name:     "override without reason is rejected",
			outcome:  AuthorizationOutcome{Mode: AuthorizationModeEmergencyOverride},
			wantCode: CodeOverrideReasonRequired,
		},
		{
			name:     "unknown mode is rejected",
			outcome:  AuthorizationOutcome{Mode: AuthorizationMode("BOGUS")},
			wantCode: CodeInvalidValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := newPlanInState(t, PlanStateDraft)
			amount := plan.IntendedAmount()
			before := *plan
			err := plan.Authorize(tt.outcome, amount, amount)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("Authorize error = %v, want code %s", err, tt.wantCode)
				}
				if plan.State() != PlanStateDraft {
					t.Errorf("State = %s, want DRAFT after rejection", plan.State())
				}
				assertPlanUnchanged(t, before, plan)
				return
			}
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if plan.State() != tt.wantState {
				t.Errorf("State = %s, want %s", plan.State(), tt.wantState)
			}
			if plan.Mode() != tt.wantMode {
				t.Errorf("Mode = %q, want %q", plan.Mode(), tt.wantMode)
			}
			if plan.Reason() != tt.wantReason {
				t.Errorf("Reason = %q, want %q", plan.Reason(), tt.wantReason)
			}
		})
	}
}

func TestPlannedOperationRejectedTransitions(t *testing.T) {
	reason := mustOverrideReasonForTest(t, "rejection fixture")
	normal := AuthorizationOutcome{Mode: AuthorizationModeNormal}
	override := AuthorizationOutcome{Mode: AuthorizationModeEmergencyOverride, Reason: reason}

	tests := []struct {
		name     string
		plan     *PlannedOperation
		apply    func(*PlannedOperation) error
		wantCode ErrorCode
	}{
		{
			name: "authorize from RESERVED",
			plan: newPlanInState(t, PlanStateReserved),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Authorize(normal, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "authorize from RESERVED_EXCEPTION",
			plan: newPlanInState(t, PlanStateReservedException),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Authorize(override, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "authorize from COMPLETED",
			plan: newPlanInState(t, PlanStateCompleted),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Authorize(normal, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "authorize from CANCELLED",
			plan: newPlanInState(t, PlanStateCancelled),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Authorize(normal, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "adjust from DRAFT",
			plan: newPlanInState(t, PlanStateDraft),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Adjust(normal, PlanStateReserved, amount, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "adjust from COMPLETED",
			plan: newPlanInState(t, PlanStateCompleted),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Adjust(normal, PlanStateReserved, amount, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "adjust from CANCELLED",
			plan: newPlanInState(t, PlanStateCancelled),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Adjust(normal, PlanStateReserved, amount, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "adjust RESERVED to COMPLETED is not an adjust target",
			plan: newPlanInState(t, PlanStateReserved),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Adjust(normal, PlanStateCompleted, amount, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "adjust RESERVED to CANCELLED is not an adjust target",
			plan: newPlanInState(t, PlanStateReserved),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Adjust(normal, PlanStateCancelled, amount, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "adjust RESERVED with an override outcome",
			plan: newPlanInState(t, PlanStateReserved),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Adjust(override, PlanStateReserved, amount, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "adjust RESERVED_EXCEPTION to RESERVED_EXCEPTION with a normal outcome",
			plan: newPlanInState(t, PlanStateReservedException),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Adjust(normal, PlanStateReservedException, amount, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "adjust RESERVED_EXCEPTION without a new reason",
			plan: newPlanInState(t, PlanStateReservedException),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Adjust(AuthorizationOutcome{Mode: AuthorizationModeEmergencyOverride}, PlanStateReservedException, amount, amount, amount)
			},
			wantCode: CodeOverrideReasonRequired,
		},
		{
			name: "adjust to an unknown state",
			plan: newPlanInState(t, PlanStateReserved),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Adjust(normal, PlanState("BOGUS"), amount, amount, amount)
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "adjust with mismatched holds",
			plan: newPlanInState(t, PlanStateReserved),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				mismatch := mustPositiveAmountForTest(t, amount.MinorUnits()+1, amount.Currency())
				return plan.Adjust(normal, PlanStateReserved, amount, mismatch, mismatch)
			},
			wantCode: CodeReservationMismatch,
		},
		{
			name: "complete from DRAFT",
			plan: newPlanInState(t, PlanStateDraft),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Complete(amount, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "complete from CANCELLED",
			plan: newPlanInState(t, PlanStateCancelled),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				return plan.Complete(amount, amount, amount)
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "complete above the authorized amount",
			plan: newPlanInState(t, PlanStateReserved),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				tooMuch := mustPositiveAmountForTest(t, amount.MinorUnits()+1, amount.Currency())
				return plan.Complete(tooMuch, amount, amount)
			},
			wantCode: CodeAmountExceedsAuthorization,
		},
		{
			name: "complete with mismatched held amounts",
			plan: newPlanInState(t, PlanStateReserved),
			apply: func(plan *PlannedOperation) error {
				amount := plan.IntendedAmount()
				mismatch := mustPositiveAmountForTest(t, amount.MinorUnits()+1, amount.Currency())
				return plan.Complete(amount, mismatch, mismatch)
			},
			wantCode: CodeReservationMismatch,
		},
		{
			name: "cancel from COMPLETED",
			plan: newPlanInState(t, PlanStateCompleted),
			apply: func(plan *PlannedOperation) error {
				return plan.Cancel()
			},
			wantCode: CodeInvalidEntityState,
		},
		{
			name: "cancel from CANCELLED",
			plan: newPlanInState(t, PlanStateCancelled),
			apply: func(plan *PlannedOperation) error {
				return plan.Cancel()
			},
			wantCode: CodeInvalidEntityState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := *tt.plan
			err := tt.apply(tt.plan)
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("error = %v, want code %s", err, tt.wantCode)
			}
			assertPlanUnchanged(t, before, tt.plan)
		})
	}
}

func TestPlannedOperationCompleteIdempotent(t *testing.T) {
	plan := newPlanInState(t, PlanStateReserved)
	amount := plan.HeldAmount()
	if err := plan.Complete(amount, amount, amount); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if plan.State() != PlanStateCompleted {
		t.Fatalf("State = %s, want COMPLETED", plan.State())
	}
	if plan.HeldAmount().MinorUnits() != 0 || plan.HeldOnAccount().MinorUnits() != 0 {
		t.Errorf("held after completion = (%d, %d), want (0, 0)", plan.HeldAmount().MinorUnits(), plan.HeldOnAccount().MinorUnits())
	}
	if plan.releasedAmount.MinorUnits() != amount.MinorUnits() || plan.releasedOnAccount.MinorUnits() != amount.MinorUnits() {
		t.Errorf("released = (%d, %d), want (%d, %d)", plan.releasedAmount.MinorUnits(), plan.releasedOnAccount.MinorUnits(), amount.MinorUnits(), amount.MinorUnits())
	}

	completed := *plan
	if err := plan.Complete(amount, amount, amount); err != nil {
		t.Errorf("idempotent Complete returned error: %v", err)
	}
	assertPlanUnchanged(t, completed, plan)

	different := mustPositiveAmountForTest(t, amount.MinorUnits()-100, amount.Currency())
	err := plan.Complete(different, amount, amount)
	if !HasCode(err, CodeInvalidEntityState) {
		t.Fatalf("second Complete with a different amount error = %v, want INVALID_ENTITY_STATE", err)
	}
	assertPlanUnchanged(t, completed, plan)
}

func TestPlannedOperationCompleteReleasesExcess(t *testing.T) {
	plan := newPlanInState(t, PlanStateReserved)
	held := plan.HeldAmount()
	final := mustPositiveAmountForTest(t, held.MinorUnits()-250, held.Currency())
	if err := plan.Complete(final, held, held); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if plan.finalAmount.MinorUnits() != final.MinorUnits() {
		t.Errorf("finalAmount = %d, want %d", plan.finalAmount.MinorUnits(), final.MinorUnits())
	}
	if plan.releasedAmount.MinorUnits() != held.MinorUnits() {
		t.Errorf("releasedAmount = %d, want full held amount %d", plan.releasedAmount.MinorUnits(), held.MinorUnits())
	}
	if plan.HeldAmount().MinorUnits() != 0 {
		t.Errorf("HeldAmount = %d, want 0", plan.HeldAmount().MinorUnits())
	}
}

func TestPlannedOperationAdjustPaths(t *testing.T) {
	newReason := mustOverrideReasonForTest(t, "revalidated override")
	normal := AuthorizationOutcome{Mode: AuthorizationModeNormal}

	t.Run("RESERVED to RESERVED records the new holds", func(t *testing.T) {
		plan := newPlanInState(t, PlanStateReserved)
		newAmount := mustPositiveAmountForTest(t, 1_500, plan.IntendedAmount().Currency())
		if err := plan.Adjust(normal, PlanStateReserved, newAmount, newAmount, newAmount); err != nil {
			t.Fatalf("Adjust: %v", err)
		}
		assertPlanState(t, plan, PlanStateReserved, 1_500, 1_500)
		if plan.HeldAmount().MinorUnits() != 1_500 || plan.HeldOnAccount().MinorUnits() != 1_500 {
			t.Errorf("holds = (%d, %d), want (1500, 1500)", plan.HeldAmount().MinorUnits(), plan.HeldOnAccount().MinorUnits())
		}
	})

	t.Run("RESERVED to DRAFT keeps zero holds", func(t *testing.T) {
		plan := newPlanInState(t, PlanStateReserved)
		newAmount := mustPositiveAmountForTest(t, 800, plan.IntendedAmount().Currency())
		if err := plan.Adjust(AuthorizationOutcome{}, PlanStateDraft, newAmount, PositiveAmount{}, PositiveAmount{}); err != nil {
			t.Fatalf("Adjust: %v", err)
		}
		assertPlanState(t, plan, PlanStateDraft, 800, 800)
		if plan.HeldAmount().MinorUnits() != 0 || plan.HeldOnAccount().MinorUnits() != 0 {
			t.Errorf("holds = (%d, %d), want (0, 0)", plan.HeldAmount().MinorUnits(), plan.HeldOnAccount().MinorUnits())
		}
	})

	t.Run("RESERVED_EXCEPTION to RESERVED drops the exception", func(t *testing.T) {
		plan := newPlanInState(t, PlanStateReservedException)
		newAmount := mustPositiveAmountForTest(t, 1_500, plan.IntendedAmount().Currency())
		if err := plan.Adjust(normal, PlanStateReserved, newAmount, newAmount, newAmount); err != nil {
			t.Fatalf("Adjust: %v", err)
		}
		if plan.State() != PlanStateReserved {
			t.Errorf("State = %s, want RESERVED", plan.State())
		}
		if plan.Mode() != AuthorizationModeNormal {
			t.Errorf("Mode = %q, want NORMAL", plan.Mode())
		}
		if !plan.Reason().IsZero() {
			t.Errorf("Reason = %q, want zero", plan.Reason())
		}
	})

	t.Run("RESERVED_EXCEPTION to RESERVED_EXCEPTION records the new reason", func(t *testing.T) {
		plan := newPlanInState(t, PlanStateReservedException)
		newAmount := mustPositiveAmountForTest(t, 1_500, plan.IntendedAmount().Currency())
		outcome := AuthorizationOutcome{Mode: AuthorizationModeEmergencyOverride, Reason: newReason}
		if err := plan.Adjust(outcome, PlanStateReservedException, newAmount, newAmount, newAmount); err != nil {
			t.Fatalf("Adjust: %v", err)
		}
		if plan.State() != PlanStateReservedException {
			t.Errorf("State = %s, want RESERVED_EXCEPTION", plan.State())
		}
		if plan.Reason() != newReason {
			t.Errorf("Reason = %q, want %q", plan.Reason(), newReason)
		}
	})
}
