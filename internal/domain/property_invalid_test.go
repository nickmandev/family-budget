// This file declares the complete matrix-row factory table and the generator
// for deliberately invalid commands. Invalid proposals exercise the property
// that a rejected command changes no observable state, and that retrying a
// rejected request produces the same rejection.
package domain

import "fmt"

// petOpFactories maps every posting-matrix row to at least one generator
// factory. property_test.go asserts the rows here equal the documented matrix
// rows exactly.
var petOpFactories = []petFactory{
	{petRowOpeningEligible, (*petGenerator).factoryOpeningEligible},
	{petRowOpeningExcluded, (*petGenerator).factoryOpeningExcluded},
	{petRowOpeningCard, (*petGenerator).factoryOpeningCard},
	{petRowIncomeEligible, (*petGenerator).factoryIncomeEligible},
	{petRowIncomeExcluded, (*petGenerator).factoryIncomeExcluded},
	{petRowAllocate, (*petGenerator).factoryAllocate},
	{petRowReallocate, (*petGenerator).factoryReallocate},
	{petRowDebitPurchase, (*petGenerator).factoryDebitPurchase},
	{petRowCardPurchase, (*petGenerator).factoryCardPurchase},
	{petRowCardPayment, (*petGenerator).factoryCardPayment},
	{petRowCardFee, (*petGenerator).factoryCardFee},
	{petRowTransferEligible, (*petGenerator).factoryTransferEligible},
	{petRowTransferExcludedToEligible, (*petGenerator).factoryTransferExcludedToEligible},
	{petRowTransferEligibleToExcluded, (*petGenerator).factoryTransferEligibleToExcluded},
	{petRowTransferExcluded, (*petGenerator).factoryTransferExcluded},
	{petRowCashRefund, (*petGenerator).factoryCashRefund},
	{petRowCardRefund, (*petGenerator).factoryCardRefund},
	{petRowReimbursement, (*petGenerator).factoryReimbursement},
	{petRowReservationCreate, (*petGenerator).factoryAuthorizePlan},
	{petRowReservationCreate, (*petGenerator).factoryAuthorizeWithReallocation},
	{petRowResizeIncrease, func(g *petGenerator) petOp { return g.factoryResizePlan(true) }},
	{petRowResizeDecrease, func(g *petGenerator) petOp { return g.factoryResizePlan(false) }},
	{petRowReservationCancel, (*petGenerator).factoryCancelPlan},
	{petRowReservationComplete, (*petGenerator).factoryCompletePlan},
	{petRowExternalTransaction, (*petGenerator).factoryConfirmSuggestion},
	{petRowReversal, (*petGenerator).factoryReversal},
	{petRowReplacementCorrection, (*petGenerator).factoryReplacement},
	{petRowMetadataCorrection, (*petGenerator).factoryMetadataCorrection},
	{petRowCategoryCorrection, (*petGenerator).factoryCategoryCorrection},
	{petRowAccountCorrection, (*petGenerator).factoryAccountCorrection},
	{petRowReconcileIncrease, func(g *petGenerator) petOp { return g.factoryReconcile(true) }},
	{petRowReconcileDecrease, func(g *petGenerator) petOp { return g.factoryReconcile(false) }},
	{petRowMonthlyBoundary, (*petGenerator).factoryMonthlyBoundary},
	{petRowDraftSuggestion, (*petGenerator).factoryCreatePlan},
	{petRowDraftSuggestion, (*petGenerator).factoryEditPlan},
	{petRowDraftSuggestion, (*petGenerator).factoryCreateSuggestion},
	{petRowDraftSuggestion, (*petGenerator).factoryEditSuggestion},
	{petRowDraftSuggestion, (*petGenerator).factoryCancelSuggestion},
	{petRowDraftSuggestion, (*petGenerator).factoryConvertSuggestion},
}

