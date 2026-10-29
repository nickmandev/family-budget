# PostgreSQL adapter

This package implements the persistence ports with GORM and PostgreSQL. The
reviewed SQL files in `migrations/` are the schema authority; the adapter never
runs `AutoMigrate`.

## Integration tests

Every test in this package skips when no database is configured. The runner
provisions a disposable PostgreSQL, exports the `TEST_POSTGRES_*` connection
components, runs the suite and tears the database down again:

```sh
scripts/integration-test.sh          # adapter integration suite
scripts/integration-test.sh --all --race
```

It prefers Docker, then Podman, then a local `initdb` cluster, and falls back
to host networking when rootless Docker cannot publish ports. `--keep-db`
leaves the server running and prints the `TEST_POSTGRES_*` components; `--help`
lists every option.

To use an already-running PostgreSQL instead, set the components yourself
(pinned image matches `compose.yaml`):

```sh
docker run --rm -d --name family-budget-test-postgres \
  -e POSTGRES_USER=test -e POSTGRES_PASSWORD=test -e POSTGRES_DB=test \
  -p 127.0.0.1:55432:5432 postgres:18.6

TEST_POSTGRES_HOST=127.0.0.1 TEST_POSTGRES_PORT=55432 \
TEST_POSTGRES_USER=test TEST_POSTGRES_PASSWORD=test \
TEST_POSTGRES_DB=test TEST_POSTGRES_SSLMODE=disable \
  go test ./internal/adapters/postgres/... -count=1

docker rm -f family-budget-test-postgres
```

Each test creates its own uniquely named schema, applies the migration, and
drops the schema on cleanup, so the suite can run repeatedly against the same
database and in parallel packages. Tests that need a database skip with a clear
message when the `TEST_POSTGRES_*` components are unset, which keeps a
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

The test harness reads the `TEST_`-prefixed mirror of the application connection
components and builds the DSN with the same `config.DatabaseConfig.DSN` helper
the server and migrate entrypoint use:

- `TEST_POSTGRES_HOST`, `TEST_POSTGRES_PORT`, `TEST_POSTGRES_USER`,
  `TEST_POSTGRES_PASSWORD`, `TEST_POSTGRES_DB`, `TEST_POSTGRES_SSLMODE`.
- `scripts/integration-test.sh` is the documented disposable database and sets
  these for you. The production `compose.yaml` database stays internal-only and
  is not used by the tests.
