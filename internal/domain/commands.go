// This file implements the financial command handlers of DOMAIN_MODEL §5 on
// top of CommandService. Each handler validates its own preconditions, builds
// effects with the pure posting-matrix functions, and commits through
// execute's validate-on-copy-then-commit path.
//
// Funding policy (POSTING_MATRIX §3/§5, IMPLEMENTATION_DECISIONS Q3/Q5):
//   - Planned budget moves enforce funding: allocation requires sufficient
//     Ready-to-assign, reallocation requires sufficient source-envelope funds,
//     and eligible-to-excluded transfers require an explicit funding plan that
//     totals the amount.
//   - Factual external transactions (purchases, card activity, refunds,
//     corrections, transfers) are recorded truthfully with NO funding checks.
//     An underfunded outcome is a visible deficit derived afterwards by
//     Deficits/ResolutionStatus, never a rejected command (Q3).
//
// Refund and reversal remainders are derived from the append-only journal
// rather than a mutable counter map: a refund counts cash- and card-refund
// operations linked to the original through RelatedRefOriginal, and a reversal
// counts reversal operations linked through RelatedRefReversal. Because the
// journal is the source of truth, rebuilds and retries cannot lose history.
package domain

import "strings"

// RecordOpeningBalance establishes an opening account or card balance. The
// signed delta carries an existing debt or overdraft as a negative balance.
func (s *CommandService) RecordOpeningBalance(env CommandEnvelope, account AccountId, delta SignedMoneyDelta) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpOpeningBalance,
		source: TransactionSourceOpeningBalance,
		build: func(_ *LedgerState) (Effects, error) {
			return OpeningBalanceEffects(account, delta)
		},
	})
}

// RecordIncome records received income into an account.
func (s *CommandService) RecordIncome(env CommandEnvelope, account AccountId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpIncome,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			return IncomeReceivedEffects(account, amount)
		},
	})
}

// RecordReimbursement records an unrelated reimbursement received as cash.
func (s *CommandService) RecordReimbursement(env CommandEnvelope, account AccountId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpReimbursement,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			return ReimbursementReceivedEffects(account, amount)
		},
	})
}

// AllocateFunds moves amount from derived Ready-to-assign into an envelope. It
// requires sufficient Ready-to-assign (POSTING_MATRIX §3.1).
func (s *CommandService) AllocateFunds(env CommandEnvelope, envelope EnvelopeId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpAllocate,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(state *LedgerState) (Effects, error) {
			if err := state.RequireReadyToAssign(amount); err != nil {
				return Effects{}, err
			}
			return AllocateFundsEffects(envelope, amount)
		},
	})
}

// ReallocateFunds moves budget value between envelopes. It requires sufficient
// spendable funds in the source envelope.
func (s *CommandService) ReallocateFunds(env CommandEnvelope, source, destination EnvelopeId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpReallocate,
		source: TransactionSourceManual,
		build: func(state *LedgerState) (Effects, error) {
			if err := state.RequireEnvelopeFunds(source, amount); err != nil {
				return Effects{}, err
			}
			return ReallocateFundsEffects(source, destination, amount)
		},
	})
}

// RecordDebitPurchase records an actual debit or cash-wallet purchase. It is a
// factual event (Q3) and is posted even when the account or envelope is
// underfunded.
func (s *CommandService) RecordDebitPurchase(env CommandEnvelope, account AccountId, envelope EnvelopeId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpDebitPurchase,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			return DebitPurchaseEffects(account, envelope, amount)
		},
	})
}

// RecordCardPurchase records an actual credit-card purchase. It is a factual
// event (Q3) and is posted even when the spending envelope is underfunded.
func (s *CommandService) RecordCardPurchase(env CommandEnvelope, card AccountId, spendingEnvelope, paymentEnvelope EnvelopeId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpCardPurchase,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			return CreditCardPurchaseEffects(card, spendingEnvelope, paymentEnvelope, amount)
		},
	})
}

