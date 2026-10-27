// This file implements the randomized operation generator used by the
// property/sequence tests in property_test.go. It draws from the whole command
// catalogue: setup postings, allocations and reallocations, purchases, card
// activity, every transfer shape, refunds, reversals, corrections,
// reconciliation, the full plan lifecycle, and every suggestion lifecycle
// command.
//
// The generator is intentionally "valid-ish" rather than always valid: some
// generated commands are deliberately rejected (overspends, unknown ids,
// funding mismatches, reused idempotency keys with a different fingerprint).
// The property under test is that a rejected command changes nothing, so a
// rejection is a first-class expected outcome, not a generator bug.
//
// Every random decision is made while building the petOp. The run closure only
// executes the command, which is what lets the runner replay it with the same
// key (idempotency) and with a different fingerprint (payload mismatch).
package domain

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// Posting-matrix row labels (POSTING_MATRIX section 2). The guard test in
// property_test.go keeps this generator from silently dropping a matrix row.
const (
	petRowOpeningEligible            = "Opening eligible cash balance"
	petRowOpeningExcluded            = "Opening excluded cash balance"
	petRowOpeningCard                = "Opening card liability"
	petRowIncomeEligible             = "Receive income into eligible account"
	petRowIncomeExcluded             = "Receive income into excluded account"
	petRowAllocate                   = "Allocate to envelope x"
	petRowReallocate                 = "Reallocate x -> y"
	petRowDebitPurchase              = "Debit/cash purchase"
	petRowCardPurchase               = "Credit-card purchase"
	petRowCardPayment                = "Credit-card payment"
	petRowCardFee                    = "Card fee / interest"
	petRowTransferEligible           = "Transfer: eligible cash account to eligible cash account"
	petRowTransferExcludedToEligible = "Transfer: excluded cash to eligible cash"
	petRowTransferEligibleToExcluded = "Transfer: eligible cash to excluded cash"
	petRowTransferExcluded           = "Transfer wholly within excluded accounts"
	petRowCashRefund                 = "Refund to eligible cash from debit/cash purchase"
	petRowCardRefund                 = "Refund to credit card from card purchase"
	petRowReimbursement              = "Unrelated reimbursement received into eligible cash"
	petRowReservationCreate          = "Purchase reservation created"
	petRowResizeIncrease             = "Reservation amount increased"
	petRowResizeDecrease             = "Reservation amount decreased"
	petRowReservationCancel          = "Reservation cancelled"
	petRowReservationComplete        = "Reserved purchase completed"
	petRowExternalTransaction        = "Actual external purchase recorded after the fact"
	petRowReversal                   = "Reversal of committed purchase/event"
	petRowReplacementCorrection      = "Replacement after correction"
	petRowMetadataCorrection         = "Metadata-only correction"
	petRowCategoryCorrection         = "Category correction"
	petRowAccountCorrection          = "Account/source correction"
	petRowReconcileIncrease          = "Reconciliation increase in eligible cash"
	petRowReconcileDecrease          = "Reconciliation decrease in eligible cash"
	petRowMonthlyBoundary            = "Monthly boundary / rollover"
	petRowDraftSuggestion            = "Draft / transaction suggestion created, edited, or cancelled"
)

// petPurchaseMeta describes a command that produced a refundable purchase.
// The runner records the committed operation id and amount after success.
type petPurchaseMeta struct {
	card            bool
	envelope        EnvelopeId
	paymentEnvelope EnvelopeId
	cardAccount     AccountId
}

// petOp is one generated command together with the metadata the runner needs:
// its matrix row, the envelope to execute it with, and an optional purchase
// shape for later refund/reversal commands.
type petOp struct {
	row             string
	label           string
	env             CommandEnvelope
	run             func(CommandEnvelope) (JournalOperation, error)
	purchase        *petPurchaseMeta
	probeSuggestion bool
}

// petPurchase is a successfully committed purchase together with enough shape
// information to build refunds against it.
type petPurchase struct {
	opID   JournalOperationId
	amount PositiveAmount
	meta   petPurchaseMeta
}

// petReversible is a committed operation that can be reversed: it carries a
// single positive amount and a non-empty posting set.
type petReversible struct {
	opID   JournalOperationId
	amount PositiveAmount
}

