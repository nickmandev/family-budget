package domain

import (
	"errors"
	"math"
	"testing"
)

func mustCurrency(t *testing.T, code string, exponent int) Currency {
	t.Helper()
	currency, err := NewCurrency(code, exponent)
	if err != nil {
		t.Fatalf("NewCurrency(%q, %d): %v", code, exponent, err)
	}
	return currency
}

func mustMoney(t *testing.T, minorUnits int64, currency Currency) Money {
	t.Helper()
	money, err := NewMoney(minorUnits, currency)
	if err != nil {
		t.Fatalf("NewMoney(%d, %s): %v", minorUnits, currency.Code(), err)
	}
	return money
}

func TestNewMoney(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)

	tests := []struct {
		name       string
		minorUnits int64
		currency   Currency
		wantCode   ErrorCode
	}{
		{name: "zero", minorUnits: 0, currency: usd},
		{name: "positive", minorUnits: 12_345, currency: usd},
		{name: "negative", minorUnits: -12_345, currency: usd},
		{name: "max bound", minorUnits: MaxMinorUnits, currency: usd},
		{name: "min bound", minorUnits: -MaxMinorUnits, currency: usd},
		{name: "above max bound", minorUnits: MaxMinorUnits + 1, currency: usd, wantCode: CodeAmountOutOfRange},
		{name: "below min bound", minorUnits: -MaxMinorUnits - 1, currency: usd, wantCode: CodeAmountOutOfRange},
		{name: "max int64", minorUnits: math.MaxInt64, currency: usd, wantCode: CodeAmountOutOfRange},
		{name: "min int64", minorUnits: math.MinInt64, currency: usd, wantCode: CodeAmountOutOfRange},
		{name: "zero currency", minorUnits: 1, currency: Currency{}, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewMoney(tt.minorUnits, tt.currency)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewMoney(%d) error = %v, want code %s", tt.minorUnits, err, tt.wantCode)
				}
				if !got.IsZero() || got.MinorUnits() != 0 {
					t.Fatalf("NewMoney(%d) returned non-zero money on error: %+v", tt.minorUnits, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewMoney(%d) unexpected error: %v", tt.minorUnits, err)
			}
			if got.MinorUnits() != tt.minorUnits {
				t.Errorf("MinorUnits() = %d, want %d", got.MinorUnits(), tt.minorUnits)
			}
			if !got.Currency().Equal(tt.currency) {
				t.Errorf("Currency() = %s, want %s", got.Currency(), tt.currency)
			}
		})
	}
}

func TestZeroMoney(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	zero, err := ZeroMoney(usd)
	if err != nil {
		t.Fatalf("ZeroMoney(USD): %v", err)
	}
	if !zero.IsZero() {
		t.Errorf("ZeroMoney(USD).IsZero() = false, want true")
	}
	if _, err := ZeroMoney(Currency{}); !HasCode(err, CodeInvalidValue) {
		t.Errorf("ZeroMoney(zero currency) error = %v, want INVALID_VALUE", err)
	}
}

func TestMoneyAddSub(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)

	tests := []struct {
		name     string
		op       string
		a        int64
		b        int64
		want     int64
		wantCode ErrorCode
	}{
		{name: "add positive", op: "add", a: 100, b: 250, want: 350},
		{name: "add negative", op: "add", a: 100, b: -250, want: -150},
		{name: "add zero", op: "add", a: 0, b: 0, want: 0},
		{name: "add at max bound", op: "add", a: MaxMinorUnits, b: 0, want: MaxMinorUnits},
		{name: "add past max bound", op: "add", a: MaxMinorUnits, b: 1, wantCode: CodeAmountOutOfRange},
		{name: "add past min bound", op: "add", a: -MaxMinorUnits, b: -1, wantCode: CodeAmountOutOfRange},
		{name: "sub positive", op: "sub", a: 250, b: 100, want: 150},
		{name: "sub to negative", op: "sub", a: 100, b: 250, want: -150},
		{name: "sub at min bound", op: "sub", a: -MaxMinorUnits, b: 0, want: -MaxMinorUnits},
		{name: "sub past min bound", op: "sub", a: -MaxMinorUnits, b: 1, wantCode: CodeAmountOutOfRange},
		{name: "sub past max bound", op: "sub", a: MaxMinorUnits, b: -1, wantCode: CodeAmountOutOfRange},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := mustMoney(t, tt.a, usd)
			right := mustMoney(t, tt.b, usd)

			var got Money
			var err error
			switch tt.op {
			case "add":
				got, err = left.Add(right)
			case "sub":
				got, err = left.Sub(right)
			default:
				t.Fatalf("unknown op %q", tt.op)
			}

			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("%s(%d, %d) error = %v, want code %s", tt.op, tt.a, tt.b, err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s(%d, %d) unexpected error: %v", tt.op, tt.a, tt.b, err)
			}
			if got.MinorUnits() != tt.want {
				t.Errorf("%s(%d, %d) = %d, want %d", tt.op, tt.a, tt.b, got.MinorUnits(), tt.want)
			}
		})
	}
}

