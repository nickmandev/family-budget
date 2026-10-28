# Implementation Decisions — Household Envelope Budgeting

- **Version:** 1.0
- **Status:** Resolved by product owner on 2026-10-27
- **Supersedes:** Nothing
- **Depends on:** [`ACCOUNTING_SPEC.md`](ACCOUNTING_SPEC.md), [`POSTING_MATRIX.md`](POSTING_MATRIX.md), [`STATE_TRANSITIONS.md`](STATE_TRANSITIONS.md), [`DOMAIN_MODEL.md`](DOMAIN_MODEL.md)

This document records product-owner-approved resolutions to questions the canonical specifications left open or under-specified. Each entry states the rule, its rationale, and the tests it requires. These decisions are implementation policy, not silence-filling: any deviation requires a reviewed change to this file and, where accounting behavior changes, to the canonical specification as well.

## Q1 — Card purchase reservation

**Rule.** A credit-card purchase reservation holds the spending envelope only (`Qe[SPEND] +P`). The card-payment envelope is not reserved at authorization; it receives funds at completion (`E[SPEND] -P; E[PAY] +P`).

**Rationale.** Reserving `PAY` as well would double-hold the same money: the spending envelope already reserves it until the purchase completes.

**Test impact.** Reservation tests assert only `S[SPEND]` decreases on authorize; `PAY` is untouched until completion.

## Q2 — Planned card repayment

**Rule.** A planned card repayment reserves both the source-account cash (`Qa[source] +P`) and the card-payment envelope (`Qe[PAY] +P`). It is modeled as a **planned repayment — a distinct planned operation — not as a purchase semantically**: it has its own commands and completion postings (eligible cash `-P`, card liability `-P`, `E[PAY] -P`), while reusing the same lifecycle state machine and reservation rules.

**Rationale.** A repayment is cash-out plus envelope-out like a debit purchase, but it settles a liability and must never look like a second expense.

**Test impact.** Separate transition/posting tests for the planned-repayment flow; completion must post the card-payment postings exactly once.

## Q3 — Direct purchase recording

**Rule.** Factual already-completed transactions (manual or external entry) remain recordable without prior app authorization. Underfunding is never a reason to reject or drop the fact; it sets budget-resolution state to requires-resolution and is flagged visibly. Pre-authorization exists only via the planned-purchase / planned-repayment flows.

**Rationale.** Accounting truthfulness (`ACCOUNTING_SPEC.md` principle 5). The ledger records what happened, especially when it does not fit the budget.

**Test impact.** Recording an underfunded external purchase succeeds, posts factual effects, and flags resolution; planned-purchase funding checks are not applied to it.

## Q4 — Budget resolution

**Rule.** Budget deficits (negative Ready-to-assign, negative envelope, negative card-payment envelope) are **derived** conditions; resolution status is derived from them (clear / requires-resolution / resolved), and user acknowledgment is a **separate** audit action with no ledger effect. A negative Ready-to-assign may legitimately represent a real funding shortfall. An accounting identity mismatch (`A ≠ R + sum(E[i])`) is an **internal integrity error** (domain error `ACCOUNTING_INVARIANT_VIOLATION`) and is never a user reconciliation or resolution state.

**Rationale.** Keeps user-fixable budget problems distinct from system bugs. `POSTING_MATRIX.md` §6 already states that projection mismatches are internal consistency errors, not user reconciliation events.

**Test impact.** Tests assert derived resolution transitions when deficits are funded/reversed; acknowledgment never changes balances; any identity mismatch fails loudly as an integrity error.

## Q5 — Duplicate out-of-scope transfer commands

