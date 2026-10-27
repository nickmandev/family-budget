// This file contains the randomized operation-sequence properties:
//
//   - the fundamental accounting identity A = R + sum(E) holds after every
//     committed command (and every rejection leaves all observable state
//     untouched);
//   - the incrementally maintained ledger equals a fresh journal replay;
//   - reservation holds on the ledger equal the sum of the held amounts of the
//     plans in RESERVED/RESERVED_EXCEPTION;
//   - reusing an idempotency key with the same fingerprint returns the original
//     operation and posts nothing, while reusing it with a different
//     fingerprint is rejected and posts nothing;
//   - retrying a rejected request produces the same rejection and no state
//     change.
//
// The generator lives in property_ops_test.go, property_commands_test.go, and
// property_factories_test.go. Seeds are fixed for reproducibility and logged;
// two additional seeds come from the wall clock.
package domain

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

type petAccountDigest struct {
	Posted   int64
	Reserved int64
}

type petEnvelopeDigest struct {
	Balance  int64
	Reserved int64
}

type petLedgerDigest struct {
	BudgetableCash int64
	ReadyToAssign  int64
	EnvelopeSum    int64
	Accounts       map[string]petAccountDigest
	Envelopes      map[string]petEnvelopeDigest
}

type petStateDigest struct {
	Version    uint64
	Ledger     petLedgerDigest
	Plans      map[string]string
	Deficits   []string
	Resolution ResolutionStatus
}

func petUnits(t *testing.T, value Money, err error) int64 {
	t.Helper()
	if err != nil {
		t.Fatalf("ledger query: %v", err)
	}
	return value.MinorUnits()
}

func petBudgetableCash(t *testing.T, state *LedgerState) int64 {
	t.Helper()
	value, err := state.BudgetableCash()
	return petUnits(t, value, err)
}

func petReadyToAssign(t *testing.T, state *LedgerState) int64 {
	t.Helper()
	value, err := state.ReadyToAssign()
	return petUnits(t, value, err)
}

func petEnvelopeSum(t *testing.T, state *LedgerState) int64 {
	t.Helper()
	value, err := state.SumEnvelopeBalances()
	return petUnits(t, value, err)
}

func petPostedBalance(t *testing.T, state *LedgerState, id AccountId) int64 {
	t.Helper()
	value, err := state.PostedAccountBalance(id)
	return petUnits(t, value, err)
}

func petAccountReserved(t *testing.T, state *LedgerState, id AccountId) int64 {
	t.Helper()
	value, err := state.ActiveAccountReservations(id)
	return petUnits(t, value, err)
}

func petAccountSpendable(t *testing.T, state *LedgerState, id AccountId) int64 {
	t.Helper()
	value, err := state.SpendableAccountBalance(id)
	return petUnits(t, value, err)
}

func petEnvelopeBalance(t *testing.T, state *LedgerState, id EnvelopeId) int64 {
	t.Helper()
	value, err := state.EnvelopeBalance(id)
	return petUnits(t, value, err)
}

func petEnvelopeReserved(t *testing.T, state *LedgerState, id EnvelopeId) int64 {
	t.Helper()
	value, err := state.ActiveEnvelopeReservations(id)
	return petUnits(t, value, err)
}

func petEnvelopeSpendable(t *testing.T, state *LedgerState, id EnvelopeId) int64 {
	t.Helper()
	value, err := state.SpendableEnvelopeBalance(id)
	return petUnits(t, value, err)
}

func petLedgerDigestOf(t *testing.T, state *LedgerState, metas []AccountMeta, envelopes []EnvelopeId) petLedgerDigest {
	t.Helper()
	digest := petLedgerDigest{
		Accounts:  make(map[string]petAccountDigest, len(metas)),
		Envelopes: make(map[string]petEnvelopeDigest, len(envelopes)),
	}
	digest.BudgetableCash = petBudgetableCash(t, state)
	digest.ReadyToAssign = petReadyToAssign(t, state)
	digest.EnvelopeSum = petEnvelopeSum(t, state)
	for _, meta := range metas {
		digest.Accounts[string(meta.ID)] = petAccountDigest{
			Posted:   petPostedBalance(t, state, meta.ID),
			Reserved: petAccountReserved(t, state, meta.ID),
		}
	}
	for _, id := range envelopes {
		digest.Envelopes[string(id)] = petEnvelopeDigest{
			Balance:  petEnvelopeBalance(t, state, id),
			Reserved: petEnvelopeReserved(t, state, id),
		}
	}
	return digest
}

