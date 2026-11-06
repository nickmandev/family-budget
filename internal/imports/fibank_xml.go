package imports

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// maxFibankEntries caps the raw SYTY_BAL_CUST_STMT rows (entries, failures
// and skipped rows combined) accepted from one statement.
const maxFibankEntries = 20000

var numericContragent = regexp.MustCompile(`^[A-Z]{3} [0-9]+$`)

// errUnsupportedCharset marks a charset the Fibank decoder refuses. It is
// internal: callers only see the safe "text encoding is not supported" reason.
var errUnsupportedCharset = errors.New("unsupported charset")

// newFibankDecoder builds a strict XML decoder that transcodes the charsets
// Fibank exports actually use, because encoding/xml is UTF-8-only otherwise.
func newFibankDecoder(data []byte) *xml.Decoder {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	decoder.CharsetReader = fibankCharsetReader
	return decoder
}

func fibankCharsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch normalizeFibankCharset(charset) {
	case "utf8":
		return input, nil
	case "windows1251", "cp1251":
		return transform.NewReader(input, charmap.Windows1251.NewDecoder()), nil
	case "windows1252", "cp1252":
		return transform.NewReader(input, charmap.Windows1252.NewDecoder()), nil
	case "iso88591", "latin1", "l1":
		return transform.NewReader(input, charmap.ISO8859_1.NewDecoder()), nil
	default:
		return nil, errUnsupportedCharset
	}
}

func normalizeFibankCharset(charset string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(charset)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// decodeError converts a decoder error into a fatal, reason-carrying parse
// error without ever echoing file content into the reason.
func decodeError(err error) error {
	if errors.Is(err, errUnsupportedCharset) {
		return invalidStatement("text encoding is not supported")
	}
	return invalidStatement("XML is malformed")
}

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
	decoder := newFibankDecoder(data)
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
		return Statement{}, invalidStatement("doctype or entity declarations are not allowed")
	}
	decoder := newFibankDecoder(data)
	depth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Statement{}, decodeError(err)
		}
		switch token.(type) {
		case xml.StartElement:
			depth++
			if depth > 64 {
				return Statement{}, invalidStatement("nesting deeper than 64 levels")
			}
		case xml.EndElement:
			depth--
		}
	}
	var document fibankDocument
	if err := newFibankDecoder(data).Decode(&document); err != nil {
		return Statement{}, decodeError(err)
	}
	if document.XMLName.Local != "SYTY_BAL_CUST_MAIN" {
		return Statement{}, invalidStatement("unexpected document root")
	}
	if len(document.Accounts.Accounts) == 0 {
		return Statement{}, invalidStatement("no account block found")
	}
	if len(document.Accounts.Accounts) > 1 {
		return Statement{}, invalidStatement("more than one account block found")
	}
	account := document.Accounts.Accounts[0]
	if strings.TrimSpace(account.Currency) == "" {
		return Statement{}, invalidStatement("account currency is missing")
	}
	statement := Statement{
		Bank:     "fibank",
		Format:   "xml",
		IBAN:     strings.TrimSpace(account.IBAN),
		Currency: strings.TrimSpace(account.Currency),
		Entries:  make([]Entry, 0),
	}
	if value := strings.TrimSpace(document.PeriodFrom); value != "" {
		parsed, ok := parseFibankDate(value)
		if !ok {
			return Statement{}, invalidStatement("statement period start is invalid")
		}
		statement.PeriodFrom = &parsed
	}
	if value := strings.TrimSpace(document.PeriodTo); value != "" {
		parsed, ok := parseFibankDate(value)
		if !ok {
			return Statement{}, invalidStatement("statement period end is invalid")
		}
		statement.PeriodTo = &parsed
	}
	if value := strings.TrimSpace(account.OpeningBalance); value != "" {
		parsed, ok := parseSignedMinor(value)
		if !ok {
			return Statement{}, invalidStatement("opening balance is invalid")
		}
		statement.OpeningBalanceMinor = &parsed
	}
	if rows := len(account.StatementRows.Rows); rows > maxFibankEntries {
		return Statement{}, invalidStatement(fmt.Sprintf("more than %d rows", maxFibankEntries))
	}
	closingRowFound := false
	for rowIndex, row := range account.StatementRows.Rows {
		kind := strings.TrimSpace(row.Type)
		if kind == "CL" {
			if value := strings.TrimSpace(row.Amount); value != "" {
				parsed, ok := parseSignedMinor(value)
				if !ok {
					statement.Failures = append(statement.Failures, fibankFailure(row, rowIndex, statement.Currency, "closing balance row is invalid"))
				} else {
					statement.ClosingBalanceMinor = &parsed
					closingRowFound = true
				}
			}
		}
		if kind == "OP" || kind == "CL" || kind == "DT" || kind == "CT" || strings.TrimSpace(row.Direction) == "" {
			statement.SkippedRows++
			continue
		}
		amount, ok := parseUnsignedMinor(row.Amount)
		if !ok {
			statement.Failures = append(statement.Failures, fibankFailure(row, rowIndex, statement.Currency, "amount is unparseable"))
			continue
		}
		if amount == 0 {
			statement.SkippedRows++
			continue
		}
		var direction Direction
		switch strings.TrimSpace(row.Direction) {
		case "D":
			direction = DirectionDebit
		case "C":
			direction = DirectionCredit
		default:
			statement.Failures = append(statement.Failures, fibankFailure(row, rowIndex, statement.Currency, "debit/credit indicator is unknown"))
			continue
		}
		bookedDate, bookedOK := parseFibankDate(row.BookedDate)
		valueDate, valueOK := parseFibankDate(row.ValueDate)
		if !bookedOK {
			statement.Failures = append(statement.Failures, fibankFailure(row, rowIndex, statement.Currency, "booked date is unparseable"))
			continue
		}
		if !valueOK {
			statement.Failures = append(statement.Failures, fibankFailure(row, rowIndex, statement.Currency, "value date is unparseable"))
			continue
		}
		entry := Entry{
			RowIndex:        rowIndex,
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
			if balance, ok := parseSignedMinor(value); ok {
				entry.BalanceAfterMinor = &balance
			}
		}
		statement.Entries = append(statement.Entries, entry)
	}
	if !closingRowFound && strings.TrimSpace(account.CurrentBalance) != "" {
		closing, ok := parseSignedMinor(account.CurrentBalance)
		if !ok {
			return Statement{}, invalidStatement("current balance is invalid")
		}
		statement.ClosingBalanceMinor = &closing
	}
	return statement, nil
}

