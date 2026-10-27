# Platform Decisions — Household Budgeting

**Status:** Owner-approved implementation direction, 2026-10-27. These decisions complement `STACK_DECISIONS.md` and the accounting contract; they do not change accounting semantics.

## A. Android application
- **Selected:** Native Android application using Kotlin and Jetpack Compose.
- **Interaction model:** Online-first.
- **Offline behavior:** Permit cached viewing and local drafts while offline. Do not commit financial operations or claim authorization from stale local balances. When connectivity returns, submit drafts for server-side validation; the server is authoritative.
- A draft is distinct from a planned purchase that has been reserved/authorized and from a committed financial transaction.
- A locally captured fact about an already-completed external payment may be queued as a draft for later recording; on synchronization, the server records the fact and flags budget-resolution issues. The client must not pretend it authorized the real-world payment.
- **Deferred:** Bank notification listener and notification parsing. Preserve the future ingestion boundary, but do not build it in the MVP.

## B. Authentication and household authorization
- Email/password authentication.
- Use a dedicated, reviewed password-hashing algorithm/library; recommended default is Argon2id with parameters benchmarked at implementation time. Never store plaintext or reversibly encrypted passwords.
- Sessions expire and can be individually revoked. Store session credentials safely; never log passwords, raw session tokens or password-reset tokens.
- Roles: `owner` and `member`.
- Both roles may manage transactions and invoke emergency overrides (with a required reason and shared audit visibility).
- Only the owner may manage household membership and household-level settings.
- Every request that accesses or mutates household data must authorize membership and scope server-side. Client-supplied household IDs are not proof of access.
- **Still open:** email verification, password recovery delivery/provider, session lifetime/rotation policy, and owner-recovery procedures. Agents must propose defaults rather than silently adding an external-service dependency.

## C. API contract
- Versioned REST/JSON API under `/api/v1`.
- Maintain a reviewable OpenAPI specification in the repository, with automated checks for contract drift where practical.
- Use UUID identifiers for public resource IDs.
- Use RFC 3339 UTC instants for audit timestamps. Use an explicit date-only `YYYY-MM-DD` for `EffectiveDate`, interpreted according to the household timezone and never obtained by converting a UTC instant implicitly.
- Define stable machine-readable error codes and a consistent JSON error envelope containing a message, optional field/details and request/correlation ID. Keep domain error codes independent of HTTP status mapping.
- Use cursor pagination for potentially unbounded collections, with stable deterministic ordering and server-enforced page limits.
- Require an idempotency key for financial mutation requests. Same key + same canonical request returns the original semantic outcome; same key + different request returns a conflict. Do not rely on in-memory process state for idempotency.
- The Android client never calculates authoritative balances or finalizes financial operations locally. Cached balances must be clearly marked as potentially stale.
- Use standard-library `net/http` and `http.ServeMux`; no Gin dependency.

## D. Persistence and schema changes
- PostgreSQL 18.x is the selected primary database; re-check supported stable release/minor and pin an explicit image version at implementation time.
- GORM is the persistence adapter. Domain types and posting logic remain independent of GORM models and hooks.
- **Migration tool selected:** Goose with versioned SQL migrations. Reviewed SQL migrations are the production schema authority; do not use GORM `AutoMigrate` as the production migration strategy.
- Migrations must define constraints, unique indexes, foreign keys and indexes intentionally. Test migrations from empty databases and upgrades from prior schema versions.
- Multi-row financial commands use explicit database transactions. Test constraints, locking, idempotency, rollback and concurrent spending/reallocation against PostgreSQL.

## E. Testing
- Pure Go domain tests for value objects, calculations, commands, authorization rules, posting effects and state transitions.
- Property/sequence tests assert accounting invariants after successful operations and verify projections can be rebuilt from journal postings.
- PostgreSQL integration tests run against real PostgreSQL; SQLite is not a substitute for concurrency/constraint integration tests.
- Test migrations, idempotency, retries, rollback, concurrent commands, cross-household isolation, reservations, card payments, reversals and transfers across eligibility boundaries.
- HTTP tests cover validation, authentication, authorization, response/error mapping and OpenAPI contract drift.
- CI runs formatting, `go vet`, unit tests, integration tests and race tests where applicable. Do not claim tests pass until commands have run.

## F. Deployment and operations — Hetzner VPS
- **Target:** self-hosted deployment on a Hetzner VPS.
- Use Docker Compose for the Go API and PostgreSQL. Keep PostgreSQL on a private Compose network and do not publish its port to the public interface.
- Terminate HTTPS at a reverse proxy. Caddy is a reasonable proposed default, but exact reverse-proxy choice remains open until implementation planning.
- Expose only required public ports. Use a host firewall, key-based SSH, timely security updates and least-privilege service accounts.
- Keep secrets out of source control and container images. Restrict permissions on deployment secret files; never log secrets.
- Persist PostgreSQL data on a durable volume and set resource/log rotation limits.
- Backups must be encrypted and copied off the VPS to a separate failure domain. A backup on the VPS itself is not an off-host backup. Select a target and document encryption-key custody and retention.
- Document and test restoration to a clean database/host before production. Define RPO/RTO and recovery steps.
- Provide liveness/readiness checks, structured redacted logs, and basic monitoring/alerts for service health, disk capacity and backup freshness.
- **Still open:** reverse proxy, off-host backup target and retention, RPO/RTO, monitoring/alerting provider, DNS/domain, and whether a staging environment is warranted.

## Confirmation scope

The owner has confirmed platform decisions A–F above, with Hetzner VPS as deployment target. The confirmation of platform decisions A–F above is independent of the accounting edge-case questions Q1–Q10. Those were subsequently approved by the owner (2026-10-27, with refinements to Q2 and Q4) and their resolutions are recorded in `IMPLEMENTATION_DECISIONS.md`.
