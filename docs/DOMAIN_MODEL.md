# Domain Model — Household Envelope Budgeting

**Version:** 1.0  
**Status:** Proposed domain design for review before persistence/API implementation  
**Depends on:** `ACCOUNTING_SPEC.md`, `POSTING_MATRIX.md`, `STATE_TRANSITIONS.md`, `DECISIONS.yaml`  
**Scope:** Framework-, database-, and deployment-neutral model for the single-currency, Android-first household-budgeting MVP.

## 1. Purpose and boundaries

This document defines the core concepts, identities, value objects, commands, domain events, and logical consistency boundaries. It does **not** prescribe database tables, an ORM, API framework, programming language, or event-sourcing architecture.

The domain must keep four concerns separate:

1. **Financial facts:** committed account/liability and budget postings.
2. **Budget authorization:** whether a planned purchase was funded or explicitly overridden.
3. **Workflow state:** the lifecycle of a planned purchase or unconfirmed suggestion.
4. **Projections:** current account balances, envelope balances, Ready to assign, spendable balances, and reports derived from committed postings plus active reservations.

A notification suggestion is not a committed financial fact. A purchase authorization is not itself a completed purchase. A card-payment envelope is not a bank account. These distinctions must remain visible in the model.

## 2. Recommended logical consistency boundary

All operations that can affect a household's accounting invariants must commit atomically at the **household budget command boundary**. This includes every affected account posting, envelope posting, reservation change, audit record, idempotency record, and command outcome.

The implementation may use smaller persistence aggregates, transactions, locks, or optimistic concurrency, but it must preserve the same logical behavior. Do not let separate account and envelope updates commit independently.

Use a monotonically increasing household-ledger version or equivalent concurrency token for commands that depend on balances. A stale command must be revalidated against the latest committed state or rejected cleanly; it must never commit against stale spendable funds.

### Journal versus domain events

- The **journal** is the authoritative, append-only record of committed financial postings and their business operation metadata.
- **Domain events** describe business facts that occurred after a successful operation, for example `IncomeReceived` or `PurchaseCompleted`. They may support notifications, projections, and future integrations.
- Domain events are not automatically the accounting source of truth. This document does not require full event sourcing.
- Derived balances are projections of journal postings and account-eligibility/budget-scope history. Projections must be rebuildable and reconcilable against their sources.

## 3. Core entities

Entity names are conceptual. They need not correspond one-to-one with database tables.

### 3.1 Household

Represents one independent budget and security boundary.

**Identity:** `HouseholdId`

**Core attributes:**
- display name;
- one configured currency for the MVP;
- configured IANA timezone;
- household ledger version/concurrency token;
- lifecycle status;
- created/updated audit timestamps.

**Rules:** all accounts, envelopes, members, plans, suggestions, journal operations, and reservations belong to exactly one household. Cross-household references are invalid. Currency and timezone changes require explicit policy and audit; they must not silently reinterpret historical postings.

### 3.2 HouseholdMembership

Associates an authenticated user with a household.

**Core attributes:** household, user, membership status, role/capabilities, joined/removed timestamps.

**Recommended MVP permissions:**
- Every active member can view the shared budget and financial history.
- Every active member can perform ordinary budget operations permitted by the accounting rules.
- Every active member may invoke an emergency override, but must supply a reason; the exception is visible to the household.
- Household administration (inviting/removing members and changing household-level settings) is restricted to the owner/admin role.
- Removed members cannot access future household data or mutate the ledger. Their historical actor attribution remains.

Authentication identity is supplied by the application/security layer; the domain stores stable user references, not passwords or authentication secrets.

### 3.3 Account

Represents an account whose balance is tracked by the product.

**Identity:** `AccountId`

**Supported MVP account kinds:**
- `CHECKING`
- `SAVINGS`
- `CASH_WALLET`
- `CREDIT_CARD_LIABILITY`

Accounts may be excluded from the budgetable-cash boundary where appropriate. A credit-card liability is never an eligible liquid account and its credit limit is never budgetable.

