package application

import (
	"context"
	"strings"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// builtOperation is the immutable operation metadata plus effects a command
// builder produces after validating against the current ledger.
type builtOperation struct {
	opType  domain.OperationType
	source  domain.TransactionSource
	amount  *domain.PositiveAmount
	reason  string
	effects domain.Effects
}

type operationBuilder func(snapshot *ledgerSnapshot) (builtOperation, error)

// executeFinancial validates authorization and idempotency, rebuilds the
// ledger, applies the command's effects to a throwaway state, and commits the
// journal operation and idempotency record inside the caller's unit of work.
func (s *Service) executeFinancial(
	ctx context.Context,
	repos ports.Repositories,
	mutation Mutation,
	builder operationBuilder,
	after func() error,
) (domain.JournalOperation, error) {
	if _, err := authorizeMember(ctx, repos, mutation.Household, mutation.Actor); err != nil {
		return domain.JournalOperation{}, err
	}
	if mutation.EffectiveDate.IsZero() {
		return domain.JournalOperation{}, domain.NewError(domain.CodeInvalidValue, "effective date must be set")
	}
	if mutation.Key.IsZero() {
		return domain.JournalOperation{}, domain.NewError(domain.CodeInvalidValue, "idempotency key must not be empty")
	}
	if mutation.Fingerprint.IsZero() {
		return domain.JournalOperation{}, domain.NewError(domain.CodeInvalidValue, "request fingerprint must not be empty")
	}

	// Replay is decided before state-dependent validation, so a retry returns
	// the original result even after balances have moved.
	if record, err := repos.Idempotency.Lookup(ctx, mutation.Household, mutation.Key); err == nil {
		if record.Fingerprint() != mutation.Fingerprint {
			return domain.JournalOperation{}, domain.NewErrorf(
				domain.CodeIdempotencyKeyPayloadMismatch,
				"idempotency key %q was reused with a different request fingerprint", mutation.Key,
			)
		}
		return repos.Journal.Get(ctx, mutation.Household, record.OperationID())
	} else if !domain.HasCode(err, domain.CodeEntityNotFoundOrNotInHousehold) {
		return domain.JournalOperation{}, err
	}

	snapshot, err := loadLedger(ctx, repos, mutation.Household)
	if err != nil {
		return domain.JournalOperation{}, err
	}
	built, err := builder(snapshot)
	if err != nil {
		return domain.JournalOperation{}, err
	}
	if err := snapshot.state.ApplyPostings(built.effects.Postings); err != nil {
		return domain.JournalOperation{}, err
	}
	if err := snapshot.state.ApplyReservationChanges(built.effects.Reservations...); err != nil {
		return domain.JournalOperation{}, err
	}
	if err := snapshot.state.CheckInvariants(); err != nil {
		return domain.JournalOperation{}, err
	}

	rawID, err := s.newID()
	if err != nil {
		return domain.JournalOperation{}, err
	}
	opID, err := domain.NewJournalOperationId(rawID)
	if err != nil {
		return domain.JournalOperation{}, err
	}
	now := s.newRecordedAt()
	op, err := domain.NewJournalOperation(domain.JournalOperationParams{
		ID:                 opID,
		Household:          mutation.Household,
		Actor:              mutation.Actor,
		Type:               built.opType,
		Source:             built.source,
		EffectiveDate:      mutation.EffectiveDate,
		Currency:           snapshot.household.Currency,
		IdempotencyKey:     mutation.Key,
		RequestFingerprint: mutation.Fingerprint,
		Amount:             built.amount,
		Reason:             built.reason,
		CreatedAt:          now,
		CommittedAt:        now,
		Effects:            built.effects,
	})
	if err != nil {
		return domain.JournalOperation{}, err
	}

	record, isNew, err := repos.Idempotency.Begin(ctx, mutation.Household, mutation.Key, mutation.Fingerprint, op.ID(), now.Time())
	if err != nil {
		return domain.JournalOperation{}, err
	}
	if !isNew {
		if record.Fingerprint() != mutation.Fingerprint {
			return domain.JournalOperation{}, domain.ErrIdempotencyKeyPayloadMismatch
		}
		return repos.Journal.Get(ctx, mutation.Household, record.OperationID())
	}
	if err := repos.Journal.Append(ctx, mutation.Household, op); err != nil {
		return domain.JournalOperation{}, err
	}
	if after != nil {
		if err := after(); err != nil {
			return domain.JournalOperation{}, err
		}
	}
	return op, nil
}

// RecordOpeningBalance posts an opening account or card balance.
func (s *Service) RecordOpeningBalance(ctx context.Context, mutation Mutation, account domain.AccountId, deltaMinor int64) (domain.JournalOperation, error) {
	var result domain.JournalOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		op, err := s.executeFinancial(ctx, repos, mutation, func(snapshot *ledgerSnapshot) (builtOperation, error) {
			delta, err := signedFromMinor(snapshot.household.Currency, deltaMinor)
			if err != nil {
				return builtOperation{}, err
			}
			effects, err := domain.OpeningBalanceEffects(account, delta)
			return builtOperation{opType: domain.OpOpeningBalance, source: domain.TransactionSourceOpeningBalance, effects: effects}, err
		}, nil)
		result = op
		return err
	})
	return result, err
}

