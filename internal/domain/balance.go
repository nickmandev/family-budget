package domain

// AccountKind identifies the static nature of an account. Liquid cash kinds can
// fund the budget; a card liability is a debt account whose positive balance
// means the household owes money.
type AccountKind string

const (
	// AccountKindChecking is a liquid checking account.
	AccountKindChecking AccountKind = "CHECKING"
	// AccountKindSavings is a liquid savings account.
	AccountKindSavings AccountKind = "SAVINGS"
	// AccountKindCashWallet is liquid physical or digital cash.
	AccountKindCashWallet AccountKind = "CASH_WALLET"
	// AccountKindCardLiability is a credit-card liability account.
	AccountKindCardLiability AccountKind = "CREDIT_CARD_LIABILITY"
)

// ParseAccountKind validates a raw account kind.
func ParseAccountKind(raw string) (AccountKind, error) {
	kind := AccountKind(raw)
	if !kind.IsValid() {
		return "", NewErrorf(CodeInvalidValue, "unknown account kind %q", raw)
	}
	return kind, nil
}

// IsValid reports whether the kind is one of the defined values.
func (k AccountKind) IsValid() bool {
	switch k {
	case AccountKindChecking,
		AccountKindSavings,
		AccountKindCashWallet,
		AccountKindCardLiability:
		return true
	default:
		return false
	}
}

// String returns the stored kind text.
func (k AccountKind) String() string {
	return string(k)
}

// IsLiquidCash reports whether the kind is a liquid cash account that may be
// budget-eligible: checking, savings, or cash wallet.
func (k AccountKind) IsLiquidCash() bool {
	switch k {
	case AccountKindChecking, AccountKindSavings, AccountKindCashWallet:
		return true
	default:
		return false
	}
}

// AccountMeta describes static account attributes. BudgetEligible must be true
// only for liquid cash kinds; card-liability accounts can never be eligible
// (credit limits are never budgetable cash).
type AccountMeta struct {
	ID             AccountId
	Kind           AccountKind
	BudgetEligible bool
}

// NewAccountMeta validates the id, kind, and eligibility rule, then builds an
// AccountMeta.
func NewAccountMeta(id AccountId, kind AccountKind, eligible bool) (AccountMeta, error) {
	if id.IsZero() {
		return AccountMeta{}, NewError(CodeInvalidValue, "account meta account id must not be empty")
	}
	if !kind.IsValid() {
		return AccountMeta{}, NewErrorf(CodeInvalidValue, "invalid account kind %q", kind)
	}
	if kind == AccountKindCardLiability && eligible {
		return AccountMeta{}, NewErrorf(CodeInvalidValue, "card liability account %q cannot be budget eligible", id)
	}
	return AccountMeta{ID: id, Kind: kind, BudgetEligible: eligible}, nil
}

// accountState is the mutable projection of one account. posted is the sum of
// committed postings; reserved is the active account reservation Qa.
type accountState struct {
	meta     AccountMeta
	posted   Money
	reserved Money
}

// envelopeState is the mutable projection of one envelope. balance is the sum
// of committed postings; reserved is the active envelope reservation Qe.
type envelopeState struct {
	balance  Money
	reserved Money
}

// LedgerState is a pure in-memory projection of committed postings and active
// reservation holds. It is the testing/derivation model for balance queries;
// persistence projections must be rebuildable to match it (Q9).
//
// The zero value is invalid. Construct with NewLedgerState.
type LedgerState struct {
	currency  Currency
	accounts  map[AccountId]*accountState
	envelopes map[EnvelopeId]*envelopeState
	plans     map[PlanId]struct{}
}

