# PostgreSQL adapter

This package implements the persistence ports with GORM and PostgreSQL. The
reviewed SQL files in `migrations/` are the schema authority; the adapter never
runs `AutoMigrate`.

## Integration tests

Every test in this package skips when no database is configured. Run the suite
against a disposable PostgreSQL 18 instance:

```sh
docker run --rm -d --name family-budget-test-postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=family_budget \
  -p 55432:5432 \
  postgres:18

TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:55432/family_budget?sslmode=disable' \
  go test ./internal/adapters/postgres/... -count=1
```

Stop and remove the container afterwards:

```sh
docker rm -f family-budget-test-postgres
```

Each test creates its own uniquely named schema, applies the migration, and
drops the schema on cleanup, so the suite can run repeatedly against the same
database and in parallel packages. Tests that need a database skip with a clear
message when `TEST_POSTGRES_DSN` (or `DATABASE_URL`) is unset, which keeps a
database-less `go test ./...` build green.

The suite covers the MVP quality gate: migration/constraint application,
CHECK/unique enforcement, household-scoped foreign keys, command idempotency and
rollback, the reservation hold lifecycle, concurrent spending/retry behavior,
and projection-rebuild verification (`projection.go`). The migration upgrade
test applies the ordered migration set through a `goose_db_version`-style
tracker rather than the Goose binary; it asserts a second run applies zero
versions without error and leaves the schema unchanged.

## Projection verification

`RebuildLedgerFromJournal` replays the journal through the port and the domain's
reference `RebuildLedger`. `VerifyProjections` compares that rebuild with every
balance the adapter derives in SQL (account and envelope balances, Qe/Qa,
budgetable cash, envelope sum, Ready-to-assign, spendables and card liability)
plus the materialized `households.ledger_version`. Any mismatch is
`ACCOUNTING_INVARIANT_VIOLATION`, per POSTING_MATRIX §6.

## Configuration

- `TEST_POSTGRES_DSN` (preferred) or `DATABASE_URL` enables integration tests.
- No docker-compose file is added to the repository; the command above is the
  documented disposable database.
