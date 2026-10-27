// This file holds the generator factories for the planned-operation lifecycle,
// suggestions, corrections, reconciliation, and the zero-effect matrix rows.
package domain

import "fmt"

// planTarget builds a reservation target whose shape is consistent with the
// plan kind: a repayment always holds a payment envelope plus a cash account, a
// purchase holds a spending envelope and optionally a cash account (a card
// purchase holds only the envelope, per Q1).
func (g *petGenerator) planTarget(kind PlanKind) (ReservationTarget, bool) {
	if kind == PlanKindRepayment {
		return ReservationTarget{
			EnvelopeID: g.f.pay,
			AccountID:  g.pickEligibleAccount(),
		}, true
	}
	envelope := g.pickSpendingEnvelope()
	if g.rng.IntN(2) == 0 {
		return ReservationTarget{EnvelopeID: envelope}, true
	}
	account := g.pickEligibleAccount()
	return ReservationTarget{EnvelopeID: envelope, AccountID: account}, true
}

func (g *petGenerator) factoryCreatePlan() petOp {
	id := g.pickUnusedPlanID()
	kind := PlanKindPurchase
	if g.rng.IntN(4) == 0 {
		kind = PlanKindRepayment
	}
	target, _ := g.planTarget(kind)
	return g.createPlanOp(id, kind, target, g.amount(1, 4_000))
}

func (g *petGenerator) factoryEditPlan() petOp {
	plan := g.pickPlan(PlanStateDraft)
	if plan == nil {
		return g.factoryCreatePlan()
	}
	target := plan.Target()
	// Keep the target shape but shuffle the envelope/account occasionally so the
	// edit is a real target change, not only an amount change.
	if g.rng.IntN(2) == 0 {
		if target.AccountID.IsZero() {
			target.EnvelopeID = g.pickSpendingEnvelope()
		} else {
			target.EnvelopeID = g.pickEnvelope()
			target.AccountID = g.pickEligibleAccount()
		}
	}
	return g.editPlanOp(plan, g.amount(1, 4_000), target)
}

func (g *petGenerator) factoryAuthorizePlan() petOp {
	plan := g.pickPlan(PlanStateDraft)
	if plan == nil {
		return g.factoryCreatePlan()
	}
	mode := AuthorizationModeNormal
	reason := ""
	if g.rng.IntN(3) == 0 {
		mode = AuthorizationModeEmergencyOverride
		reason = "property override"
	}
	env := g.nextEnv(reason)
	return petOp{
		row:   petRowReservationCreate,
		label: fmt.Sprintf("AuthorizePurchasePlan(%s, %s)", plan.ID(), mode),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			_, op, err := g.f.service.AuthorizePurchasePlan(e, plan.ID(), mode)
			return op, err
		},
	}
}

func (g *petGenerator) factoryAuthorizeWithReallocation() petOp {
	plan := g.pickPlan(PlanStateDraft)
	if plan == nil {
		return g.factoryCreatePlan()
	}
	source := g.pickEnvelopeExcept(plan.Target().EnvelopeID)
	delta := g.amount(1, 2_000)
	env := g.nextEnv("")
	return petOp{
		row:   petRowReservationCreate,
		label: fmt.Sprintf("AuthorizePurchasePlanWithReallocation(%s, %s, %d)", plan.ID(), source, delta.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			_, op, err := g.f.service.AuthorizePurchasePlanWithReallocation(e, plan.ID(), source, delta, AuthorizationModeNormal)
			return op, err
		},
	}
}