// RecordCardPayment records an actual repayment of a credit-card liability. It
// is a factual event and does not create a second expense.
func (s *CommandService) RecordCardPayment(env CommandEnvelope, cashAccount, card AccountId, paymentEnvelope EnvelopeId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpCardPayment,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			return CreditCardPaymentEffects(cashAccount, card, paymentEnvelope, amount)
		},
	})
}

// RecordCardFee records an actual card fee or interest charge.
func (s *CommandService) RecordCardFee(env CommandEnvelope, card AccountId, feeEnvelope, paymentEnvelope EnvelopeId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpCardFeeInterest,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			return CardFeeOrInterestEffects(card, feeEnvelope, paymentEnvelope, amount)
		},
	})
}

// TransferBetweenEligible records a factual transfer between two eligible cash
// accounts. It does not change Ready-to-assign or any envelope.
func (s *CommandService) TransferBetweenEligible(env CommandEnvelope, source, destination AccountId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpTransferEligible,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			return TransferEligibleToEligibleEffects(source, destination, amount)
		},
	})
}

// TransferExcludedToEligible records a factual transfer bringing excluded cash
// into the budget. It is a transfer, never income.
func (s *CommandService) TransferExcludedToEligible(env CommandEnvelope, source, destination AccountId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpTransferToEligible,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			return TransferExcludedToEligibleEffects(source, destination, amount)
		},
	})
}

// TransferEligibleOutToExcluded records a factual transfer of budget money to an
// excluded account. Because value leaves the budget boundary it requires an
// explicit funding plan (Q5): Ready-to-assign and/or named envelopes must total
// the amount exactly. An entirely empty plan is
// ACCOUNT_SCOPE_CHANGE_REQUIRES_FUNDING_PLAN; a non-empty plan that does not
// total the amount is INVALID_AMOUNT from the effects layer.
func (s *CommandService) TransferEligibleOutToExcluded(env CommandEnvelope, source, destination AccountId, amount PositiveAmount, funding FundingSource) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpTransferOutOfScope,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			if len(funding.FromEnvelopes) == 0 && funding.FromReady.IsZero() {
				return Effects{}, NewErrorf(
					CodeAccountScopeChangeRequiresFundingPlan,
					"transfer of %d minor units out of the budget requires an explicit funding plan",
					amount.MinorUnits(),
				)
			}
			return TransferEligibleOutToExcludedEffects(source, destination, amount, funding)
		},
	})
}

// TransferBetweenExcluded records a factual transfer wholly outside the budget.
func (s *CommandService) TransferBetweenExcluded(env CommandEnvelope, source, destination AccountId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpTransferExcluded,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			return TransferExcludedToExcludedEffects(source, destination, amount)
		},
	})
}

// RecordCashRefund records a genuine refund of a debit/cash purchase back to
// eligible cash. The original operation must exist and be a purchase; cumulative
// refunds linked to it may not exceed the original amount.
func (s *CommandService) RecordCashRefund(env CommandEnvelope, refundAccount AccountId, original JournalOperationId, envelope EnvelopeId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpCashRefund,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			if err := s.requireRefundableRemainder(original, amount); err != nil {
				return Effects{}, err
			}
			return CashRefundEffects(refundAccount, original, envelope, amount)
		},
	})
}

// RecordCardRefund records a genuine refund of a credit-card purchase back onto
// the card. The original operation must exist and be a purchase; cumulative
// refunds linked to it may not exceed the original amount.
func (s *CommandService) RecordCardRefund(env CommandEnvelope, card AccountId, original JournalOperationId, spendingEnvelope, paymentEnvelope EnvelopeId, amount PositiveAmount) (JournalOperation, error) {
	return s.execute(env, commandPlan{
		opType: OpCardRefund,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			if err := s.requireRefundableRemainder(original, amount); err != nil {
				return Effects{}, err
			}
			return CardRefundEffects(card, original, spendingEnvelope, paymentEnvelope, amount)
		},
	})
}

