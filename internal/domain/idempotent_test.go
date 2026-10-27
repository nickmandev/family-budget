package domain

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustIdempotencyKeyForTest(t *testing.T, raw string) IdempotencyKey {
	t.Helper()
	key, err := NewIdempotencyKey(raw)
	if err != nil {
		t.Fatalf("NewIdempotencyKey(%q): %v", raw, err)
	}
	return key
}

func mustRequestFingerprintForTest(t *testing.T, raw string) RequestFingerprint {
	t.Helper()
	fingerprint, err := NewRequestFingerprint(raw)
	if err != nil {
		t.Fatalf("NewRequestFingerprint(%q): %v", raw, err)
	}
	return fingerprint
}

func TestNewIdempotencyRecord(t *testing.T) {
	key := mustIdempotencyKeyForTest(t, "key-1")
	fingerprint := mustRequestFingerprintForTest(t, "abcdef0123456789")
	opID := mustJournalOpIDForTest(t, "op-1")
	createdAt := NewRecordedAt(time.Date(2026, time.January, 15, 9, 0, 0, 0, time.UTC))

	tests := []struct {
		name        string
		key         IdempotencyKey
		fingerprint RequestFingerprint
		opID        JournalOperationId
		createdAt   RecordedAt
		wantCode    ErrorCode
	}{
		{name: "valid", key: key, fingerprint: fingerprint, opID: opID, createdAt: createdAt},
		{name: "zero key", key: "", fingerprint: fingerprint, opID: opID, createdAt: createdAt, wantCode: CodeInvalidValue},
		{name: "whitespace key", key: IdempotencyKey("   "), fingerprint: fingerprint, opID: opID, createdAt: createdAt, wantCode: CodeInvalidValue},
		{name: "unnormalized key", key: IdempotencyKey(" key-1 "), fingerprint: fingerprint, opID: opID, createdAt: createdAt, wantCode: CodeInvalidValue},
		{name: "zero fingerprint", key: key, fingerprint: "", opID: opID, createdAt: createdAt, wantCode: CodeInvalidValue},
		{name: "non-hex fingerprint", key: key, fingerprint: RequestFingerprint("xyz"), opID: opID, createdAt: createdAt, wantCode: CodeInvalidValue},
		{name: "zero operation id", key: key, fingerprint: fingerprint, opID: "", createdAt: createdAt, wantCode: CodeInvalidValue},
		{name: "zero created timestamp", key: key, fingerprint: fingerprint, opID: opID, createdAt: RecordedAt{}, wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record, err := NewIdempotencyRecord(tt.key, tt.fingerprint, tt.opID, tt.createdAt)
			if tt.wantCode != "" {
				if !HasCode(err, tt.wantCode) {
					t.Fatalf("NewIdempotencyRecord error = %v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewIdempotencyRecord unexpected error: %v", err)
			}
			if record.Key() != key {
				t.Errorf("Key = %q, want %q", record.Key(), key)
			}
			if record.Fingerprint() != fingerprint {
				t.Errorf("Fingerprint = %q, want %q", record.Fingerprint(), fingerprint)
			}
			if record.OperationID() != opID {
				t.Errorf("OperationID = %q, want %q", record.OperationID(), opID)
			}
			if !record.CreatedAt().Time().Equal(createdAt.Time()) {
				t.Errorf("CreatedAt = %s, want %s", record.CreatedAt(), createdAt)
			}
		})
	}
}

