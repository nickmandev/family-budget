package httpapi

import (
	"errors"
	"io"
	"net/http"
	"time"

	"family-budget/internal/application"
	"family-budget/internal/domain"
	"family-budget/internal/imports"
	"family-budget/internal/ports"
)

const bankImportBodyLimit = 10 << 20

type statementDTO struct {
	Bank                string                 `json:"bank"`
	Format              string                 `json:"format"`
	IBAN                string                 `json:"iban"`
	Currency            string                 `json:"currency"`
	PeriodFrom          *string                `json:"period_from"`
	PeriodTo            *string                `json:"period_to"`
	OpeningBalanceMinor *int64                 `json:"opening_balance_minor"`
	ClosingBalanceMinor *int64                 `json:"closing_balance_minor"`
	SkippedRows         int                    `json:"skipped_rows"`
	Entries             []statementEntryDTO    `json:"entries"`
	Failures            []bankImportFailureDTO `json:"failures"`
}

type statementEntryDTO struct {
	ExternalID        string `json:"external_id"`
	BookedDate        string `json:"booked_date"`
	ValueDate         string `json:"value_date"`
	Direction         string `json:"direction"`
	AmountMinor       int64  `json:"amount_minor"`
	Currency          string `json:"currency"`
	Merchant          string `json:"merchant"`
	Description       string `json:"description"`
	Reference         string `json:"reference"`
	TransactionCode   string `json:"transaction_code"`
	BalanceAfterMinor *int64 `json:"balance_after_minor"`
}

func (s *Server) registerBankImportRoutes(mux *http.ServeMux) {
	if s.deps.Imports == nil {
		return
	}
	s.register(mux, "POST /api/v1/households/{id}/bank-imports/preview", s.requireAuth(http.HandlerFunc(s.handleBankImportPreview)))
	s.register(mux, "POST /api/v1/households/{id}/bank-imports", s.requireAuth(http.HandlerFunc(s.handleBankImportCommit)))
	s.register(mux, "GET /api/v1/households/{id}/bank-imports", s.requireAuth(http.HandlerFunc(s.handleBankImportList)))
}

type bankImportBatchDTO struct {
	ID            string  `json:"id"`
	AccountID     string  `json:"account_id"`
	BankCode      string  `json:"bank_code"`
	Format        string  `json:"format"`
	FileName      string  `json:"file_name"`
	FileSHA256    string  `json:"file_sha256"`
	PeriodFrom    *string `json:"period_from"`
	PeriodTo      *string `json:"period_to"`
	RowsTotal     int     `json:"rows_total"`
	RowsImported  int     `json:"rows_imported"`
	RowsDuplicate int     `json:"rows_duplicate"`
	RowsFailed    int     `json:"rows_failed"`
	CreatedAt     string  `json:"created_at"`
}

type bankImportFailureDTO struct {
	RowIndex        int     `json:"row_index"`
	Reason          string  `json:"reason"`
	ExternalID      *string `json:"external_id"`
	BookedDate      *string `json:"booked_date"`
	ValueDate       *string `json:"value_date"`
	Direction       *string `json:"direction"`
	AmountMinor     *int64  `json:"amount_minor"`
	Currency        *string `json:"currency"`
	Merchant        *string `json:"merchant"`
	Description     *string `json:"description"`
	Reference       *string `json:"reference"`
	TransactionCode *string `json:"transaction_code"`
}

type bankImportCommitDTO struct {
	Batch    bankImportBatchDTO     `json:"batch"`
	Failures []bankImportFailureDTO `json:"failures"`
}

func (s *Server) handleBankImportCommit(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, bankImportBodyLimit)
	if err := r.ParseMultipartForm(bankImportBodyLimit); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, r, errPayloadTooLarge)
			return
		}
		writeError(w, r, errBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	accountRaw, err := parseUUID(r.FormValue("account_id"))
	if err != nil {
		writeError(w, r, newAPIError(http.StatusBadRequest, string(domain.CodeInvalidValue), "A valid account_id is required."))
		return
	}
	accountID, err := domain.NewAccountId(accountRaw)
	if err != nil {
		writeError(w, r, newAPIError(http.StatusBadRequest, string(domain.CodeInvalidValue), "A valid account_id is required."))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, newAPIError(http.StatusBadRequest, CodeBadRequest, "A statement file is required."))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, r, errPayloadTooLarge)
			return
		}
		writeError(w, r, errBadRequest)
		return
	}
	result, err := s.deps.Imports.Commit(r.Context(), userID, household, accountID, header.Filename, data)
	if err != nil {
		if writeStatementParseError(w, r, err) {
			return
		}
		writeError(w, r, err)
		return
	}
	failures := make([]bankImportFailureDTO, 0, len(result.Failures))
	for _, failure := range result.Failures {
		failures = append(failures, toBankImportFailureDTO(failure))
	}
	writeJSON(w, http.StatusCreated, DataResponse[bankImportCommitDTO]{Data: bankImportCommitDTO{
		Batch: toBankImportBatchDTO(result.Batch), Failures: failures,
	}})
}

// writeStatementParseError maps statement parse errors to 4xx responses. The
// invalid-statement message carries the parser's safe, value-free reason so a
// 400 is diagnosable without logging statement contents.
func writeStatementParseError(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, imports.ErrUnknownFormat) {
		writeError(w, r, newAPIError(http.StatusBadRequest, string(domain.CodeInvalidValue), "The statement file format is not supported."))
		return true
	}
	if errors.Is(err, imports.ErrInvalidStatement) {
		writeError(w, r, newAPIError(http.StatusBadRequest, string(domain.CodeInvalidValue), statementInvalidMessage(err)))
		return true
	}
	return false
}

