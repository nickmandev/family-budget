package imports

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxFibankEntries = 20000

var numericContragent = regexp.MustCompile(`^[A-Z]{3} [0-9]+$`)

type fibankParser struct{}

type fibankDocument struct {
	XMLName    xml.Name       `xml:""`
	Accounts   fibankAccounts `xml:"ACC"`
	PeriodFrom string         `xml:"RP_FROM_DATE"`
	PeriodTo   string         `xml:"RP_TO_DATE"`
}

type fibankAccounts struct {
	Accounts []fibankAccount `xml:"SYTY_BAL_CUST_ACC"`
}

type fibankAccount struct {
	IBAN           string     `xml:"IBAN"`
	Currency       string     `xml:"CCY"`
	OpeningBalance string     `xml:"ACY_OPENING_BAL"`
	CurrentBalance string     `xml:"ACY_CURR_BALANCE"`
	StatementRows  fibankRows `xml:"STMT"`
}

type fibankRows struct {
	Rows []fibankRow `xml:"SYTY_BAL_CUST_STMT"`
}

type fibankRow struct {
	Type            string `xml:"TRN_TYPE"`
	EntryID         string `xml:"AC_ENTRY_SR_NO"`
	Reference       string `xml:"REFERENCE"`
	ValueDate       string `xml:"VALUE_DT"`
	BookedDate      string `xml:"DATE_TIME"`
	Amount          string `xml:"AMOUNT"`
	Direction       string `xml:"DTKT"`
	TransactionName string `xml:"TRNAME"`
	Contragent      string `xml:"CONTRAGENT"`
	TransactionCode string `xml:"TRN_CODE"`
	RemI            string `xml:"REM_I"`
	RemII           string `xml:"REM_II"`
	RemIII          string `xml:"REM_III"`
	Balance         string `xml:"AC_BAL"`
}

// NewFibankParser returns the parser for Fibank's proprietary XML export.
func NewFibankParser() Parser { return fibankParser{} }

func (fibankParser) Bank() string   { return "fibank" }
func (fibankParser) Format() string { return "xml" }

func (fibankParser) Detect(_ string, data []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if start, ok := token.(xml.StartElement); ok {
			return start.Name.Local == "SYTY_BAL_CUST_MAIN"
		}
	}
}

