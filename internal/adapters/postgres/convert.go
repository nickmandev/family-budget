package postgres

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// This file is the single bidirectional mapping layer between domain values and
// GORM rows. Two mapping decisions are worth documenting:
//
//   - Money is never a float. Every amount maps to an int64 minor-unit column,
//     and the currency comes from either the row itself (journal operations) or
//     the owning household (plans, suggestions, reservations, which have no
//     currency columns).
//   - related_links.target_plan_id has no domain representation: the current
//     domain RelatedOperationRef always targets a journal operation. The
//     adapter therefore writes target_operation_id only and reports a corrupt/
//     unsupported plan-target link as INVALID_VALUE on read rather than
//     silently dropping it.

// uuidValue converts a strong domain identifier string to a pgtype.UUID.
func uuidValue(raw string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		return pgtype.UUID{}, domain.WrapError(domain.CodeInvalidValue, fmt.Sprintf("invalid UUID %q", raw), err)
	}
	return id, nil
}

// uuidFromDomain converts a domain identifier. It is a programming-error guard:
// domain identifiers come from validated constructors, so a malformed value is
// surfaced as INVALID_VALUE instead of panicking.
func uuidFromDomain(id string) (pgtype.UUID, error) {
	if id == "" {
		return pgtype.UUID{}, domain.NewError(domain.CodeInvalidValue, "empty identifier cannot be stored as a UUID")
	}
	return uuidValue(id)
}

// uuidString returns the canonical UUID text, or "" for an invalid/NULL value.
func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}

// uuidPointer returns a domain account-id pointer for a nullable UUID column.
func uuidPointer(id pgtype.UUID) *domain.AccountId {
	if !id.Valid {
		return nil
	}
	value := domain.AccountId(id.String())
	return &value
}

// nullableUUID converts an optional string identifier to a nullable UUID.
func nullableUUID(raw string) (pgtype.UUID, error) {
	if raw == "" {
		return pgtype.UUID{}, nil
	}
	return uuidValue(raw)
}

// currencyFromRow builds a domain Currency from a persisted code/exponent pair.
func currencyFromRow(code string, exponent int16) (domain.Currency, error) {
	currency, err := domain.NewCurrency(code, int(exponent))
	if err != nil {
		return domain.Currency{}, err
	}
	return currency, nil
}

// effectiveDateValue stores a calendar date without any timezone conversion.
func effectiveDateValue(date domain.EffectiveDate) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
}

// effectiveDateFromRow reads a calendar date from a DATE column. The components
// are used as stored; no timezone conversion is applied (Q7).
func effectiveDateFromRow(value time.Time) (domain.EffectiveDate, error) {
	year, month, day := value.Date()
	return domain.NewEffectiveDate(year, month, day)
}

// optionalString maps an empty string to SQL NULL.
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// stringValue maps SQL NULL to the empty string.
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// positiveAmount builds a positive domain amount; a non-positive persisted
// value is INVALID_AMOUNT rather than a silent zero.
func positiveAmount(minorUnits int64, currency domain.Currency) (domain.PositiveAmount, error) {
	return domain.NewPositiveAmountFromMinorUnits(minorUnits, currency)
}

// positiveOrZero builds the domain's internal "not held" representation for a
// zero persisted amount. Plans use a zero PositiveAmount for absent holds and
// released amounts, matching the aggregate's own representation.
func positiveOrZero(minorUnits int64, currency domain.Currency) (domain.PositiveAmount, error) {
	if minorUnits == 0 {
		return domain.PositiveAmount{}, nil
	}
	return positiveAmount(minorUnits, currency)
}

// signedDelta builds a non-zero signed posting delta.
func signedDelta(minorUnits int64, currency domain.Currency) (domain.SignedMoneyDelta, error) {
	return domain.NewSignedMoneyDelta(minorUnits, currency)
}

// Household mappings.