// petMatrixRows is the documented POSTING_MATRIX section 2 row set. The guard
// test compares it with petOpFactories so a new matrix row cannot be added to
// the generator silently.
var petMatrixRows = []string{
	petRowOpeningEligible,
	petRowOpeningExcluded,
	petRowOpeningCard,
	petRowIncomeEligible,
	petRowIncomeExcluded,
	petRowAllocate,
	petRowReallocate,
	petRowDebitPurchase,
	petRowCardPurchase,
	petRowCardPayment,
	petRowCardFee,
	petRowTransferEligible,
	petRowTransferExcludedToEligible,
	petRowTransferEligibleToExcluded,
	petRowTransferExcluded,
	petRowCashRefund,
	petRowCardRefund,
	petRowReimbursement,
	petRowReservationCreate,
	petRowResizeIncrease,
	petRowResizeDecrease,
	petRowReservationCancel,
	petRowReservationComplete,
	petRowExternalTransaction,
	petRowReversal,
	petRowReplacementCorrection,
	petRowMetadataCorrection,
	petRowCategoryCorrection,
	petRowAccountCorrection,
	petRowReconcileIncrease,
	petRowReconcileDecrease,
	petRowMonthlyBoundary,
	petRowDraftSuggestion,
}

// invalidOp returns a command that is expected to be rejected: an unknown
// entity, a funding mismatch, a malformed envelope, or an idempotency key
// reused with a different fingerprint.
func (g *petGenerator) invalidOp() petOp {
	switch g.rng.IntN(6) {
	case 0:
		return g.invalidGhostAccount()
	case 1:
		return g.invalidGhostEnvelope()
	case 2:
		return g.invalidGhostPlan()
	case 3:
		return g.invalidFundingMismatch()
	case 4:
		return g.invalidKeyMismatch()
	default:
		return g.invalidBlankReason()
	}
}

func (g *petGenerator) invalidGhostAccount() petOp {
	env := g.nextEnv("")
	amount := g.amount(1, 1_000)
	return petOp{
		row:   petRowIncomeEligible,
		label: "RecordIncome(unknown account)",
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.RecordIncome(e, AccountId("acct-ghost"), amount)
		},
	}
}

func (g *petGenerator) invalidGhostEnvelope() petOp {
	env := g.nextEnv("")
	amount := g.amount(1, 1_000)
	return petOp{
		row:   petRowAllocate,
		label: "AllocateFunds(unknown envelope)",
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.AllocateFunds(e, EnvelopeId("env-ghost"), amount)
		},
	}
}

func (g *petGenerator) invalidGhostPlan() petOp {
	env := g.nextEnv("")
	return petOp{
		row:   petRowReservationCreate,
		label: "AuthorizePurchasePlan(unknown plan)",
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			_, op, err := g.f.service.AuthorizePurchasePlan(e, PlanId("plan-ghost"), AuthorizationModeNormal)
			return op, err
		},
	}
}

func (g *petGenerator) invalidFundingMismatch() petOp {
	// The funding plan deliberately totals one minor unit less than the amount.
	amount := g.amount(2, 1_000)
	mismatched := mustPositiveAmountForTest(g.t, amount.MinorUnits()-1, g.f.currency)
	funding := FundingSource{FromReady: g.money(mismatched.MinorUnits())}
	env := g.nextEnv("")
	source := g.pickEligibleAccount()
	destination := g.pickExcludedAccount()
	return petOp{
		row:   petRowTransferEligibleToExcluded,
		label: fmt.Sprintf("TransferEligibleOutToExcluded(funding mismatch, %d)", amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.TransferEligibleOutToExcluded(e, source, destination, amount, funding)
		},
	}
}

func (g *petGenerator) invalidKeyMismatch() petOp {
	if len(g.journaledOps) == 0 {
		return g.invalidGhostAccount()
	}
	original := g.journaledOps[g.rng.IntN(len(g.journaledOps))]
	if original.env.Key.IsZero() {
		return g.invalidGhostAccount()
	}
	mismatch := original
	mismatch.env.Fingerprint = mustRequestFingerprintForTest(
		g.t, fmt.Sprintf("%016x", 0xdead0000+uint64(g.envSeq)),
	)
	mismatch.label = "reused key with a different fingerprint"
	return mismatch
}

func (g *petGenerator) invalidBlankReason() petOp {
	if len(g.withPostings) == 0 {
		return g.invalidGhostAccount()
	}
	original := g.withPostings[g.rng.IntN(len(g.withPostings))]
	env := g.nextEnv("")
	return petOp{
		row:   petRowMetadataCorrection,
		label: fmt.Sprintf("CorrectMetadata(%s, blank reason)", original),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.CorrectMetadata(e, original, "   ")
		},
	}
}
