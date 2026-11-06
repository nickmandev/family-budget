package imports

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"
)

func TestFibankParserParsesSyntheticStatement(t *testing.T) {
	data, err := os.ReadFile("testdata/fibank_statement.xml")
	if err != nil {
		t.Fatal(err)
	}
	statement, err := NewFibankParser().Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if statement.Bank != "fibank" || statement.Format != "xml" || statement.IBAN != "BG00FINV00000000000001" || statement.Currency != "EUR" {
		t.Fatalf("statement metadata = %+v", statement)
	}
	if statement.SkippedRows != 5 {
		t.Errorf("SkippedRows = %d, want 5", statement.SkippedRows)
	}
	if len(statement.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(statement.Entries))
	}
	if len(statement.Failures) != 0 {
		t.Errorf("Failures = %v, want none", statement.Failures)
	}
	if statement.Entries[0].RowIndex != 4 || statement.Entries[1].RowIndex != 5 || statement.Entries[2].RowIndex != 6 {
		t.Errorf("entry raw row indices = %d, %d, %d; want 4, 5, 6", statement.Entries[0].RowIndex, statement.Entries[1].RowIndex, statement.Entries[2].RowIndex)
	}
	if got := statement.Entries[0]; got.ExternalID != "10001" || got.Direction != DirectionDebit || got.AmountMinor != 1025 || got.Currency != "EUR" {
		t.Errorf("POS entry identity/amount = %+v", got)
	}
	if got := statement.Entries[0].BookedDate; !got.Equal(utcDate(2026, time.October, 2)) || got.Location() != time.UTC {
		t.Errorf("BookedDate = %v, want 2026-10-02 UTC", got)
	}
	if got := statement.Entries[0].ValueDate; !got.Equal(utcDate(2026, time.October, 1)) {
		t.Errorf("ValueDate = %v, want 2026-10-01 UTC", got)
	}
	pos := statement.Entries[0]
	if pos.Merchant != "Sample Shop" || pos.Description != "Плащане ПОС | Sample Shop | Card ending 0000" {
		t.Errorf("POS merchant/description = %q / %q", pos.Merchant, pos.Description)
	}
	if pos.Reference != "REF-POS-1" || pos.TransactionCode != "POS" || pos.BalanceAfterMinor == nil || *pos.BalanceAfterMinor != 0 {
		t.Errorf("POS metadata/balance = %+v", pos)
	}
	transfer := statement.Entries[1]
	if transfer.Merchant != "Sample Recipient" || transfer.Description != "Вътрешен превод | Invoice A | Transfer note" || transfer.AmountMinor != 2799 {
		t.Errorf("transfer = %+v", transfer)
	}
	credit := statement.Entries[2]
	if credit.Direction != DirectionCredit || credit.AmountMinor != 50 || credit.Merchant != "Възстановяване" || credit.Description != "Възстановяване" {
		t.Errorf("credit = %+v", credit)
	}
	if statement.PeriodFrom == nil || !statement.PeriodFrom.Equal(utcDate(2026, time.October, 1)) || statement.PeriodTo == nil || !statement.PeriodTo.Equal(utcDate(2026, time.October, 5)) {
		t.Errorf("period = %v - %v", statement.PeriodFrom, statement.PeriodTo)
	}
	if statement.OpeningBalanceMinor == nil || *statement.OpeningBalanceMinor != 123456 || statement.ClosingBalanceMinor == nil || *statement.ClosingBalanceMinor != 119631 {
		t.Errorf("balances = %v / %v", statement.OpeningBalanceMinor, statement.ClosingBalanceMinor)
	}
}

