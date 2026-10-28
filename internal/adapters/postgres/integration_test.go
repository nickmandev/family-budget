package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// This file completes the PostgreSQL integration suite required by
// MVP_SCOPE quality gate 3 and STACK_DECISIONS §8: migrations, CHECK/unique
// constraints, household-scoped foreign keys, command idempotency and rollback,
// the reservation lifecycle, and an upgrade-path check. Every test skips without
// TEST_POSTGRES_DSN/DATABASE_URL through testStore, so the package compiles and
// passes (with skips) on machines without a database.

// TestSchemaEnforcesCriticalConstraints asserts the reviewed migration creates
// the constraints the domain relies on, then feeds deliberately invalid rows
// through direct SQL. The cases mirror the domain rules that must never depend
// on the application layer alone.
func TestSchemaEnforcesCriticalConstraints(t *testing.T) {
	store := testStore(t)
	s := seedHousehold(t, store.Repositories(), "constraints")
	db := store.DB()

	constraints := []string{
		"journal_operations_idempotency_key_key",
		"journal_operations_committed_after_created",
		"journal_operations_amount_positive_bounded",
		"account_postings_delta_nonzero_bounded",
		"envelope_postings_delta_nonzero_bounded",
		"accounts_card_liability_not_eligible",
		"plans_intended_amount_positive_bounded",
		"suggestions_amount_positive_bounded",
		"reservation_changes_account_delta_shape",
		"related_links_exactly_one_target",
		"household_memberships_removal_consistent",
	}
	for _, name := range constraints {
		var count int64
		if err := db.Raw(`SELECT count(*) FROM pg_constraint WHERE conname = ?`, name).Scan(&count).Error; err != nil {
			t.Fatalf("query constraint %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("constraint %q exists %d times, want 1", name, count)
		}
	}
	uniqueIndexes := []string{
		"users_email_lower_key",
		"envelopes_active_linked_card_key",
	}
	for _, name := range uniqueIndexes {
		var count int64
		if err := db.Raw(
			`SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`,
			name,
		).Scan(&count).Error; err != nil {
			t.Fatalf("query index %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("unique index %q exists %d times, want 1", name, count)
		}
	}

	now := journalTime()
	cases := []struct {
		name      string
		wantState string
		run       func(t *testing.T, db *gorm.DB) error
	}{
		{
			name:      "journal amount above max minor units",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				amount := domain.MaxMinorUnits + 1
				return rawInsertJournal(t, db, s, mustID(t), "INCOME", "over-max", &amount, now, now)
			},
		},
		{
			name:      "journal amount zero",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				amount := int64(0)
				return rawInsertJournal(t, db, s, mustID(t), "INCOME", "zero-amount", &amount, now, now)
			},
		},
		{
			name:      "journal committed before created",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				return rawInsertJournal(t, db, s, mustID(t), "INCOME", "time-order", nil, now, now.Add(-time.Second))
			},
		},
		{
			name:      "journal invalid operation type",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				return rawInsertJournal(t, db, s, mustID(t), "NOT_AN_OPERATION", "bad-type", nil, now, now)
			},
		},
		{
			name:      "journal duplicate household idempotency key",
			wantState: sqlStateUniqueViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				if err := rawInsertJournal(t, db, s, mustID(t), "INCOME", "duplicate-key", nil, now, now); err != nil {
					return err
				}
				return rawInsertJournal(t, db, s, mustID(t), "INCOME", "duplicate-key", nil, now, now)
			},
		},
		{
			name:      "account posting zero delta",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				opID := rawInsertValidJournal(t, db, s)
				return rawInsertAccountPosting(db, s.householdID, opID, s.accountID, 0)
			},
		},
		{
			name:      "account posting above max minor units",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				opID := rawInsertValidJournal(t, db, s)
				return rawInsertAccountPosting(db, s.householdID, opID, s.accountID, domain.MaxMinorUnits+1)
			},
		},
		{
			name:      "account posting below min minor units",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				opID := rawInsertValidJournal(t, db, s)
				return rawInsertAccountPosting(db, s.householdID, opID, s.accountID, -domain.MaxMinorUnits-1)
			},
		},
		{
			name:      "envelope posting zero delta",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				opID := rawInsertValidJournal(t, db, s)
				return rawInsertEnvelopePosting(db, s.householdID, opID, s.envelopeID, 0)
			},
		},
		{
			name:      "plan invalid kind",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				return rawInsertPlan(db, s, mustID(t), "BOGUS", "DRAFT", 100)
			},
		},
		{
			name:      "plan invalid state",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				return rawInsertPlan(db, s, mustID(t), "PURCHASE", "BOGUS", 100)
			},
		},
		{
			name:      "plan intended amount above max minor units",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				return rawInsertPlan(db, s, mustID(t), "PURCHASE", "DRAFT", domain.MaxMinorUnits+1)
			},
		},
		{
			name:      "suggestion invalid source",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				return rawInsertSuggestion(db, s, mustID(t), "BOGUS", "SUGGESTED", 100)
			},
		},
		{
			name:      "suggestion invalid state",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				return rawInsertSuggestion(db, s, mustID(t), "MANUAL", "BOGUS", 100)
			},
		},
		{
			name:      "card liability cannot be budget eligible",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				return db.Exec(
					`INSERT INTO accounts (id, household_id, name, kind, budget_eligible, status)
					 VALUES (?, ?, 'Bad card', 'CREDIT_CARD_LIABILITY', true, 'ACTIVE')`,
					mustUUID(t, mustID(t)), mustUUID(t, s.householdID.String()),
				).Error
			},
		},
		{
			name:      "reservation change account shape",
			wantState: sqlStateCheckViolation,
			run: func(t *testing.T, db *gorm.DB) error {
				opID := rawInsertValidJournal(t, db, s)
				planID := mustID(t)
				if err := rawInsertPlan(db, s, planID, "PURCHASE", "DRAFT", 100); err != nil {
					return err
				}
				return db.Exec(
					`INSERT INTO reservation_changes
					 (household_id, journal_operation_id, plan_id, envelope_id, account_id, envelope_delta_minor, account_delta_minor)
					 VALUES (?, ?, ?, ?, NULL, 100, 50)`,
					mustUUID(t, s.householdID.String()), mustUUID(t, opID.String()),
					mustUUID(t, planID), mustUUID(t, s.envelopeID.String()),
				).Error
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantSQLState(t, tc.run(t, db), tc.wantState)
		})
	}
}

