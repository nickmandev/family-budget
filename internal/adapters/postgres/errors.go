package postgres

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"family-budget/internal/domain"
)

// PostgreSQL SQLSTATE values the adapter reacts to. They are defined by
// PostgreSQL, not by this project.
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
	sqlStateCheckViolation      = "23514"
	sqlStateNotNullViolation    = "23502"
	sqlStateStringTooLong       = "22001"
	sqlStateInvalidText         = "22P02"
	sqlStateDatetimeField       = "22007"
	sqlStateDatetimeOverflow    = "22008"
	sqlStateNumericOutOfRange   = "22003"
	sqlStateSerialization       = "40001"
	sqlStateDeadlockDetected    = "40P01"
	sqlStateLockNotAvailable    = "55P03"
)

// toDomainErr translates persistence errors into the domain error contract.
//
//   - absent row -> ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD (never "forbidden");
//   - duplicate idempotency key -> IDEMPOTENCY_KEY_PAYLOAD_MISMATCH (the
//     higher layer decides replay vs mismatch by comparing fingerprints);
//   - serialization failure, deadlock, or unavailable lock -> the only
//     retryable code, CONCURRENCY_CONFLICT;
//   - other constraint violations -> INVALID_VALUE;
//   - foreign-key violations -> ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD because a
//     cross-household reference is the common cause and must not leak
//     existence.
//
// Errors that are already domain errors pass through unchanged.
func toDomainErr(err error) error {
	if err == nil {
		return nil
	}
	if domain.CodeOf(err) != "" {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlStateUniqueViolation:
			if strings.Contains(pgErr.ConstraintName, "idempotency_key") {
				return domain.WrapError(
					domain.CodeIdempotencyKeyPayloadMismatch,
					"idempotency key is already committed",
					err,
				)
			}
			return domain.WrapError(domain.CodeInvalidValue, "duplicate persisted resource", err)
		case sqlStateForeignKeyViolation:
			return domain.WrapError(
				domain.CodeEntityNotFoundOrNotInHousehold,
				"referenced row does not exist in this household",
				err,
			)
		case sqlStateCheckViolation, sqlStateNotNullViolation, sqlStateStringTooLong,
			sqlStateInvalidText, sqlStateDatetimeField, sqlStateDatetimeOverflow,
			sqlStateNumericOutOfRange:
			return domain.WrapError(domain.CodeInvalidValue, "persisted value violates a constraint", err)
		case sqlStateSerialization, sqlStateDeadlockDetected, sqlStateLockNotAvailable:
			return domain.WrapError(domain.CodeConcurrencyConflict, "concurrent transaction conflict", err)
		default:
			return domain.WrapError(domain.CodeInvalidValue, "unexpected PostgreSQL error", err)
		}
	}
	return err
}

// isUniqueViolation reports whether err is a unique-constraint violation on
// the named constraint. It is used where the adapter needs to distinguish a
// duplicate operation id from a duplicate idempotency key.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == sqlStateUniqueViolation && pgErr.ConstraintName == constraint
}