func (g *petGenerator) factoryResizePlan(increase bool) petOp {
	plan := g.pickPlan(PlanStateReserved, PlanStateReservedException)
	if plan == nil {
		return g.factoryAuthorizePlan()
	}
	held := plan.HeldAmount().MinorUnits()
	if held < 1 {
		return g.factoryAuthorizePlan()
	}
	var next int64
	row := petRowResizeDecrease
	if increase {
		next = held + g.minor(1, 1_500)
		row = petRowResizeIncrease
	} else {
		if held == 1 {
			return g.factoryAuthorizePlan()
		}
		next = g.minor(1, held-1)
	}
	mode := AuthorizationModeNormal
	reason := ""
	if g.rng.IntN(3) == 0 {
		mode = AuthorizationModeEmergencyOverride
		reason = "property resize override"
	}
	amount := mustPositiveAmountForTest(g.t, next, g.f.currency)
	env := g.nextEnv(reason)
	return petOp{
		row:   row,
		label: fmt.Sprintf("ResizePurchasePlan(%s, %d, %s)", plan.ID(), next, mode),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			_, op, err := g.f.service.ResizePurchasePlan(e, plan.ID(), amount, mode)
			return op, err
		},
	}
}

func (g *petGenerator) factoryCancelPlan() petOp {
	plan := g.pickPlan(PlanStateDraft, PlanStateReserved, PlanStateReservedException)
	if plan == nil {
		return g.factoryCreatePlan()
	}
	env := g.nextEnv("")
	return petOp{
		row:   petRowReservationCancel,
		label: fmt.Sprintf("CancelPurchasePlan(%s)", plan.ID()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			_, op, err := g.f.service.CancelPurchasePlan(e, plan.ID())
			return op, err
		},
	}
}

func (g *petGenerator) factoryCompletePlan() petOp {
	plan := g.pickPlan(PlanStateReserved, PlanStateReservedException)
	if plan == nil {
		return g.factoryAuthorizePlan()
	}
	final := plan.AuthorizedAmount()
	if final.MinorUnits() > 1 && g.rng.IntN(2) == 0 {
		final = mustPositiveAmountForTest(g.t, g.minor(1, final.MinorUnits()), g.f.currency)
	}
	target := plan.Target()
	kind := PurchaseKindDebit
	card := AccountId("")
	payment := EnvelopeId("")
	if plan.Kind() == PlanKindRepayment {
		kind = PurchaseKindRepayment
		card = g.f.card
		payment = target.EnvelopeID
	} else if target.AccountID.IsZero() {
		kind = PurchaseKindCard
		card = g.f.card
		payment = g.f.pay
	}
	env := g.nextEnv("")
	return petOp{
		row:   petRowReservationComplete,
		label: fmt.Sprintf("CompletePurchasePlan(%s, %d, %s)", plan.ID(), final.MinorUnits(), kind),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			_, op, err := g.f.service.CompletePurchasePlan(e, plan.ID(), final, kind, card, payment)
			return op, err
		},
	}
}

// --- suggestion factories ----------------------------------------------------

func (g *petGenerator) factoryCreateSuggestion() petOp {
	return g.createSuggestionOp(g.pickUnusedSuggestionID(), g.amount(1, 4_000))
}

func (g *petGenerator) factoryEditSuggestion() petOp {
	id, ok := g.pickSuggestion(SuggestionStateSuggested, SuggestionStateEdited)
	if !ok {
		return g.factoryCreateSuggestion()
	}
	return g.editSuggestionOp(id, g.amount(1, 4_000))
}

func (g *petGenerator) factoryCancelSuggestion() petOp {
	id, ok := g.pickSuggestion(SuggestionStateSuggested, SuggestionStateEdited)
	if !ok {
		return g.factoryCreateSuggestion()
	}
	return g.cancelSuggestionOp(id)
}

func (g *petGenerator) factoryConfirmSuggestion() petOp {
	id, ok := g.pickSuggestion(SuggestionStateSuggested, SuggestionStateEdited)
	if !ok {
		return g.factoryCreateSuggestion()
	}
	card := g.rng.IntN(2) == 0
	env := g.nextEnv("")
	// Resolve the posting shape now: replaying the closure must send exactly the
	// same request.
	account := g.f.card
	envelope := g.pickSpendingEnvelope()
	payment := g.f.pay
	kind := PurchaseKindCard
	if !card {
		account = g.pickEligibleAccount()
		envelope = g.pickSpendingEnvelope()
		payment = EnvelopeId("")
		kind = PurchaseKindDebit
	}
	return petOp{
		row:   petRowExternalTransaction,
		label: fmt.Sprintf("ConfirmSuggestionAsExternalTransaction(%s, %s)", id, kind),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			suggestion, op, err := g.f.service.ConfirmSuggestionAsExternalTransaction(
				e, id, kind, account, envelope, payment,
			)
			if err == nil {
				g.suggestionStates[id] = suggestion.State()
			}
			return op, err
		},
	}
}

