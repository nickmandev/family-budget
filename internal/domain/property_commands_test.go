// This file holds the generator's command builders. Each builder captures every
// random choice before returning, so the run closure is deterministic and the
// runner can safely replay it for the idempotency properties.
package domain

import "fmt"

func (g *petGenerator) openingBalanceOp(account AccountId, delta SignedMoneyDelta, row string) petOp {
	env := g.nextEnv("")
	return petOp{
		row:   row,
		label: fmt.Sprintf("RecordOpeningBalance(%s, %d)", account, delta.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.RecordOpeningBalance(e, account, delta)
		},
	}
}

func (g *petGenerator) incomeOp(account AccountId, amount PositiveAmount, row string) petOp {
	env := g.nextEnv("")
	return petOp{
		row:   row,
		label: fmt.Sprintf("RecordIncome(%s, %d)", account, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.RecordIncome(e, account, amount)
		},
	}
}

func (g *petGenerator) reimbursementOp(account AccountId, amount PositiveAmount) petOp {
	env := g.nextEnv("")
	return petOp{
		row:   petRowReimbursement,
		label: fmt.Sprintf("RecordReimbursement(%s, %d)", account, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.RecordReimbursement(e, account, amount)
		},
	}
}

func (g *petGenerator) allocateOp(envelope EnvelopeId, amount PositiveAmount) petOp {
	env := g.nextEnv("")
	return petOp{
		row:   petRowAllocate,
		label: fmt.Sprintf("AllocateFunds(%s, %d)", envelope, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.AllocateFunds(e, envelope, amount)
		},
	}
}

func (g *petGenerator) reallocateOp(source, destination EnvelopeId, amount PositiveAmount) petOp {
	env := g.nextEnv("")
	return petOp{
		row:   petRowReallocate,
		label: fmt.Sprintf("ReallocateFunds(%s, %s, %d)", source, destination, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.ReallocateFunds(e, source, destination, amount)
		},
	}
}

func (g *petGenerator) debitPurchaseOp(account AccountId, envelope EnvelopeId, amount PositiveAmount) petOp {
	env := g.nextEnv("")
	return petOp{
		row:   petRowDebitPurchase,
		label: fmt.Sprintf("RecordDebitPurchase(%s, %s, %d)", account, envelope, amount.MinorUnits()),
		env:   env,
		purchase: &petPurchaseMeta{
			envelope: envelope,
		},
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.RecordDebitPurchase(e, account, envelope, amount)
		},
	}
}

func (g *petGenerator) cardPurchaseOp(card AccountId, spendingEnvelope, paymentEnvelope EnvelopeId, amount PositiveAmount) petOp {
	env := g.nextEnv("")
	return petOp{
		row:   petRowCardPurchase,
		label: fmt.Sprintf("RecordCardPurchase(%s, %s, %s, %d)", card, spendingEnvelope, paymentEnvelope, amount.MinorUnits()),
		env:   env,
		purchase: &petPurchaseMeta{
			card:            true,
			envelope:        spendingEnvelope,
			paymentEnvelope: paymentEnvelope,
			cardAccount:     card,
		},
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.RecordCardPurchase(e, card, spendingEnvelope, paymentEnvelope, amount)
		},
	}
}

func (g *petGenerator) createPlanOp(id PlanId, kind PlanKind, target ReservationTarget, amount PositiveAmount) petOp {
	return petOp{
		row:   petRowDraftSuggestion,
		label: fmt.Sprintf("CreatePurchasePlan(%s, %s, %d)", id, kind, amount.MinorUnits()),
		run: func(_ CommandEnvelope) (JournalOperation, error) {
			_, err := g.f.service.CreatePurchasePlan(g.f.actor, id, kind, target, amount)
			return JournalOperation{}, err
		},
	}
}

func (g *petGenerator) editPlanOp(plan *PlannedOperation, amount PositiveAmount, target ReservationTarget) petOp {
	id := plan.ID()
	return petOp{
		row:   petRowDraftSuggestion,
		label: fmt.Sprintf("EditPurchasePlan(%s, %d)", id, amount.MinorUnits()),
		run: func(_ CommandEnvelope) (JournalOperation, error) {
			_, err := g.f.service.EditPurchasePlan(g.f.actor, id, amount, target)
			return JournalOperation{}, err
		},
	}
}