func fibankFailure(row fibankRow, rowIndex int, currency, reason string) ParseFailure {
	failure := ParseFailure{
		RowIndex:        rowIndex,
		Reason:          reason,
		ExternalID:      stringPointer(strings.TrimSpace(row.EntryID)),
		Currency:        currency,
		Merchant:        fibankMerchant(row),
		Description:     fibankDescription(row),
		Reference:       strings.TrimSpace(row.Reference),
		TransactionCode: strings.TrimSpace(row.TransactionCode),
	}
	switch strings.TrimSpace(row.Direction) {
	case "D":
		failure.Direction = DirectionDebit
	case "C":
		failure.Direction = DirectionCredit
	}
	if amount, ok := parseUnsignedMinor(row.Amount); ok {
		failure.AmountMinor = &amount
	}
	if bookedDate, ok := parseFibankDate(row.BookedDate); ok {
		failure.BookedDate = &bookedDate
	}
	if valueDate, ok := parseFibankDate(row.ValueDate); ok {
		failure.ValueDate = &valueDate
	}
	return failure
}

func stringPointer(value string) *string { return &value }

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

// decimalMinor parses plain decimal amounts in the forms 27, 27.5, 27.55,
// .27 and .5. Fibank omits the leading zero for sub-unit amounts.
func decimalMinor(value string) (int64, bool) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || (parts[0] == "" && (len(parts) != 2 || parts[1] == "")) {
		return 0, false
	}
	var whole int64
	if parts[0] != "" {
		var err error
		whole, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || whole < 0 {
			return 0, false
		}
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