**Rule.** Merge `MoveBudgetOutOfScope` and `TransferEligibleOutToExcluded` into a single explicit transfer command with funding sources (`DOMAIN_MODEL.md`'s `TransferEligibleOutToExcluded(source, destination, amount, fundingSources, effectiveDate)`); the budget-removal postings are part of that one atomic operation.

**Rationale.** The two documented commands describe the same accounting operation — an eligible-to-excluded transfer that must reduce Ready-to-assign and/or explicitly selected envelopes. One command prevents divergent funding-source handling and duplicate code paths across the eligibility boundary.

**Test impact.** One command path for eligible-to-excluded transfers; identity preserved after commit.

## Q6 — Account spendable balance

**Rule.** Spendable account balance = posted balance minus app-managed reservations only. No issuer/authorization holds are modeled in the MVP (`SA[a] = PostedBalance[a] - Qa[a]`).

**Rationale.** The MVP records posted balances and explicit app reservations and does not pretend to know live issuer outcomes.

**Test impact.** Spendable-account tests involve only posted balances and reservations.

## Q7 — Dates and timezone

**Rule.** Persist a household-local `EffectiveDate` as a calendar date (year, month, day) interpreted in the household's configured IANA timezone; it is never derived from a UTC timestamp. Store audit/creation/commit timestamps as UTC instants. Reporting month = `YearMonth(effectiveDate)`.

**Rationale.** A transaction's effective date is a local accounting fact, while audit timestamps are global ordering facts. Conflating them shifts transactions across month boundaries and breaks calendar-month reporting.

**Test impact.** Acceptance test for calendar-month reporting honors household timezone; late entries preserve effective date and audit timestamps.

## Q8 — Money bounds

**Rule.** Money is integer minor units with explicit checked arithmetic: every add/sub/compare validates currency equality and rejects overflow/out-of-range results; positive-amount types reject zero and negatives at construction; the same constraints are enforced again with database `CHECK` constraints at persistence time (later step).

**Rationale.** No silent wraparound in a ledger. Validating at both the value-object and database layers prevents corrupt money from entering through any path.

**Test impact.** Overflow, underflow, zero/negative, and currency-mismatch cases are unit-tested at the value-object layer.

## Q9 — Journal structure

**Rule.** The journal is an immutable journal operation record plus account posting rows and envelope posting rows referencing it (typed postings, not one generic row), with related-operation links for reversals/refunds/replacements.

**Rationale.** Typed posting rows make account/liability and envelope effects explicit and auditable per posting-matrix row, and keep projections rebuildable from the journal without a nullable generic event shape.

**Test impact.** Projection-rebuild tests aggregate posting rows and compare against derived balances.

## Q10 — Duplicate manual entries

**Rule.** Idempotency keys (with request fingerprint) protect against retried commands double-posting. Two distinct transactions are never silently merged because merchant/amount/details look similar; any duplicate suspicion is a user-facing warning at most.

**Rationale.** Idempotency addresses transport-level retries, not accounting judgment. Silently merging lookalike entries would rewrite real history; warning preserves truthfulness and user control.

**Test impact.** Retry with same key posts once; two equal-looking separate entries both post.

## Backup, restore and operations (resolved by implementation)

These are implementation-level proposals that satisfy the operational
contract in `SECURITY_AND_OPERATIONS.md` and `PLATFORM_DECISIONS.md` §F. They
do not change accounting semantics. The canonical operations specification is
left stable; the full procedure and drill checklist are in
[`RUNBOOK.md`](RUNBOOK.md), and the scripts live in `scripts/`.

- **Backup method:** `pg_dump --format=custom` (`-Fc`) executed inside the `db`
  container. A raw PGDATA copy is explicitly not the primary artifact.
- **Encryption tool:** `age` (X25519 + ChaCha20-Poly1305). The public recipient
  lives in the VPS `.env` as `BACKUP_ENCRYPTION_PUBLIC_KEY`; the private
  identity lives only on the operator's machine/password manager. Backups are
  encrypted before any off-host transfer.
- **Off-host target:** Hetzner Storage Box via rsync over SSH (separate failure
  domain, ≈ €3.81/month for the smallest 1 TB plan at the time of writing). An
  S3-compatible alternative (Hetzner Object Storage ≈ €4.99/month for the
  smallest plan; Backblaze B2 well under $0.10/month at this data size) is
  documented but not implemented.
- **Cadence and retention:** every 6 hours; 7 days of 6-hourly artifacts
  off-host (28 files), 6 monthly archives, 8 local staging artifacts.
- **RPO/RTO proposals:** RPO 6 hours (matching the cadence); RTO 30–60 minutes
  for a personal instance. Both are targets until the first drill validates
  them.
- **Restore order:** restore the dump into a freshly created database → run
  `goose up` for newer migrations → start `api` → run `verify-restore.sh`.
  Restoration must precede migrations because the dump is the data authority
  and the migration tool only applies versions newer than the dump.
- **Alerting:** healthchecks.io free tier (€0) dead-man's-switch for backup
  success/staleness and a daily disk check, plus an external `/readyz` check.
  The runbook includes systemd timer and cron examples.
- **Drill cadence:** before the first production launch (quality gate 6), then
  quarterly and after changes to the backup path or database major version.

## Still open (not resolved by this document)

No decision has been made yet on the following. They should not block domain work, but each needs a documented proposal with trade-offs before it is finalized:

- Migration tool (for example, Goose vs Atlas); any choice must preserve versioned, reviewed migrations.
- HTTP middleware/authentication library.
- API schema tooling (OpenAPI generation or handwritten contract).
- Logging/metrics libraries.
- PostgreSQL minor version and container image pinning.
- Reservation expiry/reminder policy.
- Exact API error payload schema.
- How a planned-repayment source kind maps onto `TransactionSource` (the domain enum is documented in `DOMAIN_MODEL.md` §4; adding a kind such as `PLANNED_REPAYMENT` is a proposed extension to confirm when its commands land).
