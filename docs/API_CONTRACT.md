# API Contract — Initial Direction

**Status:** Owner-approved contract direction; endpoints and schemas must be derived from the canonical domain model.

## Contract rules
- REST/JSON API versioned under `/api/v1`.
- OpenAPI is the reviewed external API contract; automate contract checks where practical.
- Public resource IDs are UUIDs.
- Audit timestamps are RFC 3339 UTC instants. `EffectiveDate` is date-only `YYYY-MM-DD` interpreted in the household timezone.
- Define stable machine-readable error codes and a consistent JSON error envelope. Recommended shape:

```json
{
  "error": {
    "code": "INSUFFICIENT_ENVELOPE_FUNDS",
    "message": "The envelope has insufficient spendable funds.",
    "field": null,
    "details": null,
    "request_id": "..."
  }
}
```

The exact details fields may be refined before implementation, but codes must be stable. Do not expose raw database errors, stack traces, credentials or secrets.

## Idempotency
Financial mutation requests require an idempotency key. Store the canonical request fingerprint and original semantic result consistently with the financial operation.

- Same key + same canonical request: return the original result and do not repeat postings.
- Same key + different canonical request: return a conflict.
- Concurrent duplicate requests: only one executes; the other gets the original result or a deterministic in-progress response.
- Define key scope and retention in the persistence implementation note. Do not rely on in-memory state.

## Pagination
Use cursor pagination for potentially unbounded collections such as transactions and audit history. Use deterministic ordering with a unique tie-breaker and enforce a maximum page size. Cursors must not grant access outside the authorized household.

## Client trust boundary
The Android app is not an accounting authority. The API authenticates the actor, authorizes household access, validates input, executes domain rules and returns authoritative balances and resource states. Client-side checks are UX hints only.

Offline drafts may be saved on device and submitted later; they are not committed transactions or reservations until server acceptance. Actual external transactions may be recorded after the fact and flagged for budget resolution if underfunded.

## Endpoint design guidance
Derive endpoint paths and payloads from domain commands/queries. Do not expose a generic endpoint that lets clients post arbitrary ledger rows. Expose application operations for account setup, envelope allocation/reallocation, purchase planning/authorization, transaction recording, correction/refund and balance/history queries. Only trusted application code creates journal postings.

OpenAPI should document auth, household scope, validation, idempotency, pagination, error codes, date semantics and the distinction between a draft, reserved plan, suggestion and committed transaction.
