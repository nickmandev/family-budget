package application

import (
	"context"
	"fmt"
	"testing"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/imports"
	"family-budget/internal/ports"
)

func TestImportCommitDeduplicatesAndPreservesCancelledSuggestion(t *testing.T) {
	entries := []imports.Entry{
		{ExternalID: "e1", BookedDate: importTestDate(2), ValueDate: importTestDate(1), Direction: imports.DirectionDebit, AmountMinor: 100, Currency: "USD", Merchant: "Shop", Description: "Purchase", Reference: "r1"},
		{ExternalID: "e2", BookedDate: importTestDate(3), ValueDate: importTestDate(2), Direction: imports.DirectionCredit, AmountMinor: 200, Currency: "USD", Description: "Refund", Reference: "r2"},
	}
	service, store, actor, household, account := newImportTestService(t, imports.Statement{Bank: "fibank", Format: "xml", Entries: entries})
	first, err := service.Commit(context.Background(), actor, household, account, "statement.xml", []byte("one"))
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if first.Batch.RowsTotal != 2 || first.Batch.RowsImported != 2 || first.Batch.RowsDuplicate != 0 || first.Batch.RowsFailed != 0 || len(first.Failures) != 0 {
		t.Fatalf("first result = %+v", first)
	}
	suggestions := store.suggestions.all()
	if len(suggestions) != 2 {
		t.Fatalf("suggestion count = %d, want 2", len(suggestions))
	}
	for i, suggestion := range suggestions {
		provenance, ok := suggestion.Provenance()
		if !ok || provenance.AccountID != account || provenance.BatchID != first.Batch.ID || provenance.Fingerprint == "" || provenance.EffectiveDate.IsZero() {
			t.Fatalf("suggestion %d provenance = %+v, ok=%t", i, provenance, ok)
		}
		if suggestion.Source() != domain.TransactionSourceExternalEntry {
			t.Fatalf("suggestion source = %s", suggestion.Source())
		}
	}
	cancelledID := suggestions[0].ID()
	cancelled, err := store.suggestions.Get(context.Background(), household, cancelledID)
	if err != nil {
		t.Fatal(err)
	}
	if err := cancelled.Cancel("dismissed"); err != nil {
		t.Fatal(err)
	}
	if err := store.suggestions.Save(context.Background(), household, cancelled, domain.SuggestionStateSuggested); err != nil {
		t.Fatal(err)
	}
	second, err := service.Commit(context.Background(), actor, household, account, "statement.xml", []byte("one"))
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if second.Batch.RowsImported != 0 || second.Batch.RowsDuplicate != 2 || second.Batch.RowsFailed != 0 {
		t.Fatalf("second counts = %+v", second.Batch)
	}
	if got := len(store.suggestions.all()); got != 2 {
		t.Fatalf("suggestion count after re-commit = %d, want 2", got)
	}
	stillCancelled, err := store.suggestions.Get(context.Background(), household, cancelledID)
	if err != nil || stillCancelled.State() != domain.SuggestionStateCancelled {
		t.Fatalf("cancelled suggestion after re-commit = %v, %v", stillCancelled, err)
	}
}

func TestImportCommitRejectsCurrencyMismatchWithoutSuggestion(t *testing.T) {
	entry := imports.Entry{ExternalID: "bad-currency", BookedDate: importTestDate(2), ValueDate: importTestDate(1), Direction: imports.DirectionDebit, AmountMinor: 100, Currency: "EUR"}
	service, store, actor, household, account := newImportTestService(t, imports.Statement{Bank: "fibank", Format: "xml", Entries: []imports.Entry{entry}})
	result, err := service.Commit(context.Background(), actor, household, account, "statement.xml", []byte("data"))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Batch.RowsTotal != 1 || result.Batch.RowsFailed != 1 || result.Batch.RowsImported != 0 || len(result.Failures) != 1 || result.Failures[0].RowIndex != 0 || result.Failures[0].Reason != "currency EUR does not match household currency" {
		t.Fatalf("result = %+v", result)
	}
	if got := len(store.suggestions.all()); got != 0 {
		t.Fatalf("suggestion count = %d, want 0", got)
	}
}

