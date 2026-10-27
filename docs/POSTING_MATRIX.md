# Posting Matrix — Household Envelope Budgeting

**Version:** 1.0  
**Status:** Proposed domain contract; implementation and tests pending.  
**Depends on:** `ACCOUNTING_SPEC.md` (canonical product and accounting principles).  
**Purpose:** Specify the exact account and budget effects of each committed event. This document is technology-neutral and intentionally precedes the database schema.

## 1. Notation and event contract

All amounts are positive integer minor units in the household's single configured currency. `P` is the event amount. Every operation must validate and commit its financial postings, budget postings, relevant reservation transitions, references, and audit event atomically.

### Balance symbols

- `A`: sum of posted balances of budget-eligible liquid accounts. A real negative eligible account balance is included as negative.
- `R`: ready to assign, derived as `A - sum(E[i])`.
- `E[x]`: posted balance of envelope `x`.
- `Qe[x]`: active reservations against envelope `x`.
- `Qa[a]`: active reservations against eligible cash account `a`.
- `S[x] = E[x] - Qe[x]`: envelope spendable balance.
- `SA[a] = AccountBalance[a] - Qa[a] - recognized holds[a]`: account spendable balance.
- `L[c]`: card liability for card `c`, positive when the household owes money.
- `SPEND[x]`: ordinary spending envelope selected for a purchase.
- `PAY[c]`: card-payment envelope associated with card `c`.

### Posting signs

- Account asset `+P` means more cash; `-P` means less cash.
- Card liability `+P` means more debt; `-P` means less debt.
- Envelope `+P` means more budget assigned to that envelope; `-P` means less.
- `R` is never directly posted. It changes as a derived result of changes in eligible cash and envelope balances.
- A posting matrix row describes the resulting net effect, not SQL statements or storage layout.

### Required event envelope

Every committed financial event must have: `event_id`, `household_id`, `actor_id` (or a documented system actor), `event_type`, `source`, `effective_date`, `created_at`, `committed_at`, currency, amount where applicable, idempotency key, and references to related events. Events that need a reason (emergency override, reconciliation, manual correction) must store it.

## 2. Posting matrix

