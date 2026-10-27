package domain

import "strings"

// maxIdentifierLength is the maximum length, in bytes, of a strong identifier.
const maxIdentifierLength = 64

// HouseholdId identifies one independent household budget and security
// boundary.
type HouseholdId string

// NewHouseholdId validates and builds a HouseholdId.
func NewHouseholdId(raw string) (HouseholdId, error) {
	return newIdentifier[HouseholdId]("household id", raw)
}

// String returns the identifier text.
func (id HouseholdId) String() string { return string(id) }

// IsZero reports whether the identifier is the invalid zero value.
func (id HouseholdId) IsZero() bool { return id == "" }

// UserId identifies an authenticated household member.
type UserId string

// NewUserId validates and builds a UserId.
func NewUserId(raw string) (UserId, error) {
	return newIdentifier[UserId]("user id", raw)
}

// String returns the identifier text.
func (id UserId) String() string { return string(id) }

// IsZero reports whether the identifier is the invalid zero value.
func (id UserId) IsZero() bool { return id == "" }

// AccountId identifies an account whose balance is tracked by the product.
type AccountId string

// NewAccountId validates and builds an AccountId.
func NewAccountId(raw string) (AccountId, error) {
	return newIdentifier[AccountId]("account id", raw)
}

// String returns the identifier text.
func (id AccountId) String() string { return string(id) }

// IsZero reports whether the identifier is the invalid zero value.
func (id AccountId) IsZero() bool { return id == "" }

// EnvelopeId identifies a budget envelope.
type EnvelopeId string

// NewEnvelopeId validates and builds an EnvelopeId.
func NewEnvelopeId(raw string) (EnvelopeId, error) {
	return newIdentifier[EnvelopeId]("envelope id", raw)
}

// String returns the identifier text.
func (id EnvelopeId) String() string { return string(id) }

// IsZero reports whether the identifier is the invalid zero value.
func (id EnvelopeId) IsZero() bool { return id == "" }

// PlanId identifies a planned purchase or planned repayment.
type PlanId string

// NewPlanId validates and builds a PlanId.
func NewPlanId(raw string) (PlanId, error) {
	return newIdentifier[PlanId]("plan id", raw)
}

// String returns the identifier text.
func (id PlanId) String() string { return string(id) }

// IsZero reports whether the identifier is the invalid zero value.
func (id PlanId) IsZero() bool { return id == "" }

// SuggestionId identifies an unconfirmed transaction suggestion.
type SuggestionId string

// NewSuggestionId validates and builds a SuggestionId.
func NewSuggestionId(raw string) (SuggestionId, error) {
	return newIdentifier[SuggestionId]("suggestion id", raw)
}

// String returns the identifier text.
func (id SuggestionId) String() string { return string(id) }

// IsZero reports whether the identifier is the invalid zero value.
func (id SuggestionId) IsZero() bool { return id == "" }

// JournalOperationId identifies an immutable committed journal operation.
type JournalOperationId string

// NewJournalOperationId validates and builds a JournalOperationId.
func NewJournalOperationId(raw string) (JournalOperationId, error) {
	return newIdentifier[JournalOperationId]("journal operation id", raw)
}

// String returns the identifier text.
func (id JournalOperationId) String() string { return string(id) }

// IsZero reports whether the identifier is the invalid zero value.
func (id JournalOperationId) IsZero() bool { return id == "" }

// ReservationId identifies money held for an explicitly planned purchase.
type ReservationId string

// NewReservationId validates and builds a ReservationId.
func NewReservationId(raw string) (ReservationId, error) {
	return newIdentifier[ReservationId]("reservation id", raw)
}

// String returns the identifier text.
func (id ReservationId) String() string { return string(id) }

// IsZero reports whether the identifier is the invalid zero value.
func (id ReservationId) IsZero() bool { return id == "" }

// identifierString constrains the strong identifier types to strings.
type identifierString interface {
	~string
}

// newIdentifier trims raw and enforces a non-empty, bounded identifier. The
// stored value is the trimmed text; the zero value stays invalid.
func newIdentifier[T identifierString](kind, raw string) (T, error) {
	var zero T
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return zero, NewErrorf(CodeInvalidValue, "%s must not be empty", kind)
	}
	if len(trimmed) > maxIdentifierLength {
		return zero, NewErrorf(CodeInvalidValue, "%s must be at most %d bytes", kind, maxIdentifierLength)
	}
	return T(trimmed), nil
}