func (g *petGenerator) factoryConvertSuggestion() petOp {
	id, ok := g.pickSuggestion(SuggestionStateSuggested, SuggestionStateEdited)
	if !ok {
		return g.factoryCreateSuggestion()
	}
	kind := PlanKindPurchase
	if g.rng.IntN(4) == 0 {
		kind = PlanKindRepayment
	}
	target, _ := g.planTarget(kind)
	return petOp{
		row:   petRowDraftSuggestion,
		label: fmt.Sprintf("ConvertSuggestionToPlannedPurchase(%s, %s)", id, kind),
		run: func(_ CommandEnvelope) (JournalOperation, error) {
			suggestion, _, err := g.f.service.ConvertSuggestionToPlannedPurchase(g.f.actor, id, kind, target)
			if err == nil {
				g.suggestionStates[id] = suggestion.State()
			}
			return JournalOperation{}, err
		},
	}
}

// --- correction, reconciliation, and zero-effect factories -------------------

func (g *petGenerator) correctionEffects() Effects {
	account := g.pickEligibleAccount()
	envelope := g.pickEnvelope()
	effects, err := DebitPurchaseEffects(account, envelope, g.amount(1, 3_000))
	if err != nil {
		g.t.Fatalf("DebitPurchaseEffects for correction: %v", err)
	}
	return effects
}

func (g *petGenerator) factoryReversal() petOp {
	if len(g.reversible) == 0 {
		return g.factoryIncomeEligible()
	}
	original := g.reversible[g.rng.IntN(len(g.reversible))]
	env := g.nextEnv("")
	return petOp{
		row:   petRowReversal,
		label: fmt.Sprintf("ReverseOperation(%s, %d)", original.opID, original.amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.ReverseOperation(e, original.opID, g.amountUpTo(original.amount), "property reversal")
		},
	}
}

func (g *petGenerator) factoryReplacement() petOp {
	if len(g.withPostings) == 0 {
		return g.factoryDebitPurchase()
	}
	original := g.withPostings[g.rng.IntN(len(g.withPostings))]
	correction := g.correctionEffects()
	env := g.nextEnv("")
	return petOp{
		row:   petRowReplacementCorrection,
		label: fmt.Sprintf("CorrectTransaction(%s)", original),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.CorrectTransaction(e, original, correction, "property replacement")
		},
	}
}

func (g *petGenerator) factoryAccountCorrection() petOp {
	if len(g.withPostings) == 0 {
		return g.factoryIncomeEligible()
	}
	original := g.withPostings[g.rng.IntN(len(g.withPostings))]
	// Account/source correction uses the same replacement command but changes
	// the cash account of a purchase-shaped posting.
	account := g.pickEligibleAccount()
	envelope := g.pickEnvelope()
	corrected, err := DebitPurchaseEffects(account, envelope, g.amount(1, 3_000))
	if err != nil {
		g.t.Fatalf("DebitPurchaseEffects for account correction: %v", err)
	}
	env := g.nextEnv("")
	return petOp{
		row:   petRowAccountCorrection,
		label: fmt.Sprintf("CorrectTransaction(%s) account", original),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.CorrectTransaction(e, original, corrected, "property account correction")
		},
	}
}

func (g *petGenerator) factoryMetadataCorrection() petOp {
	if len(g.withPostings) == 0 {
		return g.factoryIncomeEligible()
	}
	original := g.withPostings[g.rng.IntN(len(g.withPostings))]
	env := g.nextEnv("")
	return petOp{
		row:   petRowMetadataCorrection,
		label: fmt.Sprintf("CorrectMetadata(%s)", original),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.CorrectMetadata(e, original, "property metadata correction")
		},
	}
}

