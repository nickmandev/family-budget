package domain

import (
	"strings"
	"testing"
)

// validJournalOperationParams returns a fully valid envelope with income
// postings, an amount, and a padded reason.
func validJournalOperationParams(t *testing.T) JournalOperationParams {
	t.Helper()
	f := newJournalFixture(t)
	income, err := IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 1_000, f.currency))
	if err != nil {
		t.Fatalf("IncomeReceivedEffects: %v", err)
	}
	params := f.params(t, "op-valid", OpIncome, income)
	amount := mustPositiveAmountForTest(t, 1_000, f.currency)
	params.Amount = &amount
	params.Reason = "  monthly income  "
	return params
}

func TestParseOperationType(t *testing.T) {
	valid := []OperationType{
		OpOpeningBalance,
		OpIncome,
		OpReimbursement,
		OpAllocate,
		OpReallocate,
		OpDebitPurchase,
		OpCashPurchase,
		OpCardPurchase,
		OpCardPayment,
		OpCardFeeInterest,
		OpTransferEligible,
		OpTransferToEligible,
		OpTransferOutOfScope,
		OpTransferExcluded,
		OpCashRefund,
		OpCardRefund,
		OpReservationCreate,
		OpReservationResize,
		OpReservationCancel,
		OpPurchaseComplete,
		OpExternalTransaction,
		OpReversal,
		OpReplacement,
		OpCategoryCorrection,
		OpAccountCorrection,
		OpMetadataCorrection,
		OpReconcileIncrease,
		OpReconcileDecrease,
	}
	for _, want := range valid {
		got, err := ParseOperationType(want.String())
		if err != nil {
			t.Errorf("ParseOperationType(%q): %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("ParseOperationType(%q) = %q, want %q", want, got, want)
		}
		if !got.IsValid() {
			t.Errorf("%q.IsValid() = false, want true", got)
		}
	}

	for _, raw := range []string{"", "income", "UNKNOWN", "CARD_PAYMENT "} {
		if _, err := ParseOperationType(raw); !HasCode(err, CodeInvalidValue) {
			t.Errorf("ParseOperationType(%q) error = %v, want INVALID_VALUE", raw, err)
		}
	}
}

func TestOperationTypeAllowsEmptyEffects(t *testing.T) {
	if !OpMetadataCorrection.AllowsEmptyEffects() {
		t.Error("OpMetadataCorrection.AllowsEmptyEffects() = false, want true")
	}
	for _, opType := range []OperationType{OpIncome, OpReservationCancel, OpReconcileIncrease} {
		if opType.AllowsEmptyEffects() {
			t.Errorf("%q.AllowsEmptyEffects() = true, want false", opType)
		}
	}
}

func TestJournalOperationValidation(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(t *testing.T, p *JournalOperationParams)
		wantCode ErrorCode
	}{
		{name: "valid"},
		{
			name:     "zero id",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.ID = "" },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero household",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.Household = "" },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero actor",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.Actor = "" },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "unknown type",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.Type = OperationType("BOGUS") },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero type",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.Type = "" },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "unknown source",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.Source = TransactionSource("WIRE") },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero source",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.Source = "" },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero effective date",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.EffectiveDate = EffectiveDate{} },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero currency",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.Currency = Currency{} },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero idempotency key",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.IdempotencyKey = "" },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero request fingerprint",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.RequestFingerprint = "" },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero created timestamp",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.CreatedAt = RecordedAt{} },
			wantCode: CodeInvalidValue,
		},
		{
			name:     "zero committed timestamp",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.CommittedAt = RecordedAt{} },
			wantCode: CodeInvalidValue,
		},
		{
			name: "committed before created",
			mutate: func(t *testing.T, p *JournalOperationParams) {
				p.CommittedAt = p.CreatedAt
				p.CreatedAt = NewRecordedAt(p.CreatedAt.Time().AddDate(0, 0, 1))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name:     "empty effects for income",
			mutate:   func(t *testing.T, p *JournalOperationParams) { p.Effects = Effects{} },
			wantCode: CodeInvalidValue,
		},
		{
			name: "empty effects allowed for metadata correction",
			mutate: func(t *testing.T, p *JournalOperationParams) {
				p.Type = OpMetadataCorrection
				p.Effects = Effects{}
				p.Amount = nil
			},
		},
		{
			name: "amount currency mismatch",
			mutate: func(t *testing.T, p *JournalOperationParams) {
				eur := mustCurrency(t, "EUR", 2)
				amount := mustPositiveAmountForTest(t, 1_000, eur)
				p.Amount = &amount
			},
			wantCode: CodeCurrencyMismatch,
		},
		{
			name: "zero amount",
			mutate: func(t *testing.T, p *JournalOperationParams) {
				amount := PositiveAmount{}
				p.Amount = &amount
			},
			wantCode: CodeInvalidAmount,
		},
		{
			name: "posting currency mismatch",
			mutate: func(t *testing.T, p *JournalOperationParams) {
				eur := mustCurrency(t, "EUR", 2)
				account := mustAccountIDForTest(t, "acct-checking")
				postings := PostingSet{}
				if err := postings.AddAccount(account, mustSignedDeltaForTest(t, 100, eur)); err != nil {
					t.Fatalf("AddAccount: %v", err)
				}
				p.Effects = Effects{Postings: postings}
			},
			wantCode: CodeCurrencyMismatch,
		},
		{
			name: "zero posting account id",
			mutate: func(t *testing.T, p *JournalOperationParams) {
				p.Effects.Postings.Accounts = []AccountPosting{{
					AccountID: "",
					Delta:     mustSignedDeltaForTest(t, 100, p.Currency),
				}}
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "invalid related reference",
			mutate: func(t *testing.T, p *JournalOperationParams) {
				p.Effects.Related = []RelatedOperationRef{{Kind: "BOGUS", OperationID: "op-1"}}
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "reason too long",
			mutate: func(t *testing.T, p *JournalOperationParams) {
				p.Reason = strings.Repeat("x", maxJournalReasonLength+1)
			},
			wantCode: CodeInvalidValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := validJournalOperationParams(t)
			if tt.mutate != nil {
				tt.mutate(t, &params)
			}
			_, err := NewJournalOperation(params)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewJournalOperation error = %v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewJournalOperation unexpected error: %v", err)
			}
		})
	}
}