func TestImportCommitUsesUnknownMerchantForSparseEntry(t *testing.T) {
	entry := imports.Entry{ExternalID: "sparse", BookedDate: importTestDate(2), ValueDate: importTestDate(1), Direction: imports.DirectionDebit, AmountMinor: 100, Currency: "USD"}
	service, store, actor, household, account := newImportTestService(t, imports.Statement{Bank: "fibank", Format: "xml", Entries: []imports.Entry{entry}})
	result, err := service.Commit(context.Background(), actor, household, account, "statement.xml", []byte("data"))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Batch.RowsImported != 1 || result.Batch.RowsFailed != 0 {
		t.Fatalf("batch = %+v, want one imported row and no failures", result.Batch)
	}
	suggestions := store.suggestions.all()
	if len(suggestions) != 1 {
		t.Fatalf("suggestions = %d, want 1", len(suggestions))
	}
	if suggestions[0].Merchant() != "Unknown" {
		t.Errorf("suggestion merchant = %q, want Unknown", suggestions[0].Merchant())
	}
}

func TestImportCommitUsesStableFallbackFingerprintWithoutExternalID(t *testing.T) {
	entry := imports.Entry{BookedDate: importTestDate(5), ValueDate: importTestDate(5), Direction: imports.DirectionDebit, AmountMinor: 100, Currency: "USD", Merchant: "  Corner   Shop ", Description: " Test  purchase ", Reference: "REF"}
	service, store, actor, household, account := newImportTestService(t, imports.Statement{Bank: "fibank", Format: "xml", Entries: []imports.Entry{entry}})
	first, err := service.Commit(context.Background(), actor, household, account, "statement.xml", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	created := store.suggestions.all()
	fingerprint, ok := created[0].Provenance()
	if !ok || fingerprint.Fingerprint == "" {
		t.Fatalf("missing fingerprint provenance: %+v", fingerprint)
	}
	second, err := service.Commit(context.Background(), actor, household, account, "statement.xml", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Batch.RowsImported != 1 || second.Batch.RowsDuplicate != 1 || len(store.suggestions.all()) != 1 {
		t.Fatalf("fallback re-commit counts = first %+v, second %+v", first.Batch, second.Batch)
	}
}

func TestImportCommitMergesParserAndValidationFailures(t *testing.T) {
	entries := []imports.Entry{
		{RowIndex: 1, ExternalID: "e1", BookedDate: importTestDate(2), ValueDate: importTestDate(1), Direction: imports.DirectionDebit, AmountMinor: 100, Currency: "USD"},
		{RowIndex: 3, ExternalID: "bad", BookedDate: importTestDate(2), ValueDate: importTestDate(1), Direction: imports.DirectionDebit, AmountMinor: 200, Currency: "EUR"},
		{RowIndex: 6, ExternalID: "e3", BookedDate: importTestDate(3), ValueDate: importTestDate(2), Direction: imports.DirectionCredit, AmountMinor: 300, Currency: "USD"},
	}
	statement := imports.Statement{Bank: "fibank", Format: "xml", Entries: entries, Failures: []imports.ParseFailure{
		{RowIndex: 2, Reason: "amount is unparseable"},
		{RowIndex: 4, Reason: "debit/credit indicator is unknown"},
	}}
	service, _, actor, household, account := newImportTestService(t, statement)
	result, err := service.Commit(context.Background(), actor, household, account, "statement.xml", []byte("data"))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Batch.RowsTotal != 5 || result.Batch.RowsImported != 2 || result.Batch.RowsDuplicate != 0 || result.Batch.RowsFailed != 3 {
		t.Fatalf("batch = %+v, want total 5 (3 entries + 2 parser failures), imported 2, failed 3", result.Batch)
	}
	want := []ImportFailure{
		{RowIndex: 2, Reason: "amount is unparseable"},
		{RowIndex: 3, Reason: "currency EUR does not match household currency"},
		{RowIndex: 4, Reason: "debit/credit indicator is unknown"},
	}
	if len(result.Failures) != len(want) {
		t.Fatalf("failures = %+v, want %+v", result.Failures, want)
	}
	for i, failure := range result.Failures {
		if failure != want[i] {
			t.Errorf("failure %d = %+v, want %+v", i, failure, want[i])
		}
	}
}

func TestImportPreviewExposesMergedFailures(t *testing.T) {
	entry := imports.Entry{RowIndex: 1, ExternalID: "e1", BookedDate: importTestDate(2), ValueDate: importTestDate(1), Direction: imports.DirectionDebit, AmountMinor: 100, Currency: "EUR"}
	statement := imports.Statement{Bank: "fibank", Format: "xml", Entries: []imports.Entry{entry}, Failures: []imports.ParseFailure{
		{RowIndex: 0, Reason: "transaction type is unknown"},
	}}
	service, _, actor, household, _ := newImportTestService(t, statement)
	preview, err := service.Preview(context.Background(), actor, household, "statement.xml", []byte("data"))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Statement.Entries) != 1 {
		t.Fatalf("preview entries = %d, want 1", len(preview.Statement.Entries))
	}
	want := []ImportFailure{
		{RowIndex: 0, Reason: "transaction type is unknown"},
		{RowIndex: 1, Reason: "currency EUR does not match household currency"},
	}
	if len(preview.Failures) != len(want) || preview.Failures[0] != want[0] || preview.Failures[1] != want[1] {
		t.Fatalf("preview failures = %+v, want %+v", preview.Failures, want)
	}
}

func importTestDate(day int) time.Time {
	return time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC)
}