func TestFibankParserRejectsStructuralFailuresWithSafeReasons(t *testing.T) {
	const row = `<SYTY_BAL_CUST_STMT><TRN_TYPE>DE</TRN_TYPE><AC_ENTRY_SR_NO>PII-123</AC_ENTRY_SR_NO><VALUE_DT>01/01/2026</VALUE_DT><DATE_TIME>01/01/2026</DATE_TIME><AMOUNT>12.34</AMOUNT><DTKT>D</DTKT></SYTY_BAL_CUST_STMT>`
	statement := func(body string) string {
		return `<SYTY_BAL_CUST_MAIN><ACC><SYTY_BAL_CUST_ACC><IBAN>BG00TEST</IBAN><CCY>EUR</CCY><STMT>` + body + `</STMT></SYTY_BAL_CUST_ACC></ACC></SYTY_BAL_CUST_MAIN>`
	}
	tests := []struct {
		name   string
		data   string
		reason string
	}{
		{name: "unknown root", data: `<UNKNOWN/>`, reason: "unexpected document root"},
		{name: "malformed", data: `<SYTY_BAL_CUST_MAIN><ACC>`, reason: "XML is malformed"},
		{name: "doctype", data: `<!DOCTYPE foo>` + statement(row), reason: "doctype or entity declarations are not allowed"},
		{name: "entity", data: `<!ENTITY foo "bar">` + statement(row), reason: "doctype or entity declarations are not allowed"},
		{name: "deep nesting", data: `<SYTY_BAL_CUST_MAIN>` + strings.Repeat(`<A>`, 64), reason: "nesting deeper than 64 levels"},
		{name: "no account block", data: `<SYTY_BAL_CUST_MAIN><ACC></ACC></SYTY_BAL_CUST_MAIN>`, reason: "no account block found"},
		{name: "two account blocks", data: `<SYTY_BAL_CUST_MAIN><ACC><SYTY_BAL_CUST_ACC><CCY>EUR</CCY></SYTY_BAL_CUST_ACC><SYTY_BAL_CUST_ACC><CCY>EUR</CCY></SYTY_BAL_CUST_ACC></ACC></SYTY_BAL_CUST_MAIN>`, reason: "more than one account block found"},
		{name: "missing currency", data: `<SYTY_BAL_CUST_MAIN><ACC><SYTY_BAL_CUST_ACC><IBAN>BG00TEST</IBAN><STMT>` + row + `</STMT></SYTY_BAL_CUST_ACC></ACC></SYTY_BAL_CUST_MAIN>`, reason: "account currency is missing"},
		{name: "invalid opening balance", data: strings.Replace(statement(row), `<CCY>EUR</CCY>`, `<CCY>EUR</CCY><ACY_OPENING_BAL>abc</ACY_OPENING_BAL>`, 1), reason: "opening balance is invalid"},
		{name: "invalid period", data: `<SYTY_BAL_CUST_MAIN><RP_FROM_DATE>not-a-date</RP_FROM_DATE><ACC><SYTY_BAL_CUST_ACC><CCY>EUR</CCY></SYTY_BAL_CUST_ACC></ACC></SYTY_BAL_CUST_MAIN>`, reason: "statement period start is invalid"},
		{name: "unsupported charset", data: `<?xml version="1.0" encoding="koi8-r"?>` + statement(row), reason: "text encoding is not supported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewFibankParser().Parse([]byte(test.data))
			if !errors.Is(err, ErrInvalidStatement) {
				t.Fatalf("Parse error = %v, want ErrInvalidStatement", err)
			}
			if !strings.Contains(err.Error(), test.reason) {
				t.Errorf("Parse error = %q, want reason containing %q", err, test.reason)
			}
			for _, leak := range []string{"PII-123", "12.34", "BG00TEST", "01/01/2026"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("unsafe parse error %q leaks %q", err, leak)
				}
			}
		})
	}
}