func TestIdempotencyStoreFirstCallAndReplay(t *testing.T) {
	store := NewIdempotencyStore()
	key := mustIdempotencyKeyForTest(t, "key-1")
	fingerprint := mustRequestFingerprintForTest(t, "abcdef0123456789")
	opID := mustJournalOpIDForTest(t, "op-1")
	createdAt := NewRecordedAt(time.Date(2026, time.January, 15, 9, 0, 0, 0, time.UTC))

	record, isNew, err := store.Begin(key, fingerprint, opID, createdAt)
	if err != nil {
		t.Fatalf("Begin(first): %v", err)
	}
	if !isNew {
		t.Error("Begin(first) isNew = false, want true")
	}
	if record.Key() != key || record.Fingerprint() != fingerprint || record.OperationID() != opID {
		t.Errorf("Begin(first) record = %+v, want key %q fingerprint %q op %q", record, key, fingerprint, opID)
	}
	if !record.CreatedAt().Time().Equal(createdAt.Time()) {
		t.Errorf("Begin(first) CreatedAt = %s, want %s", record.CreatedAt(), createdAt)
	}

	stored, err := store.Lookup(key)
	if err != nil {
		t.Fatalf("Lookup after first Begin: %v", err)
	}
	if stored.OperationID() != opID {
		t.Errorf("Lookup OperationID = %q, want %q", stored.OperationID(), opID)
	}

	// A retry with the same key and fingerprint replays the ORIGINAL record,
	// even when the caller presents a different candidate operation id and
	// timestamp.
	replayOpID := mustJournalOpIDForTest(t, "op-retry")
	replayAt := NewRecordedAt(createdAt.Time().Add(time.Minute))
	replayed, isNew, err := store.Begin(key, fingerprint, replayOpID, replayAt)
	if err != nil {
		t.Fatalf("Begin(replay): %v", err)
	}
	if isNew {
		t.Error("Begin(replay) isNew = true, want false")
	}
	if replayed.OperationID() != opID {
		t.Errorf("Begin(replay) OperationID = %q, want original %q", replayed.OperationID(), opID)
	}
	if !replayed.CreatedAt().Time().Equal(createdAt.Time()) {
		t.Errorf("Begin(replay) CreatedAt = %s, want original %s", replayed.CreatedAt(), createdAt)
	}
}

func TestIdempotencyStoreFingerprintMismatch(t *testing.T) {
	store := NewIdempotencyStore()
	key := mustIdempotencyKeyForTest(t, "key-1")
	fingerprint := mustRequestFingerprintForTest(t, "abcdef0123456789")
	otherFingerprint := mustRequestFingerprintForTest(t, "deadbeef")
	opID := mustJournalOpIDForTest(t, "op-1")
	createdAt := NewRecordedAt(time.Date(2026, time.January, 15, 9, 0, 0, 0, time.UTC))

	if _, isNew, err := store.Begin(key, fingerprint, opID, createdAt); err != nil || !isNew {
		t.Fatalf("Begin(first) = (isNew %v, err %v), want (true, nil)", isNew, err)
	}

	record, isNew, err := store.Begin(key, otherFingerprint, mustJournalOpIDForTest(t, "op-2"), createdAt)
	if !HasCode(err, CodeIdempotencyKeyPayloadMismatch) {
		t.Fatalf("Begin(mismatch) error = %v, want IDEMPOTENCY_KEY_PAYLOAD_MISMATCH", err)
	}
	if isNew {
		t.Error("Begin(mismatch) isNew = true, want false")
	}
	if record.Key() != "" || record.Fingerprint() != "" || record.OperationID() != "" {
		t.Errorf("Begin(mismatch) returned a record %+v, want zero value", record)
	}

	stored, err := store.Lookup(key)
	if err != nil {
		t.Fatalf("Lookup after mismatch: %v", err)
	}
	if stored.Fingerprint() != fingerprint {
		t.Errorf("stored fingerprint = %q, want original %q", stored.Fingerprint(), fingerprint)
	}
	if stored.OperationID() != opID {
		t.Errorf("stored operation id = %q, want original %q", stored.OperationID(), opID)
	}
}