func petPlanDigest(plan *PlannedOperation) string {
	target := plan.Target()
	return fmt.Sprintf(
		"%s|%s|%s|%d|%d|%d|%d|%s|%s",
		plan.Kind(),
		plan.State(),
		plan.Mode(),
		plan.IntendedAmount().MinorUnits(),
		plan.AuthorizedAmount().MinorUnits(),
		plan.HeldAmount().MinorUnits(),
		plan.HeldOnAccount().MinorUnits(),
		target.EnvelopeID,
		target.AccountID,
	)
}

func petSnapshot(t *testing.T, f *commandFixture) petStateDigest {
	t.Helper()
	ledger := f.ledger(t)
	digest := petStateDigest{
		Version: f.service.Version(),
		Ledger:  petLedgerDigestOf(t, ledger, f.accountMetas(t), f.envelopeIDs()),
		Plans:   make(map[string]string),
	}
	for _, plan := range f.service.PurchasePlans() {
		digest.Plans[string(plan.ID())] = petPlanDigest(plan)
	}
	deficits, err := ledger.Deficits()
	if err != nil {
		t.Fatalf("Deficits: %v", err)
	}
	for _, deficit := range deficits {
		digest.Deficits = append(digest.Deficits, fmt.Sprintf("%s|%s|%d", deficit.Kind, deficit.EnvelopeID, deficit.Amount.MinorUnits()))
	}
	status, err := ledger.ResolutionStatus()
	if err != nil {
		t.Fatalf("ResolutionStatus: %v", err)
	}
	digest.Resolution = status
	return digest
}

// petCheckLedger re-derives every visible quantity and cross-checks it against
// the projection. It is called after every successful command.
func petCheckLedger(t *testing.T, f *commandFixture) {
	t.Helper()
	state := f.ledger(t)
	if err := state.CheckInvariants(); err != nil {
		t.Fatalf("CheckInvariants: %v", err)
	}
	budgetable := petBudgetableCash(t, state)
	ready := petReadyToAssign(t, state)
	envelopeSum := petEnvelopeSum(t, state)
	if budgetable != ready+envelopeSum {
		t.Fatalf("budget identity: budgetable %d != ready %d + envelopes %d", budgetable, ready, envelopeSum)
	}
	if err := VerifyBudgetIdentity(
		mustMoney(t, budgetable, f.currency),
		mustMoney(t, ready, f.currency),
		mustMoney(t, envelopeSum, f.currency),
	); err != nil {
		t.Fatalf("VerifyBudgetIdentity: %v", err)
	}

	metas := f.accountMetas(t)
	for _, meta := range metas {
		posted := petPostedBalance(t, state, meta.ID)
		reserved := petAccountReserved(t, state, meta.ID)
		spendable := petAccountSpendable(t, state, meta.ID)
		if spendable != posted-reserved {
			t.Fatalf("account %q spendable %d != posted %d - reserved %d", meta.ID, spendable, posted, reserved)
		}
	}
	for _, id := range f.envelopeIDs() {
		balance := petEnvelopeBalance(t, state, id)
		reserved := petEnvelopeReserved(t, state, id)
		spendable := petEnvelopeSpendable(t, state, id)
		if spendable != balance-reserved {
			t.Fatalf("envelope %q spendable %d != balance %d - reserved %d", id, spendable, balance, reserved)
		}
	}

	// The ledger's holds must mirror the plans' held amounts exactly.
	expectedEnvelope := make(map[EnvelopeId]int64)
	expectedAccount := make(map[AccountId]int64)
	for _, plan := range f.service.PurchasePlans() {
		switch plan.State() {
		case PlanStateReserved, PlanStateReservedException:
		default:
			continue
		}
		target := plan.Target()
		expectedEnvelope[target.EnvelopeID] += plan.HeldAmount().MinorUnits()
		if !target.AccountID.IsZero() {
			expectedAccount[target.AccountID] += plan.HeldOnAccount().MinorUnits()
		}
	}
	for _, meta := range metas {
		reserved := petAccountReserved(t, state, meta.ID)
		if reserved != expectedAccount[meta.ID] {
			t.Fatalf("account %q reservations %d != plans' holds %d", meta.ID, reserved, expectedAccount[meta.ID])
		}
	}
	for _, id := range f.envelopeIDs() {
		reserved := petEnvelopeReserved(t, state, id)
		if reserved != expectedEnvelope[id] {
			t.Fatalf("envelope %q reservations %d != plans' holds %d", id, reserved, expectedEnvelope[id])
		}
	}

	deficits, err := state.Deficits()
	if err != nil {
		t.Fatalf("Deficits: %v", err)
	}
	status, err := state.ResolutionStatus()
	if err != nil {
		t.Fatalf("ResolutionStatus: %v", err)
	}
	if len(deficits) > 0 && status != ResolutionRequiresResolution {
		t.Fatalf("deficits present but resolution status is %s", status)
	}
	if len(deficits) == 0 && status != ResolutionClear {
		t.Fatalf("no deficits but resolution status is %s", status)
	}
}