// RecordIncome posts received income into an account.
func (s *Service) RecordIncome(ctx context.Context, mutation Mutation, account domain.AccountId, amountMinor int64) (domain.JournalOperation, error) {
	return s.amountCommand(ctx, mutation, amountMinor, func(_ *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
		effects, err := domain.IncomeReceivedEffects(account, amount)
		return builtOperation{opType: domain.OpIncome, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
	})
}

// AllocateFunds moves ready-to-assign money into an envelope.
func (s *Service) AllocateFunds(ctx context.Context, mutation Mutation, envelope domain.EnvelopeId, amountMinor int64) (domain.JournalOperation, error) {
	return s.amountCommand(ctx, mutation, amountMinor, func(snapshot *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
		if err := snapshot.state.RequireReadyToAssign(amount); err != nil {
			return builtOperation{}, err
		}
		effects, err := domain.AllocateFundsEffects(envelope, amount)
		return builtOperation{opType: domain.OpAllocate, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
	})
}

// ReallocateFunds moves budget value between envelopes.
func (s *Service) ReallocateFunds(ctx context.Context, mutation Mutation, source, destination domain.EnvelopeId, amountMinor int64) (domain.JournalOperation, error) {
	return s.amountCommand(ctx, mutation, amountMinor, func(snapshot *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
		if err := snapshot.state.RequireEnvelopeFunds(source, amount); err != nil {
			return builtOperation{}, err
		}
		effects, err := domain.ReallocateFundsEffects(source, destination, amount)
		return builtOperation{opType: domain.OpReallocate, source: domain.TransactionSourceManual, effects: effects}, err
	})
}

// RecordDebitPurchase records a debit or cash-wallet purchase.
func (s *Service) RecordDebitPurchase(ctx context.Context, mutation Mutation, account domain.AccountId, envelope domain.EnvelopeId, amountMinor int64) (domain.JournalOperation, error) {
	return s.amountCommand(ctx, mutation, amountMinor, func(snapshot *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
		record, err := findAccount(snapshot.accounts, account)
		if err != nil {
			return builtOperation{}, err
		}
		opType := domain.OpDebitPurchase
		if record.Kind == domain.AccountKindCashWallet {
			opType = domain.OpCashPurchase
		}
		effects, err := domain.DebitPurchaseEffects(account, envelope, amount)
		return builtOperation{opType: opType, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
	})
}

// RecordCardPurchase records a credit-card purchase.
func (s *Service) RecordCardPurchase(ctx context.Context, mutation Mutation, card domain.AccountId, spendingEnvelope, paymentEnvelope domain.EnvelopeId, amountMinor int64) (domain.JournalOperation, error) {
	return s.amountCommand(ctx, mutation, amountMinor, func(_ *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
		effects, err := domain.CreditCardPurchaseEffects(card, spendingEnvelope, paymentEnvelope, amount)
		return builtOperation{opType: domain.OpCardPurchase, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
	})
}

// RecordCardPayment records a credit-card repayment.
func (s *Service) RecordCardPayment(ctx context.Context, mutation Mutation, cashAccount, card domain.AccountId, paymentEnvelope domain.EnvelopeId, amountMinor int64) (domain.JournalOperation, error) {
	return s.amountCommand(ctx, mutation, amountMinor, func(_ *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
		effects, err := domain.CreditCardPaymentEffects(cashAccount, card, paymentEnvelope, amount)
		return builtOperation{opType: domain.OpCardPayment, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
	})
}

// RecordCardFee records a card fee or interest charge.
func (s *Service) RecordCardFee(ctx context.Context, mutation Mutation, card domain.AccountId, feeEnvelope, paymentEnvelope domain.EnvelopeId, amountMinor int64) (domain.JournalOperation, error) {
	return s.amountCommand(ctx, mutation, amountMinor, func(_ *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
		effects, err := domain.CardFeeOrInterestEffects(card, feeEnvelope, paymentEnvelope, amount)
		return builtOperation{opType: domain.OpCardFeeInterest, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
	})
}

// FundingInput is the transport-independent funding plan for a transfer out of
// the budget or a reconciliation adjustment.
type FundingInput struct {
	FromReadyMinor *int64
	Envelopes      []EnvelopeFundingInput
}

// EnvelopeFundingInput names one envelope contribution.
type EnvelopeFundingInput struct {
	EnvelopeID  domain.EnvelopeId
	AmountMinor int64
}

func buildFunding(currency domain.Currency, input *FundingInput) (domain.FundingSource, error) {
	zero, err := zeroMoney(currency)
	if err != nil {
		return domain.FundingSource{}, err
	}
	funding := domain.FundingSource{FromReady: zero}
	if input == nil {
		return funding, nil
	}
	if input.FromReadyMinor != nil {
		money, err := domain.NewMoney(*input.FromReadyMinor, currency)
		if err != nil {
			return domain.FundingSource{}, err
		}
		funding.FromReady = money
	}
	for _, contribution := range input.Envelopes {
		money, err := domain.NewMoney(contribution.AmountMinor, currency)
		if err != nil {
			return domain.FundingSource{}, err
		}
		funding.FromEnvelopes = append(funding.FromEnvelopes, domain.EnvelopeFunding{
			EnvelopeID: contribution.EnvelopeID,
			Amount:     money,
		})
	}
	return funding, nil
}

// RecordTransfer records a factual transfer, selecting the domain command by
// the source and destination accounts' budget eligibility.
func (s *Service) RecordTransfer(ctx context.Context, mutation Mutation, source, destination domain.AccountId, amountMinor int64, fundingInput *FundingInput) (domain.JournalOperation, error) {
	return s.amountCommand(ctx, mutation, amountMinor, func(snapshot *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
		sourceAccount, err := findAccount(snapshot.accounts, source)
		if err != nil {
			return builtOperation{}, err
		}
		destinationAccount, err := findAccount(snapshot.accounts, destination)
		if err != nil {
			return builtOperation{}, err
		}
		if !sourceAccount.Kind.IsLiquidCash() || !destinationAccount.Kind.IsLiquidCash() {
			return builtOperation{}, domain.NewError(domain.CodeInvalidValue, "transfers require liquid cash accounts")
		}

		switch {
		case sourceAccount.BudgetEligible && destinationAccount.BudgetEligible:
			effects, err := domain.TransferEligibleToEligibleEffects(source, destination, amount)
			return builtOperation{opType: domain.OpTransferEligible, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
		case !sourceAccount.BudgetEligible && destinationAccount.BudgetEligible:
			effects, err := domain.TransferExcludedToEligibleEffects(source, destination, amount)
			return builtOperation{opType: domain.OpTransferToEligible, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
		case sourceAccount.BudgetEligible && !destinationAccount.BudgetEligible:
			funding, err := buildFunding(snapshot.household.Currency, fundingInput)
			if err != nil {
				return builtOperation{}, err
			}
			if len(funding.FromEnvelopes) == 0 && funding.FromReady.IsZero() {
				return builtOperation{}, domain.ErrAccountScopeChangeRequiresFundingPlan
			}
			effects, err := domain.TransferEligibleOutToExcludedEffects(source, destination, amount, funding)
			return builtOperation{opType: domain.OpTransferOutOfScope, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
		default:
			effects, err := domain.TransferExcludedToExcludedEffects(source, destination, amount)
			return builtOperation{opType: domain.OpTransferExcluded, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
		}
	})
}

func findAccount(accounts []ports.AccountRecord, id domain.AccountId) (ports.AccountRecord, error) {
	for _, account := range accounts {
		if account.ID == id {
			return account, nil
		}
	}
	return ports.AccountRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
}

// RecordCashRefund records a refund of a debit/cash purchase.
func (s *Service) RecordCashRefund(ctx context.Context, mutation Mutation, account domain.AccountId, original domain.JournalOperationId, envelope domain.EnvelopeId, amountMinor int64) (domain.JournalOperation, error) {
	return s.amountCommand(ctx, mutation, amountMinor, func(snapshot *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
		if err := requireRefundableRemainder(snapshot, original, amount); err != nil {
			return builtOperation{}, err
		}
		effects, err := domain.CashRefundEffects(account, original, envelope, amount)
		return builtOperation{opType: domain.OpCashRefund, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
	})
}

// RecordCardRefund records a refund against a card purchase.
func (s *Service) RecordCardRefund(ctx context.Context, mutation Mutation, card domain.AccountId, original domain.JournalOperationId, spendingEnvelope, paymentEnvelope domain.EnvelopeId, amountMinor int64) (domain.JournalOperation, error) {
	return s.amountCommand(ctx, mutation, amountMinor, func(snapshot *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
		if err := requireRefundableRemainder(snapshot, original, amount); err != nil {
			return builtOperation{}, err
		}
		effects, err := domain.CardRefundEffects(card, original, spendingEnvelope, paymentEnvelope, amount)
		return builtOperation{opType: domain.OpCardRefund, source: domain.TransactionSourceManual, amount: &amount, effects: effects}, err
	})
}

// ReverseOperation compensates an earlier operation. A reason is required.
func (s *Service) ReverseOperation(ctx context.Context, mutation Mutation, original domain.JournalOperationId, amountMinor int64, reason string) (domain.JournalOperation, error) {
	if strings.TrimSpace(reason) == "" {
		return domain.JournalOperation{}, domain.NewError(domain.CodeInvalidValue, "reversal requires a reason")
	}
	return s.amountCommand(ctx, mutation, amountMinor, func(snapshot *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
		originalOp, err := findOperation(snapshot.operations, original)
		if err != nil {
			return builtOperation{}, err
		}
		magnitude, err := operationMagnitude(originalOp)
		if err != nil {
			return builtOperation{}, err
		}
		reversed, err := linkedTotal(snapshot.operations, domain.OpReversal, domain.RelatedRefReversal, original)
		if err != nil {
			return builtOperation{}, err
		}
		remaining, err := magnitude.Sub(reversed)
		if err != nil {
			return builtOperation{}, err
		}
		comparison, err := amount.Money().Compare(remaining)
		if err != nil {
			return builtOperation{}, err
		}
		if comparison > 0 {
			return builtOperation{}, domain.NewErrorf(domain.CodeReversalExceedsRemainder,
				"reversal of %d minor units exceeds the reversible remainder of %d minor units", amount.MinorUnits(), remaining.MinorUnits())
		}
		partial, err := partialPostings(originalOp.Postings(), magnitude, amount.Money())
		if err != nil {
			return builtOperation{}, err
		}
		effects, err := domain.ReversalEffects(partial, original)
		return builtOperation{opType: domain.OpReversal, source: domain.TransactionSourceManual, amount: &amount, reason: reason, effects: effects}, err
	})
}

// CorrectionKind selects the correction command.
type CorrectionKind string

const (
	CorrectionTransaction CorrectionKind = "TRANSACTION"
	CorrectionCategory    CorrectionKind = "CATEGORY"
	CorrectionMetadata    CorrectionKind = "METADATA"
)

// ReplacementPurchase describes the replacement postings of a transaction
// correction. It is a purchase shape, never an arbitrary ledger posting.
type ReplacementPurchase struct {
	Method          domain.PurchaseKind
	Account         domain.AccountId
	Envelope        domain.EnvelopeId
	PaymentEnvelope domain.EnvelopeId
	AmountMinor     int64
}

// Correct records a compensating correction. A reason is required.
func (s *Service) Correct(ctx context.Context, mutation Mutation, kind CorrectionKind, original domain.JournalOperationId, reason string, originalEnvelope, correctedEnvelope domain.EnvelopeId, amountMinor int64, replacement *ReplacementPurchase) (domain.JournalOperation, error) {
	if strings.TrimSpace(reason) == "" {
		return domain.JournalOperation{}, domain.NewError(domain.CodeInvalidValue, "correction requires a reason")
	}
	switch kind {
	case CorrectionCategory:
		return s.amountCommand(ctx, mutation, amountMinor, func(snapshot *ledgerSnapshot, amount domain.PositiveAmount) (builtOperation, error) {
			if _, err := findOperation(snapshot.operations, original); err != nil {
				return builtOperation{}, err
			}
			effects, err := domain.CategoryCorrectionEffects(original, originalEnvelope, correctedEnvelope, amount)
			return builtOperation{opType: domain.OpCategoryCorrection, source: domain.TransactionSourceManual, amount: &amount, reason: reason, effects: effects}, err
		})
	case CorrectionMetadata:
		var result domain.JournalOperation
		err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
			op, err := s.executeFinancial(ctx, repos, mutation, func(snapshot *ledgerSnapshot) (builtOperation, error) {
				if _, err := findOperation(snapshot.operations, original); err != nil {
					return builtOperation{}, err
				}
				effects, err := domain.MetadataCorrectionEffects(original)
				return builtOperation{opType: domain.OpMetadataCorrection, source: domain.TransactionSourceManual, reason: reason, effects: effects}, err
			}, nil)
			result = op
			return err
		})
		return result, err
	case CorrectionTransaction:
		if replacement == nil {
			return domain.JournalOperation{}, domain.NewError(domain.CodeInvalidValue, "transaction correction requires replacement details")
		}
		var result domain.JournalOperation
		err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
			op, err := s.executeFinancial(ctx, repos, mutation, func(snapshot *ledgerSnapshot) (builtOperation, error) {
				originalOp, err := findOperation(snapshot.operations, original)
				if err != nil {
					return builtOperation{}, err
				}
				correction, err := replacementEffects(snapshot.household.Currency, *replacement)
				if err != nil {
					return builtOperation{}, err
				}
				effects, err := domain.ReplacementEffects(originalOp.Postings(), original, correction)
				return builtOperation{opType: domain.OpReplacement, source: domain.TransactionSourceManual, reason: reason, effects: effects}, err
			}, nil)
			result = op
			return err
		})
		return result, err
	default:
		return domain.JournalOperation{}, domain.NewErrorf(domain.CodeInvalidValue, "unknown correction kind %q", kind)
	}
}

func replacementEffects(currency domain.Currency, replacement ReplacementPurchase) (domain.Effects, error) {
	amount, err := positiveFromMinor(currency, replacement.AmountMinor)
	if err != nil {
		return domain.Effects{}, err
	}
	switch replacement.Method {
	case domain.PurchaseKindDebit, domain.PurchaseKindCash:
		if !replacement.PaymentEnvelope.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "debit/cash replacement does not use a payment envelope")
		}
		return domain.DebitPurchaseEffects(replacement.Account, replacement.Envelope, amount)
	case domain.PurchaseKindCard:
		if replacement.PaymentEnvelope.IsZero() {
			return domain.Effects{}, domain.NewError(domain.CodeInvalidValue, "card replacement requires a payment envelope")
		}
		return domain.CreditCardPurchaseEffects(replacement.Account, replacement.Envelope, replacement.PaymentEnvelope, amount)
	default:
		return domain.Effects{}, domain.NewErrorf(domain.CodeInvalidValue, "unsupported replacement method %q", replacement.Method)
	}
}

// Reconcile records a reviewed reconciliation adjustment. A reason is
// required and the delta must be nonzero.
func (s *Service) Reconcile(ctx context.Context, mutation Mutation, account domain.AccountId, deltaMinor int64, fundingInput *FundingInput, reason string) (domain.JournalOperation, error) {
	if strings.TrimSpace(reason) == "" {
		return domain.JournalOperation{}, domain.NewError(domain.CodeInvalidValue, "reconciliation requires a reason")
	}
	var result domain.JournalOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		op, err := s.executeFinancial(ctx, repos, mutation, func(snapshot *ledgerSnapshot) (builtOperation, error) {
			delta, err := signedFromMinor(snapshot.household.Currency, deltaMinor)
			if err != nil {
				return builtOperation{}, err
			}
			deltaMoney, err := delta.Money()
			if err != nil {
				return builtOperation{}, err
			}
			if deltaMoney.IsZero() {
				return builtOperation{}, domain.NewError(domain.CodeInvalidAmount, "reconciliation delta must be nonzero")
			}
			funding, err := buildFunding(snapshot.household.Currency, fundingInput)
			if err != nil {
				return builtOperation{}, err
			}
			effects, err := domain.ReconciliationAdjustmentEffects(account, delta, funding)
			if err != nil {
				return builtOperation{}, err
			}
			absMoney := deltaMoney
			if deltaMoney.IsNegative() {
				absMoney, err = deltaMoney.Neg()
				if err != nil {
					return builtOperation{}, err
				}
			}
			amount, err := domain.NewPositiveAmount(absMoney)
			if err != nil {
				return builtOperation{}, err
			}
			opType := domain.OpReconcileIncrease
			if delta.IsNegative() {
				opType = domain.OpReconcileDecrease
			}
			return builtOperation{opType: opType, source: domain.TransactionSourceReconciliation, amount: &amount, reason: reason, effects: effects}, nil
		}, nil)
		result = op
		return err
	})
	return result, err
}

func (s *Service) amountCommand(
	ctx context.Context,
	mutation Mutation,
	amountMinor int64,
	fn func(*ledgerSnapshot, domain.PositiveAmount) (builtOperation, error),
) (domain.JournalOperation, error) {
	var result domain.JournalOperation
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		op, err := s.executeFinancial(ctx, repos, mutation, func(snapshot *ledgerSnapshot) (builtOperation, error) {
			amount, err := positiveFromMinor(snapshot.household.Currency, amountMinor)
			if err != nil {
				return builtOperation{}, err
			}
			return fn(snapshot, amount)
		}, nil)
		result = op
		return err
	})
	return result, err
}

func findOperation(operations []domain.JournalOperation, id domain.JournalOperationId) (domain.JournalOperation, error) {
	for _, op := range operations {
		if op.ID() == id {
			return op, nil
		}
	}
	return domain.JournalOperation{}, domain.NewErrorf(domain.CodeEntityNotFoundOrNotInHousehold, "journal operation %q is not in this household", id)
}

func operationMagnitude(op domain.JournalOperation) (domain.Money, error) {
	if amount, ok := op.Amount(); ok {
		return amount.Money(), nil
	}
	postings := op.Postings()
	var magnitude int64
	uniform := true
	consider := func(delta domain.SignedMoneyDelta) {
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
		return domain.Money{}, domain.NewErrorf(domain.CodeInvalidValue, "operation %q has no single derivable amount", op.ID())
	}
	return domain.NewMoney(magnitude, op.Currency())
}

func linkedTotal(operations []domain.JournalOperation, opType domain.OperationType, kind domain.RelatedRefKind, target domain.JournalOperationId) (domain.Money, error) {
	currency := domain.Currency{}
	for _, op := range operations {
		if op.Type() == opType {
			currency = op.Currency()
			break
		}
	}
	if currency.IsZero() {
		for _, op := range operations {
			currency = op.Currency()
			break
		}
	}
	total, err := domain.ZeroMoney(currency)
	if err != nil {
		return domain.Money{}, err
	}
	for _, op := range operations {
		if op.Type() != opType || !linksTo(op, kind, target) {
			continue
		}
		magnitude, err := operationMagnitude(op)
		if err != nil {
			return domain.Money{}, err
		}
		total, err = total.Add(magnitude)
		if err != nil {
			return domain.Money{}, err
		}
	}
	return total, nil
}

func linksTo(op domain.JournalOperation, kind domain.RelatedRefKind, target domain.JournalOperationId) bool {
	for _, ref := range op.Related() {
		if ref.Kind == kind && ref.OperationID == target {
			return true
		}
	}
	return false
}

func requireRefundableRemainder(snapshot *ledgerSnapshot, original domain.JournalOperationId, amount domain.PositiveAmount) error {
	originalOp, err := findOperation(snapshot.operations, original)
	if err != nil {
		return err
	}
	if !isRefundablePurchase(originalOp.Type()) {
		return domain.NewErrorf(domain.CodeInvalidValue, "operation %q is not a purchase and cannot be refunded", original)
	}
	magnitude, err := operationMagnitude(originalOp)
	if err != nil {
		return err
	}
	refunded, err := linkedRefunds(snapshot.operations, original)
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
		return domain.NewErrorf(domain.CodeRefundExceedsRemainder,
			"refund of %d minor units exceeds the refundable remainder of %d minor units", amount.MinorUnits(), remaining.MinorUnits())
	}
	return nil
}