func householdToModel(record ports.HouseholdRecord) (householdModel, error) {
	id, err := uuidFromDomain(record.ID.String())
	if err != nil {
		return householdModel{}, err
	}
	return householdModel{
		ID:               id,
		Name:             record.Name,
		CurrencyCode:     record.Currency.Code(),
		CurrencyExponent: int16(record.Currency.Exponent()),
		Timezone:         record.Timezone,
		LedgerVersion:    int64(record.LedgerVersion),
		Status:           string(record.Status),
		CreatedAt:        record.CreatedAt.UTC(),
		UpdatedAt:        record.UpdatedAt.UTC(),
	}, nil
}

func householdFromModel(row householdModel) (ports.HouseholdRecord, error) {
	currency, err := currencyFromRow(row.CurrencyCode, row.CurrencyExponent)
	if err != nil {
		return ports.HouseholdRecord{}, err
	}
	return ports.HouseholdRecord{
		ID:            domain.HouseholdId(row.ID.String()),
		Name:          row.Name,
		Currency:      currency,
		Timezone:      row.Timezone,
		Status:        ports.LifecycleStatus(row.Status),
		LedgerVersion: uint64(row.LedgerVersion),
		CreatedAt:     row.CreatedAt.UTC(),
		UpdatedAt:     row.UpdatedAt.UTC(),
	}, nil
}

// User mappings.

func userToModel(record ports.UserRecord) (userModel, error) {
	id, err := uuidFromDomain(record.ID.String())
	if err != nil {
		return userModel{}, err
	}
	return userModel{
		ID:           id,
		Email:        record.Email,
		PasswordHash: record.PasswordHash,
		CreatedAt:    record.CreatedAt.UTC(),
		UpdatedAt:    record.UpdatedAt.UTC(),
	}, nil
}

func userFromModel(row userModel) ports.UserRecord {
	return ports.UserRecord{
		ID:           domain.UserId(row.ID.String()),
		Email:        row.Email,
		PasswordHash: row.PasswordHash,
		CreatedAt:    row.CreatedAt.UTC(),
		UpdatedAt:    row.UpdatedAt.UTC(),
	}
}

// Membership mappings.

func membershipToModel(household domain.HouseholdId, record ports.MembershipRecord) (membershipModel, error) {
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return membershipModel{}, err
	}
	userID, err := uuidFromDomain(record.UserID.String())
	if err != nil {
		return membershipModel{}, err
	}
	return membershipModel{
		HouseholdID: householdID,
		UserID:      userID,
		Role:        string(record.Role),
		Status:      string(record.Status),
		JoinedAt:    record.JoinedAt.UTC(),
		RemovedAt:   utcTimePointer(record.RemovedAt),
	}, nil
}