// petCheckRebuild replays the whole journal from empty and compares the
// resulting projection with the incrementally maintained ledger.
func petCheckRebuild(t *testing.T, g *petGenerator) {
	t.Helper()
	rebuilt, err := g.f.service.journal.RebuildLedger(g.f.accountMetas(t), g.f.envelopeIDs())
	if err != nil {
		t.Fatalf("RebuildLedger: %v", err)
	}
	current := g.f.ledger(t)
	want := petLedgerDigestOf(t, current, g.f.accountMetas(t), g.f.envelopeIDs())
	got := petLedgerDigestOf(t, rebuilt, g.f.accountMetas(t), g.f.envelopeIDs())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt ledger differs from incremental ledger\nrebuilt: %+v\ncurrent: %+v", got, want)
	}
	if err := rebuilt.CheckInvariants(); err != nil {
		t.Fatalf("rebuilt ledger invariants: %v", err)
	}
}

// petBootstrap seeds a sequence with enough cash and envelope funding that most
// generated commands are accepted. It commits seven operations.
func petBootstrap(t *testing.T, g *petGenerator) {
	t.Helper()
	money := func(minorUnits int64) SignedMoneyDelta {
		return mustSignedDeltaForTest(t, minorUnits, g.f.currency)
	}
	ops := []petOp{
		g.openingBalanceOp(g.f.checking, money(100_000), petRowOpeningEligible),
		g.openingBalanceOp(g.f.savings, money(50_000), petRowOpeningEligible),
		g.openingBalanceOp(g.f.excluded, money(50_000), petRowOpeningExcluded),
		g.openingBalanceOp(g.f.card, money(10_000), petRowOpeningCard),
		g.allocateOp(g.f.spend, g.amount(20_000, 20_000)),
		g.allocateOp(g.f.pay, g.amount(20_000, 20_000)),
		g.allocateOp(g.f.fee, g.amount(20_000, 20_000)),
	}
	for _, op := range ops {
		result, err := op.run(op.env)
		if err != nil {
			t.Fatalf("bootstrap %s: %v", op.label, err)
		}
		g.recordSuccess(op, result)
	}
	if got := g.f.service.Version(); got != uint64(len(ops)) {
		t.Fatalf("bootstrap committed %d operations, want %d", got, len(ops))
	}
	petCheckLedger(t, g.f)
	petCheckRebuild(t, g)
}

// petStep executes one proposal and asserts every property that applies.
func petStep(t *testing.T, g *petGenerator, op petOp, index int) bool {
	t.Helper()
	g.observed[op.row]++
	before := petSnapshot(t, g.f)

	var result JournalOperation
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("step %d (%s): panic: %v", index, op.label, recovered)
			}
		}()
		result, err = op.run(op.env)
	}()
	after := petSnapshot(t, g.f)

	if err != nil {
		// A rejection must leave every observable dimension untouched.
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("step %d (%s): rejected with %v but state changed\nbefore: %+v\nafter: %+v", index, op.label, err, before, after)
		}
		petCheckRejectedRetry(t, g, op, err)
		return false
	}

	appended := !result.ID().IsZero()
	if appended {
		if after.Version != before.Version+1 {
			t.Fatalf("step %d (%s): version %d -> %d, want exactly one append", index, op.label, before.Version, after.Version)
		}
	} else {
		// Draft plan/suggestion commands and no-op cancellations commit no
		// journal operation: the ledger and version must not move.
		if after.Version != before.Version {
			t.Fatalf("step %d (%s): no operation but version %d -> %d", index, op.label, before.Version, after.Version)
		}
		if !reflect.DeepEqual(before.Ledger, after.Ledger) {
			t.Fatalf("step %d (%s): no operation but ledger changed\nbefore: %+v\nafter: %+v", index, op.label, before.Ledger, after.Ledger)
		}
		petCheckLedger(t, g.f)
	}

	petCheckLedger(t, g.f)
	// Replaying the whole journal is O(n), so do it periodically and always at
	// the end of the sequence.
	if after.Version%17 == 0 {
		petCheckRebuild(t, g)
	}

	if appended && !op.env.Key.IsZero() {
		replayed, replayErr := op.run(op.env)
		if replayErr != nil {
			t.Fatalf("step %d (%s): replay failed: %v", index, op.label, replayErr)
		}
		if replayed.ID() != result.ID() {
			t.Fatalf("step %d (%s): replay returned %q, want %q", index, op.label, replayed.ID(), result.ID())
		}
		replaySnapshot := petSnapshot(t, g.f)
		if !reflect.DeepEqual(after, replaySnapshot) {
			t.Fatalf("step %d (%s): replay changed state\nbefore: %+v\nafter: %+v", index, op.label, after, replaySnapshot)
		}

		conflict := op
		conflict.env.Fingerprint = mustRequestFingerprintForTest(
			t, fmt.Sprintf("%016x", 0xf00d0000+uint64(g.probes)+1),
		)
		g.probes++
		if _, conflictErr := conflict.run(conflict.env); !HasCode(conflictErr, CodeIdempotencyKeyPayloadMismatch) {
			t.Fatalf("step %d (%s): key reuse with a different fingerprint got %v, want %s", index, op.label, conflictErr, CodeIdempotencyKeyPayloadMismatch)
		}
		conflictSnapshot := petSnapshot(t, g.f)
		if !reflect.DeepEqual(after, conflictSnapshot) {
			t.Fatalf("step %d (%s): fingerprint conflict changed state", index, op.label)
		}
	}

	g.recordSuccess(op, result)
	return appended
}