// ReverseOperation records a compensating reversal of a committed operation.
// A reason is required. The original must exist and cumulative reversals may
// not exceed its amount; partial reversals are supported when every original
// posting carries the same magnitude.
func (s *CommandService) ReverseOperation(env CommandEnvelope, original JournalOperationId, amount PositiveAmount, reason string) (JournalOperation, error) {
	if strings.TrimSpace(reason) == "" {
		return JournalOperation{}, NewError(CodeInvalidValue, "reversal requires a reason")
	}
	env.Reason = reason
	return s.execute(env, commandPlan{
		opType: OpReversal,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			originalOp, err := s.journal.Operation(original)
			if err != nil {
				return Effects{}, err
			}
			magnitude, err := operationMagnitude(originalOp)
			if err != nil {
				return Effects{}, err
			}
			reversed, err := s.linkedReversalTotal(original)
			if err != nil {
				return Effects{}, err
			}
			remaining, err := magnitude.Sub(reversed)
			if err != nil {
				return Effects{}, err
			}
			comparison, err := amount.Money().Compare(remaining)
			if err != nil {
				return Effects{}, err
			}
			if comparison > 0 {
				return Effects{}, NewErrorf(
					CodeReversalExceedsRemainder,
					"reversal of %d minor units exceeds the reversible remainder of %d minor units for operation %q",
					amount.MinorUnits(),
					remaining.MinorUnits(),
					original,
				)
			}
			partial, err := partialPostingSet(originalOp.Postings(), magnitude, amount.Money())
			if err != nil {
				return Effects{}, err
			}
			return ReversalEffects(partial, original)
		},
	})
}

// CorrectTransaction records a replacement-shaped correction: the original
// operation is compensated and the supplied corrected effects replace it in one
// atomic operation. A reason is required.
func (s *CommandService) CorrectTransaction(env CommandEnvelope, original JournalOperationId, correction Effects, reason string) (JournalOperation, error) {
	if strings.TrimSpace(reason) == "" {
		return JournalOperation{}, NewError(CodeInvalidValue, "transaction correction requires a reason")
	}
	env.Reason = reason
	return s.execute(env, commandPlan{
		opType: OpReplacement,
		source: TransactionSourceManual,
		build: func(_ *LedgerState) (Effects, error) {
			originalOp, err := s.journal.Operation(original)
			if err != nil {
				return Effects{}, err
			}
			return ReplacementEffects(originalOp.Postings(), original, correction)
		},
	})
}

// CorrectCategory moves a purchase's budget posting from one envelope to
// another without touching account postings or the envelope sum. A reason is
// required.
func (s *CommandService) CorrectCategory(env CommandEnvelope, original JournalOperationId, originalEnvelope, correctedEnvelope EnvelopeId, amount PositiveAmount, reason string) (JournalOperation, error) {
	if strings.TrimSpace(reason) == "" {
		return JournalOperation{}, NewError(CodeInvalidValue, "category correction requires a reason")
	}
	env.Reason = reason
	return s.execute(env, commandPlan{
		opType: OpCategoryCorrection,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			if _, err := s.journal.Operation(original); err != nil {
				return Effects{}, err
			}
			return CategoryCorrectionEffects(original, originalEnvelope, correctedEnvelope, amount)
		},
	})
}

// CorrectMetadata records an audit-only correction with no financial postings.
// A reason is required.
func (s *CommandService) CorrectMetadata(env CommandEnvelope, original JournalOperationId, reason string) (JournalOperation, error) {
	if strings.TrimSpace(reason) == "" {
		return JournalOperation{}, NewError(CodeInvalidValue, "metadata correction requires a reason")
	}
	env.Reason = reason
	return s.execute(env, commandPlan{
		opType: OpMetadataCorrection,
		source: TransactionSourceManual,
		build: func(_ *LedgerState) (Effects, error) {
			if _, err := s.journal.Operation(original); err != nil {
				return Effects{}, err
			}
			return MetadataCorrectionEffects(original)
		},
	})
}

