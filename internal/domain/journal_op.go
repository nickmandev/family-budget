// This file implements the immutable journal-operation envelope described by
// POSTING_MATRIX §1 "Required event envelope" and DOMAIN_MODEL §3.8. A journal
// operation is the committed accounting unit: its postings, reservation hold
// transitions, related-operation links, and audit metadata travel together and
// never change after commit.
//
// The envelope validates structure and metadata only. Funding, liquidity,
// authorization, remainder and idempotency-replay checks belong to the command
// layer that builds operations; this layer neither knows nor trusts them.
package domain

import (
	"strings"
	"unicode/utf8"
)

// maxJournalReasonLength is the maximum length, in runes, of an operation
// reason (emergency override, reconciliation, or manual correction).
const maxJournalReasonLength = 500

// OperationType names the committed business operation. Values map to the
// posting-matrix rows in POSTING_MATRIX §2 so audit history and projections can
// interpret an operation without re-deriving its intent.
//
// Where several matrix rows share one posting shape and differ only in
// account/envelope metadata, they share one operation type:
//   - OpOpeningBalance covers the eligible, excluded, and card-liability
//     opening rows; account kind/eligibility determines the budget effect.
//   - OpIncome covers eligible and excluded income; eligibility determines
//     whether Ready-to-assign rises.
//   - OpDebitPurchase and OpCashPurchase both cover the "Debit/cash purchase"
//     row but keep the payment instrument (bank debit vs physical cash)
//     visible in the audit trail.
//
// Matrix rows that never create a committed financial operation (drafts,
// suggestions, monthly boundary) have no type here.
type OperationType string

const (
	// OpOpeningBalance establishes an opening account or card balance.
	OpOpeningBalance OperationType = "OPENING_BALANCE"
	// OpIncome records received income.
	OpIncome OperationType = "INCOME"
	// OpReimbursement records an unrelated reimbursement received into cash.
	OpReimbursement OperationType = "REIMBURSEMENT"
	// OpAllocate moves Ready-to-assign into an envelope.
	OpAllocate OperationType = "ALLOCATE"
	// OpReallocate moves budget value between envelopes.
	OpReallocate OperationType = "REALLOCATE"
	// OpDebitPurchase records a debit-card purchase.
	OpDebitPurchase OperationType = "DEBIT_PURCHASE"
	// OpCashPurchase records a physical cash-wallet purchase.
	OpCashPurchase OperationType = "CASH_PURCHASE"
	// OpCardPurchase records a credit-card purchase.
	OpCardPurchase OperationType = "CARD_PURCHASE"
	// OpCardPayment records a credit-card repayment.
	OpCardPayment OperationType = "CARD_PAYMENT"
	// OpCardFeeInterest records a card fee or interest charge.
	OpCardFeeInterest OperationType = "CARD_FEE_INTEREST"
	// OpTransferEligible moves value between eligible cash accounts.
	OpTransferEligible OperationType = "TRANSFER_ELIGIBLE"
	// OpTransferToEligible brings excluded cash into the budget.
	OpTransferToEligible OperationType = "TRANSFER_TO_ELIGIBLE"
	// OpTransferOutOfScope moves budget money to an excluded account.
	OpTransferOutOfScope OperationType = "TRANSFER_OUT_OF_SCOPE"
	// OpTransferExcluded moves value wholly outside the budget.
	OpTransferExcluded OperationType = "TRANSFER_EXCLUDED"
	// OpCashRefund records a refund into an eligible cash account.
	OpCashRefund OperationType = "CASH_REFUND"
	// OpCardRefund records a refund against a card purchase.
	OpCardRefund OperationType = "CARD_REFUND"
	// OpReservationCreate holds funds for a planned operation.
	OpReservationCreate OperationType = "RESERVATION_CREATE"
	// OpReservationResize increases or decreases a reservation.
	OpReservationResize OperationType = "RESERVATION_RESIZE"
	// OpReservationCancel releases a reservation.
	OpReservationCancel OperationType = "RESERVATION_CANCEL"
	// OpPurchaseComplete completes a reserved purchase.
	OpPurchaseComplete OperationType = "PURCHASE_COMPLETE"
	// OpExternalTransaction records an actual external purchase after the fact.
	OpExternalTransaction OperationType = "EXTERNAL_TRANSACTION"
	// OpReversal compensates an earlier committed operation.
	OpReversal OperationType = "REVERSAL"
	// OpReplacement compensates and replaces an earlier operation.
	OpReplacement OperationType = "REPLACEMENT"
	// OpCategoryCorrection moves a budget posting to another envelope.
	OpCategoryCorrection OperationType = "CATEGORY_CORRECTION"
	// OpAccountCorrection compensates and reposts an account effect.
	OpAccountCorrection OperationType = "ACCOUNT_CORRECTION"
	// OpMetadataCorrection records an audit-only correction with no financial
	// effect. It is the only type permitted to carry empty effects.
	OpMetadataCorrection OperationType = "METADATA_CORRECTION"
	// OpReconcileIncrease records a reconciliation increase in eligible cash.
	OpReconcileIncrease OperationType = "RECONCILE_INCREASE"
	// OpReconcileDecrease records a reconciliation decrease in eligible cash.
	OpReconcileDecrease OperationType = "RECONCILE_DECREASE"
)