func (g *petGenerator) factoryCategoryCorrection() petOp {
	if len(g.withPostings) == 0 {
		return g.factoryDebitPurchase()
	}
	original := g.withPostings[g.rng.IntN(len(g.withPostings))]
	source, destination := g.pickTwoEnvelopes()
	amount := g.amount(1, 3_000)
	env := g.nextEnv("")
	return petOp{
		row:   petRowCategoryCorrection,
		label: fmt.Sprintf("CorrectCategory(%s, %s, %s, %d)", original, source, destination, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.CorrectCategory(e, original, source, destination, amount, "property category correction")
		},
	}
}

func (g *petGenerator) factoryReconcile(increase bool) petOp {
	account := g.pickEligibleAccount()
	magnitude := g.minor(1, 2_500)
	deltaMinor := magnitude
	row := petRowReconcileIncrease
	if !increase {
		deltaMinor = -magnitude
		row = petRowReconcileDecrease
	}
	delta := mustSignedDeltaForTest(g.t, deltaMinor, g.f.currency)
	funding := g.reconciliationFunding(deltaMinor)
	env := g.nextEnv("")
	return petOp{
		row:   row,
		label: fmt.Sprintf("ReconcileAccount(%s, %d)", account, deltaMinor),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.ReconcileAccount(e, account, delta, funding, "property reconciliation")
		},
	}
}

// reconciliationFunding splits a signed reconciliation amount between
// Ready-to-assign and (sometimes) one envelope. Every part follows the sign of
// the delta, as ReconciliationAdjustmentEffects requires.
func (g *petGenerator) reconciliationFunding(deltaMinor int64) FundingSource {
	sign := int64(1)
	if deltaMinor < 0 {
		sign = -1
	}
	magnitude := deltaMinor * sign
	fromEnvelope := int64(0)
	var envelopes []EnvelopeFunding
	if magnitude > 1 && g.rng.IntN(2) == 0 {
		fromEnvelope = g.minor(1, magnitude)
		envelopes = append(envelopes, EnvelopeFunding{
			EnvelopeID: g.pickEnvelope(),
			Amount:     g.money(sign * fromEnvelope),
		})
	}
	return FundingSource{
		FromReady:     g.money(sign * (magnitude - fromEnvelope)),
		FromEnvelopes: envelopes,
	}
}

// transferFunding builds a positive FundingSource for an eligible-to-excluded
// transfer: the whole amount leaves Ready-to-assign, with an occasional
// envelope attribution.
func (g *petGenerator) transferFunding(amount PositiveAmount) FundingSource {
	total := amount.MinorUnits()
	if total > 1 && g.rng.IntN(2) == 0 {
		fromEnvelope := g.minor(1, total)
		return FundingSource{
			FromReady: g.money(total - fromEnvelope),
			FromEnvelopes: []EnvelopeFunding{{
				EnvelopeID: g.pickEnvelope(),
				Amount:     g.money(fromEnvelope),
			}},
		}
	}
	return FundingSource{FromReady: g.money(total)}
}

// factoryMonthlyBoundary exercises the documented zero-effect rollover row. It
// deliberately appends nothing; the runner still verifies the ledger did not
// change.
func (g *petGenerator) factoryMonthlyBoundary() petOp {
	return petOp{
		row:   petRowMonthlyBoundary,
		label: "MonthlyBoundaryEffects()",
		run: func(_ CommandEnvelope) (JournalOperation, error) {
			effects := MonthlyBoundaryEffects()
			if !effects.IsEmpty() {
				g.t.Fatalf("MonthlyBoundaryEffects must be empty, got postings=%d reservations=%d", len(effects.Postings.Accounts)+len(effects.Postings.Envelopes), len(effects.Reservations))
			}
			return JournalOperation{}, nil
		},
	}
}