// TestHouseholdScopedForeignKeysRejectCrossHouseholdReferences proves the
// composite keys keep household boundaries intact and that RESTRICT protects
// committed financial rows from deletion.
func TestHouseholdScopedForeignKeysRejectCrossHouseholdReferences(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "fk-a")
	b := seedHousehold(t, repos, "fk-b")
	db := store.DB()

	opA := rawInsertValidJournal(t, db, a)
	opB := rawInsertValidJournal(t, db, b)

	t.Run("posting references another household account", func(t *testing.T) {
		wantSQLState(t, rawInsertAccountPosting(db, a.householdID, opA, b.accountID, 100), sqlStateForeignKeyViolation)
	})
	t.Run("posting references another household operation", func(t *testing.T) {
		wantSQLState(t, rawInsertAccountPosting(db, a.householdID, opB, a.accountID, 100), sqlStateForeignKeyViolation)
	})
	t.Run("envelope posting references another household envelope", func(t *testing.T) {
		wantSQLState(t, rawInsertEnvelopePosting(db, a.householdID, opA, b.envelopeID, 100), sqlStateForeignKeyViolation)
	})
	t.Run("plan targets another household envelope", func(t *testing.T) {
		planID := mustID(t)
		wantSQLState(t, db.Exec(
			`INSERT INTO plans (id, household_id, kind, state, target_envelope_id, intended_amount_minor)
			 VALUES (?, ?, 'PURCHASE', 'DRAFT', ?, 100)`,
			mustUUID(t, planID), mustUUID(t, a.householdID.String()), mustUUID(t, b.envelopeID.String()),
		).Error, sqlStateForeignKeyViolation)
	})
	t.Run("reservation change references another household plan", func(t *testing.T) {
		planB := mustID(t)
		if err := rawInsertPlan(db, b, planB, "PURCHASE", "DRAFT", 100); err != nil {
			t.Fatalf("seed plan in household b: %v", err)
		}
		wantSQLState(t, db.Exec(
			`INSERT INTO reservation_changes
			 (household_id, journal_operation_id, plan_id, envelope_id, envelope_delta_minor)
			 VALUES (?, ?, ?, ?, 100)`,
			mustUUID(t, a.householdID.String()), mustUUID(t, opA.String()),
			mustUUID(t, planB), mustUUID(t, a.envelopeID.String()),
		).Error, sqlStateForeignKeyViolation)
	})
	t.Run("idempotency record references another household operation", func(t *testing.T) {
		wantSQLState(t, db.Exec(
			`INSERT INTO idempotency_records (household_id, idempotency_key, request_fingerprint, operation_id)
			 VALUES (?, 'cross-household', 'fp', ?)`,
			mustUUID(t, a.householdID.String()), mustUUID(t, opB.String()),
		).Error, sqlStateForeignKeyViolation)
	})
	t.Run("deleting an account with postings is restricted", func(t *testing.T) {
		if err := rawInsertAccountPosting(db, a.householdID, opA, a.accountID, 100); err != nil {
			t.Fatalf("seed account posting: %v", err)
		}
		// PostgreSQL reports an ON DELETE RESTRICT violation as 23001
		// (restrict_violation), not as a NO ACTION foreign-key violation.
		wantSQLState(t, db.Exec(`DELETE FROM accounts WHERE id = ?`, mustUUID(t, a.accountID.String())).Error, sqlStateRestrictViolation)
	})
	t.Run("deleting an envelope with postings is restricted", func(t *testing.T) {
		if err := rawInsertEnvelopePosting(db, a.householdID, opA, a.envelopeID, 100); err != nil {
			t.Fatalf("seed envelope posting: %v", err)
		}
		wantSQLState(t, db.Exec(`DELETE FROM envelopes WHERE id = ?`, mustUUID(t, a.envelopeID.String())).Error, sqlStateRestrictViolation)
	})

	t.Run("append port rejects another household account", func(t *testing.T) {
		date, err := domain.ParseEffectiveDate("2026-10-01")
		if err != nil {
			t.Fatalf("effective date: %v", err)
		}
		amount := testPositiveAmount(t, 100, a.currency)
		operation := newJournalOperation(
			t, a, domain.JournalOperationId(mustID(t)),
			domain.IdempotencyKey("cross-"+mustID(t)), domain.RequestFingerprint("c0de"),
			domain.OpIncome, date, &amount,
			domain.Effects{Postings: domain.PostingSet{Accounts: []domain.AccountPosting{{
				AccountID: b.accountID,
				Delta:     testSignedDelta(t, 100, a.currency),
			}}}},
		)
		wantCode(t, repos.Journal.Append(t.Context(), a.householdID, operation), domain.CodeEntityNotFoundOrNotInHousehold)
	})

	t.Run("plan port rejects another household target", func(t *testing.T) {
		planID := domain.PlanId(mustID(t))
		target := domain.ReservationTarget{PlanID: planID, EnvelopeID: b.envelopeID, AccountID: b.accountID}
		plan, err := domain.NewPlannedOperation(planID, domain.PlanKindPurchase, target, testPositiveAmount(t, 100, a.currency))
		if err != nil {
			t.Fatalf("new plan: %v", err)
		}
		wantCode(t, repos.Plans.Create(t.Context(), a.householdID, plan), domain.CodeEntityNotFoundOrNotInHousehold)
	})
}

