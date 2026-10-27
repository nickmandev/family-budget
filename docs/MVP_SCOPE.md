# MVP Scope — Household Envelope Budgeting

## Product target

A shared household budgeting product with a native Android client (Kotlin + Jetpack Compose) and a self-hosted Go API on a Hetzner VPS. The first release prioritizes correct cash-backed envelope budgeting over integrations and forecasting.

## Must-have
- Household creation, owner/member roles and email/password authentication.
- Account setup with opening balances only; eligible cash accounts explicitly contribute to budgetable cash.
- Single household currency for MVP.
- Envelopes, allocations, reallocations and visible Ready-to-assign money.
- Calendar-month reporting in household timezone; envelope balances carry forward automatically.
- Manual income, spending, transfers, refunds, corrections and account reconciliation.
- Planned purchases with explicit authorization and reservations.
- Reallocation-first purchase authorization and reasoned emergency override.
- Record already-completed external transactions even when underfunded; flag budget resolution rather than falsifying reality.
- Credit-card liability accounting in domain/ledger rules; purchases and repayments must not be double-counted.
- Shared ledger, audit attribution, household isolation, idempotency and concurrent-operation safety.
- Go REST/JSON API, versioned under `/api/v1`, with OpenAPI documentation.
- PostgreSQL persistence with versioned SQL migrations using Goose.
- Native Android client with online-first behavior, cached reading while offline and local drafts requiring server validation before commit.
- Automated domain, PostgreSQL integration, property/sequence, API and concurrency tests.
- Hetzner VPS deployment with Docker Compose, TLS, private DB networking, encrypted off-host backups and tested restore before production.

## Explicitly deferred
- Android bank-notification listener/parser.
- Direct bank APIs and account aggregation.
- Multi-currency accounts or FX conversion.
- Required historical transaction import during onboarding.
- iOS application.
- AI categorization, automatic allocation suggestions and advanced forecasting.
- Investment/property net-worth accounting.

## Quality gates
Do not call the MVP ready until:
1. Core posting/state-transition tests cover each canonical posting-matrix operation and invalid transition.
2. Property/sequence tests preserve `budgetable_cash = ready_to_assign + sum(envelope_balances)` for valid accounting states.
3. PostgreSQL integration tests cover migrations, constraints, idempotency, rollback and concurrent spending/reallocation.
4. Household authorization tests prove cross-household reads/writes are denied.
5. API docs match handlers and document date semantics, idempotency, errors and pagination.
6. An instance can be restored from an encrypted off-host backup according to the runbook.