// petGenerator holds the random source, the fixture, the id pools, and the
// shadow state the generator needs to steer toward valid commands. The shadow
// tracks only committed operations and suggestion states; it never mirrors the
// ledger.
type petGenerator struct {
	t    *testing.T
	f    *commandFixture
	rng  *rand.Rand
	seed uint64

	envSeq int

	planIDs       []PlanId
	suggestionIDs []SuggestionId

	// suggestionStates is the generator's shadow of the suggestion state
	// machine. The service exposes no suggestion read accessor, so the only way
	// to know whether a command is likely to succeed is to remember the state
	// returned by each successful command. Rejected commands never update it.
	suggestionStates map[SuggestionId]SuggestionState

	// journaledOps are successfully committed commands that produced a journal
	// operation. They let the generator retry a real payload with a reused key.
	journaledOps []petOp

	debitPurchases []petPurchase
	cardPurchases  []petPurchase
	reversible     []petReversible
	withPostings   []JournalOperationId

	// observed counts proposals per matrix row. It is logged, not asserted: the
	// generator is random, so a short sequence is not required to hit every row.
	observed map[string]int

	// probes namespaces the fresh keys the runner uses to retry a rejected
	// command.
	probes int
}

// petFactory declares one matrix row family and the generator method that can
// emit it. The guard test in property_test.go asserts this table covers every
// POSTING_MATRIX row.
type petFactory struct {
	row  string
	make func(*petGenerator) petOp
}

// newPetGenerator builds an independent fixture and generator for one seed.
func newPetGenerator(t *testing.T, seed uint64) *petGenerator {
	t.Helper()
	f := newCommandFixture(t)
	g := &petGenerator{
		t:                t,
		f:                f,
		rng:              rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		seed:             seed,
		suggestionStates: make(map[SuggestionId]SuggestionState),
		observed:         make(map[string]int),
	}
	for i := 0; i < 32; i++ {
		g.planIDs = append(g.planIDs, mustPlanIDForTest(t, fmt.Sprintf("prop-plan-%02d", i)))
	}
	for i := 0; i < 24; i++ {
		g.suggestionIDs = append(g.suggestionIDs, mustSuggestionIDForTest(t, fmt.Sprintf("prop-suggestion-%02d", i)))
	}
	return g
}

// next proposes one command. Roughly one in eight proposals is deliberately
// invalid; the rest are drawn uniformly from the matrix row factories.
func (g *petGenerator) next() petOp {
	if g.rng.IntN(8) == 0 {
		return g.invalidOp()
	}
	return petOpFactories[g.rng.IntN(len(petOpFactories))].make(g)
}

// nextEnv builds a fresh unique audit envelope. Every call consumes a new key
// and fingerprint; the returned envelope is what the runner replays.
func (g *petGenerator) nextEnv(reason string) CommandEnvelope {
	g.envSeq++
	return CommandEnvelope{
		Actor:         g.f.actor,
		EffectiveDate: g.f.date,
		CommittedAt:   g.f.committed,
		Key:           mustIdempotencyKeyForTest(g.t, fmt.Sprintf("prop-key-%d", g.envSeq)),
		Fingerprint:   mustRequestFingerprintForTest(g.t, fmt.Sprintf("%016x", g.envSeq)),
		Reason:        reason,
	}
}

// recordSuccess folds a freshly committed operation into the generator's
// shadow state. Replays never reach here, so each operation is recorded once.
func (g *petGenerator) recordSuccess(op petOp, result JournalOperation) {
	if result.ID().IsZero() {
		return
	}
	g.journaledOps = append(g.journaledOps, op)
	if op.purchase != nil {
		if amount, ok := result.Amount(); ok {
			purchase := petPurchase{opID: result.ID(), amount: amount, meta: *op.purchase}
			if op.purchase.card {
				g.cardPurchases = append(g.cardPurchases, purchase)
			} else {
				g.debitPurchases = append(g.debitPurchases, purchase)
			}
		}
	}
	if amount, ok := petReversibleAmount(result); ok {
		g.reversible = append(g.reversible, petReversible{opID: result.ID(), amount: amount})
	}
	if !result.Postings().IsEmpty() {
		g.withPostings = append(g.withPostings, result.ID())
	}
}

// petReversibleAmount returns the operation amount a reversal can use, or false
// when the operation has no postings or no single posting magnitude (the same
// rules ReverseOperation applies through operationMagnitude).
func petReversibleAmount(op JournalOperation) (PositiveAmount, bool) {
	postings := op.Postings()
	if postings.IsEmpty() {
		return PositiveAmount{}, false
	}
	if amount, ok := op.Amount(); ok {
		return amount, true
	}
	var magnitude int64
	uniform := true
	consider := func(delta SignedMoneyDelta) {
		value := delta.MinorUnits()
		if value < 0 {
			value = -value
		}
		if magnitude == 0 {
			magnitude = value
			return
		}
		if value != magnitude {
			uniform = false
		}
	}
	for _, posting := range postings.Accounts {
		consider(posting.Delta)
	}
	for _, posting := range postings.Envelopes {
		consider(posting.Delta)
	}
	if magnitude == 0 || !uniform {
		return PositiveAmount{}, false
	}
	money, err := NewMoney(magnitude, op.Currency())
	if err != nil {
		return PositiveAmount{}, false
	}
	amount, err := NewPositiveAmount(money)
	if err != nil {
		return PositiveAmount{}, false
	}
	return amount, true
}