func (g *petGenerator) createSuggestionOp(id SuggestionId, amount PositiveAmount) petOp {
	return petOp{
		row:             petRowDraftSuggestion,
		label:           fmt.Sprintf("CreateSuggestion(%s, %d)", id, amount.MinorUnits()),
		probeSuggestion: true,
		run: func(_ CommandEnvelope) (JournalOperation, error) {
			suggestion, err := g.f.service.CreateSuggestion(g.f.actor, id, TransactionSourceExternalEntry, amount, "property merchant")
			if err == nil {
				g.suggestionStates[id] = suggestion.State()
			}
			return JournalOperation{}, err
		},
	}
}

func (g *petGenerator) editSuggestionOp(id SuggestionId, amount PositiveAmount) petOp {
	return petOp{
		row:             petRowDraftSuggestion,
		label:           fmt.Sprintf("EditSuggestion(%s, %d)", id, amount.MinorUnits()),
		probeSuggestion: true,
		run: func(_ CommandEnvelope) (JournalOperation, error) {
			suggestion, err := g.f.service.EditSuggestion(g.f.actor, id, amount, "property merchant edited")
			if err == nil {
				g.suggestionStates[id] = suggestion.State()
			}
			return JournalOperation{}, err
		},
	}
}

func (g *petGenerator) cancelSuggestionOp(id SuggestionId) petOp {
	return petOp{
		row:             petRowDraftSuggestion,
		label:           fmt.Sprintf("CancelSuggestion(%s)", id),
		probeSuggestion: true,
		run: func(_ CommandEnvelope) (JournalOperation, error) {
			suggestion, err := g.f.service.CancelSuggestion(g.f.actor, id, "property dismissal")
			if err == nil {
				g.suggestionStates[id] = suggestion.State()
			}
			return JournalOperation{}, err
		},
	}
}

// --- financial row factories -------------------------------------------------

func (g *petGenerator) factoryOpeningEligible() petOp {
	return g.openingBalanceOp(g.pickEligibleAccount(), g.delta(1, 5_000), petRowOpeningEligible)
}

func (g *petGenerator) factoryOpeningExcluded() petOp {
	return g.openingBalanceOp(g.pickExcludedAccount(), g.delta(1, 5_000), petRowOpeningExcluded)
}

func (g *petGenerator) factoryOpeningCard() petOp {
	return g.openingBalanceOp(g.f.card, g.delta(1, 5_000), petRowOpeningCard)
}

func (g *petGenerator) factoryIncomeEligible() petOp {
	return g.incomeOp(g.pickEligibleAccount(), g.amount(1, 5_000), petRowIncomeEligible)
}

func (g *petGenerator) factoryIncomeExcluded() petOp {
	return g.incomeOp(g.pickExcludedAccount(), g.amount(1, 5_000), petRowIncomeExcluded)
}

func (g *petGenerator) factoryAllocate() petOp {
	return g.allocateOp(g.pickEnvelope(), g.amount(1, 5_000))
}

func (g *petGenerator) factoryReallocate() petOp {
	source, destination := g.pickTwoEnvelopes()
	return g.reallocateOp(source, destination, g.amount(1, 5_000))
}

func (g *petGenerator) factoryDebitPurchase() petOp {
	return g.debitPurchaseOp(g.pickCashAccount(), g.pickEnvelope(), g.amount(1, 5_000))
}

func (g *petGenerator) factoryCardPurchase() petOp {
	return g.cardPurchaseOp(g.f.card, g.pickSpendingEnvelope(), g.f.pay, g.amount(1, 5_000))
}

func (g *petGenerator) factoryCardPayment() petOp {
	env := g.nextEnv("")
	amount := g.amount(1, 5_000)
	account := g.pickEligibleAccount()
	return petOp{
		row:   petRowCardPayment,
		label: fmt.Sprintf("RecordCardPayment(%s, %d)", account, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.RecordCardPayment(e, account, g.f.card, g.f.pay, amount)
		},
	}
}

