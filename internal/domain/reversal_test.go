package domain

import (
	"reflect"
	"testing"
)

func newReversalLedgerForTest(t *testing.T, originalMinorUnits int64) (*ReversalLedger, Currency) {
	t.Helper()
	usd := mustCurrency(t, "USD", 2)
	ledger, err := NewReversalLedger(mustPositiveAmountForTest(t, originalMinorUnits, usd))
	if err != nil {
		t.Fatalf("NewReversalLedger(%d): %v", originalMinorUnits, err)
	}
	return ledger, usd
}

func assertReversalState(t *testing.T, ledger *ReversalLedger, wantStatus ReversalStatus, wantReversible, wantRefundable int64) {
	t.Helper()
	if got := ledger.Status(); got != wantStatus {
		t.Errorf("Status = %s, want %s", got, wantStatus)
	}
	if got := ledger.ReversibleRemainder().MinorUnits(); got != wantReversible {
		t.Errorf("ReversibleRemainder = %d, want %d", got, wantReversible)
	}
	if got := ledger.RefundableRemainder().MinorUnits(); got != wantRefundable {
		t.Errorf("RefundableRemainder = %d, want %d", got, wantRefundable)
	}
}

func assertReversalUnchanged(t *testing.T, before ReversalLedger, ledger *ReversalLedger) {
	t.Helper()
	if !reflect.DeepEqual(before, *ledger) {
		t.Errorf("ledger changed after a rejected call: before %+v, after %+v", before, *ledger)
	}
}