// ParseOperationType validates a raw operation type.
func ParseOperationType(raw string) (OperationType, error) {
	opType := OperationType(raw)
	if !opType.IsValid() {
		return "", NewErrorf(CodeInvalidValue, "unknown operation type %q", raw)
	}
	return opType, nil
}

// IsValid reports whether the type is one of the defined values.
func (t OperationType) IsValid() bool {
	switch t {
	case OpOpeningBalance,
		OpIncome,
		OpReimbursement,
		OpAllocate,
		OpReallocate,
		OpDebitPurchase,
		OpCashPurchase,
		OpCardPurchase,
		OpCardPayment,
		OpCardFeeInterest,
		OpTransferEligible,
		OpTransferToEligible,
		OpTransferOutOfScope,
		OpTransferExcluded,
		OpCashRefund,
		OpCardRefund,
		OpReservationCreate,
		OpReservationResize,
		OpReservationCancel,
		OpPurchaseComplete,
		OpExternalTransaction,
		OpReversal,
		OpReplacement,
		OpCategoryCorrection,
		OpAccountCorrection,
		OpMetadataCorrection,
		OpReconcileIncrease,
		OpReconcileDecrease:
		return true
	default:
		return false
	}
}

// String returns the stored type text.
func (t OperationType) String() string {
	return string(t)
}

// AllowsEmptyEffects reports whether an operation of this type may commit with
// no postings and no reservation changes. Only metadata-only corrections are
// genuinely effect-free; every other committed type must carry effects (the
// command layer owns posting-shape rules, but an entirely empty operation is
// never meaningful for them).
func (t OperationType) AllowsEmptyEffects() bool {
	return t == OpMetadataCorrection
}

// JournalOperationParams is the validated input to NewJournalOperation. All
// required metadata from POSTING_MATRIX §1 is explicit:
//
//   - Actor is required. A documented system actor is represented by a stable
//     reserved UserId (for example NewUserId("system")); the domain does not
//     invent one here.
//   - Reason stores the human justification for any operation that needs one
//     (emergency override, reconciliation, manual correction). It is a plain
//     trimmed string rather than OverrideReason because reconciliation and
//     correction reasons are not emergency overrides; the command layer decides
//     when a reason is mandatory.
//   - Amount is optional; operations that have no single amount (for example a
//     reallocation between envelopes carries two sides) leave it nil.
//   - Effects may be empty only for types whose AllowsEmptyEffects is true.
type JournalOperationParams struct {
	ID                 JournalOperationId
	Household          HouseholdId
	Actor              UserId
	Type               OperationType
	Source             TransactionSource
	EffectiveDate      EffectiveDate
	Currency           Currency
	IdempotencyKey     IdempotencyKey
	RequestFingerprint RequestFingerprint
	Amount             *PositiveAmount
	Reason             string
	CreatedAt          RecordedAt
	CommittedAt        RecordedAt
	Effects            Effects
}

