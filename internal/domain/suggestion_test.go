package domain

import (
	"reflect"
	"testing"
)

func mustSuggestionIDForTest(t *testing.T, raw string) SuggestionId {
	t.Helper()
	id, err := NewSuggestionId(raw)
	if err != nil {
		t.Fatalf("NewSuggestionId(%q): %v", raw, err)
	}
	return id
}

// newSuggestionFixture builds a valid SUGGESTED candidate.
func newSuggestionFixture(t *testing.T) (*Suggestion, Currency) {
	t.Helper()
	usd := mustCurrency(t, "USD", 2)
	s, err := NewSuggestion(
		mustSuggestionIDForTest(t, "sug-1"),
		TransactionSourceManual,
		mustPositiveAmountForTest(t, 1_000, usd),
		"Coffee Shop",
	)
	if err != nil {
		t.Fatalf("NewSuggestion: %v", err)
	}
	return s, usd
}

// newSuggestionInState builds a suggestion directly in the requested lifecycle
// state for transition tests.
func newSuggestionInState(t *testing.T, state SuggestionState) (*Suggestion, Currency) {
	t.Helper()
	s, usd := newSuggestionFixture(t)
	switch state {
	case SuggestionStateSuggested:
		// Already in the requested state.
	case SuggestionStateEdited:
		if err := s.Edit(mustPositiveAmountForTest(t, 1_200, usd), "Coffee Shop (corrected)"); err != nil {
			t.Fatalf("Edit: %v", err)
		}
	case SuggestionStateConfirmed:
		if err := s.Confirm(); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
	case SuggestionStateCancelled:
		if err := s.Cancel("dismissed"); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	default:
		t.Fatalf("newSuggestionInState: unsupported state %q", state)
	}
	return s, usd
}

func TestParseSuggestionState(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     SuggestionState
		wantCode ErrorCode
	}{
		{name: "suggested", raw: "SUGGESTED", want: SuggestionStateSuggested},
		{name: "edited", raw: "EDITED", want: SuggestionStateEdited},
		{name: "confirmed", raw: "CONFIRMED", want: SuggestionStateConfirmed},
		{name: "cancelled", raw: "CANCELLED", want: SuggestionStateCancelled},
		{name: "lowercase", raw: "suggested", wantCode: CodeInvalidValue},
		{name: "empty", raw: "", wantCode: CodeInvalidValue},
		{name: "unknown", raw: "DRAFT", wantCode: CodeInvalidValue},
		{name: "padded", raw: " SUGGESTED", wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSuggestionState(tt.raw)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("ParseSuggestionState(%q) error = %v, want code %s", tt.raw, err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSuggestionState(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("ParseSuggestionState(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			if got.String() != tt.raw {
				t.Errorf("String() = %q, want %q", got.String(), tt.raw)
			}
			if !got.IsValid() {
				t.Errorf("%q.IsValid() = false, want true", got)
			}
		})
	}
}

func TestSuggestionStateIsValid(t *testing.T) {
	if (SuggestionState("")).IsValid() {
		t.Error("zero SuggestionState must be invalid")
	}
	if (SuggestionState("DRAFT")).IsValid() {
		t.Error("unknown SuggestionState must be invalid")
	}
}

func TestNewSuggestion(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	validAmount := mustPositiveAmountForTest(t, 1_000, usd)

	tests := []struct {
		name     string
		id       SuggestionId
		source   TransactionSource
		amount   PositiveAmount
		merchant string
		wantCode ErrorCode
	}{
		{name: "valid manual", id: mustSuggestionIDForTest(t, "sug-1"), source: TransactionSourceManual, amount: validAmount, merchant: "Coffee Shop"},
		{name: "valid external", id: mustSuggestionIDForTest(t, "sug-2"), source: TransactionSourceExternalEntry, amount: validAmount, merchant: "Grocer"},
		{
			name:     "zero id",
			id:       "",
			source:   TransactionSourceManual,
			amount:   validAmount,
			merchant: "Coffee Shop",
			wantCode: CodeInvalidValue,
		},
		{
			name:     "invalid source",
			id:       mustSuggestionIDForTest(t, "sug-3"),
			source:   TransactionSource("BOGUS"),
			amount:   validAmount,
			merchant: "Coffee Shop",
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero amount",
			id:       mustSuggestionIDForTest(t, "sug-4"),
			source:   TransactionSourceManual,
			amount:   PositiveAmount{},
			merchant: "Coffee Shop",
			wantCode: CodeInvalidValue,
		},
		{
			name:     "invalid amount currency",
			id:       mustSuggestionIDForTest(t, "sug-5"),
			source:   TransactionSourceManual,
			amount:   PositiveAmount{money: Money{minorUnits: 1_000, currency: Currency{}}},
			merchant: "Coffee Shop",
			wantCode: CodeInvalidValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewSuggestion(tt.id, tt.source, tt.amount, tt.merchant)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewSuggestion error = %v, want code %s", err, tt.wantCode)
				}
				if got != nil {
					t.Errorf("NewSuggestion returned a suggestion for rejected input: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewSuggestion unexpected error: %v", err)
			}
			if got.ID() != tt.id {
				t.Errorf("ID = %q, want %q", got.ID(), tt.id)
			}
			if got.Source() != tt.source {
				t.Errorf("Source = %q, want %q", got.Source(), tt.source)
			}
			if got.Amount().MinorUnits() != tt.amount.MinorUnits() {
				t.Errorf("Amount = %d, want %d", got.Amount().MinorUnits(), tt.amount.MinorUnits())
			}
			if got.Merchant() != tt.merchant {
				t.Errorf("Merchant = %q, want %q", got.Merchant(), tt.merchant)
			}
			if got.State() != SuggestionStateSuggested {
				t.Errorf("State = %q, want %q", got.State(), SuggestionStateSuggested)
			}
			if got.ConfirmationCount() != 0 {
				t.Errorf("ConfirmationCount = %d, want 0", got.ConfirmationCount())
			}
		})
	}
}

