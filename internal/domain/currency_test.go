package domain

import "testing"

func TestNewCurrency(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		exponent int
		wantCode ErrorCode
	}{
		{name: "usd", code: "USD", exponent: 2},
		{name: "jpy zero exponent", code: "JPY", exponent: 0},
		{name: "bhd three digits", code: "BHD", exponent: 3},
		{name: "max exponent", code: "XXX", exponent: 6},
		{name: "lowercase", code: "usd", exponent: 2, wantCode: CodeInvalidValue},
		{name: "mixed case", code: "Usd", exponent: 2, wantCode: CodeInvalidValue},
		{name: "too short", code: "US", exponent: 2, wantCode: CodeInvalidValue},
		{name: "too long", code: "USDD", exponent: 2, wantCode: CodeInvalidValue},
		{name: "digit", code: "US1", exponent: 2, wantCode: CodeInvalidValue},
		{name: "embedded space", code: "U S", exponent: 2, wantCode: CodeInvalidValue},
		{name: "empty", code: "", exponent: 2, wantCode: CodeInvalidValue},
		{name: "non ascii", code: "ÜSD", exponent: 2, wantCode: CodeInvalidValue},
		{name: "negative exponent", code: "USD", exponent: -1, wantCode: CodeInvalidValue},
		{name: "exponent too high", code: "USD", exponent: 7, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewCurrency(tt.code, tt.exponent)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewCurrency(%q, %d) error = %v, want code %s", tt.code, tt.exponent, err, tt.wantCode)
				}
				if !got.IsZero() {
					t.Fatalf("NewCurrency(%q, %d) returned non-zero currency on error: %+v", tt.code, tt.exponent, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewCurrency(%q, %d) unexpected error: %v", tt.code, tt.exponent, err)
			}
			if got.Code() != tt.code {
				t.Errorf("Code() = %q, want %q", got.Code(), tt.code)
			}
			if got.Exponent() != tt.exponent {
				t.Errorf("Exponent() = %d, want %d", got.Exponent(), tt.exponent)
			}
			if got.String() != tt.code {
				t.Errorf("String() = %q, want %q", got.String(), tt.code)
			}
		})
	}
}

func TestCurrencyEquality(t *testing.T) {
	usd := mustCurrency(t, "USD", 2)
	eur := mustCurrency(t, "EUR", 2)

	if usd.IsZero() {
		t.Error("USD must not be the zero currency")
	}
	if !(Currency{}).IsZero() {
		t.Error("zero value Currency must report IsZero")
	}
	if !usd.Equal(usd) {
		t.Error("USD must equal USD")
	}
	if usd.Equal(eur) {
		t.Error("USD must not equal EUR")
	}
	if usd.Equal(Currency{}) {
		t.Error("a valid currency must not equal the zero currency")
	}
	if (Currency{}).Equal(Currency{}) {
		t.Error("the zero currency must not equal itself for arithmetic purposes")
	}
}