// petCheckRejectedRetry retries a rejected request with fresh idempotency
// metadata and requires the same rejection code and no state change. It is
// skipped for payload-mismatch failures, whose whole point is that the same key
// cannot be reused.
func petCheckRejectedRetry(t *testing.T, g *petGenerator, op petOp, first error) {
	t.Helper()
	if HasCode(first, CodeIdempotencyKeyPayloadMismatch) {
		return
	}
	retry := op
	if !retry.env.Key.IsZero() {
		g.probes++
		retry.env.Key = mustIdempotencyKeyForTest(t, fmt.Sprintf("prop-retry-key-%d", g.probes))
		retry.env.Fingerprint = mustRequestFingerprintForTest(
			t, fmt.Sprintf("%016x", 0xbeef0000+uint64(g.probes)),
		)
	} else if !op.probeSuggestion {
		return
	}
	before := petSnapshot(t, g.f)
	_, retryErr := retry.run(retry.env)
	if retryErr == nil {
		t.Fatalf("%s: rejected once (%v) but succeeded on retry", op.label, first)
	}
	if CodeOf(retryErr) != CodeOf(first) {
		t.Fatalf("%s: retry error %v (%s), want same code as %v (%s)", op.label, retryErr, CodeOf(retryErr), first, CodeOf(first))
	}
	after := petSnapshot(t, g.f)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%s: rejected retry changed state", op.label)
	}
}

func petRunSequence(t *testing.T, seed uint64, length int) (committed, rejected int) {
	t.Helper()
	t.Logf("property seed=%#016x length=%d", seed, length)
	g := newPetGenerator(t, seed)
	petBootstrap(t, g)
	for i := 0; i < length; i++ {
		if petStep(t, g, g.next(), i) {
			committed++
		} else {
			rejected++
		}
	}
	petCheckRebuild(t, g)
	t.Logf(
		"property seed=%#016x committed=%d rejected=%d journal=%d rows=%d",
		seed, committed, rejected, g.f.service.Version(), len(g.observed),
	)
	for row, count := range g.observed {
		t.Logf("  row %-70s %d", row, count)
	}
	return committed, rejected
}

func TestPropertySequences(t *testing.T) {
	seeds := []uint64{0x5eed0001, 0x5eed0002, 0x5eed0003}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed-%#x", seed), func(t *testing.T) {
			petRunSequence(t, seed, 200)
		})
	}
	for i := 0; i < 2; i++ {
		seed := uint64(time.Now().UnixNano()) + uint64(i)*0x9e3779b97f4a7c15
		t.Run(fmt.Sprintf("time-seed-%d", i), func(t *testing.T) {
			petRunSequence(t, seed, 200)
		})
	}
	t.Run("long-sequence", func(t *testing.T) {
		petRunSequence(t, 0x5eed1000, 1000)
	})
}

// TestPropertyPostingMatrixCoverage guards the generator: every documented
// posting-matrix row must have at least one factory, and the row list must not
// contain duplicates or unknowns.
func TestPropertyPostingMatrixCoverage(t *testing.T) {
	want := make(map[string]bool, len(petMatrixRows))
	for _, row := range petMatrixRows {
		if want[row] {
			t.Fatalf("duplicate matrix row in petMatrixRows: %q", row)
		}
		want[row] = true
	}
	if len(want) != 33 {
		t.Fatalf("petMatrixRows has %d unique rows, want 33", len(want))
	}
	got := make(map[string]bool, len(petOpFactories))
	for _, factory := range petOpFactories {
		if !want[factory.row] {
			t.Fatalf("factory row %q is not a documented matrix row", factory.row)
		}
		got[factory.row] = true
	}
	missing := make([]string, 0)
	for row := range want {
		if !got[row] {
			missing = append(missing, row)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("matrix rows without a generator factory: %v", missing)
	}
}