func TestJournalOperationExposesFields(t *testing.T) {
	f := newJournalFixture(t)
	amount := mustPositiveAmountForTest(t, 2_000, f.currency)
	target := ReservationTarget{
		PlanID:     mustPlanIDForTest(t, "plan-1"),
		EnvelopeID: f.spend,
		AccountID:  f.checking,
	}

	purchase, err := DebitPurchaseEffects(f.checking, f.spend, amount)
	if err != nil {
		t.Fatalf("DebitPurchaseEffects: %v", err)
	}
	completed, err := CompleteReservedPurchaseEffects(target, purchase, amount, amount)
	if err != nil {
		t.Fatalf("CompleteReservedPurchaseEffects: %v", err)
	}
	planLink := RelatedOperationRef{
		Kind:        RelatedRefPlan,
		OperationID: mustJournalOpIDForTest(t, "op-plan"),
	}
	effects, err := MergeEffects(completed, Effects{Related: []RelatedOperationRef{planLink}})
	if err != nil {
		t.Fatalf("MergeEffects: %v", err)
	}

	params := f.params(t, "op-full", OpPurchaseComplete, effects)
	params.Amount = &amount
	params.Reason = "  completed planned purchase  "
	op, err := NewJournalOperation(params)
	if err != nil {
		t.Fatalf("NewJournalOperation: %v", err)
	}

	if op.ID() != params.ID {
		t.Errorf("ID = %q, want %q", op.ID(), params.ID)
	}
	if op.Household() != f.household {
		t.Errorf("Household = %q, want %q", op.Household(), f.household)
	}
	if op.Actor() != f.actor {
		t.Errorf("Actor = %q, want %q", op.Actor(), f.actor)
	}
	if op.Type() != OpPurchaseComplete {
		t.Errorf("Type = %q, want %q", op.Type(), OpPurchaseComplete)
	}
	if op.Source() != TransactionSourceManual {
		t.Errorf("Source = %q, want %q", op.Source(), TransactionSourceManual)
	}
	if op.EffectiveDate().String() != "2026-01-15" {
		t.Errorf("EffectiveDate = %q, want 2026-01-15", op.EffectiveDate())
	}
	if !op.Currency().Equal(f.currency) {
		t.Errorf("Currency = %q, want %q", op.Currency(), f.currency)
	}
	if op.IdempotencyKey() != params.IdempotencyKey {
		t.Errorf("IdempotencyKey = %q, want %q", op.IdempotencyKey(), params.IdempotencyKey)
	}
	if op.RequestFingerprint() != params.RequestFingerprint {
		t.Errorf("RequestFingerprint = %q, want %q", op.RequestFingerprint(), params.RequestFingerprint)
	}
	if got, ok := op.Amount(); !ok || got.MinorUnits() != amount.MinorUnits() {
		t.Errorf("Amount = (%d, %v), want (%d, true)", got.MinorUnits(), ok, amount.MinorUnits())
	}
	if op.Reason() != "completed planned purchase" {
		t.Errorf("Reason = %q, want %q", op.Reason(), "completed planned purchase")
	}
	if !op.CreatedAt().Time().Equal(f.created.Time()) {
		t.Errorf("CreatedAt = %s, want %s", op.CreatedAt(), f.created)
	}
	if !op.CommittedAt().Time().Equal(f.committed.Time()) {
		t.Errorf("CommittedAt = %s, want %s", op.CommittedAt(), f.committed)
	}
	if got := len(op.Postings().Accounts); got != 1 {
		t.Errorf("account postings = %d, want 1", got)
	}
	if got := len(op.Postings().Envelopes); got != 1 {
		t.Errorf("envelope postings = %d, want 1", got)
	}
	if got := len(op.ReservationChanges()); got != 1 {
		t.Fatalf("reservation changes = %d, want 1", got)
	}
	if op.ReservationChanges()[0].PlanID != target.PlanID {
		t.Errorf("reservation plan id = %q, want %q", op.ReservationChanges()[0].PlanID, target.PlanID)
	}
	if op.ReservationChanges()[0].Account == nil {
		t.Error("reservation account hold = nil, want non-nil")
	}
	if got := len(op.Related()); got != 1 {
		t.Fatalf("related links = %d, want 1", got)
	}
	if op.Related()[0].Kind != RelatedRefPlan {
		t.Errorf("related kind = %q, want %q", op.Related()[0].Kind, RelatedRefPlan)
	}
	if op.Effects().IsEmpty() {
		t.Error("Effects().IsEmpty() = true, want false")
	}
}