func linkedRefunds(operations []domain.JournalOperation, original domain.JournalOperationId) (domain.Money, error) {
	total, err := zeroForOperations(operations)
	if err != nil {
		return domain.Money{}, err
	}
	for _, op := range operations {
		if op.Type() != domain.OpCashRefund && op.Type() != domain.OpCardRefund {
			continue
		}
		if !linksTo(op, domain.RelatedRefOriginal, original) {
			continue
		}
		magnitude, err := operationMagnitude(op)
		if err != nil {
			return domain.Money{}, err
		}
		total, err = total.Add(magnitude)
		if err != nil {
			return domain.Money{}, err
		}
	}
	return total, nil
}

func zeroForOperations(operations []domain.JournalOperation) (domain.Money, error) {
	if len(operations) == 0 {
		return domain.Money{}, domain.NewError(domain.CodeInvalidValue, "cannot derive currency from an empty journal")
	}
	return domain.ZeroMoney(operations[0].Currency())
}

func isRefundablePurchase(opType domain.OperationType) bool {
	switch opType {
	case domain.OpDebitPurchase, domain.OpCashPurchase, domain.OpCardPurchase, domain.OpExternalTransaction, domain.OpPurchaseComplete:
		return true
	default:
		return false
	}
}

func partialPostings(original domain.PostingSet, magnitude, amount domain.Money) (domain.PostingSet, error) {
	comparison, err := amount.Compare(magnitude)
	if err != nil {
		return domain.PostingSet{}, err
	}
	if comparison > 0 {
		return domain.PostingSet{}, domain.NewErrorf(domain.CodeInvalidAmount, "partial reversal exceeds original magnitude")
	}
	if comparison == 0 {
		return original, nil
	}
	if original.IsEmpty() {
		return domain.PostingSet{}, domain.NewError(domain.CodeInvalidValue, "reversal original posting set must not be empty")
	}
	target := amount.MinorUnits()
	if target < 0 {
		target = -target
	}
	partial := domain.PostingSet{}
	for _, posting := range original.Accounts {
		delta, err := scaledDelta(posting.Delta, magnitude, target)
		if err != nil {
			return domain.PostingSet{}, err
		}
		if err := partial.AddAccount(posting.AccountID, delta); err != nil {
			return domain.PostingSet{}, err
		}
	}
	for _, posting := range original.Envelopes {
		delta, err := scaledDelta(posting.Delta, magnitude, target)
		if err != nil {
			return domain.PostingSet{}, err
		}
		if err := partial.AddEnvelope(posting.EnvelopeID, delta); err != nil {
			return domain.PostingSet{}, err
		}
	}
	return partial, nil
}

func scaledDelta(delta domain.SignedMoneyDelta, magnitude domain.Money, target int64) (domain.SignedMoneyDelta, error) {
	value := delta.MinorUnits()
	negative := value < 0
	if negative {
		value = -value
	}
	if value != magnitude.MinorUnits() {
		return domain.SignedMoneyDelta{}, domain.NewErrorf(domain.CodeInvalidValue,
			"partial reversal requires every posting to carry the original magnitude")
	}
	if negative {
		target = -target
	}
	return domain.NewSignedMoneyDelta(target, delta.Currency())
}