// JournalOperation is the immutable committed accounting unit: one business
// operation's postings, reservation transitions, links and audit metadata
// (POSTING_MATRIX §1, DOMAIN_MODEL §3.8).
//
// The zero value is invalid. Construct with NewJournalOperation. Fields are
// unexported so a committed operation cannot be rewritten; accessors return
// defensive copies of every slice.
type JournalOperation struct {
	id                 JournalOperationId
	household          HouseholdId
	actor              UserId
	opType             OperationType
	source             TransactionSource
	effectiveDate      EffectiveDate
	currency           Currency
	idempotencyKey     IdempotencyKey
	requestFingerprint RequestFingerprint
	amount             *PositiveAmount
	reason             string
	createdAt          RecordedAt
	committedAt        RecordedAt
	effects            Effects
}

// NewJournalOperation validates the envelope and builds an immutable
// operation.
//
// Required metadata rejects with INVALID_VALUE: zero id, household, actor, or
// timestamps; unknown operation type or source; zero effective date,
// idempotency key, or request fingerprint; a committed timestamp earlier than
// the created timestamp; empty effects for a type that does not allow them; and
// malformed postings, reservation changes, or related references. An invalid
// currency rejects with its own INVALID_VALUE. An amount or posting in another
// currency rejects with CURRENCY_MISMATCH; a non-positive amount rejects with
// INVALID_AMOUNT.
func NewJournalOperation(params JournalOperationParams) (JournalOperation, error) {
	if params.ID.IsZero() {
		return JournalOperation{}, NewError(CodeInvalidValue, "journal operation id must not be empty")
	}
	if params.Household.IsZero() {
		return JournalOperation{}, NewError(CodeInvalidValue, "journal operation household id must not be empty")
	}
	if params.Actor.IsZero() {
		return JournalOperation{}, NewError(CodeInvalidValue, "journal operation actor id must not be empty")
	}
	if !params.Type.IsValid() {
		return JournalOperation{}, NewErrorf(CodeInvalidValue, "unknown operation type %q", params.Type)
	}
	if !params.Source.IsValid() {
		return JournalOperation{}, NewErrorf(CodeInvalidValue, "unknown transaction source %q", params.Source)
	}
	if params.EffectiveDate.IsZero() {
		return JournalOperation{}, NewError(CodeInvalidValue, "journal operation effective date must be set")
	}
	if err := params.Currency.validate(); err != nil {
		return JournalOperation{}, err
	}
	if params.IdempotencyKey.IsZero() {
		return JournalOperation{}, NewError(CodeInvalidValue, "journal operation idempotency key must not be empty")
	}
	if params.RequestFingerprint.IsZero() {
		return JournalOperation{}, NewError(CodeInvalidValue, "journal operation request fingerprint must not be empty")
	}
	if params.CreatedAt.IsZero() {
		return JournalOperation{}, NewError(CodeInvalidValue, "journal operation created timestamp must be set")
	}
	if params.CommittedAt.IsZero() {
		return JournalOperation{}, NewError(CodeInvalidValue, "journal operation committed timestamp must be set")
	}
	if params.CommittedAt.Time().Before(params.CreatedAt.Time()) {
		return JournalOperation{}, NewError(CodeInvalidValue, "journal operation committed timestamp must not precede the created timestamp")
	}
	if params.Effects.IsEmpty() && !params.Type.AllowsEmptyEffects() {
		return JournalOperation{}, NewErrorf(CodeInvalidValue, "operation type %q requires financial effects", params.Type)
	}

	amount, err := validateOptionalAmount(params.Amount, params.Currency)
	if err != nil {
		return JournalOperation{}, err
	}
	effects, err := validateEffects(params.Effects, params.Currency)
	if err != nil {
		return JournalOperation{}, err
	}
	reason := strings.TrimSpace(params.Reason)
	if utf8.RuneCountInString(reason) > maxJournalReasonLength {
		return JournalOperation{}, NewErrorf(CodeInvalidValue, "journal operation reason must be at most %d characters", maxJournalReasonLength)
	}

	return JournalOperation{
		id:                 params.ID,
		household:          params.Household,
		actor:              params.Actor,
		opType:             params.Type,
		source:             params.Source,
		effectiveDate:      params.EffectiveDate,
		currency:           params.Currency,
		idempotencyKey:     params.IdempotencyKey,
		requestFingerprint: params.RequestFingerprint,
		amount:             amount,
		reason:             reason,
		createdAt:          params.CreatedAt,
		committedAt:        params.CommittedAt,
		effects:            effects,
	}, nil
}