func newImportTestService(t *testing.T, statement imports.Statement) (*ImportService, *importTestStore, domain.UserId, domain.HouseholdId, domain.AccountId) {
	t.Helper()
	actor := domain.UserId("00000000-0000-4000-8000-000000000001")
	household := domain.HouseholdId("00000000-0000-4000-8000-000000000002")
	account := domain.AccountId("00000000-0000-4000-8000-000000000003")
	currency, err := domain.NewCurrency("USD", 2)
	if err != nil {
		t.Fatal(err)
	}
	store := &importTestStore{
		household: ports.HouseholdRecord{ID: household, Currency: currency},
		actor:     actor, account: account,
		batches: make(map[string]ports.BankImportBatchRecord), entries: make(map[string]ports.BankImportEntryRecord),
		suggestions: &importTestSuggestions{items: make(map[domain.SuggestionId]*domain.Suggestion)},
	}
	parser := importTestParser{statement: statement}
	clock := importTestClock{now: importTestDate(28)}
	service, err := NewImportService(store, parser, &importTestIDs{}, clock)
	if err != nil {
		t.Fatalf("NewImportService: %v", err)
	}
	return service, store, actor, household, account
}

type importTestParser struct{ statement imports.Statement }

func (p importTestParser) Parse(string, []byte) (imports.Statement, error) { return p.statement, nil }

type importTestClock struct{ now time.Time }

func (c importTestClock) Now() time.Time { return c.now }

type importTestIDs struct{ next int }

func (g *importTestIDs) NewID() (string, error) {
	g.next++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", g.next), nil
}

type importTestStore struct {
	household   ports.HouseholdRecord
	actor       domain.UserId
	account     domain.AccountId
	batches     map[string]ports.BankImportBatchRecord
	entries     map[string]ports.BankImportEntryRecord
	suggestions *importTestSuggestions
}

func (s *importTestStore) WithinTransaction(ctx context.Context, fn func(context.Context, ports.Repositories) error) error {
	return fn(ctx, ports.Repositories{
		Households: importTestHouseholds{s}, Memberships: importTestMemberships{s}, Accounts: importTestAccounts{s},
		BankImports: importTestBankImports{s}, Suggestions: s.suggestions,
	})
}

type importTestHouseholds struct{ store *importTestStore }

func (r importTestHouseholds) Create(context.Context, ports.HouseholdRecord) error { return nil }
func (r importTestHouseholds) Get(context.Context, domain.HouseholdId) (ports.HouseholdRecord, error) {
	return r.store.household, nil
}
func (r importTestHouseholds) SaveSettings(context.Context, ports.HouseholdRecord) error { return nil }

type importTestMemberships struct{ store *importTestStore }

