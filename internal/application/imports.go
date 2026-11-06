package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/imports"
	"family-budget/internal/ports"
)

// StatementParser parses one uploaded statement without persisting it.
type StatementParser interface {
	Parse(filename string, data []byte) (imports.Statement, error)
}

// ImportService authorizes previews and commits uploaded bank statements.
type ImportService struct {
	uow    ports.UnitOfWork
	parser StatementParser
	ids    ports.IDGenerator
	clock  ports.Clock
}

// NewImportService validates dependencies and returns the import service.
func NewImportService(uow ports.UnitOfWork, parser StatementParser, ids ports.IDGenerator, clock ports.Clock) (*ImportService, error) {
	if uow == nil {
		return nil, errors.New("application: UnitOfWork is required")
	}
	if parser == nil {
		return nil, errors.New("application: StatementParser is required")
	}
	if ids == nil {
		return nil, errors.New("application: IDGenerator is required")
	}
	if clock == nil {
		return nil, errors.New("application: Clock is required")
	}
	return &ImportService{uow: uow, parser: parser, ids: ids, clock: clock}, nil
}

// ImportFailure describes a rejected row without persisting its details. The
// row details are returned only to the authenticated household in HTTP
// responses, so it can recreate the row; they are never logged. RowIndex is
// the 0-based index of the row in the raw statement row sequence (including
// rows the parser skipped), shared with imports.ParseFailure so parser
// failures and validation failures sort in one numbering.
type ImportFailure struct {
	RowIndex        int
	Reason          string
	ExternalID      *string
	BookedDate      *time.Time
	ValueDate       *time.Time
	Direction       imports.Direction
	AmountMinor     *int64
	Currency        string
	Merchant        string
	Description     string
	Reference       string
	TransactionCode string
}

// ImportPreview is the stateless preview of one uploaded statement. Failures
// merge parser row failures with per-row validation (currency mismatch, bad
// date, non-positive amount) using the same raw-row numbering.
type ImportPreview struct {
	Statement imports.Statement
	Skips     []imports.ParseSkip
	Failures  []ImportFailure
}

// mergeImportFailures combines parser failures with validation failures and
// keeps the merged list ordered by raw row index.
func mergeImportFailures(statement imports.Statement, validation []ImportFailure) []ImportFailure {
	failures := make([]ImportFailure, 0, len(statement.Failures)+len(validation))
	for _, failure := range statement.Failures {
		failures = append(failures, ImportFailure{
			RowIndex: failure.RowIndex, Reason: failure.Reason,
			ExternalID: clonePointer(failure.ExternalID), BookedDate: clonePointer(failure.BookedDate),
			ValueDate: clonePointer(failure.ValueDate), Direction: failure.Direction,
			AmountMinor: clonePointer(failure.AmountMinor), Currency: failure.Currency,
			Merchant: failure.Merchant, Description: failure.Description,
			Reference: failure.Reference, TransactionCode: failure.TransactionCode,
		})
	}
	failures = append(failures, validation...)
	slices.SortStableFunc(failures, func(a, b ImportFailure) int { return a.RowIndex - b.RowIndex })
	return failures
}

// Preview verifies household membership, then parses an in-memory statement.
// A statement with rejected rows still previews: the rejections come back as
// failures instead of aborting the request.
func (s *ImportService) Preview(ctx context.Context, actor domain.UserId, household domain.HouseholdId, filename string, data []byte) (ImportPreview, error) {
	var householdRecord ports.HouseholdRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		var err error
		householdRecord, err = repos.Households.Get(ctx, household)
		return err
	})
	if err != nil {
		return ImportPreview{}, err
	}
	statement, err := s.parser.Parse(filename, data)
	if err != nil {
		return ImportPreview{}, err
	}
	now := s.clock.Now().UTC()
	validation := make([]ImportFailure, 0)
	for _, entry := range statement.Entries {
		effectiveDate := dateOnly(entry.BookedDate)
		if reason := validateImportEntry(entry, householdRecord.Currency, effectiveDate, now); reason != "" {
			validation = append(validation, importEntryFailure(entry, reason))
		}
	}
	return ImportPreview{Statement: statement, Skips: statement.Skips, Failures: mergeImportFailures(statement, validation)}, nil
}

// ImportCommitResult is the durable result of committing one statement.
type ImportCommitResult struct {
	Batch    ports.BankImportBatchRecord
	Failures []ImportFailure
}

