# Household Envelope Budgeting — Accounting Specification

**Version:** 1.0  
**Status:** Domain contract for implementation planning; not yet implemented or verified by automated tests.  
**Scope:** Shared household budget, single-currency MVP, Android-first client, self-hosted backend.  
**Audience:** Engineering agents implementing domain logic, API behavior, persistence, and tests.

## 1. Product and accounting principles

1. Budget only money already present in eligible liquid accounts. Expected income and credit limits are never budgetable funds.
2. Use a monthly envelope budget with automatic rollover. Unspent envelope balances and unallocated money carry forward.
3. Users may leave money unallocated. It is visible as **Ready to assign**, but cannot be spent directly; money must be allocated to an envelope first.
4. Planned purchases are strict: reallocate money before authorization or use an explicit emergency override with a mandatory reason.
5. Record actual external transactions truthfully even if they were not approved in the app. Flag any resulting underfunded budget state for resolution rather than hiding or dropping the transaction.
6. Maintain one shared household ledger. All committed balance-changing operations are atomic, auditable, and safe to retry.
7. Keep transaction source, lifecycle/authorization state, account effect, and budget effect distinct.
8. Notification-derived transaction suggestions are a future feature. Suggestions are not finalized transactions, do not affect account/envelope balances, and do not reserve money. They are editable/cancellable until confirmed.
9. Use an append-only journal for committed financial events. Correct mistakes through compensating postings and, where needed, replacement postings; do not silently rewrite committed history.
10. Do not choose a framework, database, or deployment architecture as part of this domain contract.

## 2. Final product defaults

- **Users:** One shared household budget with multiple authenticated household members.
- **Budget period:** Calendar month in the household's configured timezone. Transaction effective date determines reporting period; audit timestamps retain when an event was entered/committed.
- **Currency:** One currency selected when the household is created. No FX, multi-currency account, or currency conversion support in the MVP.
- **Money precision:** Integer minor units, never binary floating-point. Use currency metadata for the supported currency's minor-unit precision.
- **Budgetable accounts:** Account-level eligibility flag. Eligible liquid accounts include supported checking/current accounts, savings accounts, and cash wallets. Investments, credit limits, and excluded accounts do not contribute to budgetable cash.
- **Opening balances:** Onboarding establishes opening balances only; no historical transaction import is required. Opening balances are not income or historical spending.
- **Unallocated funds:** Allowed indefinitely and displayed as Ready to assign.
- **Envelopes:** Continuous balances with automatic rollover. Targets are planning values; they are not allocations and do not reset balances.
- **Overspending:** Reallocation first. Any household member can invoke an emergency override with a mandatory reason; both household members can see it.
- **Reservations:** Only explicitly planned purchases reserve funds. Drafts and transaction suggestions never reserve funds.
- **Credit cards:** Liability accounts. Budgeted card purchases move money from the spending envelope to a card-payment envelope. Repayment settles the liability and is not a second expense.
- **Corrections:** Append-only reversals and replacement entries. Genuine refunds are new events linked to the original transaction.
- **Notification ingestion:** Later enhancement; parse locally, create editable/cancellable suggestions, and use the same confirmation/ledger path as manual entry.

## 3. Core vocabulary

- **Eligible liquid account:** A cash-like account included in the household budget.
- **Budgetable cash (`A`):** Sum of the current posted balances of eligible liquid accounts, including any real negative balance that is represented by the account ledger. Excluded accounts and card liabilities/credit limits are not included.
- **Envelope balance (`E[i]`):** The net budget allocation for envelope `i`, after committed spending, allocations, reallocations, refunds and adjustments. A negative balance is permitted only as a result of a recorded/recognized event such as an approved emergency exception, an actual external transaction, a refund/settlement edge case, or a correction; it must be visible and traceable.
- **Ready to assign (`R`):** The derived amount of budgetable cash not allocated to envelopes.
- **Reservation (`Q[i]`):** Funds held for a specific explicitly planned purchase. A reservation is not a financial transaction and does not change posted account or envelope balances.
- **Spendable envelope balance (`S[i]`):** Envelope balance minus active reservations.
- **Account reservation (`Q-account[a]`):** Cash held against an explicitly planned debit/cash purchase from account `a`.
- **Spendable account balance:** Posted account balance minus active account reservations and any other holds represented by the product.
- **Card liability (`L[j]`):** Amount owed on card `j`, positive when the household owes money. Credit limits are not assets.
- **Committed transaction:** A financial event whose validated account and budget postings have been committed to the journal.
- **Suggestion:** An unconfirmed draft derived from user entry or future notification ingestion. A notification suggestion has no ledger effect until confirmed.