// validateOptionalAmount validates the optional amount and returns a defensive
// copy.
func validateOptionalAmount(amount *PositiveAmount, currency Currency) (*PositiveAmount, error) {
	if amount == nil {
		return nil, nil
	}
	if amount.MinorUnits() <= 0 {
		return nil, NewErrorf(CodeInvalidAmount, "journal operation amount must be greater than zero, got %d minor units", amount.MinorUnits())
	}
	if err := amount.Currency().validate(); err != nil {
		return nil, err
	}
	if !amount.Currency().Equal(currency) {
		return nil, NewErrorf(CodeCurrencyMismatch, "journal operation amount currency %q does not match operation currency %q", amount.Currency(), currency)
	}
	copied := *amount
	return &copied, nil
}

// validateEffects revalidates and defensively copies postings, reservation
// changes, and related references, enforcing the operation currency.
func validateEffects(effects Effects, currency Currency) (Effects, error) {
	validated := Effects{
		Postings: PostingSet{
			Accounts:  make([]AccountPosting, 0, len(effects.Postings.Accounts)),
			Envelopes: make([]EnvelopePosting, 0, len(effects.Postings.Envelopes)),
		},
		Reservations: make([]ReservationChange, 0, len(effects.Reservations)),
		Related:      make([]RelatedOperationRef, 0, len(effects.Related)),
	}

	for _, posting := range effects.Postings.Accounts {
		if !posting.Delta.Currency().Equal(currency) {
			return Effects{}, NewErrorf(CodeCurrencyMismatch, "account posting currency %q does not match operation currency %q", posting.Delta.Currency(), currency)
		}
		revalidated, err := NewAccountPosting(posting.AccountID, posting.Delta)
		if err != nil {
			return Effects{}, err
		}
		validated.Postings.Accounts = append(validated.Postings.Accounts, revalidated)
	}
	for _, posting := range effects.Postings.Envelopes {
		if !posting.Delta.Currency().Equal(currency) {
			return Effects{}, NewErrorf(CodeCurrencyMismatch, "envelope posting currency %q does not match operation currency %q", posting.Delta.Currency(), currency)
		}
		revalidated, err := NewEnvelopePosting(posting.EnvelopeID, posting.Delta)
		if err != nil {
			return Effects{}, err
		}
		validated.Postings.Envelopes = append(validated.Postings.Envelopes, revalidated)
	}
	for _, change := range effects.Reservations {
		if !change.Envelope.Delta.Currency().Equal(currency) {
			return Effects{}, NewErrorf(CodeCurrencyMismatch, "reservation envelope hold currency %q does not match operation currency %q", change.Envelope.Delta.Currency(), currency)
		}
		if change.Account != nil && !change.Account.Delta.Currency().Equal(currency) {
			return Effects{}, NewErrorf(CodeCurrencyMismatch, "reservation account hold currency %q does not match operation currency %q", change.Account.Delta.Currency(), currency)
		}
		revalidated, err := NewReservationChange(change.PlanID, change.Envelope, change.Account)
		if err != nil {
			return Effects{}, err
		}
		validated.Reservations = append(validated.Reservations, revalidated)
	}
	for _, ref := range effects.Related {
		revalidated, err := NewRelatedOperationRef(ref.Kind, ref.OperationID)
		if err != nil {
			return Effects{}, err
		}
		validated.Related = append(validated.Related, revalidated)
	}
	return validated, nil
}