// --- random primitive helpers ------------------------------------------------

func (g *petGenerator) minor(min, max int64) int64 {
	return min + g.rng.Int64N(max-min+1)
}

func (g *petGenerator) amount(min, max int64) PositiveAmount {
	return mustPositiveAmountForTest(g.t, g.minor(min, max), g.f.currency)
}

func (g *petGenerator) amountUpTo(max PositiveAmount) PositiveAmount {
	limit := max.MinorUnits()
	if limit > 5_000 {
		limit = 5_000
	}
	if limit < 1 {
		limit = 1
	}
	return g.amount(1, limit)
}

func (g *petGenerator) signed(min, max int64) int64 {
	value := g.minor(min, max)
	if g.rng.IntN(2) == 0 {
		return -value
	}
	return value
}

func (g *petGenerator) delta(min, max int64) SignedMoneyDelta {
	return mustSignedDeltaForTest(g.t, g.signed(min, max), g.f.currency)
}

func (g *petGenerator) money(minorUnits int64) Money {
	return mustMoney(g.t, minorUnits, g.f.currency)
}

func (g *petGenerator) pickEligibleAccount() AccountId {
	if g.rng.IntN(2) == 0 {
		return g.f.checking
	}
	return g.f.savings
}

func (g *petGenerator) pickCashAccount() AccountId {
	ids := []AccountId{g.f.checking, g.f.savings, g.f.excluded, g.f.excluded2}
	return ids[g.rng.IntN(len(ids))]
}

func (g *petGenerator) pickExcludedAccount() AccountId {
	if g.rng.IntN(2) == 0 {
		return g.f.excluded
	}
	return g.f.excluded2
}

func (g *petGenerator) pickEnvelope() EnvelopeId {
	ids := g.f.envelopeIDs()
	return ids[g.rng.IntN(len(ids))]
}

func (g *petGenerator) pickEnvelopeExcept(except EnvelopeId) EnvelopeId {
	ids := g.f.envelopeIDs()
	candidates := make([]EnvelopeId, 0, len(ids))
	for _, id := range ids {
		if id != except {
			candidates = append(candidates, id)
		}
	}
	return candidates[g.rng.IntN(len(candidates))]
}

func (g *petGenerator) pickTwoEnvelopes() (EnvelopeId, EnvelopeId) {
	ids := g.f.envelopeIDs()
	i := g.rng.IntN(len(ids))
	j := g.rng.IntN(len(ids) - 1)
	if j >= i {
		j++
	}
	return ids[i], ids[j]
}

// pickSpendingEnvelope returns a non-payment envelope for purchases. The pay
// envelope is reserved for card-payment flows.
func (g *petGenerator) pickSpendingEnvelope() EnvelopeId {
	if g.rng.IntN(2) == 0 {
		return g.f.spend
	}
	return g.f.fee
}

// --- plan and suggestion selection -------------------------------------------

func (g *petGenerator) pickPlan(states ...PlanState) *PlannedOperation {
	plans := g.f.service.PurchasePlans()
	candidates := make([]*PlannedOperation, 0, len(plans))
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		matches := len(states) == 0
		for _, state := range states {
			if plan.State() == state {
				matches = true
				break
			}
		}
		if matches {
			candidates = append(candidates, plan)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[g.rng.IntN(len(candidates))]
}

func (g *petGenerator) pickUnusedPlanID() PlanId {
	for _, id := range g.planIDs {
		if _, err := g.f.service.GetPurchasePlan(id); err != nil && HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
			return id
		}
	}
	return g.planIDs[g.rng.IntN(len(g.planIDs))]
}

func (g *petGenerator) pickUnusedSuggestionID() SuggestionId {
	for _, id := range g.suggestionIDs {
		if _, exists := g.suggestionStates[id]; !exists {
			return id
		}
	}
	return g.suggestionIDs[g.rng.IntN(len(g.suggestionIDs))]
}

func (g *petGenerator) pickSuggestion(states ...SuggestionState) (SuggestionId, bool) {
	candidates := make([]SuggestionId, 0, len(g.suggestionIDs))
	for _, id := range g.suggestionIDs {
		state, known := g.suggestionStates[id]
		if !known {
			continue
		}
		for _, want := range states {
			if state == want {
				candidates = append(candidates, id)
				break
			}
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	return candidates[g.rng.IntN(len(candidates))], true
}