| Event | Account / liability effect | Budget effect | Authorization and validation |
|---|---|---|---|
| Opening eligible cash balance | Eligible account `+P` (or its actual signed opening balance) | `R +P` for a positive opening amount; derive `R` from the signed balance in all cases | Opening-balance event only; not income; reject duplicate opening event for the same account unless using explicit correction flow |
| Opening excluded cash balance | Excluded account `+P` | None | Not part of `A`; does not fund envelopes |
| Opening card liability | Card liability `+P` | None | No fabricated historical purchases; payment envelope starts at zero unless user explicitly allocates funds |
| Receive income into eligible account | Eligible account `+P` | `R` increases by `P` | Must be an actually received/posted amount; expected salary is forecast only |
| Receive income into excluded account | Excluded account `+P` | None | Transfer into eligible boundary later is not income again |
| Allocate to envelope `x` | None | `E[x] +P`; derived `R` decreases by `P` | Require `R >= P`; cannot allocate future income or credit limits |
| Reallocate `x -> y` | None | `E[x] -P`; `E[y] +P` | Require `S[x] >= P`; source and destination must be valid and distinct |
| Debit/cash purchase | Eligible cash account `-P` | `E[SPEND] -P` | Planned purchase requires `S[SPEND] >= P` and `SA[account] >= P`; actual external purchase is recorded truthfully even if underfunded |
| Credit-card purchase | Card liability `+P` | `E[SPEND] -P`; `E[PAY] +P` | Planned purchase requires sufficient envelope funds or emergency override; configured card-limit validation is separate and never increases `A` |
| Credit-card payment | Eligible cash account `-P`; card liability `-P` | `E[PAY] -P` | Planned repayment needs cash-account spendability and payment-envelope funds; normal planned payment must not exceed outstanding liability unless explicit overpayment consent; real payment always recorded truthfully |
| Card fee / interest | Card liability `+P` | `E[FeeCategory] -P`; `E[PAY] +P` | Actual fee must be recorded; underfunded fee category may go negative with an explicit unresolved-budget flag |
| Transfer: eligible cash account to eligible cash account | Source account `-P`; destination account `+P` | None | Both account effects atomic; same household and same currency |
| Transfer: excluded cash to eligible cash | Excluded account `-P`; eligible account `+P` | Derived `R` increases by `P` | Transfer, not income; source funds must exist for a planned transfer |
| Transfer: eligible cash to excluded cash | Eligible account `-P`; excluded account `+P` | Reduce `R` by `P` and/or selected envelope(s) by `P`, so `A = R + sum(E)` still holds | User must explicitly choose the budget funding source; cannot use reserved envelope/account funds without resolving the reservation |
| Transfer wholly within excluded accounts | Excluded source `-P`; excluded destination `+P` | None | Outside the budget identity |
| Refund to eligible cash from debit/cash purchase | Eligible cash account `+P` | Original spending envelope `+P` | Must reference original purchase; cumulative refunds cannot exceed refundable amount; partial refunds supported |
| Refund to credit card from card purchase | Card liability `-P` | Original spending envelope `+P`; `E[PAY] -P` | Must reference original purchase; if `PAY` becomes negative because the card was already repaid, retain and flag the legitimate condition |
| Unrelated reimbursement received into eligible cash | Eligible cash account `+P` | `R` increases by `P` by default | User may choose an explicitly supported alternate treatment before commit; never silently treat as refund to a purchase |
| Purchase reservation created | None | None; `Qe[SPEND] +P`; for debit/cash, `Qa[account] +P` | Only explicit planned purchases reserve; drafts and suggestions do not. Normal reservation requires available envelope/account funds. An override reservation may exceed envelope availability but must show the deficit; it may not exceed cash-account availability |
| Reservation amount increased | None | Increase relevant reservation by delta | Revalidate the additional amount against current spendable funds or obtain a fresh explicit override for the envelope shortfall; account liquidity cannot be overridden |
| Reservation amount decreased | None | Decrease reservation by delta | Release excess hold atomically |
| Reservation cancelled | None | None; release envelope/account reservations | Only non-completed reservation; idempotent cancellation |
| Reserved purchase completed | Apply appropriate debit/cash or card purchase postings above; release reservation(s) in same atomic operation | Apply appropriate purchase postings above; release reservation(s) in same atomic operation | Final amount cannot exceed authorization without revalidation; post once even after retries |
| Actual external purchase recorded after the fact | Actual account/liability effects as appropriate | Actual budget effects as appropriate | Do not reject a real event for missing prior authorization; flag funding deficit or account discrepancy for review |
| Reversal of committed purchase/event | Compensating account/liability postings that reverse the original amount | Compensating budget postings that reverse original budget effect | Use a distinct linked event; do not delete the original. Enforce reversible remaining amount and prevent double reversal |
| Replacement after correction | Post compensating reversal first, then replacement event with corrected values | Compensate original budget postings, then post corrected budget effects | Reversal and replacement should be one atomic correction operation where practical; preserve both audit links |
| Metadata-only correction | No account effect | No budget effect | Append an audit event; do not rewrite original committed event silently |
| Category correction | No account effect | Reverse original category posting and post corrected category posting; `sum(E)` remains unchanged | Validate target category and historical reporting treatment |
| Account/source correction | Compensate original account effect and add corrected account effect | Compensate/repost budget effects as required | Must preserve budget identity after whole correction commits |
| Reconciliation increase in eligible cash | Account `+P` | By default `R` increases by `P`; alternatively explicitly allocate to a named envelope | User-reviewed adjustment with reason and evidence/source; not ordinary income unless classified as such |
| Reconciliation decrease in eligible cash | Account `-P` | By default `R` decreases by `P`; if `R` becomes negative, flag budget deficit. User may explicitly attribute difference to one or more envelopes instead | Never silently overwrite a posted balance; actual discrepancy must be represented and traceable |
| Monthly boundary / rollover | None | None; balances continue unchanged | Calendar month changes reporting and target periods, not envelope balances |
| Draft / transaction suggestion created, edited, or cancelled | None | None; no reservations | A suggestion affects neither balances nor spendable amounts until user confirmation enters the shared validation path |

## 3. Purchase authorization rules

### 3.1 Normal approval