// ReconcileAccount records a reviewed reconciliation adjustment. A non-blank
// reason is required and the delta must be nonzero. A decrease may legitimately
// drive Ready-to-assign negative; that is a visible reviewed deficit (Q4), not
// an error.
func (s *CommandService) ReconcileAccount(env CommandEnvelope, account AccountId, delta SignedMoneyDelta, funding FundingSource, reason string) (JournalOperation, error) {
	if strings.TrimSpace(reason) == "" {
		return JournalOperation{}, NewError(CodeInvalidValue, "reconciliation requires a reason")
	}
	deltaMoney, err := delta.Money()
	if err != nil {
		return JournalOperation{}, err
	}
	if deltaMoney.IsZero() {
		return JournalOperation{}, NewError(CodeInvalidAmount, "reconciliation delta must be nonzero")
	}
	absMoney := deltaMoney
	if deltaMoney.IsNegative() {
		negated, negErr := deltaMoney.Neg()
		if negErr != nil {
			return JournalOperation{}, negErr
		}
		absMoney = negated
	}
	amount, err := NewPositiveAmount(absMoney)
	if err != nil {
		return JournalOperation{}, err
	}
	opType := OpReconcileIncrease
	if delta.IsNegative() {
		opType = OpReconcileDecrease
	}
	env.Reason = reason
	return s.execute(env, commandPlan{
		opType: opType,
		source: TransactionSourceReconciliation,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			return ReconciliationAdjustmentEffects(account, delta, funding)
		},
	})
}

// requireRefundableRemainder validates that original exists, is a purchase, and
// that this refund does not push cumulative linked refunds past the original
// amount.
func (s *CommandService) requireRefundableRemainder(original JournalOperationId, amount PositiveAmount) error {
	originalOp, err := s.journal.Operation(original)
	if err != nil {
		return err
	}
	if !isRefundablePurchase(originalOp.Type()) {
		return NewErrorf(CodeInvalidValue, "operation %q is not a purchase and cannot be refunded", original)
	}
	magnitude, err := operationMagnitude(originalOp)
	if err != nil {
		return err
	}
	refunded, err := s.linkedRefundTotal(original)
	if err != nil {
		return err
	}
	remaining, err := magnitude.Sub(refunded)
	if err != nil {
		return err
	}
	comparison, err := amount.Money().Compare(remaining)
	if err != nil {
		return err
	}
	if comparison > 0 {
		return NewErrorf(
			CodeRefundExceedsRemainder,
			"refund of %d minor units exceeds the refundable remainder of %d minor units for operation %q",
			amount.MinorUnits(),
			remaining.MinorUnits(),
			original,
		)
	}
	return nil
}

// linkedRefundTotal sums the amounts of cash and card refunds linked to
// original through RelatedRefOriginal.
func (s *CommandService) linkedRefundTotal(original JournalOperationId) (Money, error) {
	total, err := ZeroMoney(s.currency)
	if err != nil {
		return Money{}, err
	}
	for _, op := range s.journal.Operations() {
		if op.Type() != OpCashRefund && op.Type() != OpCardRefund {
			continue
		}
		if !linksTo(op, RelatedRefOriginal, original) {
			continue
		}
		refunded, err := operationMagnitude(op)
		if err != nil {
			return Money{}, err
		}
		total, err = total.Add(refunded)
		if err != nil {
			return Money{}, err
		}
	}
	return total, nil
}