func membershipFromModel(row membershipModel) ports.MembershipRecord {
	return ports.MembershipRecord{
		UserID:    domain.UserId(row.UserID.String()),
		Role:      ports.MembershipRole(row.Role),
		Status:    ports.MembershipStatus(row.Status),
		JoinedAt:  row.JoinedAt.UTC(),
		RemovedAt: utcTimePointer(row.RemovedAt),
	}
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

// Account mappings.

func accountToModel(household domain.HouseholdId, record ports.AccountRecord) (accountModel, error) {
	id, err := uuidFromDomain(record.ID.String())
	if err != nil {
		return accountModel{}, err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return accountModel{}, err
	}
	return accountModel{
		ID:             id,
		HouseholdID:    householdID,
		Name:           record.Name,
		Kind:           record.Kind.String(),
		BudgetEligible: record.BudgetEligible,
		Status:         string(record.Status),
		CreatedAt:      record.CreatedAt.UTC(),
		UpdatedAt:      record.UpdatedAt.UTC(),
	}, nil
}

func accountFromModel(row accountModel) (ports.AccountRecord, error) {
	kind, err := domain.ParseAccountKind(row.Kind)
	if err != nil {
		return ports.AccountRecord{}, err
	}
	return ports.AccountRecord{
		ID:             domain.AccountId(row.ID.String()),
		Name:           row.Name,
		Kind:           kind,
		BudgetEligible: row.BudgetEligible,
		Status:         ports.LifecycleStatus(row.Status),
		CreatedAt:      row.CreatedAt.UTC(),
		UpdatedAt:      row.UpdatedAt.UTC(),
	}, nil
}

// Envelope mappings.

func envelopeToModel(household domain.HouseholdId, record ports.EnvelopeRecord) (envelopeModel, error) {
	id, err := uuidFromDomain(record.ID.String())
	if err != nil {
		return envelopeModel{}, err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return envelopeModel{}, err
	}
	linked := pgtype.UUID{}
	if record.LinkedCardAccountID != nil {
		linked, err = uuidFromDomain(record.LinkedCardAccountID.String())
		if err != nil {
			return envelopeModel{}, err
		}
	}
	return envelopeModel{
		ID:                  id,
		HouseholdID:         householdID,
		Name:                record.Name,
		Status:              string(record.Status),
		LinkedCardAccountID: linked,
		CreatedAt:           record.CreatedAt.UTC(),
		UpdatedAt:           record.UpdatedAt.UTC(),
	}, nil
}

func envelopeFromModel(row envelopeModel) ports.EnvelopeRecord {
	return ports.EnvelopeRecord{
		ID:                  domain.EnvelopeId(row.ID.String()),
		Name:                row.Name,
		Status:              ports.LifecycleStatus(row.Status),
		LinkedCardAccountID: uuidPointer(row.LinkedCardAccountID),
		CreatedAt:           row.CreatedAt.UTC(),
		UpdatedAt:           row.UpdatedAt.UTC(),
	}
}

// Journal operation mappings.

func journalOperationToModel(op domain.JournalOperation) (journalOperationModel, error) {
	id, err := uuidFromDomain(op.ID().String())
	if err != nil {
		return journalOperationModel{}, err
	}
	household, err := uuidFromDomain(op.Household().String())
	if err != nil {
		return journalOperationModel{}, err
	}
	actor, err := uuidFromDomain(op.Actor().String())
	if err != nil {
		return journalOperationModel{}, err
	}

	var amount *int64
	if value, ok := op.Amount(); ok {
		minor := value.MinorUnits()
		amount = &minor
	}

	return journalOperationModel{
		ID:                 id,
		HouseholdID:        household,
		ActorID:            actor,
		OpType:             op.Type().String(),
		Source:             op.Source().String(),
		EffectiveDate:      effectiveDateValue(op.EffectiveDate()),
		CurrencyCode:       op.Currency().Code(),
		CurrencyExponent:   int16(op.Currency().Exponent()),
		AmountMinor:        amount,
		Reason:             optionalString(op.Reason()),
		IdempotencyKey:     op.IdempotencyKey().String(),
		RequestFingerprint: op.RequestFingerprint().String(),
		CreatedAt:          op.CreatedAt().Time().UTC(),
		CommittedAt:        op.CommittedAt().Time().UTC(),
	}, nil
}

func journalOperationFromModel(row journalOperationModel, effects domain.Effects) (domain.JournalOperation, error) {
	currency, err := currencyFromRow(row.CurrencyCode, row.CurrencyExponent)
	if err != nil {
		return domain.JournalOperation{}, err
	}
	date, err := effectiveDateFromRow(row.EffectiveDate)
	if err != nil {
		return domain.JournalOperation{}, err
	}
	opType, err := domain.ParseOperationType(row.OpType)
	if err != nil {
		return domain.JournalOperation{}, err
	}
	source, err := domain.ParseTransactionSource(row.Source)
	if err != nil {
		return domain.JournalOperation{}, err
	}

	var amount *domain.PositiveAmount
	if row.AmountMinor != nil {
		value, err := positiveAmount(*row.AmountMinor, currency)
		if err != nil {
			return domain.JournalOperation{}, err
		}
		amount = &value
	}

	return domain.NewJournalOperation(domain.JournalOperationParams{
		ID:                 domain.JournalOperationId(row.ID.String()),
		Household:          domain.HouseholdId(row.HouseholdID.String()),
		Actor:              domain.UserId(row.ActorID.String()),
		Type:               opType,
		Source:             source,
		EffectiveDate:      date,
		Currency:           currency,
		IdempotencyKey:     domain.IdempotencyKey(row.IdempotencyKey),
		RequestFingerprint: domain.RequestFingerprint(row.RequestFingerprint),
		Amount:             amount,
		Reason:             stringValue(row.Reason),
		CreatedAt:          domain.NewRecordedAt(row.CreatedAt),
		CommittedAt:        domain.NewRecordedAt(row.CommittedAt),
		Effects:            effects,
	})
}

// effectsFromRows rebuilds a domain effect set from the typed child rows.
func effectsFromRows(
	accounts []accountPostingModel,
	envelopes []envelopePostingModel,
	reservations []reservationChangeModel,
	related []relatedLinkModel,
	currency domain.Currency,
) (domain.Effects, error) {
	effects := domain.Effects{}
	for _, row := range accounts {
		delta, err := signedDelta(row.DeltaMinor, currency)
		if err != nil {
			return domain.Effects{}, err
		}
		posting, err := domain.NewAccountPosting(domain.AccountId(row.AccountID.String()), delta)
		if err != nil {
			return domain.Effects{}, err
		}
		effects.Postings.Accounts = append(effects.Postings.Accounts, posting)
	}
	for _, row := range envelopes {
		delta, err := signedDelta(row.DeltaMinor, currency)
		if err != nil {
			return domain.Effects{}, err
		}
		posting, err := domain.NewEnvelopePosting(domain.EnvelopeId(row.EnvelopeID.String()), delta)
		if err != nil {
			return domain.Effects{}, err
		}
		effects.Postings.Envelopes = append(effects.Postings.Envelopes, posting)
	}
	for _, row := range reservations {
		envelopeDelta, err := signedDelta(row.EnvelopeDeltaMinor, currency)
		if err != nil {
			return domain.Effects{}, err
		}
		envelopeHold, err := domain.NewEnvelopeHold(domain.EnvelopeId(row.EnvelopeID.String()), envelopeDelta)
		if err != nil {
			return domain.Effects{}, err
		}
		var accountHold *domain.AccountHold
		if row.AccountID.Valid && row.AccountDeltaMinor != nil {
			accountDelta, err := signedDelta(*row.AccountDeltaMinor, currency)
			if err != nil {
				return domain.Effects{}, err
			}
			hold, err := domain.NewAccountHold(domain.AccountId(row.AccountID.String()), accountDelta)
			if err != nil {
				return domain.Effects{}, err
			}
			accountHold = &hold
		}
		change, err := domain.NewReservationChange(domain.PlanId(row.PlanID.String()), envelopeHold, accountHold)
		if err != nil {
			return domain.Effects{}, err
		}
		effects.Reservations = append(effects.Reservations, change)
	}
	for _, row := range related {
		if !row.TargetOperationID.Valid {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "related link targets a plan, which the domain cannot represent")
		}
		ref, err := domain.NewRelatedOperationRef(
			domain.RelatedRefKind(row.Kind),
			domain.JournalOperationId(row.TargetOperationID.String()),
		)
		if err != nil {
			return domain.Effects{}, err
		}
		effects.Related = append(effects.Related, ref)
	}
	return effects, nil
}