func TestJournalOperationAmountAbsent(t *testing.T) {
	params := validJournalOperationParams(t)
	params.Amount = nil
	op, err := NewJournalOperation(params)
	if err != nil {
		t.Fatalf("NewJournalOperation: %v", err)
	}
	if _, ok := op.Amount(); ok {
		t.Error("Amount present = true, want false")
	}
}

func TestJournalOperationIsImmutable(t *testing.T) {
	f := newJournalFixture(t)
	income, err := IncomeReceivedEffects(f.checking, mustPositiveAmountForTest(t, 1_000, f.currency))
	if err != nil {
		t.Fatalf("IncomeReceivedEffects: %v", err)
	}
	originalDelta := income.Postings.Accounts[0].Delta.MinorUnits()

	op := f.operation(t, "op-immutable", OpIncome, income)

	// Mutating the caller's effect set after construction must not reach the
	// committed operation.
	income.Postings.Accounts[0] = AccountPosting{}
	postings := op.Postings()
	if got := postings.Accounts[0].Delta.MinorUnits(); got != originalDelta {
		t.Errorf("stored posting delta = %d, want %d", got, originalDelta)
	}

	// Mutating an accessor result must not reach the committed operation either.
	postings.Accounts[0] = AccountPosting{}
	again := op.Postings()
	if got := again.Accounts[0].Delta.MinorUnits(); got != originalDelta {
		t.Errorf("stored posting delta after accessor mutation = %d, want %d", got, originalDelta)
	}
}
