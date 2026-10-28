package postgres

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// testStore returns a Store bound to a fresh, disposable PostgreSQL schema with
// the reviewed migration applied. It skips the test when no DSN is configured:
// integration tests must never fail a build or CI job that lacks a database.
func testStore(t *testing.T) *Store {
	t.Helper()

	store := testStoreWithoutMigration(t)
	applyMigration(t, store.DB())
	return store
}

// testStoreWithoutMigration returns an empty disposable schema. The migration
// test uses it to apply the ordered migration set itself, including Gooselike
// version tracking.
func testStoreWithoutMigration(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN (or DATABASE_URL) to run PostgreSQL integration tests")
	}

	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatalf("access PostgreSQL connection pool: %v", err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })

	schema := newTestSchemaName(t)
	if err := admin.Exec(fmt.Sprintf(`CREATE SCHEMA %s`, quoteIdentifier(schema))).Error; err != nil {
		t.Fatalf("create disposable schema: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, quoteIdentifier(schema))).Error
	})

	store, err := Open(withSearchPath(dsn, schema), PoolConfig{
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("open adapter store: %v", err)
	}
	storeSQL, err := store.DB().DB()
	if err != nil {
		t.Fatalf("access adapter connection pool: %v", err)
	}
	t.Cleanup(func() { _ = storeSQL.Close() })

	return store
}

// newTestSchemaName builds a safe, unique schema name.
func newTestSchemaName(t *testing.T) string {
	t.Helper()
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("generate schema suffix: %v", err)
	}
	return "fb_test_" + hex.EncodeToString(raw[:])
}

// quoteIdentifier quotes a generated identifier. The adapter's test schema
// names contain only lowercase hex and underscores, so this is defensive.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// withSearchPath adds the disposable schema to the connection's search_path.
func withSearchPath(dsn, schema string) string {
	if strings.Contains(dsn, "://") {
		separator := "&"
		if !strings.Contains(dsn, "?") {
			separator = "?"
		}
		return dsn + separator + "search_path=" + schema
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}

// migrationDirectory returns the repository migrations directory relative to
// this package's test working directory.
func migrationDirectory() string {
	return filepath.Join("..", "..", "..", "migrations")
}

// readMigrationUp returns the Up section of one reviewed migration file.
func readMigrationUp(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(migrationDirectory(), name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", path, err)
	}
	raw := string(content)
	upStart := strings.Index(raw, "-- +goose Up")
	downStart := strings.Index(raw, "-- +goose Down")
	if upStart < 0 || downStart < 0 || downStart < upStart {
		t.Fatalf("migration %s is missing Goose sections", path)
	}
	return raw[upStart+len("-- +goose Up") : downStart]
}

// applyMigration executes the Goose Up section of the reviewed migration. The
// schema is the authority, so the test runs the real SQL rather than GORM
// AutoMigrate.
func applyMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	executeMigrationUp(t, db, readMigrationUp(t, "0001_initial_schema.sql"))
}

// executeMigrationUp runs every statement of an Up section.
func executeMigrationUp(t *testing.T, db *gorm.DB, up string) {
	t.Helper()

	for _, statement := range splitSQLStatements(up) {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("apply migration statement: %v\n%s", err, statement)
		}
	}
}

