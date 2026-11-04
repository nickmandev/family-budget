package postgres

import (
	"testing"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

func TestBankImportRepositoryDeduplicatesEntriesAndRoundTripsBatches(t *testing.T) {
	store := testStore(t)
	s := seedHousehold(t, store.Repositories(), "bank-import")
	batchID := mustID(t)
	periodFrom := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	periodTo := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	createdAt := journalTime()
	batch := ports.BankImportBatchRecord{
		ID: batchID, HouseholdID: s.householdID, AccountID: s.accountID,
		BankCode: "fibank", Format: "xml", FileName: "statement.xml", FileSHA256: "abc",
		PeriodFrom: &periodFrom, PeriodTo: &periodTo, RowsTotal: 1,
		CreatedBy: s.userID, CreatedAt: createdAt,
	}
	repo := store.Repositories().BankImports
	if err := repo.CreateBatch(t.Context(), batch); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	entry := ports.BankImportEntryRecord{
		ID: mustID(t), BatchID: batchID, HouseholdID: s.householdID, AccountID: s.accountID,
		Fingerprint: "fibank|entry-1", ExternalID: "entry-1", EffectiveDate: periodFrom,
		ValueDate: &periodFrom, Direction: domain.SuggestionDirectionDebit, AmountMinor: 123,
		Currency: "USD", Merchant: "Merchant", Description: "Description", Reference: "ref",
		TransactionCode: "POS", Status: "BOOKED", CreatedAt: createdAt,
	}
	inserted, err := repo.InsertEntry(t.Context(), entry)
	if err != nil || !inserted {
		t.Fatalf("first InsertEntry = %t, %v; want inserted", inserted, err)
	}
	entry.ID = mustID(t)
	inserted, err = repo.InsertEntry(t.Context(), entry)
	if err != nil || inserted {
		t.Fatalf("duplicate InsertEntry = %t, %v; want not inserted", inserted, err)
	}
	if err := repo.UpdateBatchCounts(t.Context(), s.householdID, batchID, 1, 0, 0); err != nil {
		t.Fatalf("update batch counts: %v", err)
	}
	got, err := repo.GetBatch(t.Context(), s.householdID, batchID)
	if err != nil {
		t.Fatalf("get batch: %v", err)
	}
	if got.RowsTotal != 1 || got.RowsImported != 1 || got.RowsDuplicate != 0 || got.RowsFailed != 0 || got.FileName != batch.FileName || !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("round-trip batch = %+v", got)
	}
	if got.PeriodFrom == nil || !got.PeriodFrom.Equal(periodFrom) || got.PeriodTo == nil || !got.PeriodTo.Equal(periodTo) {
		t.Fatalf("round-trip periods = %v / %v", got.PeriodFrom, got.PeriodTo)
	}
	listed, err := repo.ListBatches(t.Context(), s.householdID)
	if err != nil || len(listed) != 1 || listed[0].ID != batchID {
		t.Fatalf("list batches = %+v, %v", listed, err)
	}
}