## 4. Fundamental invariants

### 4.1 Household budget identity

At all committed states:

`A = R + sum(E[i])`

Therefore:

`R = A - sum(E[i])`

`R` is derived, not an independently editable balance. It may become negative only when recording actual financial reality or an explicitly handled exceptional/correction state; normal allocations and normal app-authorized operations must not make `R` negative. When it is negative, surface an accounting/budget deficit requiring review.

### 4.2 Spendable balances

For each envelope:

`S[i] = E[i] - Q[i]`

For each eligible debit/cash account:

`SpendableAccount[a] = PostedBalance[a] - Q-account[a] - other active holds[a]`

Reservations reduce spendable balances only. They do not appear as additional allocations, posted spending, or posted account debits.

### 4.3 Invariants

1. The budget identity holds after every committed operation, including opening balances, transfers across the budget boundary, refunds, corrections, and reconciliation adjustments.
2. Ready to assign is always calculated from account and envelope postings; it cannot be edited independently.
3. A normal allocation cannot exceed Ready to assign.
4. A normal reallocation cannot consume source-envelope funds already reserved or otherwise unavailable.
5. A normal planned purchase cannot exceed its envelope's spendable balance.
6. A planned debit/cash purchase cannot overdraw its selected source account after reservations and recognized holds.
7. An emergency override can bypass the envelope allocation check only. It cannot bypass account liquidity, payment-authorization checks, currency validation, idempotency, household authorization, or duplicate detection.
8. Credit limits never contribute to budgetable cash.
9. Credit-card purchases and card repayments never count the same purchase twice.
10. Transfers between eligible cash accounts have no net budget effect.
11. A transfer out of the budgetable-account boundary must have explicit corresponding budget-removal postings; a transfer into it increases Ready to assign and is not income.
12. A duplicate retry cannot produce a second financial or budget effect.
13. Every committed balance-changing event is traceable to its source, actor, timestamp, and related/reversal events.
14. Household members cannot read or mutate another household's financial records.
15. The MVP cannot silently introduce a foreign-currency amount.
16. Opening balances do not masquerade as income or historical spending.
17. A real transaction that occurred outside the app must not be falsified or discarded just because it failed the planned-purchase authorization rules.

## 5. Posting rules

All monetary amounts below are positive integer minor units `P` in the household currency. A business operation's account postings, budget postings, reservation transitions, and audit record must commit atomically. The table describes net effects.

