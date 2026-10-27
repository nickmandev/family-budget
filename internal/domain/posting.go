package domain

// AccountPosting is a signed delta applied to an account balance. Cash assets
// use positive for inflow and negative for outflow; card liabilities use
// positive for additional debt and negative for debt reduction (DOMAIN_MODEL 3.8).
type AccountPosting struct {
	AccountID AccountId
	Delta     SignedMoneyDelta
}

// NewAccountPosting validates the account id and the delta, then builds an
// AccountPosting. A zero id or an unvalidated zero-value delta fails with
// INVALID_VALUE.
func NewAccountPosting(id AccountId, delta SignedMoneyDelta) (AccountPosting, error) {
	if id.IsZero() {
		return AccountPosting{}, NewError(CodeInvalidValue, "account posting account id must not be empty")
	}
	if err := validateSignedDelta("account posting", delta); err != nil {
		return AccountPosting{}, err
	}
	return AccountPosting{AccountID: id, Delta: delta}, nil
}

// EnvelopePosting is a signed delta applied to an envelope balance.
type EnvelopePosting struct {
	EnvelopeID EnvelopeId
	Delta      SignedMoneyDelta
}

// NewEnvelopePosting validates the envelope id and the delta, then builds an
// EnvelopePosting. A zero id or an unvalidated zero-value delta fails with
// INVALID_VALUE.
func NewEnvelopePosting(id EnvelopeId, delta SignedMoneyDelta) (EnvelopePosting, error) {
	if id.IsZero() {
		return EnvelopePosting{}, NewError(CodeInvalidValue, "envelope posting envelope id must not be empty")
	}
	if err := validateSignedDelta("envelope posting", delta); err != nil {
		return EnvelopePosting{}, err
	}
	return EnvelopePosting{EnvelopeID: id, Delta: delta}, nil
}

// PostingSet is the complete set of financial postings produced by one
// committed business operation. Ready-to-assign is never posted: it is derived.
//
// A set may contain several postings for the same account or envelope;
// AccountDelta and EnvelopeDelta sum them with checked Money arithmetic.
type PostingSet struct {
	Accounts  []AccountPosting
	Envelopes []EnvelopePosting
}

// AddAccount appends a validated account posting to the set.
func (p *PostingSet) AddAccount(id AccountId, delta SignedMoneyDelta) error {
	posting, err := NewAccountPosting(id, delta)
	if err != nil {
		return err
	}
	p.Accounts = append(p.Accounts, posting)
	return nil
}

// AddEnvelope appends a validated envelope posting to the set.
func (p *PostingSet) AddEnvelope(id EnvelopeId, delta SignedMoneyDelta) error {
	posting, err := NewEnvelopePosting(id, delta)
	if err != nil {
		return err
	}
	p.Envelopes = append(p.Envelopes, posting)
	return nil
}

// AccountDelta returns the checked sum of all account postings for id, or zero
// Money when no posting targets it. When the set contains no postings at all
// the returned Money is the invalid zero value because there is no currency to
// infer; callers that always need a currency should query a LedgerState.
func (p PostingSet) AccountDelta(id AccountId) (Money, error) {
	total := Money{}
	found := false
	for _, posting := range p.Accounts {
		if posting.AccountID != id {
			continue
		}
		delta, err := posting.Delta.Money()
		if err != nil {
			return Money{}, err
		}
		if !found {
			total = delta
			found = true
			continue
		}
		next, err := total.Add(delta)
		if err != nil {
			return Money{}, err
		}
		total = next
	}
	if found {
		return total, nil
	}
	return p.zeroMoney(), nil
}

// EnvelopeDelta returns the checked sum of all envelope postings for id, or
// zero Money when no posting targets it. See AccountDelta for the empty-set
// currency caveat.
func (p PostingSet) EnvelopeDelta(id EnvelopeId) (Money, error) {
	total := Money{}
	found := false
	for _, posting := range p.Envelopes {
		if posting.EnvelopeID != id {
			continue
		}
		delta, err := posting.Delta.Money()
		if err != nil {
			return Money{}, err
		}
		if !found {
			total = delta
			found = true
			continue
		}
		next, err := total.Add(delta)
		if err != nil {
			return Money{}, err
		}
		total = next
	}
	if found {
		return total, nil
	}
	return p.zeroMoney(), nil
}