func (fibankParser) Parse(data []byte) (Statement, error) {
	if bytes.Contains(data, []byte("<!DOCTYPE")) || bytes.Contains(data, []byte("<!ENTITY")) {
		return Statement{}, ErrInvalidStatement
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	var document fibankDocument
	depth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Statement{}, ErrInvalidStatement
		}
		switch token.(type) {
		case xml.StartElement:
			depth++
			if depth > 64 {
				return Statement{}, ErrInvalidStatement
			}
		case xml.EndElement:
			depth--
		}
	}
	if err := xml.Unmarshal(data, &document); err != nil || document.XMLName.Local != "SYTY_BAL_CUST_MAIN" || len(document.Accounts.Accounts) != 1 {
		return Statement{}, ErrInvalidStatement
	}
	account := document.Accounts.Accounts[0]
	statement := Statement{
		Bank:     "fibank",
		Format:   "xml",
		IBAN:     strings.TrimSpace(account.IBAN),
		Currency: strings.TrimSpace(account.Currency),
		Entries:  make([]Entry, 0),
	}
	if statement.Currency == "" {
		return Statement{}, ErrInvalidStatement
	}
	if value := strings.TrimSpace(document.PeriodFrom); value != "" {
		parsed, ok := parseFibankDate(value)
		if !ok {
			return Statement{}, ErrInvalidStatement
		}
		statement.PeriodFrom = &parsed
	}
	if value := strings.TrimSpace(document.PeriodTo); value != "" {
		parsed, ok := parseFibankDate(value)
		if !ok {
			return Statement{}, ErrInvalidStatement
		}
		statement.PeriodTo = &parsed
	}
	if value := strings.TrimSpace(account.OpeningBalance); value != "" {
		parsed, ok := parseSignedMinor(value)
		if !ok {
			return Statement{}, ErrInvalidStatement
		}
		statement.OpeningBalanceMinor = &parsed
	}
	closingRowFound := false
	for _, row := range account.StatementRows.Rows {
		kind := strings.TrimSpace(row.Type)
		if kind == "CL" {
			if value := strings.TrimSpace(row.Amount); value != "" {
				parsed, ok := parseSignedMinor(value)
				if !ok {
					return Statement{}, ErrInvalidStatement
				}
				statement.ClosingBalanceMinor = &parsed
				closingRowFound = true
			}
		}
		if kind == "OP" || kind == "CL" || kind == "DT" || kind == "CT" || strings.TrimSpace(row.Direction) == "" {
			statement.SkippedRows++
			continue
		}
		if kind != "DE" && kind != "CE" {
			return Statement{}, ErrInvalidStatement
		}
		amount, ok := parseUnsignedMinor(row.Amount)
		if !ok {
			return Statement{}, ErrInvalidStatement
		}
		if amount == 0 {
			statement.SkippedRows++
			continue
		}
		if len(statement.Entries) >= maxFibankEntries {
			return Statement{}, ErrInvalidStatement
		}
		var direction Direction
		switch strings.TrimSpace(row.Direction) {
		case "D":
			direction = DirectionDebit
		case "C":
			direction = DirectionCredit
		default:
			return Statement{}, ErrInvalidStatement
		}
		bookedDate, bookedOK := parseFibankDate(row.BookedDate)
		valueDate, valueOK := parseFibankDate(row.ValueDate)
		if !bookedOK || !valueOK {
			return Statement{}, ErrInvalidStatement
		}
		entry := Entry{
			ExternalID:      strings.TrimSpace(row.EntryID),
			BookedDate:      bookedDate,
			ValueDate:       valueDate,
			Direction:       direction,
			AmountMinor:     amount,
			Currency:        statement.Currency,
			Merchant:        fibankMerchant(row),
			Description:     fibankDescription(row),
			Reference:       strings.TrimSpace(row.Reference),
			TransactionCode: strings.TrimSpace(row.TransactionCode),
		}
		if value := strings.TrimSpace(row.Balance); value != "" {
			balance, ok := parseSignedMinor(value)
			if !ok {
				return Statement{}, ErrInvalidStatement
			}
			entry.BalanceAfterMinor = &balance
		}
		statement.Entries = append(statement.Entries, entry)
	}
	if !closingRowFound && strings.TrimSpace(account.CurrentBalance) != "" {
		closing, ok := parseSignedMinor(account.CurrentBalance)
		if !ok {
			return Statement{}, ErrInvalidStatement
		}
		statement.ClosingBalanceMinor = &closing
	}
	return statement, nil
}

func parseFibankDate(value string) (time.Time, bool) {
	parsed, err := time.Parse("02/01/2006", strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC), true
}

func parseUnsignedMinor(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return 0, false
	}
	return decimalMinor(value)
}

func parseSignedMinor(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	sign := int64(1)
	if value[0] == '-' {
		sign = -1
		value = value[1:]
	} else if value[0] == '+' {
		value = value[1:]
	}
	minor, ok := decimalMinor(value)
	if !ok {
		return 0, false
	}
	return minor * sign, true
}

func decimalMinor(value string) (int64, bool) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, false
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, false
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > 2 {
		return 0, false
	}
	for _, digit := range fraction {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	if len(fraction) == 1 {
		fraction += "0"
	}
	if fraction == "" {
		fraction = "00"
	}
	minorFraction, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, false
	}
	if whole > (math.MaxInt64-minorFraction)/100 {
		return 0, false
	}
	minor := whole*100 + minorFraction
	if minor < 0 {
		return 0, false
	}
	return minor, true
}

// fibankMerchant prefers REM_I, then TRNAME, then CONTRAGENT for POS rows;
// other rows prefer CONTRAGENT, then REM_I, then TRNAME. Numeric contragents
// like "EUR 2588" are export codes rather than merchant names and are ignored.
func fibankMerchant(row fibankRow) string {
	type candidate struct {
		value      string
		contragent bool
	}
	values := []candidate{{value: row.Contragent, contragent: true}, {value: row.RemI}, {value: row.TransactionName}}
	if strings.TrimSpace(row.TransactionCode) == "POS" {
		values = []candidate{{value: row.RemI}, {value: row.TransactionName}, {value: row.Contragent, contragent: true}}
	}
	for _, option := range values {
		value := strings.TrimSpace(option.value)
		if value == "" || (option.contragent && numericContragent.MatchString(value)) {
			continue
		}
		return value
	}
	return ""
}

func fibankDescription(row fibankRow) string {
	values := []string{row.TransactionName, row.RemI, row.RemII, row.RemIII}
	seen := make(map[string]struct{}, len(values))
	parts := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		parts = append(parts, value)
	}
	return strings.Join(parts, " | ")
}