**Core attributes:** household, name, kind, status, budget-eligibility flag where applicable, optional external/reference hint, created/updated metadata.

**Balance:** derived from account postings, not independently edited after opening/reconciliation operations.

**Rules:**
- An eligible liquid account contributes its signed posted balance to budgetable cash, including a real negative balance if one exists.
- An excluded cash account remains trackable but does not contribute to budgetable cash.
- A credit-card account uses liability sign convention: positive means owed; a charge increases liability, repayment/refund reduces it.
- An account with active reservations cannot be deleted. Archiving prevents new use but preserves history.
- Account eligibility changes are audited, effective-dated scope changes; a bare boolean flip is insufficient because the budget identity must remain valid.
- Eligibility transitions involving a negative balance require explicit review; they must not create artificial Ready-to-assign funds.

### 3.4 Envelope

Represents a budget purpose. Money in an envelope is a budget allocation, not a separate bank balance.

**Identity:** `EnvelopeId`

**Recommended kinds:**
- `SPENDING` — everyday category such as groceries, transport, or utilities;
- `SAVINGS` — protected savings goal or sinking fund;
- `CARD_PAYMENT` — funds allocated to pay one specific credit-card liability.

**Core attributes:** household, name, kind, status, optional linked card-liability account, optional category metadata, created/updated metadata.

**Balances:**
- `EnvelopeBalance` = sum of committed budget postings for the envelope.
- `ActiveReservations` = sum of active reservations against it.
- `SpendableBalance` = `EnvelopeBalance - ActiveReservations`.

**Rules:**
- Envelope balances roll over continuously across calendar months.
- Monthly targets are planning values, not ledger postings.
- For the MVP, use one dedicated card-payment envelope per credit-card liability account. A card purchase transfers budget from the spending envelope to that card's payment envelope.
- Card-payment envelopes cannot be used as ordinary spending categories. A repayment reduces the linked card-payment envelope.
- A nonzero envelope cannot be deleted. It must be funded/transferred to another envelope or archived under a policy that preserves its balance/history. Recommended MVP rule: an envelope can be archived for new activity only after its balance and reservations are zero.
- An envelope with active reservations cannot be archived until reservations are released or completed.

### 3.5 MonthlyEnvelopeTarget

A planning record for an envelope and calendar period.

**Identity:** `(HouseholdId, EnvelopeId, YearMonth)`

**Attributes:** target amount (integer minor units), currency (household currency), author and audit metadata.

**Rules:** changing a target does not allocate money, change the envelope balance, or change Ready to assign. Targets can be zero or absent. A future UI may use targets to suggest allocation amounts, but the domain must not auto-fund an envelope merely because a target exists.

### 3.6 PlannedPurchase

Represents a purchase the user wants to authorize before it happens.

**Identity:** `PlannedPurchaseId`

**Core attributes:** household, intended amount, spending envelope, payment method/source account or card, planned effective date, lifecycle state, creator, authorization details, active reservation references, optional merchant/description, timestamps.

**Lifecycle:** follow `STATE_TRANSITIONS.md`. At minimum the model distinguishes draft, normally reserved, emergency-override reserved, completed, and cancelled states. An implementation may represent the state with an enum or richer transition records, but it must reject invalid transitions.

**Rules:**
- Drafts reserve nothing.
- Normal authorization requires sufficient envelope spendable balance and, for debit/cash, enough spendable source-account liquidity.
- Emergency override requires a nonempty reason and actor. It bypasses the envelope funding check only, never account liquidity or other hard constraints.
- A planned credit-card purchase requires envelope funding/override and card-payment budget movement; credit limit is never budgetable cash.
- Completion posts the actual financial event and releases the plan's reservation atomically.
- If final amount exceeds authorization, require reauthorization. If lower, release the excess reservation.
- Completion is idempotent; the plan cannot post twice.
- Cancellation releases reservations and creates no financial posting.

### 3.7 TransactionSuggestion

Represents unconfirmed transaction data, entered manually as a draft or later derived from a notification/import.

**Identity:** `SuggestionId`

