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
}

// Parser recognizes and parses one bank's statement format.
type Parser interface {
	Bank() string
	Format() string
	Detect(filename string, data []byte) bool
	Parse(data []byte) (Statement, error)
}
