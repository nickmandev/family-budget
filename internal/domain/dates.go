package domain

import (
	"fmt"
	"time"
)

const (
	minCalendarYear = 1
	maxCalendarYear = 9999
)

// EffectiveDate is the household-local calendar date used for transaction
// reporting. Per IMPLEMENTATION_DECISIONS.md Q7 it is a calendar fact
// (year, month, day) interpreted in the household timezone and is never
// derived from a UTC timestamp.
//
// The zero value is invalid. Construct with NewEffectiveDate or
// ParseEffectiveDate.
type EffectiveDate struct {
	year  int
	month time.Month
	day   int
}

// NewEffectiveDate validates and builds an EffectiveDate. The date must exist
// on the proleptic Gregorian calendar (for example 2023-02-29 is rejected).
func NewEffectiveDate(year int, month time.Month, day int) (EffectiveDate, error) {
	if year < minCalendarYear || year > maxCalendarYear {
		return EffectiveDate{}, NewErrorf(CodeInvalidValue, "year %d is outside the supported range %d..%d", year, minCalendarYear, maxCalendarYear)
	}
	if month < time.January || month > time.December {
		return EffectiveDate{}, NewErrorf(CodeInvalidValue, "month %d is not a valid calendar month", month)
	}
	if day < 1 || day > daysInMonth(year, month) {
		return EffectiveDate{}, NewErrorf(CodeInvalidValue, "%04d-%02d has no day %d", year, month, day)
	}
	return EffectiveDate{year: year, month: month, day: day}, nil
}

// ParseEffectiveDate parses a strict "YYYY-MM-DD" calendar date. The value is
// never interpreted as a timestamp, so timezone conversion cannot shift it.
func ParseEffectiveDate(value string) (EffectiveDate, error) {
	const layoutLength = len("2006-01-02")
	if len(value) != layoutLength || value[4] != '-' || value[7] != '-' {
		return EffectiveDate{}, NewErrorf(CodeInvalidValue, "effective date %q must use YYYY-MM-DD", value)
	}
	year, ok := parseDigits(value[0:4])
	if !ok {
		return EffectiveDate{}, NewErrorf(CodeInvalidValue, "effective date %q has a non-numeric year", value)
	}
	month, ok := parseDigits(value[5:7])
	if !ok {
		return EffectiveDate{}, NewErrorf(CodeInvalidValue, "effective date %q has a non-numeric month", value)
	}
	day, ok := parseDigits(value[8:10])
	if !ok {
		return EffectiveDate{}, NewErrorf(CodeInvalidValue, "effective date %q has a non-numeric day", value)
	}
	return NewEffectiveDate(year, time.Month(month), day)
}

// Year returns the calendar year.
func (d EffectiveDate) Year() int {
	return d.year
}

// Month returns the calendar month.
func (d EffectiveDate) Month() time.Month {
	return d.month
}

// Day returns the day of the month.
func (d EffectiveDate) Day() int {
	return d.day
}

// IsZero reports whether the date is the invalid zero value.
func (d EffectiveDate) IsZero() bool {
	return d.year == 0 || d.month == 0 || d.day == 0
}

// String formats the date as "YYYY-MM-DD".
func (d EffectiveDate) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.year, d.month, d.day)
}

// YearMonth returns the budget/reporting period that contains the date.
func (d EffectiveDate) YearMonth() YearMonth {
	return YearMonthOf(d)
}

// YearMonth is a calendar budget/reporting period. Its month is interpreted in
// the household timezone.
//
// The zero value is invalid. Construct with NewYearMonth or YearMonthOf.
type YearMonth struct {
	year  int
	month time.Month
}

// NewYearMonth validates and builds a YearMonth.
func NewYearMonth(year int, month time.Month) (YearMonth, error) {
	if year < minCalendarYear || year > maxCalendarYear {
		return YearMonth{}, NewErrorf(CodeInvalidValue, "year %d is outside the supported range %d..%d", year, minCalendarYear, maxCalendarYear)
	}
	if month < time.January || month > time.December {
		return YearMonth{}, NewErrorf(CodeInvalidValue, "month %d is not a valid calendar month", month)
	}
	return YearMonth{year: year, month: month}, nil
}

// YearMonthOf derives the reporting period of an effective date.
func YearMonthOf(date EffectiveDate) YearMonth {
	return YearMonth{year: date.year, month: date.month}
}

// Year returns the calendar year.
func (ym YearMonth) Year() int {
	return ym.year
}

// Month returns the calendar month.
func (ym YearMonth) Month() time.Month {
	return ym.month
}

// IsZero reports whether the period is the invalid zero value.
func (ym YearMonth) IsZero() bool {
	return ym.year == 0 || ym.month == 0
}

// String formats the period as "YYYY-MM".
func (ym YearMonth) String() string {
	return fmt.Sprintf("%04d-%02d", ym.year, ym.month)
}

// RecordedAt is the audit instant at which an operation was received or
// committed. Unlike EffectiveDate it is a global ordering fact and is always
// stored normalized to UTC.
type RecordedAt struct {
	instant time.Time
}

// NewRecordedAt normalizes instant to UTC. The zero time is retained as the
// zero value (meaning "unset").
func NewRecordedAt(instant time.Time) RecordedAt {
	return RecordedAt{instant: instant.UTC()}
}

// Time returns the UTC instant.
func (r RecordedAt) Time() time.Time {
	return r.instant
}

// IsZero reports whether the instant is unset.
func (r RecordedAt) IsZero() bool {
	return r.instant.IsZero()
}

// String formats the instant as RFC 3339 with nanosecond precision in UTC.
func (r RecordedAt) String() string {
	return r.instant.Format(time.RFC3339Nano)
}

// daysInMonth returns the number of days in the given month. Day zero of the
// following month normalizes to the last day of this month.
func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// parseDigits parses a fixed-width run of ASCII decimal digits.
func parseDigits(s string) (int, bool) {
	value := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		value = value*10 + int(s[i]-'0')
	}
	return value, true
}
