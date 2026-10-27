# Stack Decisions — Household Budgeting MVP

**Status:** Go, GORM, PostgreSQL 18.x, and the standard library `net/http` router are confirmed by the product owner. The accounting contract remains framework-neutral; this document records the selected implementation stack and remaining implementation choices.

## 1. Decisions and recommendations at a glance

| Concern | Recommendation | Status | Rationale |
|---|---|---|---|
| Language | Go | **Selected** | Strong standard library, simple deployment, static binaries, suitable for a small self-hosted API. |
| ORM | GORM | **Selected** | Requested ORM; use it for persistence and queries, not as the owner of accounting rules. |
| HTTP API | Standard library `net/http` and `http.ServeMux` | **Confirmed** | The API is initially modest, and modern Go ServeMux supports method-aware patterns and path wildcards. Avoid an extra router until it provides clear value. |
| HTTP framework alternative | Gin | Optional, not required | Useful for a familiar middleware/grouping ecosystem, but not necessary for the initial API. Do not mix Gin and standard-library routing styles without a specific reason. |
| Primary database | PostgreSQL 18.x (verify current supported stable major/minor at repository creation) | **Confirmed** | Best fit for transactional ledger writes, concurrent household edits, relational constraints, row locking, and robust integration tests. |
| Lightweight database alternative | SQLite | Not recommended as primary | Very simple to deploy and good for embedded/single-process use, but SQLite allows only one writer at a time and differs from PostgreSQL concurrency behavior. |
| Other relational alternative | MySQL/MariaDB | Viable but not preferred | GORM supports it, but it offers no clear benefit for this project's current requirements over PostgreSQL. |
| Schema changes | Versioned SQL migrations | Recommended | Reviewable, repeatable migration history for financially significant constraints and indexes. Do not rely on production `AutoMigrate` as the schema-change strategy. |
| Tests | Pure Go domain tests plus PostgreSQL integration tests | Recommended | Domain behavior should be tested without a database; persistence, constraints, transactions, locking and migrations should be tested against the actual production engine. |

## 2. Go version policy

Use the latest patched stable Go release available when the repository is initialized, then maintain security updates. As of 2026-10-27, the official Go release history lists Go 1.27.3 as released on 2026-10-19; verify the current stable patch when starting the implementation rather than copying a stale patch version into long-lived documentation.

The module's `go` directive and CI toolchain must be consistent. Commit `go.mod` and `go.sum`. CI should run formatting, `go vet`, unit tests, integration tests, and race-enabled tests where practical.

Reference: https://go.dev/doc/devel/release

## 3. Why PostgreSQL was selected

This app has a small expected data volume, but correctness depends on handling concurrent balance-changing commands safely. Two phones may attempt to spend or reallocate the same household funds at the same time. The database must support atomic transactions, constraints, reliable isolation and locking that the application can use to serialize conflicting operations.

PostgreSQL is a good default because it is a client/server relational database with row-level locking and a mature constraint system. As of 2026-10-27, PostgreSQL 18 is the newest released stable major, with 18.6 listed as a current supported minor; PostgreSQL 19 is still a release candidate. Recommend PostgreSQL 18.x and keep it on the latest supported minor release. Re-check the supported stable major at implementation time; do not use a release candidate for production.

Reference: https://www.postgresql.org/support/versioning/

### Proposed PostgreSQL deployment

- One PostgreSQL instance for the self-hosted deployment.
- One database/schema for the application; household isolation is enforced in application authorization and reinforced with query/constraint design.
- Use database transactions around each balance-changing application command.
- Lock or otherwise serialize the relevant household/account/envelope state during validation and posting. Define a consistent lock order to reduce deadlocks; retry only errors known to be retryable.
- Add database uniqueness constraints for idempotency keys and natural duplicate-prevention rules where applicable.
- Add foreign keys, CHECK constraints, and indexes for invariants that can be safely enforced by the database.
- Keep account postings, budget postings, journal/event records, reservation state and idempotency result consistent within the transaction.
- Avoid assuming a GORM model hook can make a multi-record financial operation atomic. Transaction boundaries belong in explicit application/service operations.

GORM supports PostgreSQL and SQLite, explicit transactions, and locking clauses. Its locking/query behavior must still be used inside a correctly scoped transaction and verified against PostgreSQL in integration tests.

References:
- https://gorm.io/docs/connecting_to_the_database.html
- https://gorm.io/docs/transactions.html
- https://gorm.io/docs/advanced_query.html
- https://www.postgresql.org/docs/current/explicit-locking.html

## 4. Why not SQLite as the primary database?

SQLite is a reasonable option for an embedded, single-user, low-write-concurrency app and remains valuable for certain local tools. It is not the recommended source of truth for this shared-household server because its single-writer behavior and different locking semantics can hide or alter concurrency bugs that will matter for the production ledger.