**Core attributes:** household, source kind, extracted/user-entered candidate fields, field confidence/provenance where applicable, lifecycle state, creator/source reference, timestamps.

**Rules:**
- Suggestions and drafts have no journal postings and no reservations.
- The user may edit or cancel a suggestion.
- Confirmation of a factual transaction that has already occurred records that fact through the shared posting path; insufficient budget funding becomes a visible resolution issue rather than a reason to discard the fact.
- Confirmation of a future planned purchase routes through `PlannedPurchase` authorization instead of committing spending immediately.
- A future notification parser should process raw notification text locally by default and synchronize only the required structured suggestion data.
- Deduplication fingerprints help suppress repeated source notifications but must not assume identical merchant/amount pairs always mean the same purchase.

### 3.8 JournalOperation and Posting

A journal operation is the immutable committed accounting unit. One business operation may create several postings across accounts, liabilities, and envelopes.

**Identity:** `JournalOperationId`

**Required operation metadata:**
- household and operation ID;
- operation type;
- actor/user where applicable;
- recorded/committed timestamp;
- effective transaction date;
- source/provenance (manual entry, planned purchase, external transaction, opening balance, reconciliation, etc.);
- idempotency key and payload fingerprint;
- related operation references (original, reversal, refund, replacement, plan);
- reason/authorization metadata where applicable;
- journal postings;
- schema/spec version if needed for replay compatibility.

**Posting kinds:**
- `AccountPosting`: signed delta to an account balance. Cash assets use positive for inflow and negative for outflow; card liabilities use positive for additional debt and negative for reduction of debt.
- `EnvelopePosting`: signed delta to an envelope balance. Positive adds allocated budget; negative removes allocated budget through spending, reallocation, card funding, correction or other explicit event.

Ready to assign is derived from current eligible cash balances minus the sum of envelope balances. It is not a separately editable account or envelope, and no independent `ReadyToAssign` balance should be maintained as a competing source of truth.

**Rules:**
- Journal operations and postings are append-only after commit.
- Corrections use compensating operation(s) and, where required, replacement operation(s).
- Genuine refunds are new linked operations, not reversals.
- All postings for a command commit atomically with related reservation transitions, idempotency result, and audit metadata.
- A rejected command produces no partial financial mutation.

### 3.9 Reservation

Represents money held for an explicitly planned purchase.

**Identity:** `ReservationId`

**Core attributes:** household, planned purchase, target envelope, amount, optional source cash account, lifecycle status, created/updated timestamps.

**Rules:**
- Only explicitly planned purchases create reservations.
- Drafts and transaction suggestions never reserve.
- Envelope reservation reduces spendable envelope balance, not posted envelope balance.
- Debit/cash plans reserve both the envelope and the selected eligible source account.
- Credit-card plans reserve envelope/card-payment funding as defined in the posting/state specs; they do not reserve account cash at authorization unless the user is separately planning a payment from that account.
- Reservations are released, resized or consumed atomically when a plan is cancelled, changed, or completed.
- Cancelled/completed plans must not retain active reservations.
- Reservation expiry, if added, must be an explicit product rule, not silent automatic deletion.

### 3.10 AuditRecord and IdempotencyRecord

These may be implemented as fields on operations or separate records.

**AuditRecord** captures actor, action, timestamp, reason, relevant references, and non-sensitive before/after context for important changes.

**IdempotencyRecord** binds a household-scoped idempotency key to a canonical request fingerprint and the original result. Reuse with the same payload returns the original result; reuse with a different payload is rejected.

Do not store passwords, access tokens, or raw banking notification bodies in audit records.

## 4. Value objects

Use immutable value objects or equivalent validated types for domain inputs and outputs.

