# Agent Brief — Household Envelope Budgeting MVP

Read the files in the following order:

1. `ACCOUNTING_SPEC.md` — canonical product/accounting contract.
2. `POSTING_MATRIX.md` — precise event postings and validation rules.
3. `STATE_TRANSITIONS.md` — lifecycle transitions and invalid-state behavior.
4. `DOMAIN_MODEL.md` — entities, value objects, commands, domain events, logical consistency boundary, and derived queries.
5. `DECISIONS.yaml` — compact decision index.
7. `STACK_DECISIONS.md` — confirmed Go/GORM/PostgreSQL/`net/http` stack and remaining implementation choices.

## Mission

Implement the domain/accounting layer for a self-hosted, Android-first, shared-household envelope-budgeting app. Go, GORM, PostgreSQL 18.x, and standard-library `net/http` are confirmed. Keep domain logic independent of GORM, HTTP routing, and persistence. Consult `STACK_DECISIONS.md` for the remaining open implementation choices. The accounting core must not depend on banking-notification integration.

## Do first

1. Review the accounting spec, posting matrix and state-transition spec together.
2. Surface real contradictions or underspecified policies with concrete examples and proposed defaults; do not silently invent rules.
3. Use `DOMAIN_MODEL.md` to define domain types, commands, events, and query models; report any unresolved proposed defaults before coding.
4. Define pure calculation/posting functions before committing to database tables.
5. Implement state transition guards and exact posting effects from `POSTING_MATRIX.md`.
6. Write automated tests for every matrix row and every allowed/rejected transition.
7. Add property/sequence tests for `budgetable_cash = ready_to_assign + sum(envelope_balances)`.
8. Add idempotency, concurrency and projection-rebuild tests.
9. Use the confirmed stack in `STACK_DECISIONS.md`: Go, GORM, PostgreSQL 18.x, and standard-library `net/http`. The migration tool and some supporting libraries remain open.
10. Propose concrete persistence/API design only after domain behavior is reviewable.

## Required core operations

- Open eligible/excluded cash-account balances and card liabilities.
- Receive income; allocate and reallocate envelope money.
- Create/edit/authorize/reserve/complete/cancel planned purchases.
- Record actual externally completed transactions, including underfunded ones flagged for review.
- Record debit/cash purchases, credit-card purchases and card repayments.
- Record fees/interest, refunds, reversals, replacements and metadata/category corrections.
- Transfer money between eligible accounts and across the eligibility boundary.
- Reconcile account discrepancies with explicit review and audit trail.
- Read derived account/envelope/Ready-to-assign/spendable/card-liability balances.
- Keep drafts and future notification suggestions out of the posted ledger and reservations.

## Non-negotiable properties

- Store money in integer minor units; no floating-point arithmetic.
- Every balance-changing operation commits atomically.
- Enforce idempotency and concurrency safety server-side.
- A normal planned purchase cannot exceed spendable envelope funds or available selected debit/cash-account funds.
- Emergency override requires a reason and bypasses the envelope constraint only.
- Credit limits are not budgetable money; card payment is not a second expense.
- Transfers between eligible accounts have no budget effect.
- Suggestions and drafts have no ledger effect and no reservation.
- Corrections are compensating entries, not silent history rewrites.
- Enforce household data isolation and record actor/timestamp/reason where applicable.
- Derive balances from postings, or use rebuildable projections verified against the journal.

## Do not build yet

- Android notification listener or bank-specific parsing.
- Bank API integrations.
- Multi-currency/FX.
- Investment/property net-worth accounting.
- Required historical transaction import.

## Deliverables expected from implementation agents

- Domain model and explicit state transitions.
- Posting implementation and derived balance projections.
- Automated unit/integration/property tests tied to the specs.
- Documentation for implementation-specific decisions.
- A list of unresolved questions, each with a proposal, example and test impact.

Do not claim compliance until tests actually run. Keep the canonical specs and code aligned; treat any behavior change as a reviewed spec change rather than an undocumented implementation detail.
