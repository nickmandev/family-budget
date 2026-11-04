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
	if got := recorder.Body.String(); bytes.Contains([]byte(got), []byte("ROWSET")) || bytes.Contains([]byte(got), []byte("TEST CUSTOMER")) {
		t.Fatalf("customer section leaked in preview response: %s", got)
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
	if result.Data.Batch.RowsFailed != 3 || result.Data.Batch.RowsImported != 0 || len(result.Data.Failures) != 3 || result.Data.Failures[0].Reason != "currency EUR does not match household currency" {
		t.Fatalf("currency mismatch result = %+v", result.Data)
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