func TestFibankParserRejectsTooManyRows(t *testing.T) {
	var xml strings.Builder
	xml.WriteString(`<SYTY_BAL_CUST_MAIN><ACC><SYTY_BAL_CUST_ACC><CCY>EUR</CCY><STMT>`)
	for index := 0; index <= maxFibankEntries; index++ {
		fmt.Fprintf(&xml, `<SYTY_BAL_CUST_STMT><TRN_TYPE>DE</TRN_TYPE><AC_ENTRY_SR_NO>%d</AC_ENTRY_SR_NO><VALUE_DT>01/01/2026</VALUE_DT><DATE_TIME>01/01/2026</DATE_TIME><AMOUNT>1.00</AMOUNT><DTKT>D</DTKT></SYTY_BAL_CUST_STMT>`, index)
	}
	xml.WriteString(`</STMT></SYTY_BAL_CUST_ACC></ACC></SYTY_BAL_CUST_MAIN>`)
	_, err := NewFibankParser().Parse([]byte(xml.String()))
	if !errors.Is(err, ErrInvalidStatement) {
		t.Fatalf("Parse error = %v, want ErrInvalidStatement", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("more than %d rows", maxFibankEntries)) {
		t.Errorf("Parse error = %q, want reason naming the limit", err)
	}
}

func fibankRowXML(entryID, amount, direction, valueDate, bookedDate, balance string) string {
	return fmt.Sprintf(`<SYTY_BAL_CUST_STMT><TRN_TYPE>DE</TRN_TYPE><AC_ENTRY_SR_NO>%s</AC_ENTRY_SR_NO><VALUE_DT>%s</VALUE_DT><DATE_TIME>%s</DATE_TIME><AMOUNT>%s</AMOUNT><DTKT>%s</DTKT><AC_BAL>%s</AC_BAL></SYTY_BAL_CUST_STMT>`, entryID, valueDate, bookedDate, amount, direction, balance)
}

func fibankStatement(rows ...string) string {
	return `<SYTY_BAL_CUST_MAIN><ACC><SYTY_BAL_CUST_ACC><IBAN>BG00TEST</IBAN><CCY>EUR</CCY><STMT>` + strings.Join(rows, "") + `</STMT></SYTY_BAL_CUST_ACC></ACC></SYTY_BAL_CUST_MAIN>`
}

func TestFibankParserCollectsRowFailuresWithoutAborting(t *testing.T) {
	good := fibankRowXML("9001", "12.34", "D", "01/01/2026", "02/01/2026", "100.00")
	unknownDirection := strings.Replace(good, "<DTKT>D</DTKT>", "<DTKT>X</DTKT>", 1)
	badAmount := strings.Replace(good, "<AMOUNT>12.34</AMOUNT>", "<AMOUNT>abc</AMOUNT>", 1)
	statement := fibankStatement(good, good, good, unknownDirection, badAmount)

	parsed, err := NewFibankParser().Parse([]byte(statement))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(parsed.Entries))
	}
	if len(parsed.Failures) != 2 {
		t.Fatalf("failures = %+v, want 2", parsed.Failures)
	}
	if parsed.Failures[0].RowIndex != 3 || parsed.Failures[0].Reason != "debit/credit indicator is unknown" {
		t.Errorf("failure 0 = %+v", parsed.Failures[0])
	}
	if parsed.Failures[1].RowIndex != 4 || parsed.Failures[1].Reason != "amount is unparseable" {
		t.Errorf("failure 1 = %+v", parsed.Failures[1])
	}
	for _, failure := range parsed.Failures {
		for _, leak := range []string{"9001", "12.34", "BG00TEST", "100.00", "01/01/2026", "abc"} {
			if strings.Contains(failure.Reason, leak) {
				t.Errorf("failure reason %q leaks %q", failure.Reason, leak)
			}
		}
	}
}