func (r importTestMemberships) Add(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}
func (r importTestMemberships) Get(_ context.Context, _ domain.HouseholdId, user domain.UserId) (ports.MembershipRecord, error) {
	if user != r.store.actor {
		return ports.MembershipRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return ports.MembershipRecord{UserID: user, Status: ports.MembershipStatusActive}, nil
}
func (r importTestMemberships) List(context.Context, domain.HouseholdId) ([]ports.MembershipRecord, error) {
	return nil, nil
}
func (r importTestMemberships) ListForUser(context.Context, domain.UserId) ([]ports.UserMembershipRecord, error) {
	return nil, nil
}
func (r importTestMemberships) Save(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}

type importTestAccounts struct{ store *importTestStore }

func (r importTestAccounts) Create(context.Context, domain.HouseholdId, ports.AccountRecord) error {
	return nil
}
func (r importTestAccounts) Get(_ context.Context, household domain.HouseholdId, id domain.AccountId) (ports.AccountRecord, error) {
	if household != r.store.household.ID || id != r.store.account {
		return ports.AccountRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return ports.AccountRecord{ID: id}, nil
}
func (r importTestAccounts) List(context.Context, domain.HouseholdId, ports.AccountFilter) ([]ports.AccountRecord, error) {
	return nil, nil
}
func (r importTestAccounts) Save(context.Context, domain.HouseholdId, ports.AccountRecord) error {
	return nil
}
func (r importTestAccounts) HasActiveReservations(context.Context, domain.HouseholdId, domain.AccountId) (bool, error) {
	return false, nil
}

type importTestBankImports struct{ store *importTestStore }

func (r importTestBankImports) CreateBatch(_ context.Context, batch ports.BankImportBatchRecord) error {
	r.store.batches[batch.ID] = batch
	return nil
}
func (r importTestBankImports) UpdateBatchCounts(_ context.Context, _ domain.HouseholdId, id string, imported, duplicate, failed int) error {
	batch := r.store.batches[id]
	batch.RowsImported, batch.RowsDuplicate, batch.RowsFailed = imported, duplicate, failed
	r.store.batches[id] = batch
	return nil
}
func (r importTestBankImports) InsertEntry(_ context.Context, entry ports.BankImportEntryRecord) (bool, error) {
	key := entry.HouseholdID.String() + "/" + entry.AccountID.String() + "/" + entry.Fingerprint
	if _, exists := r.store.entries[key]; exists {
		return false, nil
	}
	r.store.entries[key] = entry
	return true, nil
}
func (r importTestBankImports) AttachSuggestion(_ context.Context, _ domain.HouseholdId, entryID, suggestionID string) error {
	for key, entry := range r.store.entries {
		if entry.ID == entryID {
			id := suggestionID
			entry.SuggestionID = &id
			r.store.entries[key] = entry
			return nil
		}
	}
	return domain.ErrEntityNotFoundOrNotInHousehold
}
func (r importTestBankImports) ListBatches(_ context.Context, household domain.HouseholdId) ([]ports.BankImportBatchRecord, error) {
	var batches []ports.BankImportBatchRecord
	for _, batch := range r.store.batches {
		if batch.HouseholdID == household {
			batches = append(batches, batch)
		}
	}
	return batches, nil
}
func (r importTestBankImports) GetBatch(_ context.Context, household domain.HouseholdId, id string) (ports.BankImportBatchRecord, error) {
	batch, ok := r.store.batches[id]
	if !ok || batch.HouseholdID != household {
		return ports.BankImportBatchRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return batch, nil
}

type importTestSuggestions struct {
	items map[domain.SuggestionId]*domain.Suggestion
}

func (r *importTestSuggestions) Create(_ context.Context, _ domain.HouseholdId, suggestion *domain.Suggestion) error {
	r.items[suggestion.ID()] = suggestion
	return nil
}
func (r *importTestSuggestions) Get(_ context.Context, _ domain.HouseholdId, id domain.SuggestionId) (*domain.Suggestion, error) {
	suggestion, ok := r.items[id]
	if !ok {
		return nil, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return suggestion, nil
}
func (r *importTestSuggestions) List(context.Context, domain.HouseholdId) ([]*domain.Suggestion, error) {
	return r.all(), nil
}
func (r *importTestSuggestions) all() []*domain.Suggestion {
	suggestions := make([]*domain.Suggestion, 0, len(r.items))
	for _, suggestion := range r.items {
		suggestions = append(suggestions, suggestion)
	}
	return suggestions
}
func (r *importTestSuggestions) Save(_ context.Context, _ domain.HouseholdId, suggestion *domain.Suggestion, _ domain.SuggestionState) error {
	r.items[suggestion.ID()] = suggestion
	return nil
}