// TestCommandIdempotencyAndRollbackEndToEnd exercises the full command-style
// transaction (idempotency record + journal operation + postings) through the
// ports: same-key replay, different-fingerprint conflict, and a mid-transaction
// failure leaving zero rows.
func TestCommandIdempotencyAndRollbackEndToEnd(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "command")
	ctx := t.Context()

	date, err := domain.ParseEffectiveDate("2026-10-01")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}
	now := journalTime()

	newIncome := func(key string, minor int64) domain.JournalOperation {
		amount := testPositiveAmount(t, minor, a.currency)
		return newJournalOperation(
			t, a, domain.JournalOperationId(mustID(t)),
			domain.IdempotencyKey(key), domain.RequestFingerprint(fmt.Sprintf("%x", key)),
			domain.OpIncome, date, &amount,
			domain.Effects{Postings: domain.PostingSet{Accounts: []domain.AccountPosting{{
				AccountID: a.accountID,
				Delta:     testSignedDelta(t, minor, a.currency),
			}}}},
		)
	}

	operation := newIncome("command-key", 2_000)
	fingerprint := operation.RequestFingerprint()
	err = store.WithinTransaction(ctx, func(ctx context.Context, txRepos ports.Repositories) error {
		if _, isNew, err := txRepos.Idempotency.Begin(ctx, a.householdID, operation.IdempotencyKey(), fingerprint, operation.ID(), now); err != nil {
			return err
		} else if !isNew {
			return domain.NewError(domain.CodeInvalidValue, "first command attempt must be new")
		}
		return txRepos.Journal.Append(ctx, a.householdID, operation)
	})
	if err != nil {
		t.Fatalf("command transaction: %v", err)
	}
	assertRowCount(t, store.DB(), &journalOperationModel{}, 1)
	assertRowCount(t, store.DB(), &accountPostingModel{}, 1)
	assertRowCount(t, store.DB(), &idempotencyRecordModel{}, 1)

	// Same key and fingerprint replays the original operation without posting
	// again.
	err = store.WithinTransaction(ctx, func(ctx context.Context, txRepos ports.Repositories) error {
		record, isNew, err := txRepos.Idempotency.Begin(ctx, a.householdID, operation.IdempotencyKey(), fingerprint, operation.ID(), now.Add(time.Second))
		if err != nil {
			return err
		}
		if isNew {
			return domain.NewError(domain.CodeInvalidValue, "replay must not create a new idempotency record")
		}
		if record.OperationID() != operation.ID() {
			return domain.NewErrorf(domain.CodeInvalidValue, "replay returned operation %q, want %q", record.OperationID(), operation.ID())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("same-key replay: %v", err)
	}
	assertRowCount(t, store.DB(), &journalOperationModel{}, 1)
	assertRowCount(t, store.DB(), &accountPostingModel{}, 1)

	// A different fingerprint for the same key is a payload mismatch.
	_, _, err = repos.Idempotency.Begin(ctx, a.householdID, operation.IdempotencyKey(), domain.RequestFingerprint("d1ff"), operation.ID(), now)
	wantCode(t, err, domain.CodeIdempotencyKeyPayloadMismatch)

	// A failure after Begin and Append rolls back every write of this command.
	failing := newIncome("rollback-key", 900)
	sentinel := domain.NewError(domain.CodeAccountingInvariantViolation, "intentional command failure")
	err = store.WithinTransaction(ctx, func(ctx context.Context, txRepos ports.Repositories) error {
		if _, isNew, err := txRepos.Idempotency.Begin(ctx, a.householdID, failing.IdempotencyKey(), failing.RequestFingerprint(), failing.ID(), now); err != nil {
			return err
		} else if !isNew {
			return domain.NewError(domain.CodeInvalidValue, "rollback command key must be new")
		}
		if err := txRepos.Journal.Append(ctx, a.householdID, failing); err != nil {
			return err
		}
		return sentinel
	})
	if !domain.HasCode(err, domain.CodeAccountingInvariantViolation) {
		t.Fatalf("expected sentinel rollback, got %v", err)
	}
	_, err = repos.Journal.Get(ctx, a.householdID, failing.ID())
	wantCode(t, err, domain.CodeEntityNotFoundOrNotInHousehold)
	_, err = repos.Idempotency.Lookup(ctx, a.householdID, failing.IdempotencyKey())
	wantCode(t, err, domain.CodeEntityNotFoundOrNotInHousehold)
	assertRowCount(t, store.DB(), &journalOperationModel{}, 1)
	assertRowCount(t, store.DB(), &accountPostingModel{}, 1)
	assertRowCount(t, store.DB(), &idempotencyRecordModel{}, 1)
	version, err := repos.Journal.Version(ctx, a.householdID)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != 1 {
		t.Fatalf("ledger version = %d, want 1 after rollback", version)
	}
}

// TestReservationLifecycleAtDatabaseLevel drives create/resize/cancel/complete
// hold transitions through the journal and repository ports, asserting that the
// derived Qe/Qa reads match a reference domain LedgerState at every step.
func TestReservationLifecycleAtDatabaseLevel(t *testing.T) {
	store := testStore(t)
	repos := store.Repositories()
	a := seedHousehold(t, repos, "holds")
	ctx := t.Context()

	date, err := domain.ParseEffectiveDate("2026-10-01")
	if err != nil {
		t.Fatalf("effective date: %v", err)
	}

	meta, err := domain.NewAccountMeta(a.accountID, domain.AccountKindChecking, true)
	if err != nil {
		t.Fatalf("account meta: %v", err)
	}
	state, err := domain.NewLedgerState(a.currency, []domain.AccountMeta{meta}, []domain.EnvelopeId{a.envelopeID})
	if err != nil {
		t.Fatalf("ledger state: %v", err)
	}

	planID := domain.PlanId(mustID(t))
	target := domain.ReservationTarget{PlanID: planID, EnvelopeID: a.envelopeID, AccountID: a.accountID}
	plan, err := domain.NewPlannedOperation(planID, domain.PlanKindPurchase, target, testPositiveAmount(t, 20_000, a.currency))
	if err != nil {
		t.Fatalf("new plan: %v", err)
	}
	if err := repos.Plans.Create(ctx, a.householdID, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	sequence := 0
	appendStep := func(opType domain.OperationType, amount int64, effects domain.Effects) {
		t.Helper()
		appendProjectionOperation(t, store, a, sequence, opType, date, amountPointer(t, amount, a.currency), effects)
		sequence++
		if err := state.ApplyPostings(effects.Postings); err != nil {
			t.Fatalf("apply postings to reference state: %v", err)
		}
		if err := state.ApplyReservationChanges(effects.Reservations...); err != nil {
			t.Fatalf("apply reservations to reference state: %v", err)
		}
		if err := state.CheckInvariants(); err != nil {
			t.Fatalf("reference state invariants: %v", err)
		}
		assertReservationHolds(t, repos, state, a, planID)
	}

	funding := mustEffects(t)(domain.IncomeReceivedEffects(a.accountID, testPositiveAmount(t, 20_000, a.currency)))
	appendStep(domain.OpIncome, 20_000, funding)

	allocation := mustEffects(t)(domain.AllocateFundsEffects(a.envelopeID, testPositiveAmount(t, 20_000, a.currency)))
	appendStep(domain.OpAllocate, 20_000, allocation)

	create := mustEffects(t)(domain.CreateReservationEffects(target, testPositiveAmount(t, 6_000, a.currency)))
	appendStep(domain.OpReservationCreate, 6_000, create)

	increase := mustEffects(t)(domain.ResizeReservationEffects(target, testSignedDelta(t, 2_000, a.currency)))
	appendStep(domain.OpReservationResize, 2_000, increase)

	decrease := mustEffects(t)(domain.ResizeReservationEffects(target, testSignedDelta(t, -1_000, a.currency)))
	appendStep(domain.OpReservationResize, 1_000, decrease)

	cancel := mustEffects(t)(domain.CancelReservationEffects(
		target,
		testPositiveAmount(t, 7_000, a.currency),
		testPositiveAmount(t, 7_000, a.currency),
	))
	appendStep(domain.OpReservationCancel, 7_000, cancel)

	recreate := mustEffects(t)(domain.CreateReservationEffects(target, testPositiveAmount(t, 4_000, a.currency)))
	appendStep(domain.OpReservationCreate, 4_000, recreate)

	purchase := mustEffects(t)(domain.DebitPurchaseEffects(a.accountID, a.envelopeID, testPositiveAmount(t, 4_000, a.currency)))
	complete := mustEffects(t)(domain.CompleteReservedPurchaseEffects(
		target,
		purchase,
		testPositiveAmount(t, 4_000, a.currency),
		testPositiveAmount(t, 4_000, a.currency),
	))
	appendStep(domain.OpPurchaseComplete, 4_000, complete)

	if err := store.VerifyProjections(ctx, a.householdID); err != nil {
		t.Fatalf("projections after reservation lifecycle: %v", err)
	}
}

// TestMigrationSetAppliesIdempotentlyUnderVersionTracking documents and checks
// the upgrade path for the reviewed migration set. The Goose binary is not a Go
// dependency here, so this test reproduces Goose's applied-version tracking with
// a goose_db_version table: the ordered migration set is applied to an empty
// schema, a second run applies zero versions, returns no error and leaves the
// schema unchanged.
func TestMigrationSetAppliesIdempotentlyUnderVersionTracking(t *testing.T) {
	store := testStoreWithoutMigration(t)
	db := store.DB()

	want := migrationFiles(t)
	applied := applyTrackedMigrationSet(t, db)
	if !reflect.DeepEqual(applied, want) {
		t.Fatalf("first migration run applied %v, want %v", applied, want)
	}
	before := schemaTables(t, db)

	applied = applyTrackedMigrationSet(t, db)
	if len(applied) != 0 {
		t.Fatalf("second migration run applied %v, want none", applied)
	}
	after := schemaTables(t, db)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("second migration run changed the schema:\nbefore %v\nafter  %v", before, after)
	}
}

// assertReservationHolds compares the repository's derived Qe/Qa reads with a
// reference LedgerState.
func assertReservationHolds(
	t *testing.T,
	repos ports.Repositories,
	state *domain.LedgerState,
	s seed,
	planID domain.PlanId,
) {
	t.Helper()

	wantEnvelope, err := state.ActiveEnvelopeReservations(s.envelopeID)
	if err != nil {
		t.Fatalf("reference Qe: %v", err)
	}
	wantAccount, err := state.ActiveAccountReservations(s.accountID)
	if err != nil {
		t.Fatalf("reference Qa: %v", err)
	}

	gotEnvelope, err := repos.Reservations.ActiveEnvelopeHolds(t.Context(), s.householdID, s.envelopeID)
	if err != nil {
		t.Fatalf("active envelope holds: %v", err)
	}
	if gotEnvelope.MinorUnits() != wantEnvelope.MinorUnits() {
		t.Fatalf("Qe = %d, want %d", gotEnvelope.MinorUnits(), wantEnvelope.MinorUnits())
	}
	gotAccount, err := repos.Reservations.ActiveAccountHolds(t.Context(), s.householdID, s.accountID)
	if err != nil {
		t.Fatalf("active account holds: %v", err)
	}
	if gotAccount.MinorUnits() != wantAccount.MinorUnits() {
		t.Fatalf("Qa = %d, want %d", gotAccount.MinorUnits(), wantAccount.MinorUnits())
	}
	planEnvelope, planAccount, err := repos.Reservations.HoldsForPlan(t.Context(), s.householdID, planID)
	if err != nil {
		t.Fatalf("holds for plan: %v", err)
	}
	if planEnvelope.MinorUnits() != wantEnvelope.MinorUnits() || planAccount.MinorUnits() != wantAccount.MinorUnits() {
		t.Fatalf("plan holds = %d/%d, want %d/%d",
			planEnvelope.MinorUnits(), planAccount.MinorUnits(), wantEnvelope.MinorUnits(), wantAccount.MinorUnits())
	}
}

// sqlStateRestrictViolation is PostgreSQL's restrict_violation code, raised by
// ON DELETE RESTRICT (as opposed to 23503 for a NO ACTION foreign key).
const sqlStateRestrictViolation = "23001"

// wantSQLState asserts err is a PostgreSQL error carrying the wanted SQLSTATE.
func wantSQLState(t *testing.T, err error, state string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected SQLSTATE %s, got nil", state)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected *pgconn.PgError, got %T: %v", err, err)
	}
	if pgErr.Code != state {
		t.Fatalf("SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.Message, state)
	}
}

