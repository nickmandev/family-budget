package domain

import (
	"errors"
	"testing"
)

func TestNewPositiveAmount(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)

	tests := []struct {
		name       string
		minorUnits int64
		currency   Currency
		wantCode   ErrorCode
	}{
		{name: "one minor unit", minorUnits: 1, currency: usd},
		{name: "typical", minorUnits: 12_345, currency: usd},
		{name: "max bound", minorUnits: MaxMinorUnits, currency: usd},
		{name: "zero", minorUnits: 0, currency: usd, wantCode: CodeInvalidAmount},
		{name: "negative", minorUnits: -1, currency: usd, wantCode: CodeInvalidAmount},
		{name: "above bound", minorUnits: MaxMinorUnits + 1, currency: usd, wantCode: CodeAmountOutOfRange},
		{name: "zero currency", minorUnits: 1, currency: Currency{}, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewPositiveAmountFromMinorUnits(tt.minorUnits, tt.currency)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewPositiveAmountFromMinorUnits(%d) error = %v, want code %s", tt.minorUnits, err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewPositiveAmountFromMinorUnits(%d) unexpected error: %v", tt.minorUnits, err)
			}
			if got.MinorUnits() != tt.minorUnits {
				t.Errorf("MinorUnits() = %d, want %d", got.MinorUnits(), tt.minorUnits)
			}
			if !got.Currency().Equal(tt.currency) {
				t.Errorf("Currency() = %s, want %s", got.Currency(), tt.currency)
			}
			if got.Money().MinorUnits() != tt.minorUnits {
				t.Errorf("Money().MinorUnits() = %d, want %d", got.Money().MinorUnits(), tt.minorUnits)
			}
		})
	}
}

func TestNewPositiveAmountRejectsInvalidMoney(t *testing.T) {
	// The zero Money value has no currency and must be rejected.
	if _, err := NewPositiveAmount(Money{}); !HasCode(err, CodeInvalidValue) {
		t.Errorf("NewPositiveAmount(zero Money) error = %v, want INVALID_VALUE", err)
	}
	if _, err := NewPositiveAmount(Money{minorUnits: 1}); !HasCode(err, CodeInvalidValue) {
		t.Errorf("NewPositiveAmount(money without currency) error = %v, want INVALID_VALUE", err)
	}
}

func TestNewSignedMoneyDelta(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)

	tests := []struct {
		name       string
		minorUnits int64
		currency   Currency
		wantCode   ErrorCode
	}{
		{name: "positive", minorUnits: 5, currency: usd},
		{name: "negative", minorUnits: -5, currency: usd},
		{name: "max bound", minorUnits: MaxMinorUnits, currency: usd},
		{name: "min bound", minorUnits: -MaxMinorUnits, currency: usd},
		{name: "zero is rejected", minorUnits: 0, currency: usd, wantCode: CodeInvalidAmount},
		{name: "above bound", minorUnits: MaxMinorUnits + 1, currency: usd, wantCode: CodeAmountOutOfRange},
		{name: "zero currency", minorUnits: 1, currency: Currency{}, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewSignedMoneyDelta(tt.minorUnits, tt.currency)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewSignedMoneyDelta(%d) error = %v, want code %s", tt.minorUnits, err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewSignedMoneyDelta(%d) unexpected error: %v", tt.minorUnits, err)
			}
			if got.MinorUnits() != tt.minorUnits {
				t.Errorf("MinorUnits() = %d, want %d", got.MinorUnits(), tt.minorUnits)
			}
			if got.IsNegative() != (tt.minorUnits < 0) {
				t.Errorf("IsNegative() = %v, want %v", got.IsNegative(), tt.minorUnits < 0)
			}
			money, err := got.Money()
			if err != nil {
				t.Fatalf("Money() unexpected error: %v", err)
			}
			if money.MinorUnits() != tt.minorUnits {
				t.Errorf("Money().MinorUnits() = %d, want %d", money.MinorUnits(), tt.minorUnits)
			}
		})
	}
}

func TestSignedMoneyDeltaApplyTo(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)

	tests := []struct {
		name     string
		base     int64
		delta    int64
		want     int64
		wantCode ErrorCode
	}{
		{name: "increase", base: 100, delta: 30, want: 130},
		{name: "decrease", base: 100, delta: -30, want: 70},
		{name: "decrease to zero", base: 100, delta: -100, want: 0},
		{name: "increase past max bound", base: 100, delta: MaxMinorUnits, wantCode: CodeAmountOutOfRange},
		{name: "decrease past min bound", base: -100, delta: -MaxMinorUnits, wantCode: CodeAmountOutOfRange},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delta, err := NewSignedMoneyDelta(tt.delta, usd)
			if err != nil {
				t.Fatalf("NewSignedMoneyDelta(%d) unexpected error: %v", tt.delta, err)
			}
			got, err := delta.ApplyTo(mustMoney(t, tt.base, usd))
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("ApplyTo(%d) error = %v, want code %s", tt.base, err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplyTo(%d) unexpected error: %v", tt.base, err)
			}
			if got.MinorUnits() != tt.want {
				t.Errorf("ApplyTo(%d) = %d, want %d", tt.base, got.MinorUnits(), tt.want)
			}
		})
	}
}

func TestSignedMoneyDeltaApplyToCurrencyMismatch(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	eur := mustCurrency(t, "EUR", 2)

	delta, err := NewSignedMoneyDelta(-500, eur)
	if err != nil {
		t.Fatalf("NewSignedMoneyDelta: %v", err)
	}
	if _, err := delta.ApplyTo(mustMoney(t, 100, usd)); !HasCode(err, CodeCurrencyMismatch) {
		t.Fatalf("ApplyTo across currencies error = %v, want CURRENCY_MISMATCH", err)
	}
}

func TestSignedMoneyDeltaZeroValueIsRejected(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	_, err := (SignedMoneyDelta{}).ApplyTo(mustMoney(t, 100, usd))
	if !HasCode(err, CodeInvalidValue) {
		t.Fatalf("zero SignedMoneyDelta.ApplyTo error = %v, want INVALID_VALUE", err)
	}
	if _, err := (SignedMoneyDelta{}).Money(); !HasCode(err, CodeInvalidValue) {
		t.Fatalf("zero SignedMoneyDelta.Money error = %v, want INVALID_VALUE", err)
	}
}

func TestPositiveAmountRejectsZeroWithSentinel(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	_, err := NewPositiveAmountFromMinorUnits(0, usd)
	if !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("errors.Is(err, ErrInvalidAmount) = false for %v", err)
	}
}