// Plan mappings. Plans have no currency columns, so the household currency is
// supplied by the repository.

func planToModel(plan *domain.PlannedOperation, now time.Time) (planModel, error) {
	id, err := uuidFromDomain(plan.ID().String())
	if err != nil {
		return planModel{}, err
	}
	// The household id is filled by the repository; it is not part of the
	// aggregate.
	envelopeID, err := uuidFromDomain(plan.Target().EnvelopeID.String())
	if err != nil {
		return planModel{}, err
	}
	accountID := pgtype.UUID{}
	if !plan.Target().AccountID.IsZero() {
		accountID, err = uuidFromDomain(plan.Target().AccountID.String())
		if err != nil {
			return planModel{}, err
		}
	}

	authorized := plan.AuthorizedAmount().MinorUnits()
	final := (*int64)(nil)
	if value, ok := plan.FinalAmount(); ok {
		minor := value.MinorUnits()
		final = &minor
	}

	return planModel{
		ID:                     id,
		Kind:                   plan.Kind().String(),
		State:                  plan.State().String(),
		TargetEnvelopeID:       envelopeID,
		TargetAccountID:        accountID,
		IntendedAmountMinor:    plan.IntendedAmount().MinorUnits(),
		AuthorizedAmountMinor:  &authorized,
		Mode:                   optionalString(plan.Mode().String()),
		Reason:                 optionalString(plan.Reason().String()),
		HeldAmountMinor:        plan.HeldAmount().MinorUnits(),
		HeldOnAccountMinor:     plan.HeldOnAccount().MinorUnits(),
		ReleasedAmountMinor:    plan.ReleasedAmount().MinorUnits(),
		ReleasedOnAccountMinor: plan.ReleasedOnAccount().MinorUnits(),
		FinalAmountMinor:       final,
		CreatedAt:              now.UTC(),
		UpdatedAt:              now.UTC(),
	}, nil
}

