package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// Preview verifies household membership, then parses an in-memory statement.
func (s *ImportService) Preview(ctx context.Context, actor domain.UserId, household domain.HouseholdId, filename string, data []byte) (imports.Statement, error) {
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		_, err := authorizeMember(ctx, repos, household, actor)
		return err
	})
	if err != nil {
		return imports.Statement{}, err
	}
	return s.parser.Parse(filename, data)
}

// ImportFailure describes a parsed row rejected without persisting its details.
type ImportFailure struct {
	RowIndex int
	Reason   string
}

// ImportCommitResult is the durable result of committing one statement.
type ImportCommitResult struct {
	Batch    ports.BankImportBatchRecord
	Failures []ImportFailure
}

// Commit persists an import batch and creates reviewable suggestions for its
// valid, previously unseen rows in one transaction.
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
			RowsTotal:   len(statement.Entries),
			CreatedBy:   actor,
			CreatedAt:   now,
		}
		if err := repos.BankImports.CreateBatch(ctx, batch); err != nil {
			return err
		}
		result.Failures = make([]ImportFailure, 0)
		imported, duplicates, failed := 0, 0, 0
		for index, entry := range statement.Entries {
			effectiveDate := dateOnly(entry.BookedDate)
			reason := validateImportEntry(entry, householdRecord.Currency, effectiveDate, now)
			if reason != "" {
				failed++
				result.Failures = append(result.Failures, ImportFailure{RowIndex: index, Reason: reason})
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
		return fmt.Sprintf("currency %s does not match household currency", entry.Currency)
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