// assertRowCount asserts a table holds the wanted number of rows.
func assertRowCount(t *testing.T, db *gorm.DB, model any, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(model).Count(&count).Error; err != nil {
		t.Fatalf("count %T: %v", model, err)
	}
	if count != want {
		t.Fatalf("%T row count = %d, want %d", model, count, want)
	}
}

// Raw-SQL seeding helpers. They bypass the adapter deliberately: constraint
// tests must observe the database as the authority.

const rawJournalInsertSQL = `INSERT INTO journal_operations
	(id, household_id, actor_id, op_type, source, effective_date, currency_code, currency_exponent,
	 amount_minor, idempotency_key, request_fingerprint, created_at, committed_at)
	VALUES (?, ?, ?, ?, 'MANUAL', DATE '2026-10-01', 'USD', 2, ?, ?, 'raw-fingerprint', ?, ?)`

func rawInsertJournal(
	t *testing.T,
	db *gorm.DB,
	s seed,
	id string,
	opType string,
	key string,
	amount *int64,
	created time.Time,
	committed time.Time,
) error {
	t.Helper()
	return db.Exec(
		rawJournalInsertSQL,
		mustUUID(t, id), mustUUID(t, s.householdID.String()), mustUUID(t, s.userID.String()),
		opType, amount, key, created, committed,
	).Error
}

