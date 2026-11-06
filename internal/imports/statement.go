// Package imports parses bank statements into a normalized, stateless preview.
package imports

import "time"

// Direction is the sign-independent movement direction of a statement entry.
type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

// Entry is a normalized bank transaction. AmountMinor is always positive.
type Entry struct {
	// RowIndex is the 0-based index of the source row in the statement's raw
	// row sequence (including skipped rows). It shares its numbering with
	// ParseFailure so parser and application failures stay comparable.
	RowIndex          int
	ExternalID        string
	BookedDate        time.Time
	ValueDate         time.Time
	Direction         Direction
	AmountMinor       int64
	Currency          string
	Merchant          string
	Description       string
	Reference         string
	TransactionCode   string
	BalanceAfterMinor *int64
}

// ParseFailure records one rejected raw row. Reason is safe and value-free.
// The detail fields contain only the row's own content and are returned to
// the authenticated household in the HTTP response so it can be recreated;
// they are never persisted or logged. RowIndex is the 0-based index in the raw
// row sequence of the source statement (for Fibank, the SYTY_BAL_CUST_STMT
// sequence, including skipped rows), so parser failures and application
// validation failures share one numbering.
type ParseFailure struct {
	RowIndex        int
	Reason          string
	ExternalID      *string
	BookedDate      *time.Time
	ValueDate       *time.Time
	Direction       Direction
	AmountMinor     *int64
	Currency        string
	Merchant        string
	Description     string
	Reference       string
	TransactionCode string
}

// Statement contains normalized account metadata and transactions only. Raw
// customer records from statement files are deliberately not represented.
type Statement struct {
	Bank                string
	Format              string
	IBAN                string
	Currency            string
	PeriodFrom          *time.Time
	PeriodTo            *time.Time
	OpeningBalanceMinor *int64
	ClosingBalanceMinor *int64
	Entries             []Entry
	SkippedRows         int
	// Failures are rows the parser rejected. They never abort the statement;
	// valid rows still parse.
	Failures []ParseFailure
}

// Parser recognizes and parses one bank's statement format.
type Parser interface {
	Bank() string
	Format() string
	Detect(filename string, data []byte) bool
	Parse(data []byte) (Statement, error)
}