func statementInvalidMessage(err error) string {
	if reason := imports.StatementReason(err); reason != "" {
		return "The statement file is invalid: " + reason + "."
	}
	return "The statement file is invalid."
}

func (s *Server) handleBankImportList(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	batches, err := s.deps.Imports.ListBatches(r.Context(), userID, household)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]bankImportBatchDTO, 0, len(batches))
	for _, batch := range batches {
		items = append(items, toBankImportBatchDTO(batch))
	}
	writeJSON(w, http.StatusOK, struct {
		Items []bankImportBatchDTO `json:"items"`
	}{Items: items})
}

func toBankImportBatchDTO(batch ports.BankImportBatchRecord) bankImportBatchDTO {
	return bankImportBatchDTO{
		ID: batch.ID, AccountID: batch.AccountID.String(), BankCode: batch.BankCode,
		Format: batch.Format, FileName: batch.FileName, FileSHA256: batch.FileSHA256,
		PeriodFrom: optionalStatementDate(batch.PeriodFrom), PeriodTo: optionalStatementDate(batch.PeriodTo),
		RowsTotal: batch.RowsTotal, RowsImported: batch.RowsImported, RowsDuplicate: batch.RowsDuplicate,
		RowsFailed: batch.RowsFailed, CreatedAt: formatTimestamp(batch.CreatedAt),
	}
}

func (s *Server) handleBankImportPreview(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	// The handler caps multipart overhead and file data together at 10 MiB.
	// Large exports may also require raising HTTP_MAX_BODY_BYTES (to at most 16 MiB).
	r.Body = http.MaxBytesReader(w, r.Body, bankImportBodyLimit)
	if err := r.ParseMultipartForm(bankImportBodyLimit); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, r, errPayloadTooLarge)
			return
		}
		writeError(w, r, errBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, newAPIError(http.StatusBadRequest, CodeBadRequest, "A statement file is required."))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, r, errPayloadTooLarge)
			return
		}
		writeError(w, r, errBadRequest)
		return
	}
	preview, err := s.deps.Imports.Preview(r.Context(), userID, household, header.Filename, data)
	if err != nil {
		if writeStatementParseError(w, r, err) {
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[statementDTO]{Data: toStatementDTO(preview)})
}

func toStatementDTO(preview application.ImportPreview) statementDTO {
	statement := preview.Statement
	entries := make([]statementEntryDTO, 0, len(statement.Entries))
	for _, entry := range statement.Entries {
		entries = append(entries, statementEntryDTO{
			ExternalID:        entry.ExternalID,
			BookedDate:        formatStatementDate(entry.BookedDate),
			ValueDate:         formatStatementDate(entry.ValueDate),
			Direction:         string(entry.Direction),
			AmountMinor:       entry.AmountMinor,
			Currency:          entry.Currency,
			Merchant:          entry.Merchant,
			Description:       entry.Description,
			Reference:         entry.Reference,
			TransactionCode:   entry.TransactionCode,
			BalanceAfterMinor: entry.BalanceAfterMinor,
		})
	}
	failures := make([]bankImportFailureDTO, 0, len(preview.Failures))
	for _, failure := range preview.Failures {
		failures = append(failures, toBankImportFailureDTO(failure))
	}
	return statementDTO{
		Bank:                statement.Bank,
		Format:              statement.Format,
		IBAN:                statement.IBAN,
		Currency:            statement.Currency,
		PeriodFrom:          optionalStatementDate(statement.PeriodFrom),
		PeriodTo:            optionalStatementDate(statement.PeriodTo),
		OpeningBalanceMinor: statement.OpeningBalanceMinor,
		ClosingBalanceMinor: statement.ClosingBalanceMinor,
		SkippedRows:         statement.SkippedRows,
		Entries:             entries,
		Failures:            failures,
	}
}

func toBankImportFailureDTO(failure application.ImportFailure) bankImportFailureDTO {
	var bookedDate, valueDate, direction *string
	if failure.BookedDate != nil {
		bookedDate = stringValuePointer(formatStatementDate(*failure.BookedDate))
	}
	if failure.ValueDate != nil {
		valueDate = stringValuePointer(formatStatementDate(*failure.ValueDate))
	}
	if failure.Direction != "" {
		direction = stringValuePointer(string(failure.Direction))
	}
	return bankImportFailureDTO{
		RowIndex: failure.RowIndex, Reason: failure.Reason,
		ExternalID: cloneHTTPPointer(failure.ExternalID), BookedDate: bookedDate, ValueDate: valueDate,
		Direction: direction, AmountMinor: cloneHTTPPointer(failure.AmountMinor),
		Currency: stringValuePointer(failure.Currency), Merchant: stringValuePointer(failure.Merchant),
		Description: stringValuePointer(failure.Description), Reference: stringValuePointer(failure.Reference),
		TransactionCode: stringValuePointer(failure.TransactionCode),
	}
}

func stringValuePointer(value string) *string { return &value }

func cloneHTTPPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func formatStatementDate(date time.Time) string {
	return date.UTC().Format("2006-01-02")
}

func optionalStatementDate(date *time.Time) *string {
	if date == nil {
		return nil
	}
	formatted := formatStatementDate(*date)
	return &formatted
}