| Event | Account effect | Budget effect | Notes |
|---|---|---|---|
| Opening eligible cash balance | Establish account balance `+P` | `R +P` | Opening balance, not income. |
| Opening excluded-account balance | Establish excluded account balance `+P` | None | Outside budget identity. |
| Opening credit-card liability | Establish liability `+P` | None | Do not invent past spending. |
| Receive income into eligible account | Account `+P` | `R +P` | Income only when received/posted. |
| Income received into excluded account | Excluded account `+P` | None | Later transfer into eligible accounts is not income. |
| Allocate funds | None | `R -P`; target envelope `+P` | Require `R >= P` under normal operation. |
| Reallocate | None | Source envelope `-P`; target envelope `+P` | Require sufficient source spendable balance. |
| Debit/cash purchase | Eligible account `-P` | Spending envelope `-P` | Planned purchase requires envelope and source-account checks. |
| Credit-card purchase | Card liability `+P` | Spending envelope `-P`; card-payment envelope `+P` | No eligible cash movement at purchase time. Fund category normally; override may result in a negative spending envelope. |
| Credit-card repayment | Eligible cash account `-P`; card liability `-P` | Card-payment envelope `-P` | Not a second expense. Normal planned payment requires funded payment envelope and account liquidity. |
| Transfer between eligible cash accounts | Source `-P`; destination `+P` | None | Budgetable cash unchanged. |
| Transfer excluded -> eligible | Excluded account `-P`; eligible account `+P` | `R +P` | Transfer, not income. |
| Transfer eligible -> excluded | Eligible account `-P` | Reduce `R` and/or explicitly chosen envelope balances by total `P` | Require explicit funding source(s). Do not silently overspend/rescind reservations. A real completed external transfer must be recorded and flagged if underfunded. |
| Refund to eligible cash for budgeted debit/cash purchase | Eligible account `+P` | Original spending envelope `+P` | Link to original; only unrefunded amount allowed. |
| Refund to credit card | Card liability `-P` | Original spending envelope `+P`; card-payment envelope `-P` | Can produce a negative card-payment envelope if the original card payment was already made; preserve and flag the legitimate event. |
| Unrelated reimbursement received into eligible account | Account `+P` | `R +P` by default | User may explicitly choose a documented alternate budget treatment before commit. |
| Credit-card fee/interest | Card liability `+P` | Fee/interest envelope `-P`; card-payment envelope `+P` | Avoid double-counting payment as expense. |
| Cancel reservation | None | None | Release reservation only. |
| Complete reserved purchase | Apply appropriate purchase postings | Apply appropriate purchase postings | Consume/release reservation atomically; additional amount requires reauthorization. |
| Correct committed event | Compensating postings and optional replacement | Compensating postings and optional replacement | Never silently rewrite posted financial history. |
| Reconcile cash discrepancy | Confirmed account adjustment | Matching Ready-to-assign/envelope posting | Requires review and reason; cannot be silent. |

### 5.1 Reallocation and emergency override

For a planned purchase of `P` from envelope `i`:

- Normal authorization requires `P <= S[i]`.
- If not, first offer reallocation from another envelope with sufficient spendable funds.
- A reallocation and authorization/reservation should be one atomic business operation, or the reallocation should be committed first with a clear reviewable result. Never approve against stale balances.
- An emergency override requires a reason, initiating member, timestamp, related purchase, and resulting deficit.
- For a debit/cash purchase, an override does not authorize spending unavailable cash. For a credit-card purchase, it does not bypass the card issuer's decision or configured limit check.
- A negative envelope persists until resolved by new allocation/reallocation or a valid reversing event. New allocations do not silently erase the history of the deficit.

### 5.2 Planned purchases and completion

Recommended lifecycle: `draft -> reserved/authorized -> completed`, with cancellation from a non-completed state. An explicitly authorized emergency purchase may reserve an underfunded category only if the resulting deficit is visible and reasoned.

- Draft: no balance impact and no reservation.
- Reserved: envelope spendable balance decreases; for debit/cash, selected account spendable balance also decreases. Posted account and envelope balances remain unchanged.
- Completed: release the reservation and post the actual financial/budget event atomically.
- Cancelled: release reservations; no posted balance changes.
- Actual transaction outside app: record the real event even if not preauthorized. Mark it as unresolved/underfunded where applicable.
- Amount increases after authorization: authorize/reserve only the additional amount after rechecking funding. Amount decreases: release the excess reservation.

## 6. Credit-card policy

