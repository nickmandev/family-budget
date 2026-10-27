package domain

// PositiveAmount is a Money that is strictly greater than zero. It is used
// wherever the domain requires a purchase, allocation, payment, or other
// genuinely positive amount.
//
// The zero value is invalid. Construct with NewPositiveAmount.
type PositiveAmount struct {
	money Money
}

// NewPositiveAmount validates that m is strictly positive.
func NewPositiveAmount(m Money) (PositiveAmount, error) {
	if err := m.currency.validate(); err != nil {
		return PositiveAmount{}, err
	}
	if !m.IsPositive() {
		return PositiveAmount{}, NewErrorf(CodeInvalidAmount, "positive amount must be greater than zero, got %d minor units", m.MinorUnits())
	}
	return PositiveAmount{money: m}, nil
}

// NewPositiveAmountFromMinorUnits builds a PositiveAmount from integer minor
// units and a currency, applying the same validation as NewMoney.
func NewPositiveAmountFromMinorUnits(minorUnits int64, currency Currency) (PositiveAmount, error) {
	m, err := NewMoney(minorUnits, currency)
	if err != nil {
		return PositiveAmount{}, err
	}
	return NewPositiveAmount(m)
}

// Money returns the underlying Money value.
func (p PositiveAmount) Money() Money {
	return p.money
}

// MinorUnits returns the amount in integer minor units.
func (p PositiveAmount) MinorUnits() int64 {
	return p.money.MinorUnits()
}

// Currency returns the amount's currency.
func (p PositiveAmount) Currency() Currency {
	return p.money.Currency()
}

// SignedMoneyDelta is a nonzero signed integer minor-unit change together with
// its currency, intended for journal postings (positive increases a balance,
// negative decreases it).
//
// Zero deltas are rejected at construction with INVALID_AMOUNT: a posting that
// changes nothing is a modelling error, and rejecting it keeps journal rows
// meaningful and auditable.
//
// The zero value is invalid. Construct with NewSignedMoneyDelta.
type SignedMoneyDelta struct {
	minorUnits int64
	currency   Currency
}

// NewSignedMoneyDelta validates the currency, the domain bound, and rejects a
// zero delta.
func NewSignedMoneyDelta(minorUnits int64, currency Currency) (SignedMoneyDelta, error) {
	m, err := NewMoney(minorUnits, currency)
	if err != nil {
		return SignedMoneyDelta{}, err
	}
	if m.IsZero() {
		return SignedMoneyDelta{}, NewErrorf(CodeInvalidAmount, "signed money delta must be nonzero")
	}
	return SignedMoneyDelta{minorUnits: m.MinorUnits(), currency: m.Currency()}, nil
}

// MinorUnits returns the signed delta in integer minor units.
func (d SignedMoneyDelta) MinorUnits() int64 {
	return d.minorUnits
}

// Currency returns the delta's currency.
func (d SignedMoneyDelta) Currency() Currency {
	return d.currency
}

// IsNegative reports whether the delta decreases a balance.
func (d SignedMoneyDelta) IsNegative() bool {
	return d.minorUnits < 0
}

// Money converts the delta to a Money value, validating it again so an
// invalid zero value is rejected instead of being applied.
func (d SignedMoneyDelta) Money() (Money, error) {
	return NewMoney(d.minorUnits, d.currency)
}

// ApplyTo returns m plus the delta, using Money's checked addition and
// currency validation. Overflow, out-of-range results, and currency mismatch
// are returned as errors.
func (d SignedMoneyDelta) ApplyTo(m Money) (Money, error) {
	delta, err := d.Money()
	if err != nil {
		return Money{}, err
	}
	return m.Add(delta)
}
