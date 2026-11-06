package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestBankImportPreviewReturnsStatelessStatement(t *testing.T) {
	harness := newResourceServer(t)
	household := harness.createHousehold(t)
	statement, err := os.ReadFile("../../imports/testdata/fibank_statement.xml")
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "renamed.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(file, bytes.NewReader(statement)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/households/"+household.ID+"/bank-imports/preview", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+harness.token)
	recorder := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response DataResponse[statementDTO]
	decodeResponse(t, recorder, &response)
	if response.Data.Bank != "fibank" || response.Data.Format != "xml" || len(response.Data.Entries) != 3 || response.Data.SkippedRows != 5 {
		t.Fatalf("preview response = %+v", response.Data)
	}
	// The sample is a EUR statement and the harness household is USD: preview
	// exposes validation failures for every entry instead of aborting.
	if len(response.Data.Failures) != 3 {
		t.Fatalf("preview failures = %+v, want 3 currency mismatches", response.Data.Failures)
	}
	for index, failure := range response.Data.Failures {
		if failure.Reason != "currency does not match household currency" {
			t.Errorf("failure %d = %+v", index, failure)
		}
	}
	if got := recorder.Body.String(); bytes.Contains([]byte(got), []byte("ROWSET")) || bytes.Contains([]byte(got), []byte("TEST CUSTOMER")) {
		t.Fatalf("customer section leaked in preview response: %s", got)
	}
}

