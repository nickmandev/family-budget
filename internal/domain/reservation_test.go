package domain

import "testing"

func TestNewEnvelopeHold(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	envelope := mustEnvelopeIDForTest(t, "env-groceries")

	tests := []struct {
		name     string
		id       EnvelopeId
		delta    SignedMoneyDelta
		wantCode ErrorCode
	}{
		{name: "hold", id: envelope, delta: mustSignedDeltaForTest(t, 500, usd)},
		{name: "release", id: envelope, delta: mustSignedDeltaForTest(t, -500, usd)},
		{name: "zero id", id: "", delta: mustSignedDeltaForTest(t, 500, usd), wantCode: CodeInvalidValue},
		{name: "zero delta", id: envelope, delta: SignedMoneyDelta{}, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewEnvelopeHold(tt.id, tt.delta)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewEnvelopeHold error = %v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewEnvelopeHold unexpected error: %v", err)
			}
			if got.EnvelopeID != tt.id {
				t.Errorf("EnvelopeID = %q, want %q", got.EnvelopeID, tt.id)
			}
		})
	}
}

func TestNewAccountHold(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	account := mustAccountIDForTest(t, "acct-checking")

	tests := []struct {
		name     string
		id       AccountId
		delta    SignedMoneyDelta
		wantCode ErrorCode
	}{
		{name: "hold", id: account, delta: mustSignedDeltaForTest(t, 500, usd)},
		{name: "release", id: account, delta: mustSignedDeltaForTest(t, -500, usd)},
		{name: "zero id", id: "", delta: mustSignedDeltaForTest(t, 500, usd), wantCode: CodeInvalidValue},
		{name: "zero delta", id: account, delta: SignedMoneyDelta{}, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewAccountHold(tt.id, tt.delta)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewAccountHold error = %v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewAccountHold unexpected error: %v", err)
			}
			if got.AccountID != tt.id {
				t.Errorf("AccountID = %q, want %q", got.AccountID, tt.id)
			}
		})
	}
}

func TestNewReservationChange(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	plan := mustPlanIDForTest(t, "plan-1")
	envelope := mustEnvelopeIDForTest(t, "env-groceries")
	account := mustAccountIDForTest(t, "acct-checking")

	envelopeHold, err := NewEnvelopeHold(envelope, mustSignedDeltaForTest(t, 500, usd))
	if err != nil {
		t.Fatalf("NewEnvelopeHold: %v", err)
	}
	accountHold, err := NewAccountHold(account, mustSignedDeltaForTest(t, 500, usd))
	if err != nil {
		t.Fatalf("NewAccountHold: %v", err)
	}
	zeroIDAccountHold := AccountHold{Delta: mustSignedDeltaForTest(t, 500, usd)}
	zeroDeltaAccountHold := AccountHold{AccountID: account}

	tests := []struct {
		name     string
		plan     PlanId
		envelope EnvelopeHold
		account  *AccountHold
		wantCode ErrorCode
		wantAcct bool
	}{
		{name: "envelope only (Q1 card purchase)", plan: plan, envelope: envelopeHold, account: nil, wantAcct: false},
		{name: "envelope and account (Q2 repayment)", plan: plan, envelope: envelopeHold, account: &accountHold, wantAcct: true},
		{name: "zero plan", plan: "", envelope: envelopeHold, wantCode: CodeInvalidValue},
		{name: "zero envelope id", plan: plan, envelope: EnvelopeHold{Delta: mustSignedDeltaForTest(t, 1, usd)}, wantCode: CodeInvalidValue},
		{name: "zero envelope delta", plan: plan, envelope: EnvelopeHold{EnvelopeID: envelope}, wantCode: CodeInvalidValue},
		{name: "partial account: zero id", plan: plan, envelope: envelopeHold, account: &zeroIDAccountHold, wantCode: CodeInvalidValue},
		{name: "partial account: zero delta", plan: plan, envelope: envelopeHold, account: &zeroDeltaAccountHold, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewReservationChange(tt.plan, tt.envelope, tt.account)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewReservationChange error = %v, want code %s", err, tt.wantCode)
				}
				if !got.PlanID.IsZero() || !got.Envelope.EnvelopeID.IsZero() || got.Account != nil {
					t.Fatalf("NewReservationChange returned non-zero value on error: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewReservationChange unexpected error: %v", err)
			}
			if got.PlanID != tt.plan {
				t.Errorf("PlanID = %q, want %q", got.PlanID, tt.plan)
			}
			if got.Envelope.EnvelopeID != tt.envelope.EnvelopeID {
				t.Errorf("Envelope.EnvelopeID = %q, want %q", got.Envelope.EnvelopeID, tt.envelope.EnvelopeID)
			}
			if (got.Account != nil) != tt.wantAcct {
				t.Fatalf("Account presence = %v, want %v", got.Account != nil, tt.wantAcct)
			}
			if tt.wantAcct && got.Account.AccountID != account {
				t.Errorf("Account.AccountID = %q, want %q", got.Account.AccountID, account)
			}
		})
	}
}

func TestNewReservationChangeCopiesAccountHold(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	plan := mustPlanIDForTest(t, "plan-1")
	envelope := mustEnvelopeIDForTest(t, "env-groceries")
	account := mustAccountIDForTest(t, "acct-checking")

	envelopeHold, err := NewEnvelopeHold(envelope, mustSignedDeltaForTest(t, 500, usd))
	if err != nil {
		t.Fatalf("NewEnvelopeHold: %v", err)
	}
	accountHold, err := NewAccountHold(account, mustSignedDeltaForTest(t, 500, usd))
	if err != nil {
		t.Fatalf("NewAccountHold: %v", err)
	}
	change, err := NewReservationChange(plan, envelopeHold, &accountHold)
	if err != nil {
		t.Fatalf("NewReservationChange: %v", err)
	}

	accountHold.Delta = mustSignedDeltaForTest(t, 1, usd)
	if change.Account.Delta.MinorUnits() != 500 {
		t.Errorf("ReservationChange aliases caller's AccountHold; delta = %d, want 500", change.Account.Delta.MinorUnits())
	}
}
