package httpapi

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
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
