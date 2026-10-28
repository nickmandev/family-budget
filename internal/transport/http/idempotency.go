package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

const maxIdempotencyBodyBytes = 1 << 20

// readCanonicalJSON reads exactly one JSON value, retains its raw bytes and
// computes the idempotency canonical form. The canonical form is produced by
// decoding into interface{} with json.Number, then re-encoding: Go sorts map
// keys, so semantically identical objects with different key order normalize
// to the same bytes while numeric lexemes are preserved.
func readCanonicalJSON(r *http.Request) (raw []byte, canonical []byte, err error) {
	if r.Body == nil {
		return nil, nil, newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body is required.")
	}
	limited := io.LimitReader(r.Body, maxIdempotencyBodyBytes+1)
	raw, err = io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, nil, errPayloadTooLarge
		}
		return nil, nil, newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body could not be read.")
	}
	if len(raw) > maxIdempotencyBodyBytes {
		return nil, nil, errPayloadTooLarge
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil, newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body is required.")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, nil, newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body is not valid JSON.")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, nil, newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body must contain a single JSON value.")
	}
	canonical, err = json.Marshal(value)
	if err != nil {
		return nil, nil, newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body is not valid JSON.")
	}
	return raw, canonical, nil
}

// decodeIdempotentRequest decodes a financial mutation and derives its
// idempotency key and canonical request fingerprint. The key is required:
// silently generating one would make retry safety unverifiable.
func decodeIdempotentRequest(r *http.Request, dst any) (domain.IdempotencyKey, domain.RequestFingerprint, error) {
	rawKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if rawKey == "" {
		return "", "", newAPIError(http.StatusBadRequest, CodeBadRequest, "The Idempotency-Key header is required.")
	}
	key, err := domain.NewIdempotencyKey(rawKey)
	if err != nil {
		return "", "", err
	}
	raw, canonical, err := readCanonicalJSON(r)
	if err != nil {
		return "", "", err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return "", "", newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body is not valid JSON.")
	}
	sum := sha256.Sum256([]byte(r.Method + " " + r.URL.EscapedPath() + "\n" + string(canonical)))
	fingerprint, err := domain.NewRequestFingerprint(hex.EncodeToString(sum[:]))
	if err != nil {
		return "", "", err
	}
	return key, fingerprint, nil
}

// decodeOptionalIdempotency accepts a mutation that may or may not be keyed
// (currently only imports/non-financial flows would use it; financial routes
// must use decodeIdempotentRequest).
func decodeOptionalIdempotency(r *http.Request, dst any) (domain.IdempotencyKey, domain.RequestFingerprint, error) {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		if err := decodeJSONBody(r, dst); err != nil {
			return "", "", err
		}
		return "", "", nil
	}
	return decodeIdempotentRequest(r, dst)
}

func decodeJSONBody(r *http.Request, dst any) error {
	raw, _, err := readCanonicalJSON(r)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return newAPIError(http.StatusBadRequest, CodeBadRequest, "The request body is not valid JSON.")
	}
	return nil
}

// encodeJournalCursor renders an opaque, URL-safe cursor. Clients must treat it
// as opaque; the RFC3339Nano instant and operation UUID are an implementation
// detail.
func encodeJournalCursor(cursor *ports.JournalCursor) *string {
	if cursor == nil {
		return nil
	}
	raw := cursor.CommittedAt.UTC().Format(time.RFC3339Nano) + "|" + cursor.ID.String()
	value := base64.RawURLEncoding.EncodeToString([]byte(raw))
	return &value
}

func decodeJournalCursor(raw string) (*ports.JournalCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, domain.NewError(domain.CodeInvalidValue, "cursor is invalid")
	}
	instant, id, found := strings.Cut(string(decoded), "|")
	if !found || !isUUID(id) {
		return nil, domain.NewError(domain.CodeInvalidValue, "cursor is invalid")
	}
	committedAt, err := time.Parse(time.RFC3339Nano, instant)
	if err != nil {
		return nil, domain.NewError(domain.CodeInvalidValue, "cursor is invalid")
	}
	operationID, err := domain.NewJournalOperationId(strings.ToLower(id))
	if err != nil {
		return nil, domain.NewError(domain.CodeInvalidValue, "cursor is invalid")
	}
	return &ports.JournalCursor{CommittedAt: committedAt.UTC(), ID: operationID}, nil
}