// Negate returns compensating postings whose every delta is the exact negation
// of the receiver's corresponding delta. It is the mechanical basis of
// reversal operations and does not perform authorization checks.
func (p PostingSet) Negate() (PostingSet, error) {
	negated := PostingSet{
		Accounts:  make([]AccountPosting, 0, len(p.Accounts)),
		Envelopes: make([]EnvelopePosting, 0, len(p.Envelopes)),
	}
	for _, posting := range p.Accounts {
		delta, err := negateSignedDelta(posting.Delta)
		if err != nil {
			return PostingSet{}, err
		}
		compensating, err := NewAccountPosting(posting.AccountID, delta)
		if err != nil {
			return PostingSet{}, err
		}
		negated.Accounts = append(negated.Accounts, compensating)
	}
	for _, posting := range p.Envelopes {
		delta, err := negateSignedDelta(posting.Delta)
		if err != nil {
			return PostingSet{}, err
		}
		compensating, err := NewEnvelopePosting(posting.EnvelopeID, delta)
		if err != nil {
			return PostingSet{}, err
		}
		negated.Envelopes = append(negated.Envelopes, compensating)
	}
	return negated, nil
}

// IsEmpty reports whether the set contains no postings.
func (p PostingSet) IsEmpty() bool {
	return len(p.Accounts) == 0 && len(p.Envelopes) == 0
}

// MergePostings concatenates the postings of every set. Duplicate ids are kept
// as separate postings; use AccountDelta or EnvelopeDelta to sum them with
// checked arithmetic. No currency is enforced here because the household has a
// single configured currency and any mismatch surfaces when deltas are summed
// or applied.
func MergePostings(sets ...PostingSet) (PostingSet, error) {
	merged := PostingSet{}
	for _, set := range sets {
		merged.Accounts = append(merged.Accounts, set.Accounts...)
		merged.Envelopes = append(merged.Envelopes, set.Envelopes...)
	}
	return merged, nil
}

// zeroMoney returns a zero Money in the currency of the first posting in the
// set, or the invalid zero Money when the set has no postings.
func (p PostingSet) zeroMoney() Money {
	for _, posting := range p.Accounts {
		if currency := posting.Delta.Currency(); !currency.IsZero() {
			return mustZeroMoney(currency)
		}
	}
	for _, posting := range p.Envelopes {
		if currency := posting.Delta.Currency(); !currency.IsZero() {
			return mustZeroMoney(currency)
		}
	}
	return Money{}
}

// mustZeroMoney builds a zero Money for a currency already validated by a
// SignedMoneyDelta.
func mustZeroMoney(currency Currency) Money {
	zero, err := ZeroMoney(currency)
	if err != nil {
		return Money{}
	}
	return zero
}

// validateSignedDelta rejects an unvalidated zero-value SignedMoneyDelta. The
// delta type already rejects zero amounts at construction, so this only guards
// against struct-literal bypasses.
func validateSignedDelta(kind string, delta SignedMoneyDelta) error {
	if _, err := delta.Money(); err != nil {
		return WrapError(CodeInvalidValue, kind+" delta is invalid", err)
	}
	return nil
}

// negateSignedDelta returns the exact negation of delta using checked
// arithmetic.
func negateSignedDelta(delta SignedMoneyDelta) (SignedMoneyDelta, error) {
	money, err := delta.Money()
	if err != nil {
		return SignedMoneyDelta{}, err
	}
	negated, err := money.Neg()
	if err != nil {
		return SignedMoneyDelta{}, err
	}
	return NewSignedMoneyDelta(negated.MinorUnits(), negated.Currency())
}

// Effects is the complete mechanical result of one business operation:
// financial postings, reservation hold transitions, and links to related
// operations. It contains NO funding or authorization checks — those live in
// the authorization layer built later.
//
// It lives in posting.go because it is the aggregate of the posting and
// reservation primitives defined in this file and reservation.go.
type Effects struct {
	Postings     PostingSet
	Reservations []ReservationChange
	Related      []RelatedOperationRef
}

// IsEmpty reports whether the effects contain no postings, reservations, or
// relation links.
func (e Effects) IsEmpty() bool {
	return e.Postings.IsEmpty() && len(e.Reservations) == 0 && len(e.Related) == 0
}

// MergeEffects combines effects by concatenating reservations and relation
// links and merging postings. Identical related references (same kind and
// operation id) are deduplicated; references that share only one component are
// kept separately.
func MergeEffects(parts ...Effects) (Effects, error) {
	merged := Effects{}
	for _, part := range parts {
		postings, err := MergePostings(merged.Postings, part.Postings)
		if err != nil {
			return Effects{}, err
		}
		merged.Postings = postings
		merged.Reservations = append(merged.Reservations, part.Reservations...)
	}
	seen := make(map[relatedRefKey]struct{})
	for _, part := range parts {
		for _, ref := range part.Related {
			key := relatedRefKey{kind: ref.Kind, operationID: ref.OperationID}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged.Related = append(merged.Related, ref)
		}
	}
	return merged, nil
}

// relatedRefKey identifies an identical related-operation reference for
// deduplication.
type relatedRefKey struct {
	kind        RelatedRefKind
	operationID JournalOperationId
}