func TestIdempotencyStoreDistinctKeysAndUnknownLookup(t *testing.T) {
	store := NewIdempotencyStore()
	keyA := mustIdempotencyKeyForTest(t, "key-a")
	keyB := mustIdempotencyKeyForTest(t, "key-b")
	fingerprintA := mustRequestFingerprintForTest(t, "aa")
	fingerprintB := mustRequestFingerprintForTest(t, "bb")
	opA := mustJournalOpIDForTest(t, "op-a")
	opB := mustJournalOpIDForTest(t, "op-b")
	now := NewRecordedAt(time.Date(2026, time.January, 15, 9, 0, 0, 0, time.UTC))

	if _, isNew, err := store.Begin(keyA, fingerprintA, opA, now); err != nil || !isNew {
		t.Fatalf("Begin(A) = (isNew %v, err %v), want (true, nil)", isNew, err)
	}
	if _, isNew, err := store.Begin(keyB, fingerprintB, opB, now); err != nil || !isNew {
		t.Fatalf("Begin(B) = (isNew %v, err %v), want (true, nil)", isNew, err)
	}

	for _, tt := range []struct {
		key   IdempotencyKey
		opID  JournalOperationId
		print string
	}{
		{key: keyA, opID: opA, print: "A"},
		{key: keyB, opID: opB, print: "B"},
	} {
		record, err := store.Lookup(tt.key)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", tt.print, err)
		}
		if record.OperationID() != tt.opID {
			t.Errorf("Lookup(%s) operation id = %q, want %q", tt.print, record.OperationID(), tt.opID)
		}
	}

	unknown := mustIdempotencyKeyForTest(t, "key-unknown")
	if _, err := store.Lookup(unknown); !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
		t.Errorf("Lookup(unknown) error = %v, want ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD", err)
	}
}

func TestIdempotencyStoreBeginRejectsInvalidInput(t *testing.T) {
	store := NewIdempotencyStore()
	key := mustIdempotencyKeyForTest(t, "key-1")
	fingerprint := mustRequestFingerprintForTest(t, "abcdef0123456789")
	opID := mustJournalOpIDForTest(t, "op-1")
	now := NewRecordedAt(time.Date(2026, time.January, 15, 9, 0, 0, 0, time.UTC))

	tests := []struct {
		name        string
		key         IdempotencyKey
		fingerprint RequestFingerprint
		opID        JournalOperationId
		now         RecordedAt
	}{
		{name: "zero key", key: "", fingerprint: fingerprint, opID: opID, now: now},
		{name: "zero fingerprint", key: key, fingerprint: "", opID: opID, now: now},
		{name: "zero operation id", key: key, fingerprint: fingerprint, opID: "", now: now},
		{name: "zero timestamp", key: key, fingerprint: fingerprint, opID: opID, now: RecordedAt{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := store.Begin(tt.key, tt.fingerprint, tt.opID, tt.now); !HasCode(err, CodeInvalidValue) {
				t.Fatalf("Begin invalid input error = %v, want INVALID_VALUE", err)
			}
			if _, err := store.Lookup(key); !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
				t.Errorf("store changed after rejected Begin: Lookup error = %v", err)
			}
		})
	}
}

func TestIdempotencyStoreConcurrentBegin(t *testing.T) {
	store := NewIdempotencyStore()
	key := mustIdempotencyKeyForTest(t, "key-concurrent")
	fingerprint := mustRequestFingerprintForTest(t, "abcdef0123456789")
	opID := mustJournalOpIDForTest(t, "op-concurrent")
	now := NewRecordedAt(time.Date(2026, time.January, 15, 9, 0, 0, 0, time.UTC))

	const callers = 64
	var newCount atomic.Int64
	var wg sync.WaitGroup
	results := make(chan IdempotencyRecord, callers)
	errors := make(chan error, callers)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record, isNew, err := store.Begin(key, fingerprint, opID, now)
			if err != nil {
				errors <- err
				return
			}
			if isNew {
				newCount.Add(1)
			}
			results <- record
		}()
	}
	wg.Wait()
	close(results)
	close(errors)

	for err := range errors {
		t.Errorf("concurrent Begin: %v", err)
	}
	if got := newCount.Load(); got != 1 {
		t.Errorf("isNew count = %d, want exactly 1", got)
	}
	for record := range results {
		if record.OperationID() != opID {
			t.Errorf("concurrent Begin operation id = %q, want %q", record.OperationID(), opID)
		}
	}

	stored, err := store.Lookup(key)
	if err != nil {
		t.Fatalf("Lookup after concurrent Begin: %v", err)
	}
	if stored.OperationID() != opID {
		t.Errorf("stored operation id = %q, want %q", stored.OperationID(), opID)
	}
}