Do not use SQLite as a substitute for PostgreSQL in persistence integration tests. It is fine for isolated experiments if the tests are clearly labelled and are not treated as proof of PostgreSQL correctness.

Reference: https://www.sqlite.org/whentouse.html

## 5. HTTP layer: confirmed `net/http`, keep it thin

The first server is a JSON API for the Android client. Start with `net/http` and `http.ServeMux`; modern Go routing supports method-aware patterns and path wildcards. Organize handlers by API feature and use explicit middleware for authentication, request IDs, logging, recovery, timeouts and request-size limits.

Keep HTTP handlers thin:

1. Decode and validate transport input.
2. Authenticate and authorize the actor against the household.
3. Call an application command/query.
4. Map domain outcomes to documented HTTP responses.

Handlers must not implement money arithmetic or write ledger records directly.

Do not add Gin unless a later, explicit engineering decision demonstrates a concrete benefit. The domain and application layers must not import Gin or `net/http` types.

Reference: https://go.dev/doc/go1.22

## 6. GORM usage rules

GORM is a persistence adapter, not the domain model.

- Keep domain entities/value objects and posting calculations independent of GORM tags and persistence structs.
- Map between domain values and persistence records at repository boundaries.
- Use explicit `db.Transaction(...)` boundaries for commands that change balances.
- Do not put accounting business rules into `BeforeSave`, `AfterCreate`, or other ORM hooks.
- Do not use `Save` or implicit upserts where they could obscure a balance-changing operation's intended semantics.
- Do not use floating-point types for money. Persist integer minor units and the household currency code.
- Use database constraints for uniqueness and referential integrity, while keeping business validation in the domain/application layer.
- Use versioned SQL migrations for deployed schema changes. `AutoMigrate` may be used for disposable experiments or narrowly scoped development setup, but not as a substitute for reviewed production migrations.
- Use explicit SQL through the GORM transaction handle where required for PostgreSQL-specific constraints, row locking, or concurrency primitives. Document why a query is database-specific.

GORM's migration documentation notes that `AutoMigrate` does not delete unused columns and describes versioned migration strategies/integrations. A financially sensitive schema should have intentional migration review and a rollback/recovery plan.

Reference: https://gorm.io/docs/migration.html

## 7. Suggested code boundaries

Keep the first implementation as a modular monolith. Do not introduce microservices.

- `cmd/server`: process startup, dependency wiring, graceful shutdown.
- `internal/domain`: value objects, pure rules, entities/aggregates, posting calculations, domain errors.
- `internal/application`: commands, queries, transaction boundaries/use cases, authorization checks and idempotency orchestration.
- `internal/ports` or feature-local interfaces: repositories, clock, ID generator and transaction/unit-of-work abstractions where they provide real test value.
- `internal/adapters/postgres`: GORM persistence models, repository implementations, PostgreSQL-specific locking/queries.
- `internal/transport/http`: request/response DTOs, routing, middleware and error mapping.
- `internal/config`: environment/configuration validation.
- `migrations/`: versioned SQL migrations.

Avoid creating interfaces for every concrete type merely for layering. Introduce interfaces at genuine seams such as persistence and clocks, and keep the domain package small and straightforward.

## 8. Test strategy

1. **Pure domain unit tests:** posting calculations, allocation constraints, state transitions, error categories, balance identities and rounding/overflow checks.
2. **Property/sequence tests:** generate valid and invalid operation sequences; after every committed operation verify the balance identities and that projections can be rebuilt from postings.
3. **PostgreSQL integration tests:** migrations, foreign keys, CHECK/unique constraints, transactions, concurrent spending/reallocation, reservation lifecycle, idempotency and rollback behavior.
4. **HTTP tests:** auth/household isolation, input validation, error mapping and API idempotency behavior.
5. **Race tests:** run Go's race detector where useful; it does not replace database concurrency tests.

Use a disposable PostgreSQL instance for CI integration tests (for example, a test service in CI or a container). Domain unit tests should not need a database.

## 9. Open choices that should not block domain work

- Exact HTTP middleware/auth library.
- Migration tool (for example, Goose or Atlas); decision must preserve versioned, reviewed migrations.
- PostgreSQL minor version and container image pinning policy at repository creation.
- Logging/metrics libraries.
- API schema tooling (OpenAPI generation or handwritten contract).

## 10. Decision status

- **Confirmed by product owner:** Go; GORM; PostgreSQL 18.x as the primary and integration-test database; standard library `net/http` with `http.ServeMux`.
- **Recommended implementation approach:** versioned SQL migrations and a modular monolith. The migration tool remains undecided.
- **Still open:** PostgreSQL minor version/image pinning at repository creation, HTTP middleware/authentication libraries, API schema tooling, and logging/metrics libraries.

Agents should implement the selected stack unless they identify a concrete technical blocker. Any proposed deviation must be documented with its rationale, impact, and tests before changing the stack decision.