func TestSuggestionAllowedTransitions(t *testing.T) {
	// Every allowed edge is listed explicitly. If AllowedSuggestionTransitions
	// grows, the coverage guard below fails until the new edge is tested.
	explicit := []struct {
		from SuggestionState
		to   SuggestionState
	}{
		{SuggestionStateSuggested, SuggestionStateEdited},
		{SuggestionStateSuggested, SuggestionStateConfirmed},
		{SuggestionStateSuggested, SuggestionStateCancelled},
		{SuggestionStateEdited, SuggestionStateEdited},
		{SuggestionStateEdited, SuggestionStateConfirmed},
		{SuggestionStateEdited, SuggestionStateCancelled},
	}

	explicitSet := make(map[[2]SuggestionState]bool, len(explicit))
	for _, pair := range explicit {
		explicitSet[[2]SuggestionState{pair.from, pair.to}] = true
	}

	allowed := AllowedSuggestionTransitions()
	seen := make(map[[2]SuggestionState]bool, len(explicit))
	for from, targets := range allowed {
		for _, to := range targets {
			pair := [2]SuggestionState{from, to}
			if !explicitSet[pair] {
				t.Errorf("allowed transition %s -> %s has no explicit coverage", from, to)
			}
			seen[pair] = true
		}
	}
	for _, pair := range explicit {
		if !seen[[2]SuggestionState{pair.from, pair.to}] {
			t.Errorf("explicit transition %s -> %s is not in AllowedSuggestionTransitions", pair.from, pair.to)
		}
	}

	// Terminal states have no outbound transitions.
	for _, terminal := range []SuggestionState{SuggestionStateConfirmed, SuggestionStateCancelled} {
		if len(allowed[terminal]) != 0 {
			t.Errorf("terminal state %s has outbound transitions: %v", terminal, allowed[terminal])
		}
	}
}

func TestSuggestionMutatorTransitions(t *testing.T) {
	allowed := AllowedSuggestionTransitions()
	for from, targets := range allowed {
		for _, to := range targets {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				suggestion, usd := newSuggestionInState(t, from)

				var err error
				switch to {
				case SuggestionStateEdited:
					err = suggestion.Edit(mustPositiveAmountForTest(t, 1_500, usd), "Corrected Merchant")
				case SuggestionStateConfirmed:
					err = suggestion.Confirm()
				case SuggestionStateCancelled:
					err = suggestion.Cancel("dismissed")
				default:
					t.Fatalf("test has no mutator for target state %q", to)
				}
				if err != nil {
					t.Fatalf("transition %s -> %s failed: %v", from, to, err)
				}
				if got := suggestion.State(); got != to {
					t.Errorf("State = %q, want %q", got, to)
				}
			})
		}
	}
}