// NewLedgerState builds an empty ledger projection with all balances at zero.
// Opening balances arrive later as postings.
//
// It rejects an empty or invalid currency, duplicate or zero account/envelope
// ids, invalid account kinds, and card-liability accounts marked
// budget-eligible, all with INVALID_VALUE.
func NewLedgerState(currency Currency, accounts []AccountMeta, envelopes []EnvelopeId) (*LedgerState, error) {
	if err := currency.validate(); err != nil {
		return nil, err
	}
	zero, err := ZeroMoney(currency)
	if err != nil {
		return nil, err
	}

	state := &LedgerState{
		currency:  currency,
		accounts:  make(map[AccountId]*accountState, len(accounts)),
		envelopes: make(map[EnvelopeId]*envelopeState, len(envelopes)),
		plans:     make(map[PlanId]struct{}),
	}

	for _, meta := range accounts {
		if meta.ID.IsZero() {
			return nil, NewError(CodeInvalidValue, "ledger account id must not be empty")
		}
		if _, exists := state.accounts[meta.ID]; exists {
			return nil, NewErrorf(CodeInvalidValue, "duplicate ledger account id %q", meta.ID)
		}
		validated, err := NewAccountMeta(meta.ID, meta.Kind, meta.BudgetEligible)
		if err != nil {
			return nil, err
		}
		state.accounts[meta.ID] = &accountState{meta: validated, posted: zero, reserved: zero}
	}

	for _, id := range envelopes {
		if id.IsZero() {
			return nil, NewError(CodeInvalidValue, "ledger envelope id must not be empty")
		}
		if _, exists := state.envelopes[id]; exists {
			return nil, NewErrorf(CodeInvalidValue, "duplicate ledger envelope id %q", id)
		}
		state.envelopes[id] = &envelopeState{balance: zero, reserved: zero}
	}

	return state, nil
}

// Currency returns the ledger's configured household currency.
func (s *LedgerState) Currency() Currency {
	return s.currency
}

// ApplyPostings applies every financial posting to the posted balances. It
// never touches reservations. The whole set is validated and summed before any
// balance changes, so a rejected set leaves the ledger unchanged.
//
// An unknown account or envelope id fails with
// ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD; a delta in another currency fails with
// CURRENCY_MISMATCH; checked-arithmetic failures surface as MONEY_OVERFLOW or
// AMOUNT_OUT_OF_RANGE.
func (s *LedgerState) ApplyPostings(p PostingSet) error {
	zero, err := ZeroMoney(s.currency)
	if err != nil {
		return err
	}

	accountDeltas := make(map[AccountId]Money, len(p.Accounts))
	for _, posting := range p.Accounts {
		if posting.AccountID.IsZero() {
			return NewError(CodeInvalidValue, "posting account id must not be empty")
		}
		if _, known := s.accounts[posting.AccountID]; !known {
			return NewErrorf(CodeEntityNotFoundOrNotInHousehold, "account %q is not part of the ledger", posting.AccountID)
		}
		delta, err := posting.Delta.Money()
		if err != nil {
			return err
		}
		total, exists := accountDeltas[posting.AccountID]
		if !exists {
			total = zero
		}
		sum, err := total.Add(delta)
		if err != nil {
			return err
		}
		accountDeltas[posting.AccountID] = sum
	}

	envelopeDeltas := make(map[EnvelopeId]Money, len(p.Envelopes))
	for _, posting := range p.Envelopes {
		if posting.EnvelopeID.IsZero() {
			return NewError(CodeInvalidValue, "posting envelope id must not be empty")
		}
		if _, known := s.envelopes[posting.EnvelopeID]; !known {
			return NewErrorf(CodeEntityNotFoundOrNotInHousehold, "envelope %q is not part of the ledger", posting.EnvelopeID)
		}
		delta, err := posting.Delta.Money()
		if err != nil {
			return err
		}
		total, exists := envelopeDeltas[posting.EnvelopeID]
		if !exists {
			total = zero
		}
		sum, err := total.Add(delta)
		if err != nil {
			return err
		}
		envelopeDeltas[posting.EnvelopeID] = sum
	}

	newAccountPosted := make(map[AccountId]Money, len(accountDeltas))
	for id, delta := range accountDeltas {
		posted, err := s.accounts[id].posted.Add(delta)
		if err != nil {
			return err
		}
		newAccountPosted[id] = posted
	}
	newEnvelopeBalance := make(map[EnvelopeId]Money, len(envelopeDeltas))
	for id, delta := range envelopeDeltas {
		balance, err := s.envelopes[id].balance.Add(delta)
		if err != nil {
			return err
		}
		newEnvelopeBalance[id] = balance
	}

	for id, posted := range newAccountPosted {
		s.accounts[id].posted = posted
	}
	for id, balance := range newEnvelopeBalance {
		s.envelopes[id].balance = balance
	}
	return nil
}