func planFromModel(row planModel, currency domain.Currency) (*domain.PlannedOperation, error) {
	kind, err := domain.ParsePlanKind(row.Kind)
	if err != nil {
		return nil, err
	}
	state, err := domain.ParsePlanState(row.State)
	if err != nil {
		return nil, err
	}

	intended, err := positiveAmount(row.IntendedAmountMinor, currency)
	if err != nil {
		return nil, err
	}
	authorized := intended
	if row.AuthorizedAmountMinor != nil {
		authorized, err = positiveAmount(*row.AuthorizedAmountMinor, currency)
		if err != nil {
			return nil, err
		}
	}
	held, err := positiveOrZero(row.HeldAmountMinor, currency)
	if err != nil {
		return nil, err
	}
	heldOnAccount, err := positiveOrZero(row.HeldOnAccountMinor, currency)
	if err != nil {
		return nil, err
	}
	released, err := positiveOrZero(row.ReleasedAmountMinor, currency)
	if err != nil {
		return nil, err
	}
	releasedOnAccount, err := positiveOrZero(row.ReleasedOnAccountMinor, currency)
	if err != nil {
		return nil, err
	}
	var final *domain.PositiveAmount
	if row.FinalAmountMinor != nil {
		value, err := positiveAmount(*row.FinalAmountMinor, currency)
		if err != nil {
			return nil, err
		}
		final = &value
	}

	snapshot := domain.PlannedOperationSnapshot{
		ID:    domain.PlanId(row.ID.String()),
		Kind:  kind,
		State: state,
		Target: domain.ReservationTarget{
			PlanID:     domain.PlanId(row.ID.String()),
			EnvelopeID: domain.EnvelopeId(row.TargetEnvelopeID.String()),
			AccountID:  accountIDValue(row.TargetAccountID),
		},
		IntendedAmount:    intended,
		AuthorizedAmount:  authorized,
		Mode:              domain.AuthorizationMode(stringValue(row.Mode)),
		Reason:            domain.OverrideReason(stringValue(row.Reason)),
		HeldAmount:        held,
		HeldOnAccount:     heldOnAccount,
		ReleasedAmount:    released,
		ReleasedOnAccount: releasedOnAccount,
		FinalAmount:       final,
	}
	return domain.RehydratePlannedOperation(snapshot)
}

// accountIDValue converts a nullable UUID to an account id, using the domain's
// zero value for NULL.
func accountIDValue(id pgtype.UUID) domain.AccountId {
	if !id.Valid {
		return ""
	}
	return domain.AccountId(id.String())
}