func (g *petGenerator) factoryCardFee() petOp {
	env := g.nextEnv("")
	amount := g.amount(1, 5_000)
	return petOp{
		row:   petRowCardFee,
		label: fmt.Sprintf("RecordCardFee(%d)", amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.RecordCardFee(e, g.f.card, g.f.fee, g.f.pay, amount)
		},
	}
}

func (g *petGenerator) factoryTransferEligible() petOp {
	source, destination := g.f.checking, g.f.savings
	if g.rng.IntN(2) == 0 {
		source, destination = destination, source
	}
	env := g.nextEnv("")
	amount := g.amount(1, 5_000)
	return petOp{
		row:   petRowTransferEligible,
		label: fmt.Sprintf("TransferBetweenEligible(%s, %s, %d)", source, destination, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.TransferBetweenEligible(e, source, destination, amount)
		},
	}
}

func (g *petGenerator) factoryTransferExcludedToEligible() petOp {
	env := g.nextEnv("")
	source := g.pickExcludedAccount()
	destination := g.pickEligibleAccount()
	amount := g.amount(1, 5_000)
	return petOp{
		row:   petRowTransferExcludedToEligible,
		label: fmt.Sprintf("TransferExcludedToEligible(%s, %s, %d)", source, destination, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.TransferExcludedToEligible(e, source, destination, amount)
		},
	}
}

func (g *petGenerator) factoryTransferEligibleToExcluded() petOp {
	env := g.nextEnv("")
	source := g.pickEligibleAccount()
	destination := g.pickExcludedAccount()
	amount := g.amount(1, 5_000)
	funding := g.transferFunding(amount)
	return petOp{
		row:   petRowTransferEligibleToExcluded,
		label: fmt.Sprintf("TransferEligibleOutToExcluded(%s, %s, %d)", source, destination, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.TransferEligibleOutToExcluded(e, source, destination, amount, funding)
		},
	}
}

func (g *petGenerator) factoryTransferExcluded() petOp {
	env := g.nextEnv("")
	source, destination := g.f.excluded, g.f.excluded2
	if g.rng.IntN(2) == 0 {
		source, destination = destination, source
	}
	amount := g.amount(1, 5_000)
	return petOp{
		row:   petRowTransferExcluded,
		label: fmt.Sprintf("TransferBetweenExcluded(%s, %s, %d)", source, destination, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.TransferBetweenExcluded(e, source, destination, amount)
		},
	}
}

func (g *petGenerator) factoryCashRefund() petOp {
	if len(g.debitPurchases) == 0 {
		return g.factoryDebitPurchase()
	}
	purchase := g.debitPurchases[g.rng.IntN(len(g.debitPurchases))]
	env := g.nextEnv("")
	amount := g.amountUpTo(purchase.amount)
	account := g.pickEligibleAccount()
	return petOp{
		row:   petRowCashRefund,
		label: fmt.Sprintf("RecordCashRefund(%s, %s, %d)", purchase.opID, account, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.RecordCashRefund(e, account, purchase.opID, purchase.meta.envelope, amount)
		},
	}
}

func (g *petGenerator) factoryCardRefund() petOp {
	if len(g.cardPurchases) == 0 {
		return g.factoryCardPurchase()
	}
	purchase := g.cardPurchases[g.rng.IntN(len(g.cardPurchases))]
	env := g.nextEnv("")
	amount := g.amountUpTo(purchase.amount)
	card := purchase.meta.cardAccount
	if card.IsZero() {
		card = g.f.card
	}
	payment := purchase.meta.paymentEnvelope
	if payment.IsZero() {
		payment = g.f.pay
	}
	return petOp{
		row:   petRowCardRefund,
		label: fmt.Sprintf("RecordCardRefund(%s, %s, %d)", purchase.opID, card, amount.MinorUnits()),
		env:   env,
		run: func(e CommandEnvelope) (JournalOperation, error) {
			return g.f.service.RecordCardRefund(e, card, purchase.opID, purchase.meta.envelope, payment, amount)
		},
	}
}

func (g *petGenerator) factoryReimbursement() petOp {
	return g.reimbursementOp(g.pickEligibleAccount(), g.amount(1, 5_000))
}