// rawInsertValidJournal seeds one valid operation and returns its id.
func rawInsertValidJournal(t *testing.T, db *gorm.DB, s seed) domain.JournalOperationId {
	t.Helper()
	id := mustID(t)
	now := journalTime()
	if err := rawInsertJournal(t, db, s, id, "INCOME", "valid-"+id, nil, now, now); err != nil {
		t.Fatalf("seed valid journal operation: %v", err)
	}
	return domain.JournalOperationId(id)
}

func rawInsertAccountPosting(db *gorm.DB, household domain.HouseholdId, opID domain.JournalOperationId, account domain.AccountId, delta int64) error {
	return db.Exec(
		`INSERT INTO account_postings (household_id, journal_operation_id, account_id, delta_minor) VALUES (?, ?, ?, ?)`,
		mustUUIDNoT(household.String()), mustUUIDNoT(opID.String()), mustUUIDNoT(account.String()), delta,
	).Error
}

func rawInsertEnvelopePosting(db *gorm.DB, household domain.HouseholdId, opID domain.JournalOperationId, envelope domain.EnvelopeId, delta int64) error {
	return db.Exec(
		`INSERT INTO envelope_postings (household_id, journal_operation_id, envelope_id, delta_minor) VALUES (?, ?, ?, ?)`,
		mustUUIDNoT(household.String()), mustUUIDNoT(opID.String()), mustUUIDNoT(envelope.String()), delta,
	).Error
}