// Suggestion mappings. Suggestions have no currency column, so the household
// currency is supplied by the repository.

func suggestionToModel(household domain.HouseholdId, suggestion *domain.Suggestion, now time.Time) (suggestionModel, error) {
	id, err := uuidFromDomain(suggestion.ID().String())
	if err != nil {
		return suggestionModel{}, err
	}
	householdID, err := uuidFromDomain(household.String())
	if err != nil {
		return suggestionModel{}, err
	}
	return suggestionModel{
		ID:                id,
		HouseholdID:       householdID,
		Source:            suggestion.Source().String(),
		AmountMinor:       suggestion.Amount().MinorUnits(),
		Merchant:          suggestion.Merchant(),
		State:             suggestion.State().String(),
		CancelReason:      optionalString(suggestion.CancelReason()),
		ConfirmationCount: suggestion.ConfirmationCount(),
		CreatedAt:         now.UTC(),
		UpdatedAt:         now.UTC(),
	}, nil
}

// suggestionFromModel rebuilds a suggestion through its public lifecycle
// methods. That is lossless for every state the domain can produce:
// SUGGESTED -> constructor, EDITED -> Edit, CONFIRMED -> Confirm (always count
// 1 because a second Confirm is an idempotent no-op), CANCELLED -> Cancel.
// A persisted confirmation_count other than 0/1 for the corresponding state is
// treated as corrupt INVALID_VALUE because the domain cannot represent it.
func suggestionFromModel(row suggestionModel, currency domain.Currency) (*domain.Suggestion, error) {
	source, err := domain.ParseTransactionSource(row.Source)
	if err != nil {
		return nil, err
	}
	amount, err := positiveAmount(row.AmountMinor, currency)
	if err != nil {
		return nil, err
	}
	suggestion, err := domain.NewSuggestion(domain.SuggestionId(row.ID.String()), source, amount, row.Merchant)
	if err != nil {
		return nil, err
	}

	state, err := domain.ParseSuggestionState(row.State)
	if err != nil {
		return nil, err
	}
	switch state {
	case domain.SuggestionStateSuggested:
		if row.ConfirmationCount != 0 {
			return nil, domain.NewError(domain.CodeInvalidValue, "a suggested suggestion cannot have confirmations")
		}
	case domain.SuggestionStateEdited:
		if row.ConfirmationCount != 0 {
			return nil, domain.NewError(domain.CodeInvalidValue, "an edited suggestion cannot have confirmations")
		}
		if err := suggestion.Edit(amount, row.Merchant); err != nil {
			return nil, err
		}
	case domain.SuggestionStateConfirmed:
		if row.ConfirmationCount != 1 {
			return nil, domain.NewErrorf(domain.CodeInvalidValue, "confirmed suggestion has unsupported confirmation count %d", row.ConfirmationCount)
		}
		if err := suggestion.Confirm(); err != nil {
			return nil, err
		}
	case domain.SuggestionStateCancelled:
		if row.ConfirmationCount != 0 {
			return nil, domain.NewError(domain.CodeInvalidValue, "a cancelled suggestion cannot have confirmations")
		}
		if err := suggestion.Cancel(stringValue(row.CancelReason)); err != nil {
			return nil, err
		}
	default:
		return nil, domain.NewErrorf(domain.CodeInvalidValue, "unknown suggestion state %q", row.State)
	}
	return suggestion, nil
}

// idempotencyRecordFromModel builds a validated domain record.
func idempotencyRecordFromModel(row idempotencyRecordModel) (domain.IdempotencyRecord, error) {
	return domain.NewIdempotencyRecord(
		domain.IdempotencyKey(row.IdempotencyKey),
		domain.RequestFingerprint(row.RequestFingerprint),
		domain.JournalOperationId(row.OperationID.String()),
		domain.NewRecordedAt(row.CreatedAt),
	)
}
