package httpapi

import (
	"errors"
	"io"
	"net/http"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/imports"
)

const bankImportBodyLimit = 10 << 20

type statementDTO struct {
	Bank                string              `json:"bank"`
	Format              string              `json:"format"`
	IBAN                string              `json:"iban"`
	Currency            string              `json:"currency"`
	PeriodFrom          *string             `json:"period_from"`
	PeriodTo            *string             `json:"period_to"`
	OpeningBalanceMinor *int64              `json:"opening_balance_minor"`
	ClosingBalanceMinor *int64              `json:"closing_balance_minor"`
	SkippedRows         int                 `json:"skipped_rows"`
	Entries             []statementEntryDTO `json:"entries"`
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
	statement, err := s.deps.Imports.Preview(r.Context(), userID, household, header.Filename, data)
	if err != nil {
		if errors.Is(err, imports.ErrUnknownFormat) {
			writeError(w, r, newAPIError(http.StatusBadRequest, string(domain.CodeInvalidValue), "The statement file format is not supported."))
			return
		}
		if errors.Is(err, imports.ErrInvalidStatement) {
			writeError(w, r, newAPIError(http.StatusBadRequest, string(domain.CodeInvalidValue), "The statement file is invalid."))
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[statementDTO]{Data: toStatementDTO(statement)})
}

func toStatementDTO(statement imports.Statement) statementDTO {
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
	}
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