| Type | Meaning and validation |
|---|---|
| `Money` | Integer minor units plus household currency identity; addition/comparison only within same currency. No floating point. |
| `PositiveAmount` | A strictly positive `Money` amount used for purchases, allocations and payments. |
| `SignedMoneyDelta` | A signed integer minor-unit delta for postings. |
| `HouseholdId`, `UserId`, `AccountId`, `EnvelopeId`, `PlanId`, `SuggestionId`, `JournalOperationId`, `ReservationId` | Strong identifiers; never accept a reference from another household. |
| `EffectiveDate` | Local calendar date used for transaction reporting. |
| `RecordedAt` | Instant at which the operation was received/committed; separate from effective date. |
| `YearMonth` | Calendar budget/reporting period interpreted using household timezone. |
| `Currency` | Single configured household currency; precision from explicit currency metadata. |
| `IdempotencyKey` | Stable key for safely retrying a command. |
| `RequestFingerprint` | Canonical hash/equivalent used to reject changed payloads under a reused idempotency key. |
| `AuthorizationMode` | `NORMAL` or `EMERGENCY_OVERRIDE`. |
| `OverrideReason` | Validated nonblank reason, actor and timestamp. |
| `TransactionSource` | `MANUAL`, `PLANNED_PURCHASE`, `EXTERNAL_ENTRY`, `OPENING_BALANCE`, `RECONCILIATION`; future source kinds must not change posting semantics by themselves. |
| `RelatedOperationRef` | Typed link to original, refund, reversal or replacement operation. |

Validation belongs in these types and domain commands rather than only in UI form validation.

## 5. Command catalogue

Commands are requests to perform a domain operation. A successful command commits all effects atomically and returns stable identifiers/result metadata. A rejected command leaves financial and lifecycle state unchanged.

### Household and setup

- `CreateHousehold(currency, timezone, owner)`
- `InviteMember(household, actor, invitee)`
- `AcceptMembership(household, user)`
- `RemoveMember(household, actor, member)`
- `CreateAccount(household, kind, name, eligibility)`
- `RecordOpeningAccountBalance(account, amount, effectiveDate)`
- `RecordOpeningCardLiability(card, amount, effectiveDate)`
- `ChangeAccountEligibility(account, newEligibility, fundingPlan, effectiveDate)`
- `ArchiveAccount(account)`
- `CreateEnvelope(household, name, kind, linkedCard?)`
- `SetMonthlyEnvelopeTarget(envelope, yearMonth, target)`
- `ArchiveEnvelope(envelope, resolutionPlan)`

### Budget allocations

- `AllocateFunds(envelope, amount)` — requires sufficient Ready to assign.
- `ReallocateFunds(sourceEnvelope, destinationEnvelope, amount)` — requires sufficient source spendable balance.
- `MoveBudgetOutOfScope(account, amount, fundingSources)` — for an actual transfer to an excluded account; uses Ready to assign and/or explicitly selected envelopes and maintains the budget identity.

### Income, spending and transfers

- `RecordIncome(account, amount, effectiveDate, metadata)`
- `RecordDebitPurchase(account, envelope, amount, effectiveDate, authorization?)`
- `RecordCashPurchase(cashWallet, envelope, amount, effectiveDate, authorization?)`
- `RecordCreditCardPurchase(card, spendingEnvelope, amount, effectiveDate, authorization?)`
- `RecordCreditCardRepayment(cashAccount, card, amount, paymentEnvelope, effectiveDate)`
- `TransferBetweenEligibleAccounts(source, destination, amount, effectiveDate)`
- `TransferExcludedIntoEligible(source, destination, amount, effectiveDate)`
- `TransferEligibleOutToExcluded(source, destination, amount, fundingSources, effectiveDate)`
- `RecordCardFeeOrInterest(card, feeEnvelope, amount, effectiveDate)`

### Planned purchases and reservations

- `CreatePurchaseDraft(...)`
- `EditPurchaseDraft(plan, changes)`
- `AuthorizePurchase(plan)`
- `AuthorizeEmergencyOverride(plan, reason)`
- `ResizePurchaseReservation(plan, newAmount)`
- `CancelPurchase(plan, reason?)`
- `CompletePurchase(plan, finalAmount, effectiveDate, idempotencyKey)`

### Real-world transactions, corrections and reconciliation