// linkedReversalTotal sums the amounts of reversal operations linked to original
// through RelatedRefReversal.
func (s *CommandService) linkedReversalTotal(original JournalOperationId) (Money, error) {
	total, err := ZeroMoney(s.currency)
	if err != nil {
		return Money{}, err
	}
	for _, op := range s.journal.Operations() {
		if op.Type() != OpReversal {
			continue
		}
		if !linksTo(op, RelatedRefReversal, original) {
			continue
		}
		reversed, err := operationMagnitude(op)
		if err != nil {
			return Money{}, err
		}
		total, err = total.Add(reversed)
		if err != nil {
			return Money{}, err
		}
	}
	return total, nil
}

// linksTo reports whether op carries a related reference of the given kind to
// target.
func linksTo(op JournalOperation, kind RelatedRefKind, target JournalOperationId) bool {
	for _, ref := range op.Related() {
		if ref.Kind == kind && ref.OperationID == target {
			return true
		}
	}
	return false
}

// isRefundablePurchase reports whether an operation type represents a purchase
// whose amount can be refunded.
func isRefundablePurchase(opType OperationType) bool {
	switch opType {
	case OpDebitPurchase,
		OpCashPurchase,
		OpCardPurchase,
		OpExternalTransaction,
		OpPurchaseComplete:
		return true
	default:
		return false
	}
}

// operationMagnitude returns the amount an operation represents: its declared
// amount when present, otherwise the common absolute magnitude of its postings.
// A zero or non-uniform posting set without an amount is INVALID_VALUE.
func operationMagnitude(op JournalOperation) (Money, error) {
	if amount, ok := op.Amount(); ok {
		return amount.Money(), nil
	}
	postings := op.Postings()
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
		return Money{}, NewErrorf(CodeInvalidValue, "operation %q has no single derivable amount", op.ID())
	}
	return NewMoney(magnitude, op.Currency())
}

// partialPostingSet builds the original-side posting set for a reversal of
// amount. A full-amount reversal returns the original postings unchanged;
// partial reversals require every original posting to carry the same magnitude
// so the requested amount can be scaled while preserving each posting's sign.
func partialPostingSet(original PostingSet, magnitude, amount Money) (PostingSet, error) {
	comparison, err := amount.Compare(magnitude)
	if err != nil {
		return PostingSet{}, err
	}
	if comparison > 0 {
		return PostingSet{}, NewErrorf(CodeInvalidAmount, "partial reversal amount %d exceeds original magnitude %d", amount.MinorUnits(), magnitude.MinorUnits())
	}
	if comparison == 0 {
		return original, nil
	}
	if original.IsEmpty() {
		return PostingSet{}, NewError(CodeInvalidValue, "reversal original posting set must not be empty")
	}

	target := amount.MinorUnits()
	if target < 0 {
		target = -target
	}
	partial := PostingSet{}
	for _, posting := range original.Accounts {
		delta, err := scaledDelta(posting.Delta, magnitude, target)
		if err != nil {
			return PostingSet{}, err
		}
		if err := partial.AddAccount(posting.AccountID, delta); err != nil {
			return PostingSet{}, err
		}
	}
	for _, posting := range original.Envelopes {
		delta, err := scaledDelta(posting.Delta, magnitude, target)
		if err != nil {
			return PostingSet{}, err
		}
		if err := partial.AddEnvelope(posting.EnvelopeID, delta); err != nil {
			return PostingSet{}, err
		}
	}
	return partial, nil
}

// scaledDelta preserves delta's sign and currency while replacing its magnitude
// with target. It rejects a posting whose magnitude does not match the
// operation magnitude, because scaling such a shape would silently distort it.
func scaledDelta(delta SignedMoneyDelta, magnitude Money, target int64) (SignedMoneyDelta, error) {
	value := delta.MinorUnits()
	negative := value < 0
	if negative {
		value = -value
	}
	if value != magnitude.MinorUnits() {
		return SignedMoneyDelta{}, NewErrorf(
			CodeInvalidValue,
			"partial reversal requires every posting to carry the original magnitude %d, got %d",
			magnitude.MinorUnits(),
			value,
		)
	}
	if negative {
		target = -target
	}
	return NewSignedMoneyDelta(target, delta.Currency())
}
