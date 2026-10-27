package domain

const (
	currencyCodeLength  = 3
	minCurrencyExponent = 0
	maxCurrencyExponent = 6
)

// Currency identifies the household currency and the number of decimal digits
// in its minor unit. For example USD has exponent 2 (cents) and JPY has
// exponent 0 (yen).
//
// The zero value is invalid. Construct currencies with NewCurrency.
type Currency struct {
	code     string
	exponent uint8
}

// NewCurrency validates and builds a Currency. The code must be exactly three
// uppercase ASCII letters (ISO-style) and the exponent must be in [0, 6].
func NewCurrency(code string, exponent int) (Currency, error) {
	if !isUpperAlpha3(code) {
		return Currency{}, NewErrorf(CodeInvalidValue, "currency code %q must be three uppercase ASCII letters", code)
	}
	if exponent < minCurrencyExponent || exponent > maxCurrencyExponent {
		return Currency{}, NewErrorf(CodeInvalidValue, "currency exponent %d must be between %d and %d", exponent, minCurrencyExponent, maxCurrencyExponent)
	}
	return Currency{code: code, exponent: uint8(exponent)}, nil
}

// Code returns the ISO-style currency code, for example "USD".
func (c Currency) Code() string {
	return c.code
}

// Exponent returns the number of decimal digits in the currency's minor unit.
func (c Currency) Exponent() int {
	return int(c.exponent)
}

// String returns the currency code.
func (c Currency) String() string {
	return c.code
}

// Equal reports whether the two currencies share the same code. Per
// DOMAIN_MODEL.md §4 currency equality is defined by code; valid currency
// metadata is expected to be globally consistent for a given code.
func (c Currency) Equal(other Currency) bool {
	return c.code != "" && c.code == other.code
}

// IsZero reports whether the currency is the invalid zero value.
func (c Currency) IsZero() bool {
	return c.code == ""
}

// validate checks that the currency was built through NewCurrency (or is an
// equivalent valid value).
func (c Currency) validate() error {
	if !isUpperAlpha3(c.code) {
		return NewErrorf(CodeInvalidValue, "currency code %q must be three uppercase ASCII letters", c.code)
	}
	if c.exponent > maxCurrencyExponent {
		return NewErrorf(CodeInvalidValue, "currency exponent %d must be between %d and %d", c.exponent, minCurrencyExponent, maxCurrencyExponent)
	}
	return nil
}

func isUpperAlpha3(s string) bool {
	if len(s) != currencyCodeLength {
		return false
	}
	for i := 0; i < currencyCodeLength; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}