func TestSuggestionRejectedTransitions(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	operations := []struct {
		name string
		run  func(*Suggestion) error
	}{
		{name: "edit", run: func(s *Suggestion) error {
			return s.Edit(mustPositiveAmountForTest(t, 1_500, usd), "Changed")
		}},
		{name: "cancel", run: func(s *Suggestion) error { return s.Cancel("changed my mind") }},
		{name: "confirm", run: func(s *Suggestion) error { return s.Confirm() }},
	}

	for _, from := range []SuggestionState{SuggestionStateConfirmed, SuggestionStateCancelled} {
		for _, op := range operations {
			// Confirming an already-confirmed suggestion is the documented
			// idempotent no-op, covered by TestSuggestionConfirmIdempotent.
			if from == SuggestionStateConfirmed && op.name == "confirm" {
				continue
			}
			t.Run(string(from)+"_"+op.name, func(t *testing.T) {
				suggestion, _ := newSuggestionInState(t, from)
				before := *suggestion

				err := op.run(suggestion)
				if !HasCode(err, CodeInvalidEntityState) {
					t.Fatalf("%s from %s error = %v, want code %s", op.name, from, err, CodeInvalidEntityState)
				}
				if !reflect.DeepEqual(before, *suggestion) {
					t.Errorf("%s from %s mutated the suggestion: before %+v, after %+v", op.name, from, before, *suggestion)
				}
			})
		}
	}
}

func TestSuggestionConfirmIdempotent(t *testing.T) {
	suggestion, _ := newSuggestionFixture(t)

	if err := suggestion.Confirm(); err != nil {
		t.Fatalf("first Confirm: %v", err)
	}
	if got := suggestion.State(); got != SuggestionStateConfirmed {
		t.Fatalf("State = %q, want %q", got, SuggestionStateConfirmed)
	}
	if got := suggestion.ConfirmationCount(); got != 1 {
		t.Fatalf("ConfirmationCount = %d, want 1", got)
	}

	before := *suggestion
	if err := suggestion.Confirm(); err != nil {
		t.Fatalf("second Confirm must be an idempotent no-op, got error: %v", err)
	}
	if got := suggestion.State(); got != SuggestionStateConfirmed {
		t.Errorf("State after second Confirm = %q, want %q", got, SuggestionStateConfirmed)
	}
	if got := suggestion.ConfirmationCount(); got != 1 {
		t.Errorf("ConfirmationCount after second Confirm = %d, want 1", got)
	}
	if !reflect.DeepEqual(before, *suggestion) {
		t.Errorf("second Confirm mutated the suggestion: before %+v, after %+v", before, *suggestion)
	}
}

func TestSuggestionCancelPreservesFingerprint(t *testing.T) {
	tests := []struct {
		name       string
		reason     string
		wantReason string
	}{
		{name: "with reason", reason: "duplicate notification", wantReason: "duplicate notification"},
		{name: "without reason", reason: "", wantReason: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suggestion, _ := newSuggestionFixture(t)
			id := suggestion.ID()
			source := suggestion.Source()
			amount := suggestion.Amount().MinorUnits()
			merchant := suggestion.Merchant()

			if err := suggestion.Cancel(tt.reason); err != nil {
				t.Fatalf("Cancel: %v", err)
			}
			if got := suggestion.State(); got != SuggestionStateCancelled {
				t.Errorf("State = %q, want %q", got, SuggestionStateCancelled)
			}
			if got := suggestion.CancelReason(); got != tt.wantReason {
				t.Errorf("CancelReason = %q, want %q", got, tt.wantReason)
			}

			// Q10: the fingerprint survives dismissal so ingestion can suppress
			// the suggestion without merging lookalike purchases.
			if got := suggestion.ID(); got != id {
				t.Errorf("ID after cancel = %q, want %q", got, id)
			}
			if got := suggestion.Source(); got != source {
				t.Errorf("Source after cancel = %q, want %q", got, source)
			}
			if got := suggestion.Amount().MinorUnits(); got != amount {
				t.Errorf("Amount after cancel = %d, want %d", got, amount)
			}
			if got := suggestion.Merchant(); got != merchant {
				t.Errorf("Merchant after cancel = %q, want %q", got, merchant)
			}
			if suggestion.CanTransition(SuggestionStateConfirmed) {
				t.Error("a cancelled suggestion must not transition to confirmed")
			}
		})
	}
}

// TestSuggestionExposesNoFinancialEffects asserts structurally that the
// suggestion aggregate cannot put anything on the ledger: no method on
// *Suggestion returns an Effects or PostingSet. Effects are produced only by
// the command-layer confirmation path, never by the suggestion itself.
func TestSuggestionExposesNoFinancialEffects(t *testing.T) {
	effectsType := reflect.TypeOf(Effects{})
	postingSetType := reflect.TypeOf(PostingSet{})

	typ := reflect.TypeOf(&Suggestion{})
	for i := 0; i < typ.NumMethod(); i++ {
		method := typ.Method(i)
		for j := 0; j < method.Type.NumOut(); j++ {
			out := method.Type.Out(j)
			if out == effectsType || out == postingSetType {
				t.Errorf("Suggestion.%s returns %s; suggestions must never expose financial effects", method.Name, out)
			}
		}
	}
}