- `RecordExternalCompletedTransaction(...)` — factual event; can post despite underfunding, which is separately flagged for budget resolution.
- `RecordRefund(originalOperation, amount, destination, effectiveDate)`
- `ReverseOperation(originalOperation, amount, reason)`
- `CorrectTransaction(originalOperation, correction, reason)`
- `CorrectTransactionMetadata(originalOperation, changes, reason)` — no financial postings if only nonfinancial metadata changes.
- `ReconcileAccount(account, observedBalance, effectiveDate, explanation)`

### Suggestions (future capability; model lifecycle now, do not implement notification ingestion in MVP)

- `CreateSuggestion(source, candidateFields)`
- `EditSuggestion(suggestion, changes)`
- `CancelSuggestion(suggestion, reason?)`
- `ConfirmSuggestionAsExternalTransaction(suggestion, corrections?)`
- `ConvertSuggestionToPlannedPurchase(suggestion, corrections?)`

## 6. Domain event catalogue

Emit events only after their command successfully commits. Event delivery to other components should use reliable outbox/equivalent behavior if required; do not publish an event that says a financial operation succeeded before the commit succeeds.

Suggested facts:

- `HouseholdCreated`, `HouseholdMemberAdded`, `HouseholdMemberRemoved`
- `AccountCreated`, `OpeningAccountBalanceRecorded`, `OpeningCardLiabilityRecorded`
- `AccountEligibilityChanged`, `AccountArchived`
- `EnvelopeCreated`, `EnvelopeTargetChanged`, `EnvelopeArchived`
- `FundsAllocated`, `FundsReallocated`
- `IncomeReceived`, `DebitPurchaseRecorded`, `CashPurchaseRecorded`, `CreditCardPurchaseRecorded`
- `CreditCardRepaymentRecorded`, `AccountTransferRecorded`, `BudgetBoundaryTransferRecorded`, `CardFeeOrInterestRecorded`
- `PurchaseDraftCreated`, `PurchaseAuthorized`, `EmergencyOverrideAuthorized`, `PurchaseReservationChanged`, `PurchaseCancelled`, `PurchaseCompleted`
- `ExternalTransactionRecorded`, `TransactionRefunded`, `TransactionPartiallyReversed`, `TransactionFullyReversed`, `TransactionCorrected`, `TransactionMetadataCorrected`
- `AccountReconciled`, `TransactionSuggestionCreated`, `TransactionSuggestionEdited`, `TransactionSuggestionCancelled`, `TransactionSuggestionConfirmed`

Domain event names are guidance, not a required event-sourcing list. A single command may emit one business event plus several journal postings. Do not treat each posting line as an independent user-visible financial transaction.

## 7. State ownership and separation

Avoid a single overloaded `status` column on a generic transaction object. At least these independent state dimensions exist:

1. **Plan lifecycle:** draft/reserved/reserved-with-exception/completed/cancelled.
2. **Financial posting lifecycle:** committed/partially reversed/fully reversed, with state derivable from original postings and linked compensations.
3. **Budget resolution:** clear/requires-resolution/resolved. A factual external transaction may be committed while budget resolution remains outstanding.
4. **Suggestion lifecycle:** suggested/edited/cancelled/confirmed.
5. **Reservation lifecycle:** active/released/consumed.
6. **Account/envelope lifecycle:** active/archived, with archive rules preventing new use without erasing history.

Every transition must conform to `STATE_TRANSITIONS.md`. No state field should imply an account or budget effect unless the corresponding operation actually committed.

## 8. Derived query models

The domain should expose query results with explicit names rather than a generic `balance` field:

- `PostedAccountBalance(account)` — sum of committed account postings plus the account's opening posting, subject to eligibility being a separate property.
- `BudgetableCash(household)` — sum of posted balances for currently budget-eligible liquid accounts under the effective eligibility history.
- `EnvelopeBalance(envelope)` — sum of committed envelope postings.
- `ActiveEnvelopeReservations(envelope)` — sum of active reservation amounts.
- `SpendableEnvelopeBalance(envelope)` — envelope balance minus active reservations.
- `SpendableAccountBalance(account)` — posted eligible cash balance minus active account reservations and recognized holds.
- `ReadyToAssign(household)` — budgetable cash minus the sum of envelope balances.
- `CardLiabilityBalance(card)` — opening liability plus account/liability posting deltas.
- `CardPaymentEnvelopeBalance(card)` — balance of the one payment envelope linked to that card.
- `BudgetSummary(household, YearMonth)` — target, allocations, activity and carried balance as separate fields; report period is based on effective dates and household timezone.