func TestBankImportPreviewReportsBadRowsAsFailures(t *testing.T) {
	harness := newResourceServer(t)
	household, _ := createImportHouseholdAndAccount(t, harness, "EUR")
	row := func(direction string) string {
		return `<SYTY_BAL_CUST_STMT><TRN_TYPE>DE</TRN_TYPE><AC_ENTRY_SR_NO>` + direction + `</AC_ENTRY_SR_NO><VALUE_DT>01/10/2026</VALUE_DT><DATE_TIME>02/10/2026</DATE_TIME><AMOUNT>12.34</AMOUNT><DTKT>` + direction + `</DTKT></SYTY_BAL_CUST_STMT>`
	}
	unknownDirection := strings.Replace(row("D"), "</SYTY_BAL_CUST_STMT>", `<TRNAME>PRIVATE-FAILURE-MERCHANT</TRNAME><REFERENCE>PRIVATE-FAILURE-REFERENCE</REFERENCE></SYTY_BAL_CUST_STMT>`, 1)
	unknownDirection = strings.Replace(unknownDirection, "<DTKT>D</DTKT>", "<DTKT>X</DTKT>", 1)
	badAmount := strings.Replace(row("D"), "<AMOUNT>12.34</AMOUNT>", "<AMOUNT>bad-amount</AMOUNT>", 1)
	statement := `<SYTY_BAL_CUST_MAIN><ACC><SYTY_BAL_CUST_ACC><IBAN>BG00TEST</IBAN><CCY>EUR</CCY><STMT>` +
		row("D") + row("C") + unknownDirection + badAmount +
		`</STMT></SYTY_BAL_CUST_ACC></ACC></SYTY_BAL_CUST_MAIN>`
	logger, logs := testLogger()
	server, err := NewServer(Dependencies{
		Config: harness.server.deps.Config, Logger: logger, Clock: harness.clock,
		Auth: harness.auth, App: harness.app, Imports: harness.server.deps.Imports,
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.server = server
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "statement.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(file, strings.NewReader(statement)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/households/"+household.ID+"/bank-imports/preview", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+harness.token)
	recorder := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response DataResponse[statementDTO]
	decodeResponse(t, recorder, &response)
	if len(response.Data.Entries) != 2 {
		t.Fatalf("entries = %d, want the 2 valid rows", len(response.Data.Entries))
	}
	if len(response.Data.Failures) != 2 || response.Data.Failures[0].RowIndex != 2 || response.Data.Failures[0].Reason != "debit/credit indicator is unknown" || response.Data.Failures[1].RowIndex != 3 || response.Data.Failures[1].Reason != "amount is unparseable" {
		t.Fatalf("failures = %+v", response.Data.Failures)
	}
	if response.Data.Failures[0].Direction != nil || response.Data.Failures[0].AmountMinor == nil || *response.Data.Failures[0].AmountMinor != 1234 || response.Data.Failures[0].Merchant == nil || *response.Data.Failures[0].Merchant != "PRIVATE-FAILURE-MERCHANT" || response.Data.Failures[0].BookedDate == nil || response.Data.Failures[0].ValueDate == nil {
		t.Errorf("unknown-direction failure details = %+v", response.Data.Failures[0])
	}
	if response.Data.Failures[1].Direction == nil || *response.Data.Failures[1].Direction != "DEBIT" || response.Data.Failures[1].AmountMinor != nil || response.Data.Failures[1].BookedDate == nil || response.Data.Failures[1].ValueDate == nil {
		t.Errorf("bad-amount failure details = %+v", response.Data.Failures[1])
	}
	var wire struct {
		Data struct {
			Failures []map[string]json.RawMessage `json:"failures"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode raw failure payload: %v", err)
	}
	for _, field := range []string{"external_id", "booked_date", "value_date", "direction", "amount_minor", "currency", "merchant", "description", "reference", "transaction_code"} {
		if _, ok := wire.Data.Failures[0][field]; !ok {
			t.Errorf("failure JSON omits %q: %s", field, recorder.Body.String())
		}
	}
	if string(wire.Data.Failures[0]["direction"]) != "null" || string(wire.Data.Failures[1]["amount_minor"]) != "null" {
		t.Errorf("nullable details are not explicit nulls: %s", recorder.Body.String())
	}
	if strings.Contains(logs.String(), "PRIVATE-FAILURE-MERCHANT") || strings.Contains(logs.String(), "PRIVATE-FAILURE-REFERENCE") {
		t.Errorf("request logs contain failure row details: %s", logs.String())
	}
}

func TestBankImportStructuralErrorsReturnSafeReason(t *testing.T) {
	harness := newResourceServer(t)
	household, account := createImportHouseholdAndAccount(t, harness, "EUR")
	doctype := []byte("<!DOCTYPE foo><SYTY_BAL_CUST_MAIN/>")

	preview := newPreviewRequest(t, household.ID, "statement.xml", doctype)
	preview.Header.Set("Authorization", "Bearer "+harness.token)
	recorder := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(recorder, preview)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("preview status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Body.String(); !strings.Contains(got, "The statement file is invalid: doctype or entity declarations are not allowed.") {
		t.Fatalf("preview error message = %s", got)
	}

	commit := postImport(t, harness, household.ID, account.ID, "statement.xml", doctype)
	if commit.Code != http.StatusBadRequest {
		t.Fatalf("commit status = %d: %s", commit.Code, commit.Body.String())
	}
	if got := commit.Body.String(); !strings.Contains(got, "The statement file is invalid: doctype or entity declarations are not allowed.") {
		t.Fatalf("commit error message = %s", got)
	}

	unknown := newPreviewRequest(t, household.ID, "statement.xml", []byte("not an XML statement"))
	unknown.Header.Set("Authorization", "Bearer "+harness.token)
	recorder = httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(recorder, unknown)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "The statement file format is not supported.") {
		t.Fatalf("unknown format status = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestBankImportCommitCreatesSuggestionsAndDeduplicatesReupload(t *testing.T) {
	harness := newResourceServer(t)
	household, account := createImportHouseholdAndAccount(t, harness, "EUR")
	statement := readImportSample(t)
	first := postImport(t, harness, household.ID, account.ID, "statement.xml", statement)
	if first.Code != http.StatusCreated {
		t.Fatalf("commit status = %d: %s", first.Code, first.Body.String())
	}
	var firstResponse DataResponse[bankImportCommitDTO]
	decodeResponse(t, first, &firstResponse)
	if firstResponse.Data.Batch.RowsTotal != 3 || firstResponse.Data.Batch.RowsImported != 3 || firstResponse.Data.Batch.RowsDuplicate != 0 || firstResponse.Data.Batch.RowsFailed != 0 {
		t.Fatalf("first batch = %+v", firstResponse.Data.Batch)
	}
	suggestions := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID+"/suggestions", "", nil)
	if suggestions.Code != http.StatusOK || !strings.Contains(suggestions.Body.String(), `"account_id":"`+account.ID+`"`) || !strings.Contains(suggestions.Body.String(), firstResponse.Data.Batch.ID) {
		t.Fatalf("suggestion response = %d %s", suggestions.Code, suggestions.Body.String())
	}
	second := postImport(t, harness, household.ID, account.ID, "statement.xml", statement)
	if second.Code != http.StatusCreated {
		t.Fatalf("re-upload status = %d: %s", second.Code, second.Body.String())
	}
	var secondResponse DataResponse[bankImportCommitDTO]
	decodeResponse(t, second, &secondResponse)
	if secondResponse.Data.Batch.RowsImported != 0 || secondResponse.Data.Batch.RowsDuplicate != 3 || secondResponse.Data.Batch.RowsFailed != 0 {
		t.Fatalf("re-upload batch = %+v", secondResponse.Data.Batch)
	}
	var list struct {
		Items []bankImportBatchDTO `json:"items"`
	}
	response := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID+"/bank-imports", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || len(list.Items) != 2 {
		t.Fatalf("list = %+v, err=%v", list, err)
	}
}

func TestBankImportCommitCurrencyMismatchAndUnknownFormat(t *testing.T) {
	harness := newResourceServer(t)
	household, account := createImportHouseholdAndAccount(t, harness, "USD")
	response := postImport(t, harness, household.ID, account.ID, "statement.xml", readImportSample(t))
	if response.Code != http.StatusCreated {
		t.Fatalf("currency mismatch commit status = %d: %s", response.Code, response.Body.String())
	}
	var result DataResponse[bankImportCommitDTO]
	decodeResponse(t, response, &result)
	if result.Data.Batch.RowsFailed != 3 || result.Data.Batch.RowsImported != 0 || len(result.Data.Failures) != 3 || result.Data.Failures[0].Reason != "currency does not match household currency" {
		t.Fatalf("currency mismatch result = %+v", result.Data)
	}
	if result.Data.Failures[0].ExternalID == nil || result.Data.Failures[0].BookedDate == nil || result.Data.Failures[0].ValueDate == nil || result.Data.Failures[0].Direction == nil || result.Data.Failures[0].AmountMinor == nil || result.Data.Failures[0].Currency == nil {
		t.Errorf("commit failure omitted parsed row details: %+v", result.Data.Failures[0])
	}
	suggestions := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID+"/suggestions", "", nil)
	if suggestions.Code != http.StatusOK || !strings.Contains(suggestions.Body.String(), `"items":[]`) {
		t.Fatalf("suggestions after failed rows = %d %s", suggestions.Code, suggestions.Body.String())
	}
	unknown := postImport(t, harness, household.ID, account.ID, "unknown.xml", []byte("not an XML statement"))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown format status = %d: %s", unknown.Code, unknown.Body.String())
	}
}

func TestBankImportCommitRejectsNonMemberAndListsEmptyHistory(t *testing.T) {
	harness := newResourceServer(t)
	household, account := createImportHouseholdAndAccount(t, harness, "EUR")
	if _, err := harness.auth.Register(t.Context(), "outsider@example.com", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	login, err := harness.auth.Login(t.Context(), "outsider@example.com", "correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	request := newImportRequest(t, household.ID, account.ID, "statement.xml", readImportSample(t))
	request.Header.Set("Authorization", "Bearer "+login.Token)
	recorder := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("non-member status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []bankImportBatchDTO `json:"items"`
	}
	list := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID+"/bank-imports", "", nil)
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &response) != nil || response.Items == nil || len(response.Items) != 0 {
		t.Fatalf("empty list response = %d %s", list.Code, list.Body.String())
	}
}

func createImportHouseholdAndAccount(t *testing.T, harness *resourceHarness, currency string) (householdDTO, accountDTO) {
	t.Helper()
	body := fmt.Sprintf(`{"name":"Import home","currency":%q,"timezone":"UTC"}`, currency)
	created := harness.do(t, http.MethodPost, "/api/v1/households", body, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create household: %d %s", created.Code, created.Body.String())
	}
	var household DataResponse[householdDTO]
	decodeResponse(t, created, &household)
	account := harness.createAccount(t, household.Data.ID, "CHECKING", true)
	return household.Data, account
}

func readImportSample(t *testing.T) []byte {
	t.Helper()
	statement, err := os.ReadFile("../../imports/testdata/fibank_statement.xml")
	if err != nil {
		t.Fatal(err)
	}
	return statement
}

func postImport(t *testing.T, harness *resourceHarness, householdID, accountID, filename string, statement []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := newImportRequest(t, householdID, accountID, filename, statement)
	request.Header.Set("Authorization", "Bearer "+harness.token)
	recorder := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func newPreviewRequest(t *testing.T, householdID, filename string, statement []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(file, bytes.NewReader(statement)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/households/"+householdID+"/bank-imports/preview", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func newImportRequest(t *testing.T, householdID, accountID, filename string, statement []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("account_id", accountID); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(file, bytes.NewReader(statement)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/households/"+householdID+"/bank-imports", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+"unused")
	return request
}