func TestMoneyArithmeticRejectsInt64Overflow(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)

	// The public constructors reject values above MaxMinorUnits, so build
	// extreme values directly inside the package to exercise the int64
	// overflow guard itself.
	maxInt64Money := Money{minorUnits: math.MaxInt64, currency: usd}
	minInt64Money := Money{minorUnits: math.MinInt64, currency: usd}
	one := mustMoney(t, 1, usd)
	minusOne := mustMoney(t, -1, usd)

	tests := []struct {
		name string
		call func() (Money, error)
	}{
		{name: "add overflows", call: func() (Money, error) { return maxInt64Money.Add(one) }},
		{name: "sub overflows", call: func() (Money, error) { return minInt64Money.Sub(one) }},
		{name: "sub negative overflows", call: func() (Money, error) { return maxInt64Money.Sub(minusOne) }},
		{name: "neg overflows", call: func() (Money, error) { return minInt64Money.Neg() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.call()
			if !HasCode(err, CodeMoneyOverflow) {
				t.Fatalf("error = %v, want MONEY_OVERFLOW", err)
			}
			if !got.IsZero() {
				t.Fatalf("overflow returned non-zero money: %+v", got)
			}
		})
	}
}

func TestMoneyCurrencyMismatch(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	eur := mustCurrency(t, "EUR", 2)
	// Same code but inconsistent minor-unit metadata must never be mixed.
	usdThreeDigits := mustCurrency(t, "USD", 3)

	usdMoney := mustMoney(t, 100, usd)
	eurMoney := mustMoney(t, 100, eur)
	usdThreeMoney := mustMoney(t, 100, usdThreeDigits)

	tests := []struct {
		name string
		call func() error
	}{
		{name: "add different code", call: func() error { _, err := usdMoney.Add(eurMoney); return err }},
		{name: "sub different code", call: func() error { _, err := usdMoney.Sub(eurMoney); return err }},
		{name: "compare different code", call: func() error { _, err := usdMoney.Compare(eurMoney); return err }},
		{name: "add inconsistent exponent", call: func() error { _, err := usdMoney.Add(usdThreeMoney); return err }},
		{name: "sub inconsistent exponent", call: func() error { _, err := usdMoney.Sub(usdThreeMoney); return err }},
		{name: "compare inconsistent exponent", call: func() error { _, err := usdMoney.Compare(usdThreeMoney); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !HasCode(err, CodeCurrencyMismatch) {
				t.Fatalf("error = %v, want CURRENCY_MISMATCH", err)
			}
		})
	}
}

func TestMoneyCompare(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)

	tests := []struct {
		name string
		a    int64
		b    int64
		want int
	}{
		{name: "less", a: -5, b: 5, want: -1},
		{name: "greater", a: 5, b: -5, want: 1},
		{name: "equal", a: 7, b: 7, want: 0},
		{name: "equal negative", a: -7, b: -7, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mustMoney(t, tt.a, usd).Compare(mustMoney(t, tt.b, usd))
			if err != nil {
				t.Fatalf("Compare(%d, %d) unexpected error: %v", tt.a, tt.b, err)
			}
			if got != tt.want {
				t.Errorf("Compare(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestMoneyNeg(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)

	tests := []struct {
		name       string
		minorUnits int64
		want       int64
	}{
		{name: "positive", minorUnits: 100, want: -100},
		{name: "negative", minorUnits: -100, want: 100},
		{name: "zero", minorUnits: 0, want: 0},
		{name: "max bound", minorUnits: MaxMinorUnits, want: -MaxMinorUnits},
		{name: "min bound", minorUnits: -MaxMinorUnits, want: MaxMinorUnits},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mustMoney(t, tt.minorUnits, usd).Neg()
			if err != nil {
				t.Fatalf("Neg(%d) unexpected error: %v", tt.minorUnits, err)
			}
			if got.MinorUnits() != tt.want {
				t.Errorf("Neg(%d) = %d, want %d", tt.minorUnits, got.MinorUnits(), tt.want)
			}
		})
	}
}

func TestMoneyPredicates(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)

	tests := []struct {
		name       string
		minorUnits int64
		isZero     bool
		isNegative bool
		isPositive bool
	}{
		{name: "negative", minorUnits: -1, isNegative: true},
		{name: "zero", minorUnits: 0, isZero: true},
		{name: "positive", minorUnits: 1, isPositive: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			money := mustMoney(t, tt.minorUnits, usd)
			if money.IsZero() != tt.isZero {
				t.Errorf("IsZero() = %v, want %v", money.IsZero(), tt.isZero)
			}
			if money.IsNegative() != tt.isNegative {
				t.Errorf("IsNegative() = %v, want %v", money.IsNegative(), tt.isNegative)
			}
			if money.IsPositive() != tt.isPositive {
				t.Errorf("IsPositive() = %v, want %v", money.IsPositive(), tt.isPositive)
			}
		})
	}
}

func TestMoneyAddErrorIsMatchable(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	_, err := mustMoney(t, MaxMinorUnits, usd).Add(mustMoney(t, 1, usd))
	if !errors.Is(err, ErrAmountOutOfRange) {
		t.Fatalf("errors.Is(err, ErrAmountOutOfRange) = false for %v", err)
	}
}