// splitSQLStatements splits a Goose Up section on statement terminators. It
// ignores semicolons inside `--` line comments and single-quoted string
// literals: the migration's prose comments and CHECK constraints contain both,
// and splitting naively on every semicolon feeds comment fragments to the
// server as SQL.
func splitSQLStatements(up string) []string {
	var statements []string
	var current strings.Builder
	inLineComment := false
	inString := false
	for i := 0; i < len(up); i++ {
		c := up[i]
		if inLineComment {
			current.WriteByte(c)
			if c == '\n' {
				inLineComment = false
			}
			continue
		}
		if inString {
			current.WriteByte(c)
			if c == '\'' {
				if i+1 < len(up) && up[i+1] == '\'' {
					// A doubled quote is an escaped quote, not the end of the literal.
					current.WriteByte(up[i+1])
					i++
				} else {
					inString = false
				}
			}
			continue
		}
		switch c {
		case '-':
			if i+1 < len(up) && up[i+1] == '-' {
				inLineComment = true
				current.WriteString("--")
				i++
				continue
			}
			current.WriteByte(c)
		case '\'':
			inString = true
			current.WriteByte(c)
		case ';':
			if statement := strings.TrimSpace(current.String()); statement != "" {
				statements = append(statements, statement)
			}
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}
	if statement := strings.TrimSpace(current.String()); statement != "" {
		statements = append(statements, statement)
	}
	return statements
}

// seed is the standard test fixture: a user, household, cash account and
// envelope in one household.
type seed struct {
	userID      domain.UserId
	householdID domain.HouseholdId
	accountID   domain.AccountId
	envelopeID  domain.EnvelopeId
	currency    domain.Currency
}

func seedHousehold(t *testing.T, repos ports.Repositories, suffix string) seed {
	t.Helper()

	currency, err := domain.NewCurrency("USD", 2)
	if err != nil {
		t.Fatalf("currency: %v", err)
	}
	generator := UUIDGenerator{}

	newID := func() string {
		id, err := generator.NewID()
		if err != nil {
			t.Fatalf("id: %v", err)
		}
		return id
	}

	userID := domain.UserId(newID())
	if err := repos.Users.Create(t.Context(), ports.UserRecord{
		ID:           userID,
		Email:        "owner-" + suffix + "@example.test",
		PasswordHash: "unusable-test-hash",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	householdID := domain.HouseholdId(newID())
	if err := repos.Households.Create(t.Context(), ports.HouseholdRecord{
		ID:       householdID,
		Name:     "Household " + suffix,
		Currency: currency,
		Timezone: "UTC",
		Status:   ports.LifecycleActive,
	}); err != nil {
		t.Fatalf("create household: %v", err)
	}

	if err := repos.Memberships.Add(t.Context(), householdID, ports.MembershipRecord{
		UserID: userID,
		Role:   ports.MembershipRoleOwner,
		Status: ports.MembershipStatusActive,
	}); err != nil {
		t.Fatalf("add membership: %v", err)
	}

	accountID := domain.AccountId(newID())
	if err := repos.Accounts.Create(t.Context(), householdID, ports.AccountRecord{
		ID:             accountID,
		Name:           "Checking " + suffix,
		Kind:           domain.AccountKindChecking,
		BudgetEligible: true,
		Status:         ports.LifecycleActive,
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}

	envelopeID := domain.EnvelopeId(newID())
	if err := repos.Envelopes.Create(t.Context(), householdID, ports.EnvelopeRecord{
		ID:     envelopeID,
		Name:   "Groceries " + suffix,
		Status: ports.LifecycleActive,
	}); err != nil {
		t.Fatalf("create envelope: %v", err)
	}

	return seed{
		userID:      userID,
		householdID: householdID,
		accountID:   accountID,
		envelopeID:  envelopeID,
		currency:    currency,
	}
}

// positiveAmount is a test helper for a positive amount.
func testPositiveAmount(t *testing.T, minor int64, currency domain.Currency) domain.PositiveAmount {
	t.Helper()
	amount, err := domain.NewPositiveAmountFromMinorUnits(minor, currency)
	if err != nil {
		t.Fatalf("positive amount: %v", err)
	}
	return amount
}

// signedDelta is a test helper for a signed posting delta.
func testSignedDelta(t *testing.T, minor int64, currency domain.Currency) domain.SignedMoneyDelta {
	t.Helper()
	delta, err := domain.NewSignedMoneyDelta(minor, currency)
	if err != nil {
		t.Fatalf("signed delta: %v", err)
	}
	return delta
}

// mustID returns a generated UUID string or fails the test.
func mustID(t *testing.T) string {
	t.Helper()
	id, err := (UUIDGenerator{}).NewID()
	if err != nil {
		t.Fatalf("generate id: %v", err)
	}
	return id
}

// journalTime returns a timestamp truncated to PostgreSQL's microsecond
// resolution so round-trip equality assertions are exact.
func journalTime() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// newJournalOperation builds a committed operation for adapter tests.
func newJournalOperation(
	t *testing.T,
	s seed,
	opID domain.JournalOperationId,
	key domain.IdempotencyKey,
	fingerprint domain.RequestFingerprint,
	opType domain.OperationType,
	effectiveDate domain.EffectiveDate,
	amount *domain.PositiveAmount,
	effects domain.Effects,
) domain.JournalOperation {
	t.Helper()
	created := journalTime()
	committed := created.Add(time.Millisecond)
	operation, err := domain.NewJournalOperation(domain.JournalOperationParams{
		ID:                 opID,
		Household:          s.householdID,
		Actor:              s.userID,
		Type:               opType,
		Source:             domain.TransactionSourceManual,
		EffectiveDate:      effectiveDate,
		Currency:           s.currency,
		IdempotencyKey:     key,
		RequestFingerprint: fingerprint,
		Amount:             amount,
		CreatedAt:          domain.NewRecordedAt(created),
		CommittedAt:        domain.NewRecordedAt(committed),
		Effects:            effects,
	})
	if err != nil {
		t.Fatalf("new journal operation: %v", err)
	}
	return operation
}

// wantCode fails unless err carries the expected domain code.
func wantCode(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected domain error %s, got nil", code)
	}
	if actual := domain.CodeOf(err); actual != code {
		t.Fatalf("expected domain code %s, got %s (%v)", code, actual, err)
	}
}
