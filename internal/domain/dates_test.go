package domain

import (
	"testing"
	"time"
)

func TestNewEffectiveDate(t *testing.T) {
	tests := []struct {
		name     string
		year     int
		month    time.Month
		day      int
		want     string
		wantCode ErrorCode
	}{
		{name: "leap day", year: 2024, month: time.February, day: 29, want: "2024-02-29"},
		{name: "common year february", year: 2023, month: time.February, day: 28, want: "2023-02-28"},
		{name: "year 2000 leap", year: 2000, month: time.February, day: 29, want: "2000-02-29"},
		{name: "year 1900 not leap", year: 1900, month: time.February, day: 29, wantCode: CodeInvalidValue},
		{name: "february 30", year: 2024, month: time.February, day: 30, wantCode: CodeInvalidValue},
		{name: "april 31", year: 2024, month: time.April, day: 31, wantCode: CodeInvalidValue},
		{name: "day zero", year: 2024, month: time.January, day: 0, wantCode: CodeInvalidValue},
		{name: "day 32", year: 2024, month: time.January, day: 32, wantCode: CodeInvalidValue},
		{name: "month zero", year: 2024, month: time.Month(0), day: 1, wantCode: CodeInvalidValue},
		{name: "month 13", year: 2024, month: time.Month(13), day: 1, wantCode: CodeInvalidValue},
		{name: "year zero", year: 0, month: time.January, day: 1, wantCode: CodeInvalidValue},
		{name: "year too high", year: 10000, month: time.January, day: 1, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewEffectiveDate(tt.year, tt.month, tt.day)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewEffectiveDate(%d, %d, %d) error = %v, want code %s", tt.year, tt.month, tt.day, err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewEffectiveDate(%d, %d, %d) unexpected error: %v", tt.year, tt.month, tt.day, err)
			}
			if got.String() != tt.want {
				t.Errorf("String() = %q, want %q", got.String(), tt.want)
			}
			if got.Year() != tt.year || got.Month() != tt.month || got.Day() != tt.day {
				t.Errorf("accessors = %d-%d-%d, want %d-%d-%d", got.Year(), got.Month(), got.Day(), tt.year, tt.month, tt.day)
			}
		})
	}
}

func TestParseEffectiveDate(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		want     string
		wantCode ErrorCode
	}{
		{name: "valid leap day", value: "2024-02-29", want: "2024-02-29"},
		{name: "padded month and day", value: "2024-01-02", want: "2024-01-02"},
		{name: "empty", value: "", wantCode: CodeInvalidValue},
		{name: "unpadded month", value: "2024-2-09", wantCode: CodeInvalidValue},
		{name: "unpadded day", value: "2024-02-9", wantCode: CodeInvalidValue},
		{name: "wrong separator", value: "2024/02/09", wantCode: CodeInvalidValue},
		{name: "timestamp is not a date", value: "2024-02-29T00:00:00Z", wantCode: CodeInvalidValue},
		{name: "month 13", value: "2024-13-01", wantCode: CodeInvalidValue},
		{name: "february 30", value: "2024-02-30", wantCode: CodeInvalidValue},
		{name: "non numeric year", value: "20xx-02-09", wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEffectiveDate(tt.value)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("ParseEffectiveDate(%q) error = %v, want code %s", tt.value, err, tt.wantCode)
				}
				if !got.IsZero() {
					t.Fatalf("ParseEffectiveDate(%q) returned non-zero date on error: %v", tt.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseEffectiveDate(%q) unexpected error: %v", tt.value, err)
			}
			if got.String() != tt.want {
				t.Errorf("ParseEffectiveDate(%q).String() = %q, want %q", tt.value, got.String(), tt.want)
			}
			// Round-trip through the canonical string form.
			again, err := ParseEffectiveDate(got.String())
			if err != nil {
				t.Fatalf("round-trip ParseEffectiveDate(%q): %v", got.String(), err)
			}
			if again != got {
				t.Errorf("round-trip = %v, want %v", again, got)
			}
		})
	}
}

func TestEffectiveDateZeroValue(t *testing.T) {
	if !(EffectiveDate{}).IsZero() {
		t.Error("zero EffectiveDate must report IsZero")
	}
	date, err := NewEffectiveDate(2024, time.January, 1)
	if err != nil {
		t.Fatalf("NewEffectiveDate: %v", err)
	}
	if date.IsZero() {
		t.Error("constructed EffectiveDate must not report IsZero")
	}
}

func TestYearMonth(t *testing.T) {
	date, err := NewEffectiveDate(2024, time.February, 29)
	if err != nil {
		t.Fatalf("NewEffectiveDate: %v", err)
	}

	derived := YearMonthOf(date)
	if derived != date.YearMonth() {
		t.Errorf("YearMonthOf(date) = %v, want date.YearMonth() = %v", derived, date.YearMonth())
	}
	if derived.Year() != 2024 {
		t.Errorf("Year() = %d, want 2024", derived.Year())
	}
	if derived.Month() != time.February {
		t.Errorf("Month() = %d, want February", derived.Month())
	}
	if derived.String() != "2024-02" {
		t.Errorf("String() = %q, want %q", derived.String(), "2024-02")
	}
	if derived.IsZero() {
		t.Error("derived YearMonth must not report IsZero")
	}

	january, err := NewYearMonth(2024, time.January)
	if err != nil {
		t.Fatalf("NewYearMonth: %v", err)
	}
	if january.String() != "2024-01" {
		t.Errorf("String() = %q, want %q", january.String(), "2024-01")
	}

	if !(YearMonth{}).IsZero() {
		t.Error("zero YearMonth must report IsZero")
	}

	invalid := []struct {
		name  string
		year  int
		month time.Month
	}{
		{name: "year zero", year: 0, month: time.January},
		{name: "year too high", year: 10000, month: time.January},
		{name: "month zero", year: 2024, month: time.Month(0)},
		{name: "month 13", year: 2024, month: time.Month(13)},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewYearMonth(tt.year, tt.month); !HasCode(err, CodeInvalidValue) {
				t.Fatalf("NewYearMonth(%d, %d) error = %v, want INVALID_VALUE", tt.year, tt.month, err)
			}
		})
	}
}

func TestRecordedAtNormalizesToUTC(t *testing.T) {
	location := time.FixedZone("UTC+5", 5*60*60)
	input := time.Date(2026, time.October, 27, 12, 30, 15, 123_456_789, location)

	got := NewRecordedAt(input)
	if got.Time().Location() != time.UTC {
		t.Errorf("Time().Location() = %v, want UTC", got.Time().Location())
	}
	if !got.Time().Equal(input) {
		t.Errorf("Time() = %v, want the same instant as %v", got.Time(), input)
	}
	if got.IsZero() {
		t.Error("constructed RecordedAt must not report IsZero")
	}
	if got.String() != "2026-10-27T07:30:15.123456789Z" {
		t.Errorf("String() = %q, want %q", got.String(), "2026-10-27T07:30:15.123456789Z")
	}
	if !(RecordedAt{}).IsZero() {
		t.Error("zero RecordedAt must report IsZero")
	}
}
