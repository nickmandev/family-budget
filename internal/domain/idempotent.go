// This file implements household-scoped idempotency records for financial
// mutations (DOMAIN_MODEL §3.10, API_CONTRACT "Idempotency", and
// IMPLEMENTATION_DECISIONS Q10). A record binds an idempotency key to the
// canonical request fingerprint and the original committed operation, so a
// retried command replays the original result instead of posting again.
//
// The in-memory store is the reference behaviour for the persistence adapter.
// Household scoping is the store-per-household arrangement: the persistence
// adapter must enforce household scope inside its key space (for example by a
// composite household/key primary key) and must persist records with the same
// atomicity as the journal operation they describe.
package domain

import "sync"

// IdempotencyRecord binds a household-scoped key to the canonical request
// fingerprint and the original semantic result: the committed journal
// operation created for the first request under that key.
//
// The zero value is invalid. Construct with NewIdempotencyRecord.
type IdempotencyRecord struct {
	key         IdempotencyKey
	fingerprint RequestFingerprint
	operationID JournalOperationId
	createdAt   RecordedAt
}

// NewIdempotencyRecord validates and builds an idempotency record. A zero or
// malformed key, fingerprint, operation id, or timestamp fails with
// INVALID_VALUE.
func NewIdempotencyRecord(key IdempotencyKey, fingerprint RequestFingerprint, opID JournalOperationId, createdAt RecordedAt) (IdempotencyRecord, error) {
	if key.IsZero() {
		return IdempotencyRecord{}, NewError(CodeInvalidValue, "idempotency record key must not be empty")
	}
	if fingerprint.IsZero() {
		return IdempotencyRecord{}, NewError(CodeInvalidValue, "idempotency record fingerprint must not be empty")
	}
	if opID.IsZero() {
		return IdempotencyRecord{}, NewError(CodeInvalidValue, "idempotency record operation id must not be empty")
	}
	if createdAt.IsZero() {
		return IdempotencyRecord{}, NewError(CodeInvalidValue, "idempotency record created timestamp must be set")
	}

	// Re-run the value-object constructors so a struct-literal bypass cannot
	// store an unnormalized or non-hex value.
	validatedKey, err := NewIdempotencyKey(key.String())
	if err != nil {
		return IdempotencyRecord{}, err
	}
	if validatedKey != key {
		return IdempotencyRecord{}, NewErrorf(CodeInvalidValue, "idempotency record key %q is not normalized", key)
	}
	validatedFingerprint, err := NewRequestFingerprint(fingerprint.String())
	if err != nil {
		return IdempotencyRecord{}, err
	}
	if validatedFingerprint != fingerprint {
		return IdempotencyRecord{}, NewErrorf(CodeInvalidValue, "idempotency record fingerprint %q is not normalized", fingerprint)
	}

	return IdempotencyRecord{
		key:         key,
		fingerprint: fingerprint,
		operationID: opID,
		createdAt:   createdAt,
	}, nil
}

// Key returns the household-scoped idempotency key.
func (r IdempotencyRecord) Key() IdempotencyKey {
	return r.key
}

// Fingerprint returns the canonical request fingerprint bound to the key.
func (r IdempotencyRecord) Fingerprint() RequestFingerprint {
	return r.fingerprint
}

// OperationID returns the original committed journal operation.
func (r IdempotencyRecord) OperationID() JournalOperationId {
	return r.operationID
}

// CreatedAt returns the instant at which the original request was recorded.
func (r IdempotencyRecord) CreatedAt() RecordedAt {
	return r.createdAt
}

// IdempotencyStore is the household-scoped key registry for financial
// mutations. One store holds the records for one household; cross-household
// isolation is the persistence adapter's key space.
//
// The zero value is invalid. Construct with NewIdempotencyStore.
type IdempotencyStore struct {
	mu      sync.Mutex
	records map[string]IdempotencyRecord
}

// NewIdempotencyStore builds an empty store.
func NewIdempotencyStore() *IdempotencyStore {
	return &IdempotencyStore{records: make(map[string]IdempotencyRecord)}
}

// Begin validates a request under key against any existing record and, when
// the key is new, records it. It is the atomic check-and-insert used by a
// command handler before committing.
//
//   - No record for key: stores the fingerprint and original operation id and
//     returns (record, true, nil); the caller proceeds to commit.
//   - Existing record with an equal fingerprint: returns the existing record
//     and false; the caller replays the original result without posting again.
//   - Existing record with a different fingerprint: returns
//     IDEMPOTENCY_KEY_PAYLOAD_MISMATCH and leaves the store unchanged.
//
// A zero or malformed key, fingerprint, operation id, or timestamp fails with
// INVALID_VALUE before any lookup and leaves the store unchanged.
func (s *IdempotencyStore) Begin(key IdempotencyKey, fingerprint RequestFingerprint, opID JournalOperationId, now RecordedAt) (IdempotencyRecord, bool, error) {
	record, err := NewIdempotencyRecord(key, fingerprint, opID, now)
	if err != nil {
		return IdempotencyRecord{}, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, known := s.records[key.String()]; known {
		if existing.fingerprint != fingerprint {
			return IdempotencyRecord{}, false, NewErrorf(
				CodeIdempotencyKeyPayloadMismatch,
				"idempotency key %q was reused with a different request fingerprint",
				key,
			)
		}
		return existing, false, nil
	}
	s.records[key.String()] = record
	return record, true, nil
}

// Lookup returns the record for key, or ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD
// when the store has no record for it.
func (s *IdempotencyStore) Lookup(key IdempotencyKey) (IdempotencyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, known := s.records[key.String()]
	if !known {
		return IdempotencyRecord{}, NewErrorf(CodeEntityNotFoundOrNotInHousehold, "idempotency key %q has no record in this household", key)
	}
	return record, nil
}
