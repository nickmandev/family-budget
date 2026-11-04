package imports

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
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

func TestFibankParserRejectsInvalidInputsSafely(t *testing.T) {
	base := `<SYTY_BAL_CUST_MAIN><ACC><SYTY_BAL_CUST_ACC><IBAN>BG00TEST</IBAN><CCY>EUR</CCY><STMT><SYTY_BAL_CUST_STMT><TRN_TYPE>DE</TRN_TYPE><AC_ENTRY_SR_NO>PII-123</AC_ENTRY_SR_NO><VALUE_DT>01/01/2026</VALUE_DT><DATE_TIME>01/01/2026</DATE_TIME><AMOUNT>12.34</AMOUNT><DTKT>%s</DTKT></SYTY_BAL_CUST_STMT></STMT></SYTY_BAL_CUST_ACC></ACC></SYTY_BAL_CUST_MAIN>`
	tests := []struct {
		name string
		data string
	}{
		{name: "unknown root", data: `<UNKNOWN/>`},
		{name: "malformed", data: `<SYTY_BAL_CUST_MAIN><ACC>`},
		{name: "doctype", data: `<!DOCTYPE foo><SYTY_BAL_CUST_MAIN/>`},
		{name: "unknown direction", data: fmt.Sprintf(base, "X")},
		{name: "negative amount", data: strings.Replace(fmt.Sprintf(base, "D"), `<AMOUNT>12.34</AMOUNT>`, `<AMOUNT>-12.34</AMOUNT>`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewFibankParser().Parse([]byte(test.data))
			if !errors.Is(err, ErrInvalidStatement) {
				t.Fatalf("Parse error = %v, want ErrInvalidStatement", err)
			}
			if strings.Contains(err.Error(), "PII-123") || strings.Contains(err.Error(), "12.34") {
				t.Errorf("unsafe parse error %q", err)
			}
		})
	}
}

func TestFibankParserRejectsTooManyEntries(t *testing.T) {
	var xml strings.Builder
	xml.WriteString(`<SYTY_BAL_CUST_MAIN><ACC><SYTY_BAL_CUST_ACC><CCY>EUR</CCY><STMT>`)
	for index := 0; index <= maxFibankEntries; index++ {
		fmt.Fprintf(&xml, `<SYTY_BAL_CUST_STMT><TRN_TYPE>DE</TRN_TYPE><AC_ENTRY_SR_NO>%d</AC_ENTRY_SR_NO><VALUE_DT>01/01/2026</VALUE_DT><DATE_TIME>01/01/2026</DATE_TIME><AMOUNT>1.00</AMOUNT><DTKT>D</DTKT></SYTY_BAL_CUST_STMT>`, index)
	}
	xml.WriteString(`</STMT></SYTY_BAL_CUST_ACC></ACC></SYTY_BAL_CUST_MAIN>`)
	if _, err := NewFibankParser().Parse([]byte(xml.String())); !errors.Is(err, ErrInvalidStatement) {
		t.Fatalf("Parse error = %v, want ErrInvalidStatement", err)
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
