// This file implements the in-memory atomic command substrate for financial
// mutations (DOMAIN_MODEL §5, POSTING_MATRIX §5, ACCOUNTING_SPEC §11).
//
// Every command validates against a throwaway working copy of the ledger,
// builds the immutable journal operation, and only then commits the journal
// append, the ledger projection swap, and the idempotency record together. The
// in-memory service is the reference behaviour for the persistence adapter:
// Step 6 will port the three commit steps to one database transaction (the
// journal row, the projection update, and the idempotency row).
//
// Validation strategy: validate-on-copy-then-commit. Candidate effects are
// applied only to a cloned LedgerState and checked with CheckInvariants. The
// real ledger is replaced with that validated clone at commit time, so a failed
// command never mutates the live journal, ledger, or idempotency registry.
package domain

import (
	"fmt"
	"sync"
)

// CommandService executes domain commands atomically against one household's
// journal, ledger projection, and idempotency registry.
//
// The zero value is invalid. Construct with NewCommandService. Command
// execution is serialized by an internal mutex, which models the single
// household consistency boundary described by DOMAIN_MODEL §2; the persistence
// adapter must instead rely on a database transaction and concurrency control.
type CommandService struct {
	mu sync.Mutex

	household HouseholdId
	currency  Currency
	accounts  []AccountMeta
	envelopes []EnvelopeId

	journal *Journal
	ledger  *LedgerState
	idem    *IdempotencyStore

	// nextOp is the monotonically increasing operation-id sequence. Operation
	// ids are allocated only at commit time, after idempotency replay has been
	// ruled out, so replays never consume an id.
	nextOp uint64
}

// CommandEnvelope is the audit and idempotency metadata every command carries.
//
// Source is derived by each handler from its business operation (opening
// balance, reconciliation, or manual entry), so it is not part of the caller
// envelope. CommittedAt doubles as the created timestamp: the in-memory command
// records receipt and commit at the same instant. Reason is optional here and
// required by the specific commands documented in commands.go.
type CommandEnvelope struct {
	Actor         UserId
	EffectiveDate EffectiveDate
	CommittedAt   RecordedAt
	Key           IdempotencyKey
	Fingerprint   RequestFingerprint
	Reason        string
}

// JournalView is the read-only accessor surface for a service journal. Callers
// can inspect committed operations and the version but cannot append.
type JournalView interface {
	Household() HouseholdId
	Version() uint64
	Operation(id JournalOperationId) (JournalOperation, error)
	Operations() []JournalOperation
	OperationsByType(t OperationType) []JournalOperation
}

// IdempotencyView is the read-only accessor surface for a service idempotency
// registry. Callers can look up a record but cannot begin or mutate one.
type IdempotencyView interface {
	Lookup(key IdempotencyKey) (IdempotencyRecord, error)
}

// NewCommandService builds an empty command service for one household with a
// zeroed ledger and no idempotency records.
//
// It rejects an empty household id and invalid accounts/envelopes/currency
// through NewLedgerState, all with INVALID_VALUE (or the specific currency
// error). The account and envelope slices are copied so later caller mutation
// cannot change the service's household shape.
func NewCommandService(household HouseholdId, currency Currency, accounts []AccountMeta, envelopes []EnvelopeId) (*CommandService, error) {
	if household.IsZero() {
		return nil, NewError(CodeInvalidValue, "command service household id must not be empty")
	}
	ledger, err := NewLedgerState(currency, accounts, envelopes)
	if err != nil {
		return nil, err
	}
	return &CommandService{
		household: household,
		currency:  currency,
		accounts:  append([]AccountMeta(nil), accounts...),
		envelopes: append([]EnvelopeId(nil), envelopes...),
		journal:   NewJournal(household),
		ledger:    ledger,
		idem:      NewIdempotencyStore(),
	}, nil
}

// Household returns the household this service commands.
func (s *CommandService) Household() HouseholdId {
	return s.household
}

// Currency returns the household currency every command must use.
func (s *CommandService) Currency() Currency {
	return s.currency
}

// Journal returns a read-only view of the committed operation journal.
func (s *CommandService) Journal() JournalView {
	return s.journal
}

// Version returns the current household ledger version (the number of committed
// journal operations).
func (s *CommandService) Version() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.journal.Version()
}

// Ledger returns a deep-copy snapshot of the current ledger projection. The
// caller may query it freely; mutating the snapshot never changes the service.
func (s *CommandService) Ledger() (*LedgerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ledger.Clone()
}

// Idempotency returns a read-only view of the idempotency registry.
func (s *CommandService) Idempotency() IdempotencyView {
	return s.idem
}

// commandPlan describes one command's immutable operation metadata plus a builder
// that validates state-dependent preconditions and computes its effects.
//
// The builder runs only for a genuinely new request, after idempotency replay
// has been ruled out and against the working ledger copy. That keeps funding,
// remainder, and funding-plan checks from rejecting a legitimate replay whose
// historical preconditions no longer hold.
type commandPlan struct {
	opType OperationType
	source TransactionSource
	amount *PositiveAmount
	build  func(state *LedgerState) (Effects, error)
}