func TestFibankParserClassifiesSkipsAndFailures(t *testing.T) {
	good := fibankRowXML("9001", "12.34", "D", "01/01/2026", "02/01/2026", "100.00")
	noExternalID := strings.Replace(good, "<AC_ENTRY_SR_NO>9001</AC_ENTRY_SR_NO>", "", 1)
	emptyDirection := strings.Replace(good, "<DTKT>D</DTKT>", "<DTKT></DTKT>", 1)
	zeroAmount := strings.Replace(good, "<AMOUNT>12.34</AMOUNT>", "<AMOUNT>0</AMOUNT>", 1)
	unknownType := strings.Replace(good, "<TRN_TYPE>DE</TRN_TYPE>", "<TRN_TYPE>XX</TRN_TYPE>", 1)
	negativeAmount := strings.Replace(good, "<AMOUNT>12.34</AMOUNT>", "<AMOUNT>-12.34</AMOUNT>", 1)
	badBooked := strings.Replace(good, "02/01/2026", "31/02/2026", 1)
	badValue := strings.Replace(good, "<VALUE_DT>01/01/2026</VALUE_DT>", "<VALUE_DT>not-a-date</VALUE_DT>", 1)
	badBalance := strings.Replace(good, "<AC_BAL>100.00</AC_BAL>", "<AC_BAL>xyz</AC_BAL>", 1)

	parsed, err := NewFibankParser().Parse([]byte(fibankStatement(good, noExternalID, emptyDirection, zeroAmount, unknownType, negativeAmount, badBooked, badValue, badBalance)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed.Entries) != 2 || parsed.Entries[0].RowIndex != 0 || parsed.Entries[1].RowIndex != 1 {
		t.Errorf("entries = %+v, want rows 0 and 1", parsed.Entries)
	}
	if parsed.SkippedRows != 2 {
		t.Errorf("SkippedRows = %d, want 2", parsed.SkippedRows)
	}
	want := map[int]string{
		4: "transaction type is unknown",
		5: "amount is unparseable",
		6: "booked date is unparseable",
		7: "value date is unparseable",
		8: "balance is unparseable",
	}
	if len(parsed.Failures) != len(want) {
		t.Fatalf("failures = %+v, want %d", parsed.Failures, len(want))
	}
	for _, failure := range parsed.Failures {
		if reason, ok := want[failure.RowIndex]; !ok || reason != failure.Reason {
			t.Errorf("failure = %+v, want row in %v", failure, want)
		}
	}
}

func TestFibankParserDecodesWindows1251Statement(t *testing.T) {
	utf8Body := `<ACC><SYTY_BAL_CUST_ACC><IBAN>BG00TEST</IBAN><CCY>EUR</CCY><STMT>` +
		`<SYTY_BAL_CUST_STMT><TRN_TYPE>DE</TRN_TYPE><AC_ENTRY_SR_NO>9001</AC_ENTRY_SR_NO><VALUE_DT>01/10/2026</VALUE_DT><DATE_TIME>01/10/2026</DATE_TIME><AMOUNT>10.25</AMOUNT><DTKT>D</DTKT><TRNAME>Плащане ПОС</TRNAME><REM_I>Магазин център</REM_I></SYTY_BAL_CUST_STMT>` +
		`</STMT></SYTY_BAL_CUST_ACC></ACC>`
	body, err := charmap.Windows1251.NewEncoder().String(utf8Body)
	if err != nil {
		t.Fatal(err)
	}
	declared := `<?xml version="1.0" encoding="windows-1251"?><SYTY_BAL_CUST_MAIN>` + body + `<RP_FROM_DATE>01/10/2026</RP_FROM_DATE></SYTY_BAL_CUST_MAIN>`
	reference := `<?xml version="1.0" encoding="UTF-8"?><SYTY_BAL_CUST_MAIN>` + utf8Body + `<RP_FROM_DATE>01/10/2026</RP_FROM_DATE></SYTY_BAL_CUST_MAIN>`

	windows, err := NewFibankParser().Parse([]byte(declared))
	if err != nil {
		t.Fatalf("Parse windows-1251: %v", err)
	}
	utf8, err := NewFibankParser().Parse([]byte(reference))
	if err != nil {
		t.Fatalf("Parse UTF-8: %v", err)
	}
	if len(windows.Entries) != 1 {
		t.Fatalf("windows entries = %d, want 1", len(windows.Entries))
	}
	if windows.Entries[0].Merchant != "Магазин център" || windows.Entries[0].Description != "Плащане ПОС | Магазин център" {
		t.Errorf("windows merchant/description = %q / %q", windows.Entries[0].Merchant, windows.Entries[0].Description)
	}
	if !reflect.DeepEqual(windows, utf8) {
		t.Errorf("windows-1251 statement != UTF-8 statement:\nwindows %+v\nutf8    %+v", windows, utf8)
	}
	if !NewFibankParser().Detect("statement.xml", []byte(declared)) {
		t.Error("Detect rejected a windows-1251 Fibank document")
	}
}

func TestRegistryDetectsByContentAndReportsUnknownFormat(t *testing.T) {
	data, err := os.ReadFile("testdata/fibank_statement.xml")
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(NewFibankParser())
	statement, err := registry.Parse("misleading.csv", data)
	if err != nil || statement.Bank != "fibank" || statement.Format != "xml" {
		t.Fatalf("Parse(misleading.csv) = %+v, %v", statement, err)
	}
	if _, err := registry.Parse("statement.csv", []byte("a,b\n1,2")); !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("unknown format error = %v", err)
	}
	if got := registry.Supported(); len(got) != 1 || got[0] != "fibank:xml" {
		t.Errorf("Supported() = %v", got)
	}
}

func utcDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
