// This file implements the append-only in-memory journal for one household plus
// its monotonically increasing ledger version (DOMAIN_MODEL §2). It is the
// reference behaviour for the later persistence adapter: the journal is the
// authoritative record of committed operations, and every derived balance can
// be rebuilt from it.
//
// Committed operations are never updated or deleted. There is deliberately no
// mutation API beyond Append, which rejects duplicate operation ids.
package domain

import "sync"

// Journal is the append-only record of committed operations for one household
// plus the monotonically increasing ledger version used for concurrency control
// (DOMAIN_MODEL §2). The version starts at zero and increments on every
// successful append.
//
// The zero value is invalid. Construct with NewJournal. The household id is
// assumed to have been validated by the caller; operations for any other
// household are rejected on append.
type Journal struct {
	mu        sync.RWMutex
	household HouseholdId
	ops       []JournalOperation
	version   uint64
	index     map[JournalOperationId]int
}

// NewJournal builds an empty journal for household.
func NewJournal(household HouseholdId) *Journal {
	return &Journal{
		household: household,
		index:     make(map[JournalOperationId]int),
	}
}

// Household returns the household this journal belongs to.
func (j *Journal) Household() HouseholdId {
	return j.household
}

// Version returns the current household ledger version. It starts at zero and
// increments on every successful append.
func (j *Journal) Version() uint64 {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.version
}

// Append commits op to the journal, bumping the ledger version. It rejects a
// zero operation id, an operation belonging to another household, and a
// duplicate operation id, all with INVALID_VALUE; a rejected append leaves the
// journal and version unchanged.
//
// Append does not fold effects into a cached ledger. Derived state is rebuilt
// from the journal on demand with RebuildLedger, which keeps the journal itself
// the single source of truth.
func (j *Journal) Append(op JournalOperation) error {
	if op.id.IsZero() {
		return NewError(CodeInvalidValue, "journal operation id must not be empty")
	}
	if op.household != j.household {
		return NewErrorf(CodeInvalidValue, "journal operation %q belongs to a different household", op.id)
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if _, exists := j.index[op.id]; exists {
		return NewErrorf(CodeInvalidValue, "journal operation %q is already committed", op.id)
	}
	j.ops = append(j.ops, op)
	j.index[op.id] = len(j.ops) - 1
	j.version++
	return nil
}

// Operation returns the committed operation with id, or
// ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD when the journal has never committed it.
func (j *Journal) Operation(id JournalOperationId) (JournalOperation, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	position, known := j.index[id]
	if !known {
		return JournalOperation{}, NewErrorf(CodeEntityNotFoundOrNotInHousehold, "journal operation %q is not in this household's journal", id)
	}
	return j.ops[position], nil
}

// Operations returns a copy of every committed operation in commit order.
func (j *Journal) Operations() []JournalOperation {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return append([]JournalOperation(nil), j.ops...)
}

// OperationsByType returns the committed operations of type t in commit order.
func (j *Journal) OperationsByType(t OperationType) []JournalOperation {
	j.mu.RLock()
	defer j.mu.RUnlock()
	filtered := make([]JournalOperation, 0)
	for _, op := range j.ops {
		if op.opType == t {
			filtered = append(filtered, op)
		}
	}
	return filtered
}

// RebuildLedger replays every committed operation in order into a fresh
// LedgerState and returns it. This is the "projections rebuildable from the
// journal" contract of DOMAIN_MODEL §2.
//
// The household currency is taken from the first committed operation, so an
// empty journal cannot be rebuilt and fails with INVALID_VALUE; the caller must
// provide the household currency some other way until at least one operation
// exists. Every operation must share that currency (CURRENCY_MISMATCH).
//
// Postings and reservation changes are applied per operation in commit order.
// After each operation CheckInvariants must hold; a failure is returned as
// ACCOUNTING_INVARIANT_VIOLATION with the underlying check as the cause. An
// unknown account or envelope surfaces from ApplyPostings or
// ApplyReservationChanges unchanged.
func (j *Journal) RebuildLedger(accounts []AccountMeta, envelopes []EnvelopeId) (*LedgerState, error) {
	j.mu.RLock()
	ops := append([]JournalOperation(nil), j.ops...)
	j.mu.RUnlock()

	if len(ops) == 0 {
		return nil, NewError(CodeInvalidValue, "cannot rebuild a ledger from an empty journal")
	}

	state, err := NewLedgerState(ops[0].currency, accounts, envelopes)
	if err != nil {
		return nil, err
	}
	for _, op := range ops {
		if !op.currency.Equal(ops[0].currency) || op.currency.Exponent() != ops[0].currency.Exponent() {
			return nil, NewErrorf(CodeCurrencyMismatch, "journal operation %q currency %q does not match the journal currency %q", op.id, op.currency, ops[0].currency)
		}
		if err := state.ApplyPostings(op.effects.Postings); err != nil {
			return nil, err
		}
		if err := state.ApplyReservationChanges(op.effects.Reservations...); err != nil {
			return nil, err
		}
		if err := state.CheckInvariants(); err != nil {
			return nil, WrapError(CodeAccountingInvariantViolation, "rebuilt ledger failed accounting invariants", err)
		}
	}
	return state, nil
}