1. A credit card is a liability account; its credit limit is never budgetable cash.
2. A normal purchase transfers budget from the selected spending envelope to that card's payment envelope while increasing card liability.
3. Paying the card reduces eligible cash, card liability, and the card-payment envelope by the payment amount. Do not record the original purchase again.
4. Existing card debt at onboarding is recorded as an opening liability only. Do not create historical expenses. Start the card-payment envelope at zero unless the user explicitly allocates cash to it.
5. The UI may prompt the user to allocate funds for existing debt, but must not silently force full funding or claim that the liability is already funded.
6. A planned repayment should not exceed the outstanding liability unless the user explicitly confirms an overpayment. A real overpayment or card credit must still be recorded accurately.
7. A refund arriving on a credit card reduces liability and restores the original spending envelope. The card-payment envelope may become negative if its funds have already been used to pay the original bill; this is a traceable refund/settlement condition, not an unexplained mutation.
8. Fees and interest are spending events and must have a category/envelope effect; card settlement remains a separate liability payment.

## 7. Account eligibility boundary

Eligibility changes are accounting operations, not simple metadata changes.

- Eligible -> eligible transfers have no budget effect.
- Excluded -> eligible transfers add to budgetable cash and Ready to assign; they are not income.
- Eligible -> excluded transfers reduce budgetable cash and must reduce Ready to assign and/or explicitly selected envelope balances by the same amount.
- Planned transfers out of the budget require an explicit funding source. Do not consume reserved funds without an explicit cancellation/reallocation flow.
- If a real external transfer already occurred while underfunded, record the account event truthfully and flag the budget discrepancy for resolution.
- A direct eligibility toggle must either be rejected while it would break the budget identity or execute an explicit audited scope-entry/scope-exit operation with balancing postings.
- Accounts outside the budget can be tracked, but transactions wholly within excluded accounts have no budget effect by default.

## 8. Corrections, refunds, and reconciliation

- **Metadata correction** (e.g. merchant text): append an audit event; no financial effect.
- **Category correction:** reverse the incorrect budget postings and post the corrected budget postings; account postings remain unchanged.
- **Amount/source-account correction:** compensate the erroneous account and budget postings, then add replacement postings.
- **Cancellation of a committed duplicate/error:** add compensating postings. Preserve the original and its audit link.
- **Refund:** a genuine new transaction linked to the original purchase; cannot exceed its unrefunded balance. Partial refunds reverse only their amount.
- **Reconciliation difference:** requires user review, a reason, and matching account/budget effects. Never silently overwrite a bank balance or envelope balance.
- A deleted/archived category with historical transactions must remain resolvable in audit/history; prevent hard deletion if it would break references or balance explanations.

## 9. Monthly behavior and date rules

- Household timezone and currency are household-level settings.
- The effective transaction date determines the reporting month. Record creation/commit timestamps independently.
- Rollover is continuous balance carry-forward; there is no reset transaction at month-end.
- Monthly targets are planning values only. They neither allocate money nor cap rollover unless a future explicit feature adds configurable caps.
- Forecast income is never included in Ready to assign before actual receipt/posted event.
- Late entry/correction updates the appropriate historical reporting month and the current carried balance, while preserving audit history.
- Define and test a single policy for timezone conversion and date-only transactions; do not infer an arbitrary date from UTC timestamps without applying household timezone.

## 10. Reservations and suggestions

- Only explicitly planned purchases create reservations.
- Transaction drafts and future notification suggestions do not reserve funds or affect any financial balance.
- For debit/cash planned purchases, reserve both spendable envelope funds and spendable funds in the selected eligible source account.
- For credit-card purchases, reserve the envelope funds; a configured credit limit may be checked separately but does not affect budgeting identity.
- Reservation expiry/reminder policy may be implemented later, but reservations must be cancellable and queryable. Stale reservations must not be silently forgotten.
- Confirmation of a notification suggestion must run through the same transaction validation, authorization, idempotency, and ledger-posting code path as manual entry.

## 11. Concurrency, audit, and integrity

- The backend is authoritative. Client-side balance checks are advisory only.
- Balance checks plus postings must be concurrency-safe. Two users cannot spend or reallocate the same funds based on the same stale balance.
- Every mutation request should have an idempotency key (or equivalent deterministic deduplication mechanism) so retrying after network failure cannot double-post.
- Every committed event must record household, actor, effective date, creation/commit timestamp, source, event type, reference links, currency/amount, and reason when needed.
- Raw bank notification text is out of scope for the accounting core. Future Android ingestion should parse locally by default and sync only structured, consented suggestion data.
- Derived balance projections are acceptable only if rebuildable and regularly testable against journal postings.
- No operation may modify one household's data without authorization for that household.