For envelope `x` and amount `P`, normal authorization requires:

`P <= S[x]`

For debit/cash, it additionally requires:

`P <= SA[account]`

For a card purchase, card issuer authorization and any locally configured limit policy are separate from the budget test. Credit limit never contributes to `A` or `R`.

### 3.2 Reallocation-first

If the envelope is underfunded, the primary path is to reallocate from a source envelope `y` where `S[y] >= delta`. The ideal operation is one atomic command that:

1. rechecks the source envelope against current committed state;
2. posts `E[y] - delta` and `E[x] + delta`;
3. creates the reservation; and
4. records the authorization audit event.

If implementation instead commits reallocation as a separate operation, it must give the user an explicit reviewable step and must repeat authorization checks at reservation time.

### 3.3 Emergency override

Any household member may invoke it with a mandatory reason. It bypasses the envelope-funding test only. It does not bypass source-account liquidity, card-issuer authorization, configured card-limit checks, authentication, household isolation, duplicate detection, currency validation or concurrency checks.

If a planned debit purchase is underfunded by its envelope but cash-account funds are sufficient, the override reservation can make `S[x]` negative while posted `E[x]` remains unchanged. On completion, the reservation is released and the actual purchase is posted, leaving `E[x]` negative. Both the authorization exception and final deficit remain traceable.

## 4. State-transition specification

### 4.1 Planned purchase state machine

States:

- `DRAFT`: editable proposed purchase; no postings or reservations.
- `RESERVED`: authorized normally and reservation(s) exist.
- `RESERVED_EXCEPTION`: authorized by a reasoned emergency override; any envelope shortfall is visible.
- `COMPLETED`: actual purchase has been posted and all reservation(s) consumed/released.
- `CANCELLED`: plan was cancelled before completion; all reservation(s) released.

Allowed transitions:

| From | Command | To | Required behavior |
|---|---|---|---|
| DRAFT | Save/edit | DRAFT | No financial effect |
| DRAFT | Authorize normally | RESERVED | Validate envelope, account and payment constraints; create reservation(s) atomically |
| DRAFT | Authorize with override | RESERVED_EXCEPTION | Mandatory reason; validate all constraints except envelope funding; create reservation(s) atomically |
| DRAFT | Cancel | CANCELLED | No financial effect |
| RESERVED | Change amount/category/account | RESERVED or DRAFT | Revalidate deltas; adjust/release/recreate reservations atomically, or return to draft with old reservation explicitly released |
| RESERVED_EXCEPTION | Change amount/category/account | RESERVED, RESERVED_EXCEPTION or DRAFT | Reauthorize new amount/category/account; keep reasoned exception tied to the amount it authorizes |
| RESERVED | Complete | COMPLETED | Final amount within authorized amount; release reservation(s) and post transaction atomically |
| RESERVED_EXCEPTION | Complete | COMPLETED | Same as above; preserve override audit link and post resulting deficit |
| RESERVED | Cancel | CANCELLED | Release reservation(s); no posted financial effect |
| RESERVED_EXCEPTION | Cancel | CANCELLED | Release reservation(s); exception authorization remains in audit history but no expense is posted |
| COMPLETED | Cancel/edit in place | Rejected | Use linked reversal/correction; committed history is immutable |
| CANCELLED | Any completion/re-authorization | Rejected | Create a new plan; cancelled plan remains auditable |

If final amount is lower than reserved amount, release the excess. If final amount is higher, reject completion until the delta has been reauthorized/re-reserved. Repeated completion requests must be idempotent.

### 4.2 Committed financial transaction lifecycle

A committed financial transaction is not edited or deleted in place.

- `COMMITTED`: its journal and budget postings exist.
- `PARTIALLY_REVERSED`: some but not all reversible value has been reversed, or a linked partial refund has occurred; the original remains committed and linked events explain the net effect.
- `FULLY_REVERSED`: linked compensating events have reversed the original's full reversible effect.

Refund is a genuine new event, not a correction. Keep refunds distinct from reversals in event type and audit semantics. A full refund may economically offset a purchase, but must not erase the fact that both happened.

Corrections produce linked compensating postings and optional replacement postings. A metadata-only correction does not change financial postings.