Projections may be materialized for performance, but must be rebuildable and checked against journal postings. Do not persist independent balances as competing sources of truth.

## 9. Required domain error categories

Use stable domain errors that the API/UI can map to user-facing messages. Examples:

- `HOUSEHOLD_ACCESS_DENIED`
- `ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD`
- `INVALID_ENTITY_STATE`
- `INVALID_AMOUNT`
- `CURRENCY_MISMATCH`
- `INSUFFICIENT_READY_TO_ASSIGN`
- `INSUFFICIENT_ENVELOPE_FUNDS`
- `INSUFFICIENT_ACCOUNT_FUNDS`
- `OVERRIDE_REASON_REQUIRED`
- `RESERVATION_MISMATCH`
- `AMOUNT_EXCEEDS_AUTHORIZATION`
- `REFUND_EXCEEDS_REMAINDER`
- `REVERSAL_EXCEEDS_REMAINDER`
- `IDEMPOTENCY_KEY_PAYLOAD_MISMATCH`
- `CONCURRENCY_CONFLICT`
- `ACCOUNT_SCOPE_CHANGE_REQUIRES_FUNDING_PLAN`
- `ACCOUNTING_INVARIANT_VIOLATION`

Do not leak whether an entity exists in another household through distinct externally observable errors; security boundary failures may map to a common not-found/forbidden response at the API boundary.

## 10. Open questions and proposed defaults

These are domain decisions recommended for consistency, but should be surfaced as such rather than silently baked into database design:

1. **One card-payment envelope per card:** recommended and specified here to keep card funding distinct. If allowing a shared envelope later, define how a payment is attributed across cards.
2. **Envelope archive:** recommended only when balance is zero and no active reservations remain; transfer funds first.
3. **Account archive:** allowed only when no active reservations remain; historical postings stay intact. Do not delete account history.
4. **Account eligibility changes:** effective-dated, audited, and require an explicit funding plan when they alter the budgetable-cash boundary. Changing eligibility for an account with a negative balance requires a reviewed resolution; no automatic Ready-to-assign windfall.
5. **Opening balance for excluded accounts:** record the balance, but do not post it to budgetable cash. Enabling eligibility later is a budget-scope transition, not income.
6. **Roles:** active members can perform budget operations; only owner/admin manages membership and household settings. Emergency override remains available to any active member with a required reason.
7. **Reconciliation adjustment category:** require an explicit category or funding treatment when an adjustment affects eligible cash; do not silently assign discrepancy to income or miscellaneous spending.

If implementation reveals a need to change one of these defaults, agents must propose the exact scenario, preferred rule, alternatives, and tests affected before altering the canonical specification.

## 11. Agent workflow and acceptance criteria

1. Read `AGENT_BRIEF.md`, `ACCOUNTING_SPEC.md`, `POSTING_MATRIX.md`, `STATE_TRANSITIONS.md`, `DOMAIN_MODEL.md`, and `DECISIONS.yaml`.
2. Report contradictions or missing rules before choosing a persistence shape; suggest defaults with test impacts.
3. Implement validated value objects and pure posting/authorization functions first.
4. Implement commands as atomic operations and return explicit outcomes/errors.
5. Write unit tests for each posting matrix entry and state transition.
6. Add property/sequence tests for budget identity, reservations, refunds, correction chains and transfers across the eligibility boundary.
7. Add concurrency/idempotency tests and rebuild projections from journal postings.
8. Only then propose schema/API details, keeping them aligned with this domain contract.

Do not claim acceptance criteria have passed unless tests were executed and results are reported. This document is a design artifact, not implemented behavior.