## 12. Required acceptance tests

At minimum, write automated tests for the following. Include property-based or randomized sequence tests for the core balance identity if supported by the chosen stack.

1. Opening eligible balance increases Ready to assign without creating income/expense history.
2. Allocating `P` changes only the envelope and Ready to assign; rejects if `R < P`.
3. Reallocation conserves account balances and total allocations; rejects if the source has insufficient spendable funds.
4. A normal purchase within envelope and account limits posts once and preserves `A = R + sum(E)`.
5. An underfunded planned purchase requires reallocation or a reasoned emergency override.
6. An emergency override creates a visible, linked deficit and does not create cash or Ready to assign.
7. An actual external purchase can be recorded even when underfunded, with an explicit unresolved status.
8. Debit purchase reduces eligible cash and its envelope by equal amounts.
9. Credit-card purchase increases liability, decreases spending envelope, increases card-payment envelope, and leaves eligible cash unchanged.
10. Card payment reduces cash, liability, and card-payment envelope without a second spending entry.
11. Opening card debt does not create historical category spending or add to Ready to assign.
12. Transfers between eligible accounts do not change Ready to assign or envelopes.
13. Eligible-to-excluded transfer requires balancing budget removal; excluded-to-eligible transfer increases Ready to assign but is not income.
14. Reservation reduces envelope/account spendable amounts but leaves posted balances and the budget identity unchanged.
15. Cancelling a reservation releases it without a ledger effect.
16. Completing a reservation posts once and releases reservation atomically; amount variance is reauthorized.
17. Partial refund restores only the refunded amount; duplicate refund is rejected/deduplicated.
18. Credit-card refund after repayment handles a negative payment envelope/credit balance transparently while preserving the budget identity.
19. Reversal and replacement restore the correct account and budget effects without deleting the original audit event.
20. Confirmed reconciliation adjustment balances account and budget changes and records actor/reason.
21. Retrying any committed request with the same idempotency key does not duplicate postings.
22. Concurrent purchase/reallocation attempts cannot double-spend an envelope or source account.
23. Calendar-month reporting honors household timezone; late entries preserve effective date and audit timestamps.
24. All examples reject cross-household access and unauthorized mutations.
25. All financial arithmetic uses integer minor units and rejects unsupported currency mismatches.
26. A duplicate notification suggestion, if/when ingestion is implemented, does not create duplicate committed transactions.

## 13. Suggested implementation sequence

1. Implement pure domain types and balance/posting functions, independent of persistence.
2. Implement table-driven tests for every posting rule and failure case.
3. Add journal persistence with atomic commits, idempotency, and concurrency protection.
4. Add projections/read models for account balances, envelope balances, Ready to assign, spendable balances, and card liabilities.
5. Implement manual transaction, allocation, reallocation, transfer, refund, correction, and reservation API flows.
6. Implement household authorization and audit visibility.
7. Build the Android MVP on the stable domain/API contract.
8. Add future transaction-suggestion ingestion through the existing validation path; do not create a parallel ledger.

## 14. Non-goals for the MVP

- Automatic bank transaction imports or notification listening.
- Raw notification text upload or cloud parsing.
- Multi-currency accounts, FX, or exchange-rate gains/losses.
- Investment/property net-worth accounting.
- Historical import as a prerequisite for onboarding.
- Forecast income being usable for allocations.
- Automatic category spending or silently approving an underfunded planned purchase.
- Mandatory dual approval for every household emergency override.

---

**Implementation gate:** Before schema design is considered complete, the team should review the posting matrix, account-eligibility boundary, card-refund edge case, and the distinction between planned authorization and recording real-world transactions. These are the areas most likely to create accounting bugs if left implicit.