// ApplyReservationChanges applies hold transitions to the active reservations
// Qe and Qa. It never touches posted balances. Changes are validated and
// accumulated first, then committed together, so a rejected batch leaves the
// ledger unchanged.
//
// An unknown envelope or account id fails with
// ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD. A batch that would drive an active
// reservation below zero fails with INVALID_AMOUNT. Multiple changes for the
// same plan and envelope (or plan and account) accumulate.
//
// Plans are registered on first use: NewLedgerState has no plan list, so an
// entirely new plan cannot be distinguished from an unknown one. Zero plan ids
// are rejected with INVALID_VALUE.
func (s *LedgerState) ApplyReservationChanges(changes ...ReservationChange) error {
	zero, err := ZeroMoney(s.currency)
	if err != nil {
		return err
	}

	envelopeDeltas := make(map[EnvelopeId]Money, len(changes))
	accountDeltas := make(map[AccountId]Money, len(changes))
	for _, change := range changes {
		if change.PlanID.IsZero() {
			return NewError(CodeInvalidValue, "reservation change plan id must not be empty")
		}
		if change.Envelope.EnvelopeID.IsZero() {
			return NewError(CodeInvalidValue, "reservation change envelope id must not be empty")
		}
		if _, known := s.envelopes[change.Envelope.EnvelopeID]; !known {
			return NewErrorf(CodeEntityNotFoundOrNotInHousehold, "envelope %q is not part of the ledger", change.Envelope.EnvelopeID)
		}
		delta, err := change.Envelope.Delta.Money()
		if err != nil {
			return err
		}
		total, exists := envelopeDeltas[change.Envelope.EnvelopeID]
		if !exists {
			total = zero
		}
		sum, err := total.Add(delta)
		if err != nil {
			return err
		}
		envelopeDeltas[change.Envelope.EnvelopeID] = sum

		if change.Account == nil {
			continue
		}
		if change.Account.AccountID.IsZero() {
			return NewError(CodeInvalidValue, "reservation change account id must not be empty")
		}
		if _, known := s.accounts[change.Account.AccountID]; !known {
			return NewErrorf(CodeEntityNotFoundOrNotInHousehold, "account %q is not part of the ledger", change.Account.AccountID)
		}
		delta, err = change.Account.Delta.Money()
		if err != nil {
			return err
		}
		total, exists = accountDeltas[change.Account.AccountID]
		if !exists {
			total = zero
		}
		sum, err = total.Add(delta)
		if err != nil {
			return err
		}
		accountDeltas[change.Account.AccountID] = sum
	}

	newEnvelopeReserved := make(map[EnvelopeId]Money, len(envelopeDeltas))
	for id, delta := range envelopeDeltas {
		reserved, err := s.envelopes[id].reserved.Add(delta)
		if err != nil {
			return err
		}
		if reserved.IsNegative() {
			return NewErrorf(CodeInvalidAmount, "envelope %q reservation would become negative", id)
		}
		newEnvelopeReserved[id] = reserved
	}
	newAccountReserved := make(map[AccountId]Money, len(accountDeltas))
	for id, delta := range accountDeltas {
		reserved, err := s.accounts[id].reserved.Add(delta)
		if err != nil {
			return err
		}
		if reserved.IsNegative() {
			return NewErrorf(CodeInvalidAmount, "account %q reservation would become negative", id)
		}
		newAccountReserved[id] = reserved
	}

	for id, reserved := range newEnvelopeReserved {
		s.envelopes[id].reserved = reserved
	}
	for id, reserved := range newAccountReserved {
		s.accounts[id].reserved = reserved
	}
	for _, change := range changes {
		s.plans[change.PlanID] = struct{}{}
	}
	return nil
}

// PostedAccountBalance returns the sum of committed account postings, or zero
// Money when the known account has no postings. An unknown id fails with
// ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD.
func (s *LedgerState) PostedAccountBalance(id AccountId) (Money, error) {
	account, err := s.lookupAccount(id)
	if err != nil {
		return Money{}, err
	}
	return account.posted, nil
}

// BudgetableCash returns A: the sum of posted balances of budget-eligible
// liquid accounts. A real negative eligible balance is included as negative.
func (s *LedgerState) BudgetableCash() (Money, error) {
	total, err := ZeroMoney(s.currency)
	if err != nil {
		return Money{}, err
	}
	for _, account := range s.accounts {
		if !account.meta.BudgetEligible || !account.meta.Kind.IsLiquidCash() {
			continue
		}
		next, err := total.Add(account.posted)
		if err != nil {
			return Money{}, err
		}
		total = next
	}
	return total, nil
}