### 4.3 Suggestions and drafts

Future notification suggestions and ordinary drafts have lifecycle independent from the committed ledger:

- `SUGGESTED` -> `EDITED` -> `CONFIRMED` (confirmation invokes shared validation/posting) or `CANCELLED`.
- Suggestions and drafts never affect posted balances or reserve funds.
- If confirmation represents a purchase already completed outside the app, record the real event even if the planned-purchase path would have required reallocation; flag any resulting deficit for review.
- If confirmation is a planned purchase not yet completed, it must enter the planned-purchase authorization/reservation workflow, not directly post the expense.
- Deduplication may suppress duplicate suggestions, but must not merge two actual purchases merely because their amounts and merchants are equal.

## 5. Failure behavior and atomicity

Reject a planned/authorized operation without partial changes when any of the following applies:

- insufficient `R` for allocation;
- insufficient spendable source envelope for reallocation;
- insufficient spendable envelope funds without an override;
- insufficient spendable eligible account funds for planned debit/cash purchase or payment;
- invalid household membership or account/category ownership;
- missing reason for emergency override or reconciliation adjustment;
- invalid currency or unsupported foreign-currency amount;
- duplicate idempotency key with mismatched request payload;
- duplicate reversal/refund beyond remaining reversible/refundable amount;
- stale reservation/amount change without reauthorization;
- concurrency conflict after the server-side balance recheck.

The operation must either commit all financial/budget postings and related state transitions or commit none. On a retry with the same idempotency key and same logical payload, return the original result without posting again. Reuse of a key with materially different payload must be rejected.

Actual externally completed transactions are different: record their factual account/budget effects even if underfunded, but surface the discrepancy and require resolution. If the amount, currency, source account, or transaction identity is uncertain, store it as an unconfirmed suggestion instead of inventing a committed event.

## 6. Reconciliation rules

- The bank's actual balance is external evidence; the application must not silently overwrite its ledger balance to match it.
- A positive eligible-cash discrepancy normally increases account balance and derived `R`; a negative discrepancy normally decreases account balance and derived `R` and may cause negative `R`.
- If the user identifies the discrepancy as a specific purchase, refund, transfer or fee, record that real event instead of a generic reconciliation adjustment.
- Explicit adjustment requires actor, reason, effective date, source/evidence note where available, and balanced account/budget effect.
- Reconcile projections against journal postings; any mismatch between a materialized balance and journal-derived balance is an internal consistency error, not a user reconciliation event.

## 7. Minimal acceptance tests for this artifact

1. Each posting-matrix row preserves `A = R + sum(E)` after commit.
2. Each rejected operation leaves account, envelope, liability, reservation and audit state unchanged except for a separately defined failed-attempt/security log.
3. Each allowed state transition is tested; all other transitions are rejected.
4. A reservation affects spendable balances only, never posted balances.
5. Cancellation releases reservations and has no financial effect.
6. Completion consumes reservations and posts exactly one financial event atomically.
7. Underfunded actual external spending is recorded and visibly flagged, not dropped.
8. Credit-card purchase, repayment, fee and refund sequences preserve budget identity without duplicate expense.
9. Eligibility-boundary transfers keep the budget identity intact.
10. Reversal, partial refund, full refund, and correction sequences prevent over-reversal and preserve linked history.
11. Duplicate retries and concurrent purchases cannot double-spend the same funds.
12. A generated journal projection matches account, liability, envelope, `R`, and spendable balances after randomized valid event sequences.

## 8. Known implementation choices left open

These are not accounting policy changes; they are choices for an implementation agent to propose with trade-offs and tests before finalizing storage/API details:

- Database and transaction isolation mechanism.
- Whether the journal is represented as one generalized event with posting rows or typed immutable event records plus generated postings.
- Reservation expiry/reminder policy.
- Exact enum names and API error payload schema.
- Whether a reconciliation discrepancy defaults to `R` or requires an explicit funding envelope for a decrease. Current default is to use `R`, allowing a negative result only as a visible, reviewed real-world discrepancy.
- Exact account balance model for supported overdrafts, pending bank holds and card issuer authorization. The MVP records posted balances and explicit app reservations; it does not pretend to know live issuer authorization outcomes.