// Commit persists an import batch and creates reviewable suggestions for its
// valid, previously unseen rows in one transaction. Rows rejected by the
// parser are reported as failures and counted against the batch without
// aborting the rest of the statement.
func (s *ImportService) Commit(ctx context.Context, actor domain.UserId, household domain.HouseholdId, accountID domain.AccountId, filename string, data []byte) (ImportCommitResult, error) {
	var result ImportCommitResult
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		householdRecord, err := repos.Households.Get(ctx, household)
		if err != nil {
			return err
		}
		if _, err := repos.Accounts.Get(ctx, household, accountID); err != nil {
			return err
		}
		statement, err := s.parser.Parse(filename, data)
		if err != nil {
			return err
		}
		batchID, err := s.newID()
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		fileHash := sha256.Sum256(data)
		batch := ports.BankImportBatchRecord{
			ID:          batchID,
			HouseholdID: household,
			AccountID:   accountID,
			BankCode:    statement.Bank,
			Format:      statement.Format,
			FileName:    filename,
			FileSHA256:  hex.EncodeToString(fileHash[:]),
			PeriodFrom:  statement.PeriodFrom,
			PeriodTo:    statement.PeriodTo,
			RowsTotal:   len(statement.Entries) + len(statement.Failures),
			CreatedBy:   actor,
			CreatedAt:   now,
		}
		if err := repos.BankImports.CreateBatch(ctx, batch); err != nil {
			return err
		}
		validation := make([]ImportFailure, 0)
		imported, duplicates := 0, 0
		for _, entry := range statement.Entries {
			effectiveDate := dateOnly(entry.BookedDate)
			if reason := validateImportEntry(entry, householdRecord.Currency, effectiveDate, now); reason != "" {
				validation = append(validation, importEntryFailure(entry, reason))
				continue
			}
			fingerprint := imports.Fingerprint(statement.Bank, entry.ExternalID, entry)
			entryID, err := s.newID()
			if err != nil {
				return err
			}
			entryRecord := ports.BankImportEntryRecord{
				ID: entryID, BatchID: batchID, HouseholdID: household, AccountID: accountID,
				Fingerprint: fingerprint, ExternalID: entry.ExternalID, EffectiveDate: effectiveDate,
				ValueDate: optionalImportDate(entry.ValueDate), Direction: domain.SuggestionDirection(entry.Direction),
				AmountMinor: entry.AmountMinor, Currency: entry.Currency, Merchant: entry.Merchant,
				Description: entry.Description, Reference: entry.Reference, TransactionCode: entry.TransactionCode,
				Status: "BOOKED", CreatedAt: now,
			}
			inserted, err := repos.BankImports.InsertEntry(ctx, entryRecord)
			if err != nil {
				return err
			}
			if !inserted {
				duplicates++
				continue
			}
			suggestionID, err := s.newID()
			if err != nil {
				return err
			}
			amount, err := domain.NewPositiveAmountFromMinorUnits(entry.AmountMinor, householdRecord.Currency)
			if err != nil {
				return err
			}
			merchant := strings.TrimSpace(entry.Merchant)
			if merchant == "" {
				merchant = strings.TrimSpace(entry.Description)
			}
			if merchant == "" {
				merchant = "Unknown"
			}
			suggestion, err := domain.NewImportedSuggestion(domain.SuggestionId(suggestionID), domain.TransactionSourceExternalEntry, amount, merchant, domain.ImportProvenance{
				AccountID: accountID, EffectiveDate: effectiveDate, Direction: domain.SuggestionDirection(entry.Direction),
				Reference: entry.Reference, RawDescription: entry.Description, BatchID: batchID, Fingerprint: fingerprint,
			})
			if err != nil {
				return err
			}
			if err := repos.Suggestions.Create(ctx, household, suggestion); err != nil {
				return err
			}
			if err := repos.BankImports.AttachSuggestion(ctx, household, entryID, suggestionID); err != nil {
				return err
			}
			imported++
		}
		failed := len(statement.Failures) + len(validation)
		result.Failures = mergeImportFailures(statement, validation)
		if err := repos.BankImports.UpdateBatchCounts(ctx, household, batchID, imported, duplicates, failed); err != nil {
			return err
		}
		batch.RowsImported = imported
		batch.RowsDuplicate = duplicates
		batch.RowsFailed = failed
		result.Batch = batch
		return nil
	})
	if err != nil {
		return ImportCommitResult{}, err
	}
	return result, nil
}

// ListBatches returns the household's import history to any active member.
func (s *ImportService) ListBatches(ctx context.Context, actor domain.UserId, household domain.HouseholdId) ([]ports.BankImportBatchRecord, error) {
	var batches []ports.BankImportBatchRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		var err error
		batches, err = repos.BankImports.ListBatches(ctx, household)
		return err
	})
	return batches, err
}

func (s *ImportService) newID() (string, error) {
	id, err := s.ids.NewID()
	if err != nil {
		return "", err
	}
	if !isUUIDText(id) {
		return "", domain.NewError(domain.CodeInvalidValue, "generated id is not a UUID")
	}
	return strings.ToLower(id), nil
}

func validateImportEntry(entry imports.Entry, householdCurrency domain.Currency, effectiveDate, now time.Time) string {
	if entry.Currency != householdCurrency.Code() {
		return "currency does not match household currency"
	}
	if entry.AmountMinor <= 0 || entry.AmountMinor > domain.MaxMinorUnits {
		return "amount must be greater than zero and within the supported range"
	}
	if effectiveDate.IsZero() || effectiveDate.Year() < 2000 || effectiveDate.After(dateOnly(now).Add(24*time.Hour)) {
		return "effective date is outside the supported range"
	}
	if direction, err := domain.ParseSuggestionDirection(string(entry.Direction)); err != nil || !direction.IsValid() {
		return "direction is invalid"
	}
	return ""
}

func importEntryFailure(entry imports.Entry, reason string) ImportFailure {
	bookedDate := entry.BookedDate
	valueDate := entry.ValueDate
	amount := entry.AmountMinor
	externalID := entry.ExternalID
	return ImportFailure{
		RowIndex: entry.RowIndex, Reason: reason,
		ExternalID: &externalID, BookedDate: &bookedDate, ValueDate: &valueDate,
		Direction: entry.Direction, AmountMinor: &amount, Currency: entry.Currency,
		Merchant: entry.Merchant, Description: entry.Description,
		Reference: entry.Reference, TransactionCode: entry.TransactionCode,
	}
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func dateOnly(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func optionalImportDate(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	date := dateOnly(value)
	return &date
}