// EnvelopeBalance returns E[x]: the sum of committed envelope postings, or zero
// Money when the known envelope has no postings.
func (s *LedgerState) EnvelopeBalance(id EnvelopeId) (Money, error) {
	envelope, err := s.lookupEnvelope(id)
	if err != nil {
		return Money{}, err
	}
	return envelope.balance, nil
}

// SumEnvelopeBalances returns the sum of every envelope balance.
func (s *LedgerState) SumEnvelopeBalances() (Money, error) {
	total, err := ZeroMoney(s.currency)
	if err != nil {
		return Money{}, err
	}
	for _, envelope := range s.envelopes {
		next, err := total.Add(envelope.balance)
		if err != nil {
			return Money{}, err
		}
		total = next
	}
	return total, nil
}

// ActiveEnvelopeReservations returns Qe[x]: the active reservations against
// one envelope.
func (s *LedgerState) ActiveEnvelopeReservations(id EnvelopeId) (Money, error) {
	envelope, err := s.lookupEnvelope(id)
	if err != nil {
		return Money{}, err
	}
	return envelope.reserved, nil
}

// ActiveAccountReservations returns Qa[a]: the active reservations against one
// account.
func (s *LedgerState) ActiveAccountReservations(id AccountId) (Money, error) {
	account, err := s.lookupAccount(id)
	if err != nil {
		return Money{}, err
	}
	return account.reserved, nil
}

// SpendableEnvelopeBalance returns S[x] = E[x] - Qe[x]. It may be negative
// under an emergency override (IMPLEMENTATION_DECISIONS Q4).
func (s *LedgerState) SpendableEnvelopeBalance(id EnvelopeId) (Money, error) {
	envelope, err := s.lookupEnvelope(id)
	if err != nil {
		return Money{}, err
	}
	return envelope.balance.Sub(envelope.reserved)
}

// SpendableAccountBalance returns SA[a] = posted balance - Qa[a]. The MVP
// models no issuer holds (IMPLEMENTATION_DECISIONS Q6).
func (s *LedgerState) SpendableAccountBalance(id AccountId) (Money, error) {
	account, err := s.lookupAccount(id)
	if err != nil {
		return Money{}, err
	}
	return account.posted.Sub(account.reserved)
}

// ReadyToAssign returns R = A - sum(E[i]). It is always derived and never
// posted; a negative value is a legitimate recorded funding shortfall (Q4).
func (s *LedgerState) ReadyToAssign() (Money, error) {
	budgetable, err := s.BudgetableCash()
	if err != nil {
		return Money{}, err
	}
	envelopeSum, err := s.SumEnvelopeBalances()
	if err != nil {
		return Money{}, err
	}
	return budgetable.Sub(envelopeSum)
}

// CardLiabilityBalance returns L[c]: the posted balance of a card-liability
// account, positive when the household owes money.
func (s *LedgerState) CardLiabilityBalance(id AccountId) (Money, error) {
	account, err := s.lookupAccount(id)
	if err != nil {
		return Money{}, err
	}
	if account.meta.Kind != AccountKindCardLiability {
		return Money{}, NewErrorf(CodeInvalidValue, "account %q is not a card liability account", id)
	}
	return account.posted, nil
}

// lookupAccount returns the known account state or a not-found domain error.
func (s *LedgerState) lookupAccount(id AccountId) (*accountState, error) {
	if id.IsZero() {
		return nil, NewError(CodeInvalidValue, "account id must not be empty")
	}
	account, known := s.accounts[id]
	if !known {
		return nil, NewErrorf(CodeEntityNotFoundOrNotInHousehold, "account %q is not part of the ledger", id)
	}
	return account, nil
}

// lookupEnvelope returns the known envelope state or a not-found domain error.
func (s *LedgerState) lookupEnvelope(id EnvelopeId) (*envelopeState, error) {
	if id.IsZero() {
		return nil, NewError(CodeInvalidValue, "envelope id must not be empty")
	}
	envelope, known := s.envelopes[id]
	if !known {
		return nil, NewErrorf(CodeEntityNotFoundOrNotInHousehold, "envelope %q is not part of the ledger", id)
	}
	return envelope, nil
}