// ID returns the operation identifier.
func (o JournalOperation) ID() JournalOperationId {
	return o.id
}

// Household returns the owning household.
func (o JournalOperation) Household() HouseholdId {
	return o.household
}

// Actor returns the user or documented system actor that committed the
// operation.
func (o JournalOperation) Actor() UserId {
	return o.actor
}

// Type returns the committed business operation type.
func (o JournalOperation) Type() OperationType {
	return o.opType
}

// Source returns the operation provenance.
func (o JournalOperation) Source() TransactionSource {
	return o.source
}

// EffectiveDate returns the household-local transaction date.
func (o JournalOperation) EffectiveDate() EffectiveDate {
	return o.effectiveDate
}

// Currency returns the operation currency.
func (o JournalOperation) Currency() Currency {
	return o.currency
}

// IdempotencyKey returns the idempotency key that admitted the operation.
func (o JournalOperation) IdempotencyKey() IdempotencyKey {
	return o.idempotencyKey
}

// RequestFingerprint returns the canonical request fingerprint bound to the
// idempotency key.
func (o JournalOperation) RequestFingerprint() RequestFingerprint {
	return o.requestFingerprint
}

// Amount returns the operation amount when it has one.
func (o JournalOperation) Amount() (PositiveAmount, bool) {
	if o.amount == nil {
		return PositiveAmount{}, false
	}
	return *o.amount, true
}

// Reason returns the stored trimmed reason, or the empty string when none was
// supplied.
func (o JournalOperation) Reason() string {
	return o.reason
}

// CreatedAt returns the recorded instant at which the command was received.
func (o JournalOperation) CreatedAt() RecordedAt {
	return o.createdAt
}

// CommittedAt returns the recorded instant at which the operation committed.
func (o JournalOperation) CommittedAt() RecordedAt {
	return o.committedAt
}

// Postings returns a defensive copy of the stored posting set.
func (o JournalOperation) Postings() PostingSet {
	return PostingSet{
		Accounts:  append([]AccountPosting(nil), o.effects.Postings.Accounts...),
		Envelopes: append([]EnvelopePosting(nil), o.effects.Postings.Envelopes...),
	}
}

// ReservationChanges returns a defensive copy of the stored reservation hold
// transitions.
func (o JournalOperation) ReservationChanges() []ReservationChange {
	return copyReservationChanges(o.effects.Reservations)
}

// Related returns a defensive copy of the stored related-operation links.
func (o JournalOperation) Related() []RelatedOperationRef {
	return append([]RelatedOperationRef(nil), o.effects.Related...)
}

// Effects returns a defensive copy of the complete effect set.
func (o JournalOperation) Effects() Effects {
	return Effects{
		Postings:     o.Postings(),
		Reservations: o.ReservationChanges(),
		Related:      o.Related(),
	}
}

// copyReservationChanges copies the slice and each non-nil account hold.
func copyReservationChanges(changes []ReservationChange) []ReservationChange {
	if len(changes) == 0 {
		return nil
	}
	copied := make([]ReservationChange, len(changes))
	for i, change := range changes {
		copied[i] = change
		if change.Account != nil {
			account := *change.Account
			copied[i].Account = &account
		}
	}
	return copied
}