// execute is the internal atomic apply shared by every financial command.
//
// Sequence:
//  1. Validate envelope metadata (pure, no mutation).
//  2. Look the idempotency key up. A matching record replays the original
//     journal operation; a mismatched fingerprint is rejected. Neither touches
//     state.
//  3. Clone the ledger, run the command builder (funding/remainder checks),
//     apply the resulting effects to the clone, and run CheckInvariants. Any
//     failure here leaves the live journal, ledger, and registry untouched.
//  4. Commit: begin the idempotency record, append the immutable journal
//     operation, and swap the validated ledger clone into place.
//
// After step 3 every remaining commit step is infallible for this in-memory
// model: the envelope was validated, the key was confirmed absent while the
// service mutex was held, the operation id is freshly allocated, and the ledger
// clone has already proven it accepts the effects. A persistence adapter must
// wrap the same three writes in one transaction.
func (s *CommandService) execute(env CommandEnvelope, plan commandPlan) (JournalOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateCommandEnvelope(env); err != nil {
		return JournalOperation{}, err
	}

	// Idempotent replay is decided before any state-dependent validation so a
	// retry returns the original result even if balances have since moved.
	if record, err := s.idem.Lookup(env.Key); err == nil {
		if record.Fingerprint() != env.Fingerprint {
			return JournalOperation{}, NewErrorf(
				CodeIdempotencyKeyPayloadMismatch,
				"idempotency key %q was reused with a different request fingerprint",
				env.Key,
			)
		}
		return s.replay(record)
	} else if !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
		return JournalOperation{}, err
	}

	working, err := s.ledger.Clone()
	if err != nil {
		return JournalOperation{}, err
	}
	effects, err := plan.build(working)
	if err != nil {
		return JournalOperation{}, err
	}
	if err := working.ApplyPostings(effects.Postings); err != nil {
		return JournalOperation{}, err
	}
	if err := working.ApplyReservationChanges(effects.Reservations...); err != nil {
		return JournalOperation{}, err
	}
	if err := working.CheckInvariants(); err != nil {
		return JournalOperation{}, err
	}

	opID, err := s.nextOperationID()
	if err != nil {
		return JournalOperation{}, err
	}
	op, err := NewJournalOperation(JournalOperationParams{
		ID:                 opID,
		Household:          s.household,
		Actor:              env.Actor,
		Type:               plan.opType,
		Source:             plan.source,
		EffectiveDate:      env.EffectiveDate,
		Currency:           s.currency,
		IdempotencyKey:     env.Key,
		RequestFingerprint: env.Fingerprint,
		Amount:             plan.amount,
		Reason:             env.Reason,
		CreatedAt:          env.CommittedAt,
		CommittedAt:        env.CommittedAt,
		Effects:            effects,
	})
	if err != nil {
		return JournalOperation{}, err
	}

	record, isNew, err := s.idem.Begin(env.Key, env.Fingerprint, op.ID(), env.CommittedAt)
	if err != nil {
		return JournalOperation{}, err
	}
	if !isNew {
		// Unreachable while the service mutex is held after the lookup above,
		// but kept correct if the execution model later allows interleaving.
		return s.replay(record)
	}
	if err := s.journal.Append(op); err != nil {
		return JournalOperation{}, err
	}
	s.ledger = working
	return op, nil
}

// replay returns the original committed operation for an existing idempotency
// record without posting again.
func (s *CommandService) replay(record IdempotencyRecord) (JournalOperation, error) {
	return s.journal.Operation(record.OperationID())
}

// validateCommandEnvelope rejects missing audit/idempotency metadata before any
// lookup or mutation. Reason length and normalization are enforced later by
// NewJournalOperation.
func validateCommandEnvelope(env CommandEnvelope) error {
	if env.Actor.IsZero() {
		return NewError(CodeInvalidValue, "command actor id must not be empty")
	}
	if env.EffectiveDate.IsZero() {
		return NewError(CodeInvalidValue, "command effective date must be set")
	}
	if env.CommittedAt.IsZero() {
		return NewError(CodeInvalidValue, "command committed timestamp must be set")
	}
	if env.Key.IsZero() {
		return NewError(CodeInvalidValue, "command idempotency key must not be empty")
	}
	if env.Fingerprint.IsZero() {
		return NewError(CodeInvalidValue, "command request fingerprint must not be empty")
	}
	return nil
}

// nextOperationID allocates a unique journal operation id. The sequence is
// service-local; an id is only consumed when the command is actually going to
// commit.
func (s *CommandService) nextOperationID() (JournalOperationId, error) {
	for {
		s.nextOp++
		id, err := NewJournalOperationId(fmt.Sprintf("op-%d", s.nextOp))
		if err != nil {
			return "", err
		}
		if _, err := s.journal.Operation(id); err != nil {
			if HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
				return id, nil
			}
			return "", err
		}
	}
}
