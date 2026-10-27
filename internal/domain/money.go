package domain

import "math"

// MaxMinorUnits is the largest absolute amount, in minor units, that a Money
// value may hold: 10^15. Together with the checked arithmetic below it keeps
// every supported amount far away from int64 overflow while remaining large
// enough for any realistic household budget (for USD that is ten trillion
// dollars).
const MaxMinorUnits int64 = 1_000_000_000_000_000

// Money is a signed amount in integer minor units together with its currency
// identity. It never uses floating point.
//
// The zero value is invalid. Construct Money values with NewMoney (or
// ZeroMoney) so the domain bound and currency are validated.
type Money struct {
	minorUnits int64
	currency   Currency
}

// NewMoney validates the currency and the domain bound, then builds a Money.
// Amounts whose absolute value exceeds MaxMinorUnits are rejected with
// AMOUNT_OUT_OF_RANGE.
func NewMoney(minorUnits int64, currency Currency) (Money, error) {
	if err := currency.validate(); err != nil {
		return Money{}, err
	}
	if minorUnits > MaxMinorUnits || minorUnits < -MaxMinorUnits {
		return Money{}, NewErrorf(CodeAmountOutOfRange, "money amount %d is outside the domain bound of ±%d minor units", minorUnits, MaxMinorUnits)
	}
	return Money{minorUnits: minorUnits, currency: currency}, nil
}

// ZeroMoney returns a zero Money in the given currency.
func ZeroMoney(currency Currency) (Money, error) {
	return NewMoney(0, currency)
}

// Currency returns the money's currency.
func (m Money) Currency() Currency {
	return m.currency
}

// MinorUnits returns the signed amount in integer minor units.
func (m Money) MinorUnits() int64 {
	return m.minorUnits
}

// IsZero reports whether the amount is exactly zero.
func (m Money) IsZero() bool {
	return m.minorUnits == 0
}

// IsNegative reports whether the amount is strictly below zero.
func (m Money) IsNegative() bool {
	return m.minorUnits < 0
}

// IsPositive reports whether the amount is strictly above zero.
func (m Money) IsPositive() bool {
	return m.minorUnits > 0
}

// Add returns m + other. Both operands must share the same currency and the
// result must stay within the domain bound; overflow or an out-of-range result
// is returned as an error and never wraps silently.
func (m Money) Add(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	sum, err := checkedAdd(m.minorUnits, other.minorUnits)
	if err != nil {
		return Money{}, err
	}
	return NewMoney(sum, m.currency)
}

// Sub returns m - other. Both operands must share the same currency and the
// result must stay within the domain bound; overflow or an out-of-range result
// is returned as an error and never wraps silently.
func (m Money) Sub(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	difference, err := checkedSub(m.minorUnits, other.minorUnits)
	if err != nil {
		return Money{}, err
	}
	return NewMoney(difference, m.currency)
}

// Neg returns -m. Negating math.MinInt64 would overflow and is rejected.
func (m Money) Neg() (Money, error) {
	if m.minorUnits == math.MinInt64 {
		return Money{}, NewErrorf(CodeMoneyOverflow, "cannot negate %d", m.minorUnits)
	}
	return NewMoney(-m.minorUnits, m.currency)
}

// Compare compares m with other, which must share the same currency. It
// returns -1 if m < other, 0 if m == other, and +1 if m > other.
func (m Money) Compare(other Money) (int, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return 0, err
	}
	switch {
	case m.minorUnits < other.minorUnits:
		return -1, nil
	case m.minorUnits > other.minorUnits:
		return 1, nil
	default:
		return 0, nil
	}
}

// requireSameCurrency enforces currency identity for arithmetic. Equality is
// code-based per the domain contract; arithmetic additionally requires that
// both values agree on the minor-unit exponent so inconsistent metadata can
// never silently mix units.
func (m Money) requireSameCurrency(other Money) error {
	if err := m.currency.validate(); err != nil {
		return err
	}
	if err := other.currency.validate(); err != nil {
		return err
	}
	if !m.currency.Equal(other.currency) || m.currency.Exponent() != other.currency.Exponent() {
		return NewErrorf(CodeCurrencyMismatch, "currency mismatch: %s (exponent %d) vs %s (exponent %d)", m.currency.Code(), m.currency.Exponent(), other.currency.Code(), other.currency.Exponent())
	}
	return nil
}

// checkedAdd adds two int64 values, returning MONEY_OVERFLOW instead of
// wrapping.
func checkedAdd(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, NewErrorf(CodeMoneyOverflow, "integer overflow adding %d and %d", a, b)
	}
	return a + b, nil
}

// checkedSub subtracts b from a, returning MONEY_OVERFLOW instead of wrapping.
func checkedSub(a, b int64) (int64, error) {
	if (b > 0 && a < math.MinInt64+b) || (b < 0 && a > math.MaxInt64+b) {
		return 0, NewErrorf(CodeMoneyOverflow, "integer overflow subtracting %d from %d", b, a)
	}
	return a - b, nil
}