func rawInsertPlan(db *gorm.DB, s seed, id string, kind string, state string, intended int64) error {
	return db.Exec(
		`INSERT INTO plans (id, household_id, kind, state, target_envelope_id, intended_amount_minor)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		mustUUIDNoT(id), mustUUIDNoT(s.householdID.String()), kind, state, mustUUIDNoT(s.envelopeID.String()), intended,
	).Error
}

func rawInsertSuggestion(db *gorm.DB, s seed, id string, source string, state string, amount int64) error {
	return db.Exec(
		`INSERT INTO suggestions (id, household_id, source, amount_minor, merchant, state)
		 VALUES (?, ?, ?, ?, 'Merchant', ?)`,
		mustUUIDNoT(id), mustUUIDNoT(s.householdID.String()), source, amount, state,
	).Error
}

// Migration helpers.

// applyTrackedMigrationSet mirrors Goose's version tracking: it records applied
// versions in goose_db_version and executes only versions not yet applied.
func applyTrackedMigrationSet(t *testing.T, db *gorm.DB) []string {
	t.Helper()

	if err := db.Exec(`CREATE TABLE IF NOT EXISTS goose_db_version (
		id serial PRIMARY KEY,
		version_id bigint NOT NULL,
		is_applied boolean NOT NULL,
		tstamp timestamptz NOT NULL DEFAULT now()
	)`).Error; err != nil {
		t.Fatalf("create migration version table: %v", err)
	}

	var applied []string
	for _, name := range migrationFiles(t) {
		version := migrationVersion(t, name)
		var count int64
		if err := db.Raw(
			`SELECT count(*) FROM goose_db_version WHERE version_id = ? AND is_applied`,
			version,
		).Scan(&count).Error; err != nil {
			t.Fatalf("read migration version %d: %v", version, err)
		}
		if count > 0 {
			continue
		}
		executeMigrationUp(t, db, readMigrationUp(t, name))
		if err := db.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, true)`,
			version,
		).Error; err != nil {
			t.Fatalf("record migration version %d: %v", version, err)
		}
		applied = append(applied, name)
	}
	return applied
}

// migrationFiles returns the migration filenames in version order.
func migrationFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(migrationDirectory())
	if err != nil {
		t.Fatalf("read migrations directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no migration files found")
	}
	return names
}

// migrationVersion parses the numeric Goose version prefix of a filename.
func migrationVersion(t *testing.T, name string) int64 {
	t.Helper()
	prefix, _, found := strings.Cut(name, "_")
	if !found {
		t.Fatalf("migration %q has no version prefix", name)
	}
	version, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil {
		t.Fatalf("parse migration version %q: %v", prefix, err)
	}
	return version
}

// schemaTables lists the tables in the test schema.
func schemaTables(t *testing.T, db *gorm.DB) []string {
	t.Helper()

	type tableRow struct {
		Tablename string `gorm:"column:tablename"`
	}
	var rows []tableRow
	if err := db.Raw(
		`SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY tablename`,
	).Scan(&rows).Error; err != nil {
		t.Fatalf("list schema tables: %v", err)
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Tablename)
	}
	return names
}