func TestParseReversalStatus(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     ReversalStatus
		wantCode ErrorCode
	}{
		{name: "committed", raw: "COMMITTED", want: ReversalStatusCommitted},
		{name: "partially reversed", raw: "PARTIALLY_REVERSED", want: ReversalStatusPartiallyReversed},
		{name: "fully reversed", raw: "FULLY_REVERSED", want: ReversalStatusFullyReversed},
		{name: "lowercase", raw: "committed", wantCode: CodeInvalidValue},
		{name: "empty", raw: "", wantCode: CodeInvalidValue},
		{name: "unknown", raw: "REFUNDED", wantCode: CodeInvalidValue},
		{name: "padded", raw: " COMMITTED", wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReversalStatus(tt.raw)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("ParseReversalStatus(%q) error = %v, want code %s", tt.raw, err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseReversalStatus(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("ParseReversalStatus(%q) = %q, want %q", tt.raw, got, tt.want)
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

func TestNewReversalLedger(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	tests := []struct {
		name     string
		original PositiveAmount
		wantCode ErrorCode
	}{
		{name: "valid", original: mustPositiveAmountForTest(t, 1_000, usd)},
		{name: "zero amount", original: PositiveAmount{}, wantCode: CodeInvalidAmount},
		{
			name:     "negative amount",
			original: PositiveAmount{money: Money{minorUnits: -100, currency: usd}},
			wantCode: CodeInvalidAmount,
		},
		{
			name:     "invalid amount currency",
			original: PositiveAmount{money: Money{minorUnits: 1_000, currency: Currency{}}},
			wantCode: CodeInvalidValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger, err := NewReversalLedger(tt.original)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewReversalLedger error = %v, want code %s", err, tt.wantCode)
				}
				if ledger != nil {
					t.Errorf("NewReversalLedger returned a ledger for rejected input: %+v", ledger)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewReversalLedger unexpected error: %v", err)
			}
			assertReversalState(t, ledger, ReversalStatusCommitted, 1_000, 1_000)
			if links := ledger.Links(); len(links) != 0 {
				t.Errorf("fresh ledger has links: %v", links)
			}
		})
	}
}

func TestReversalLedgerPartialReversal(t *testing.T) {
	ledger, usd := newReversalLedgerForTest(t, 1_000)

	if err := ledger.RecordReversal(mustPositiveAmountForTest(t, 400, usd), mustJournalOpIDForTest(t, "op-reversal-1")); err != nil {
		t.Fatalf("RecordReversal: %v", err)
	}
	assertReversalState(t, ledger, ReversalStatusPartiallyReversed, 600, 1_000)

	// A refund after a partial reversal is allowed and must not change status.
	if err := ledger.RecordRefund(mustPositiveAmountForTest(t, 1_000, usd), mustJournalOpIDForTest(t, "op-refund-1")); err != nil {
		t.Fatalf("RecordRefund: %v", err)
	}
	assertReversalState(t, ledger, ReversalStatusPartiallyReversed, 600, 0)
}

func TestReversalLedgerFullReversal(t *testing.T) {
	ledger, usd := newReversalLedgerForTest(t, 1_000)

	if err := ledger.RecordReversal(mustPositiveAmountForTest(t, 1_000, usd), mustJournalOpIDForTest(t, "op-reversal-1")); err != nil {
		t.Fatalf("RecordReversal: %v", err)
	}
	assertReversalState(t, ledger, ReversalStatusFullyReversed, 0, 1_000)

	before := *ledger
	err := ledger.RecordReversal(mustPositiveAmountForTest(t, 1, usd), mustJournalOpIDForTest(t, "op-reversal-2"))
	if !HasCode(err, CodeReversalExceedsRemainder) {
		t.Fatalf("over-reversal error = %v, want code %s", err, CodeReversalExceedsRemainder)
	}
	assertReversalUnchanged(t, before, ledger)
	assertReversalState(t, ledger, ReversalStatusFullyReversed, 0, 1_000)
}

func TestReversalLedgerOverReversalKeepsState(t *testing.T) {
	ledger, usd := newReversalLedgerForTest(t, 1_000)

	if err := ledger.RecordReversal(mustPositiveAmountForTest(t, 600, usd), mustJournalOpIDForTest(t, "op-reversal-1")); err != nil {
		t.Fatalf("first RecordReversal: %v", err)
	}
	assertReversalState(t, ledger, ReversalStatusPartiallyReversed, 400, 1_000)

	before := *ledger
	err := ledger.RecordReversal(mustPositiveAmountForTest(t, 500, usd), mustJournalOpIDForTest(t, "op-reversal-2"))
	if !HasCode(err, CodeReversalExceedsRemainder) {
		t.Fatalf("over-reversal error = %v, want code %s", err, CodeReversalExceedsRemainder)
	}
	assertReversalUnchanged(t, before, ledger)
	assertReversalState(t, ledger, ReversalStatusPartiallyReversed, 400, 1_000)
}

func TestReversalLedgerFullRefundStaysCommitted(t *testing.T) {
	ledger, usd := newReversalLedgerForTest(t, 1_000)

	if err := ledger.RecordRefund(mustPositiveAmountForTest(t, 1_000, usd), mustJournalOpIDForTest(t, "op-refund-1")); err != nil {
		t.Fatalf("RecordRefund: %v", err)
	}
	// STATE_TRANSITIONS §3 / POSTING_MATRIX §4.2: a refund is a genuine new
	// event, not a reversal, so the original transaction stays COMMITTED.
	assertReversalState(t, ledger, ReversalStatusCommitted, 1_000, 0)

	before := *ledger
	err := ledger.RecordRefund(mustPositiveAmountForTest(t, 1, usd), mustJournalOpIDForTest(t, "op-refund-2"))
	if !HasCode(err, CodeRefundExceedsRemainder) {
		t.Fatalf("over-refund error = %v, want code %s", err, CodeRefundExceedsRemainder)
	}
	assertReversalUnchanged(t, before, ledger)
	assertReversalState(t, ledger, ReversalStatusCommitted, 1_000, 0)
}

func TestReversalLedgerMixedSequence(t *testing.T) {
	ledger, usd := newReversalLedgerForTest(t, 1_000)

	steps := []struct {
		name         string
		record       func() error
		wantStatus   ReversalStatus
		wantRevers   int64
		wantRefunded int64
	}{
		{
			name: "half reversal",
			record: func() error {
				return ledger.RecordReversal(mustPositiveAmountForTest(t, 500, usd), mustJournalOpIDForTest(t, "op-reversal-1"))
			},
			wantStatus:   ReversalStatusPartiallyReversed,
			wantRevers:   500,
			wantRefunded: 1_000,
		},
		{
			name: "full refund",
			record: func() error {
				return ledger.RecordRefund(mustPositiveAmountForTest(t, 1_000, usd), mustJournalOpIDForTest(t, "op-refund-1"))
			},
			wantStatus:   ReversalStatusPartiallyReversed,
			wantRevers:   500,
			wantRefunded: 0,
		},
		{
			name: "second half reversal",
			record: func() error {
				return ledger.RecordReversal(mustPositiveAmountForTest(t, 500, usd), mustJournalOpIDForTest(t, "op-reversal-2"))
			},
			wantStatus:   ReversalStatusFullyReversed,
			wantRevers:   0,
			wantRefunded: 0,
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			if err := step.record(); err != nil {
				t.Fatalf("%s: %v", step.name, err)
			}
			assertReversalState(t, ledger, step.wantStatus, step.wantRevers, step.wantRefunded)
		})
	}
}

func TestReversalLedgerRejectedAmounts(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	eur := mustCurrency(t, "EUR", 2)
	validOp := mustJournalOpIDForTest(t, "op-1")

	type recordFunc func(*ReversalLedger, PositiveAmount, JournalOperationId) error
	operations := []struct {
		name   string
		record recordFunc
	}{
		{name: "reversal", record: (*ReversalLedger).RecordReversal},
		{name: "refund", record: (*ReversalLedger).RecordRefund},
	}

	amounts := []struct {
		name   string
		amount PositiveAmount
		code   ErrorCode
		op     JournalOperationId
	}{
		{name: "zero amount", amount: PositiveAmount{}, code: CodeInvalidAmount, op: validOp},
		{
			name:   "negative amount",
			amount: PositiveAmount{money: Money{minorUnits: -100, currency: usd}},
			code:   CodeInvalidAmount,
			op:     validOp,
		},
		{
			name:   "invalid currency amount",
			amount: PositiveAmount{money: Money{minorUnits: 100, currency: Currency{}}},
			code:   CodeInvalidValue,
			op:     validOp,
		},
		{
			name:   "currency mismatch",
			amount: mustPositiveAmountForTest(t, 100, eur),
			code:   CodeCurrencyMismatch,
			op:     validOp,
		},
		{
			name:   "zero operation id",
			amount: mustPositiveAmountForTest(t, 100, usd),
			code:   CodeInvalidValue,
			op:     "",
		},
	}

	for _, operation := range operations {
		for _, tt := range amounts {
			t.Run(operation.name+"_"+tt.name, func(t *testing.T) {
				ledger, _ := newReversalLedgerForTest(t, 1_000)
				before := *ledger

				err := operation.record(ledger, tt.amount, tt.op)
				if !HasCode(err, tt.code) {
					t.Fatalf("error = %v, want code %s", err, tt.code)
				}
				assertReversalUnchanged(t, before, ledger)
				assertReversalState(t, ledger, ReversalStatusCommitted, 1_000, 1_000)
			})
		}
	}
}

func TestReversalLedgerLinks(t *testing.T) {
	ledger, usd := newReversalLedgerForTest(t, 1_000)
	reversal1 := mustJournalOpIDForTest(t, "op-reversal-1")
	refund1 := mustJournalOpIDForTest(t, "op-refund-1")
	reversal2 := mustJournalOpIDForTest(t, "op-reversal-2")

	if err := ledger.RecordReversal(mustPositiveAmountForTest(t, 200, usd), reversal1); err != nil {
		t.Fatalf("RecordReversal(1): %v", err)
	}
	if err := ledger.RecordRefund(mustPositiveAmountForTest(t, 300, usd), refund1); err != nil {
		t.Fatalf("RecordRefund: %v", err)
	}
	if err := ledger.RecordReversal(mustPositiveAmountForTest(t, 100, usd), reversal2); err != nil {
		t.Fatalf("RecordReversal(2): %v", err)
	}

	want := []RelatedOperationRef{
		{Kind: RelatedRefReversal, OperationID: reversal1},
		{Kind: RelatedRefRefund, OperationID: refund1},
		{Kind: RelatedRefReversal, OperationID: reversal2},
	}
	links := ledger.Links()
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("Links = %+v, want %+v", links, want)
	}

	// Links returns a copy: mutating it must not affect the ledger.
	links[0].Kind = RelatedRefOriginal
	if got := ledger.Links()[0].Kind; got != RelatedRefReversal {
		t.Errorf("Links returned an aliased slice; first link kind = %q", got)
	}
}
