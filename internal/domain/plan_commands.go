// This file implements the planned-operation lifecycle commands on top of
// CommandService (STATE_TRANSITIONS §2, POSTING_MATRIX §4.1). The aggregate in
// plan.go decides whether a transition is legal and plan_change.go builds the
// pure effects; this layer owns the household command boundary: it looks plans
// up, runs the authorization layer against a throwaway ledger copy, and commits
// the journal append, the ledger projection swap, the idempotency record and the
// plan state together.
//
// Atomicity follows the same validate-on-copy-then-commit rule as commands.go.
// A failed authorization or transition leaves the plan state, reservations,
// journal and idempotency registry untouched (STATE_TRANSITIONS §5).
//
// Idempotency policy: every lifecycle command that appends a journal operation
// (authorize, reallocation authorization, resize, cancel of a reserved plan,
// complete) records its key with IdempotencyStore.Begin. Draft create/edit and
// cancellation of a still-DRAFT plan append nothing and therefore record no
// idempotency entry.
//
// Completion postings need data that is not part of ReservationTarget: the card
// account for a card purchase or repayment, and the card-payment envelope for a
// card purchase (Q1 reserves the spending envelope only). They are supplied to
// CompletePurchasePlan as explicit parameters; the plan target continues to
// describe the reservation holds only.
package domain

import "slices"

// PurchaseKind is the payment-method shape used when a planned operation
// completes. It is deliberately separate from PlanKind: PlanKind says what the
// plan means (purchase vs repayment, Q2), PurchaseKind says which posting-matrix
// row completes it.
type PurchaseKind string

const (
	// PurchaseKindDebit completes a planned debit purchase with
	// DebitPurchaseEffects(account, envelope, amount).
	PurchaseKindDebit PurchaseKind = "DEBIT"
	// PurchaseKindCash completes a planned cash-wallet purchase with the same
	// posting shape as a debit purchase.
	PurchaseKindCash PurchaseKind = "CASH"
	// PurchaseKindCard completes a planned card purchase with
	// CreditCardPurchaseEffects(card, spendingEnvelope, paymentEnvelope, amount).
	PurchaseKindCard PurchaseKind = "CARD"
	// PurchaseKindRepayment completes a planned card repayment with
	// CreditCardPaymentEffects(cashAccount, card, paymentEnvelope, amount) (Q2).
	PurchaseKindRepayment PurchaseKind = "REPAYMENT"
)

// IsValid reports whether the kind is one of the defined values.
func (k PurchaseKind) IsValid() bool {
	switch k {
	case PurchaseKindDebit, PurchaseKindCash, PurchaseKindCard, PurchaseKindRepayment:
		return true
	default:
		return false
	}
}

// String returns the stored kind text.
func (k PurchaseKind) String() string {
	return string(k)
}

// lifecycleOutcome is what a lifecycle command's prepare step produces after it
// has validated preconditions against a working ledger copy. commit installs the
// new lifecycle state; it runs only after every infallible commit step.
type lifecycleOutcome struct {
	effects Effects
	amount  *PositiveAmount
	source  TransactionSource
	commit  func()
}

// lifecycleCommand describes one lifecycle command for executeLifecycle.
type lifecycleCommand struct {
	opType OperationType
	// prepare validates state-dependent preconditions against working and
	// returns the effects, journal metadata and a state-installing closure. It
	// must never mutate live state.
	prepare func(working *LedgerState) (lifecycleOutcome, error)
	// journalOptional marks a command whose empty effect set must commit state
	// without appending a journal operation (cancelling a DRAFT plan). Such a
	// commit records no idempotency entry.
	journalOptional bool
}

// executeLifecycle is the atomic apply shared by plan and suggestion lifecycle
// commands. It mirrors CommandService.execute: idempotency replay is decided
// first, the command is validated against a cloned ledger, and the live journal,
// ledger, idempotency registry and lifecycle map are mutated only at commit.
//
// snapshot runs while the service mutex is still held, so it returns the state
// installed by this command (or the already-committed state on replay).
func executeLifecycle[T any](s *CommandService, env CommandEnvelope, cmd lifecycleCommand, snapshot func() T) (T, JournalOperation, error) {
	var zero T

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateCommandEnvelope(env); err != nil {
		return zero, JournalOperation{}, err
	}

	if record, err := s.idem.Lookup(env.Key); err == nil {
		if record.Fingerprint() != env.Fingerprint {
			return zero, JournalOperation{}, NewErrorf(
				CodeIdempotencyKeyPayloadMismatch,
				"idempotency key %q was reused with a different request fingerprint",
				env.Key,
			)
		}
		op, err := s.replay(record)
		if err != nil {
			return zero, JournalOperation{}, err
		}
		return snapshot(), op, nil
	} else if !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
		return zero, JournalOperation{}, err
	}

	working, err := s.ledger.Clone()
	if err != nil {
		return zero, JournalOperation{}, err
	}
	outcome, err := cmd.prepare(working)
	if err != nil {
		return zero, JournalOperation{}, err
	}
	if outcome.effects.IsEmpty() && cmd.journalOptional {
		outcome.commit()
		return snapshot(), JournalOperation{}, nil
	}
	if err := working.ApplyPostings(outcome.effects.Postings); err != nil {
		return zero, JournalOperation{}, err
	}
	if err := working.ApplyReservationChanges(outcome.effects.Reservations...); err != nil {
		return zero, JournalOperation{}, err
	}
	if err := working.CheckInvariants(); err != nil {
		return zero, JournalOperation{}, err
	}

	opID, err := s.nextOperationID()
	if err != nil {
		return zero, JournalOperation{}, err
	}
	op, err := NewJournalOperation(JournalOperationParams{
		ID:                 opID,
		Household:          s.household,
		Actor:              env.Actor,
		Type:               cmd.opType,
		Source:             outcome.source,
		EffectiveDate:      env.EffectiveDate,
		Currency:           s.currency,
		IdempotencyKey:     env.Key,
		RequestFingerprint: env.Fingerprint,
		Amount:             outcome.amount,
		Reason:             env.Reason,
		CreatedAt:          env.CommittedAt,
		CommittedAt:        env.CommittedAt,
		Effects:            outcome.effects,
	})
	if err != nil {
		return zero, JournalOperation{}, err
	}

	record, isNew, err := s.idem.Begin(env.Key, env.Fingerprint, op.ID(), env.CommittedAt)
	if err != nil {
		return zero, JournalOperation{}, err
	}
	if !isNew {
		replayed, err := s.replay(record)
		if err != nil {
			return zero, JournalOperation{}, err
		}
		return snapshot(), replayed, nil
	}
	if err := s.journal.Append(op); err != nil {
		return zero, JournalOperation{}, err
	}
	s.ledger = working
	outcome.commit()
	return snapshot(), op, nil
}

// GetPurchasePlan returns a snapshot of one plan, or
// ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD when the household has no such plan.
func (s *CommandService) GetPurchasePlan(id PlanId) (*PlannedOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, err := s.requirePlanLocked(id)
	if err != nil {
		return nil, err
	}
	return clonePlan(plan), nil
}

// PurchasePlans returns snapshots of every plan in deterministic id order.
func (s *CommandService) PurchasePlans() []*PlannedOperation {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]PlanId, 0, len(s.plans))
	for id := range s.plans {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	plans := make([]*PlannedOperation, 0, len(ids))
	for _, id := range ids {
		plans = append(plans, clonePlan(s.plans[id]))
	}
	return plans
}

// CreatePurchasePlan creates a DRAFT planned operation (purchase or repayment
// kind per Q2). No ledger effect, no reservation, no journal append.
func (s *CommandService) CreatePurchasePlan(actor UserId, id PlanId, kind PlanKind, target ReservationTarget, amount PositiveAmount) (*PlannedOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createPurchasePlanLocked(actor, id, kind, target, amount)
}

// createPurchasePlanLocked is CreatePurchasePlan for callers that already hold
// the service mutex (for example suggestion conversion).
func (s *CommandService) createPurchasePlanLocked(actor UserId, id PlanId, kind PlanKind, target ReservationTarget, amount PositiveAmount) (*PlannedOperation, error) {
	if actor.IsZero() {
		return nil, NewError(CodeInvalidValue, "plan actor id must not be empty")
	}
	if _, exists := s.plans[id]; exists {
		return nil, NewErrorf(CodeInvalidValue, "plan %q already exists", id)
	}
	if err := s.validatePlanTargetLocked(target); err != nil {
		return nil, err
	}
	plan, err := NewPlannedOperation(id, kind, target, amount)
	if err != nil {
		return nil, err
	}
	s.plans[id] = plan
	return clonePlan(plan), nil
}

// EditPurchasePlan updates a DRAFT's amount/target fields before authorization.
// Editing a non-DRAFT plan is INVALID_ENTITY_STATE.
func (s *CommandService) EditPurchasePlan(actor UserId, id PlanId, amount PositiveAmount, target ReservationTarget) (*PlannedOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actor.IsZero() {
		return nil, NewError(CodeInvalidValue, "plan actor id must not be empty")
	}
	current, err := s.requirePlanLocked(id)
	if err != nil {
		return nil, err
	}
	if current.State() != PlanStateDraft {
		return nil, NewErrorf(CodeInvalidEntityState, "cannot edit a plan in state %s", current.State())
	}
	if err := s.validatePlanTargetLocked(target); err != nil {
		return nil, err
	}
	replacement, err := NewPlannedOperation(id, current.Kind(), target, amount)
	if err != nil {
		return nil, err
	}
	next := clonePlan(current)
	next.target = replacement.target
	next.intendedAmount = replacement.intendedAmount
	next.authorizedAmount = replacement.authorizedAmount
	s.plans[id] = next
	return clonePlan(next), nil
}

// AuthorizePurchasePlan authorizes a DRAFT plan (normal or override mode),
// creates its reservation holds and appends the authorization journal operation
// atomically (POSTING_MATRIX "Purchase reservation created" row). Override
// requires env.Reason non-empty (OVERRIDE_REASON_REQUIRED).
func (s *CommandService) AuthorizePurchasePlan(env CommandEnvelope, id PlanId, mode AuthorizationMode) (*PlannedOperation, JournalOperation, error) {
	return executeLifecycle(s, env, lifecycleCommand{
		opType: OpReservationCreate,
		prepare: func(working *LedgerState) (lifecycleOutcome, error) {
			current, err := s.requirePlanLocked(id)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			reason, err := overrideReasonFor(mode, env.Reason)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			amount := current.IntendedAmount()
			outcome, err := AuthorizePurchase(working, current.Target(), amount, mode, reason)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			heldOnAccount := heldOnAccountFor(current.Target(), amount)
			next := clonePlan(current)
			if err := next.Authorize(outcome, amount, heldOnAccount); err != nil {
				return lifecycleOutcome{}, err
			}
			return lifecycleOutcome{
				effects: outcome.Effects,
				amount:  &amount,
				source:  TransactionSourcePlannedPurchase,
				commit:  func() { s.plans[id] = next },
			}, nil
		},
	}, func() *PlannedOperation { return clonePlan(s.plans[id]) })
}

// AuthorizePurchasePlanWithReallocation authorizes a DRAFT plan atomically with
// a reallocation from a source envelope (POSTING_MATRIX §3.2 reallocation-first).
// The reallocation postings and the reservation holds travel in the same
// journal operation, recorded as a reservation-create.
func (s *CommandService) AuthorizePurchasePlanWithReallocation(env CommandEnvelope, id PlanId, source EnvelopeId, delta PositiveAmount, mode AuthorizationMode) (*PlannedOperation, JournalOperation, error) {
	return executeLifecycle(s, env, lifecycleCommand{
		opType: OpReservationCreate,
		prepare: func(working *LedgerState) (lifecycleOutcome, error) {
			current, err := s.requirePlanLocked(id)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			reason, err := overrideReasonFor(mode, env.Reason)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			amount := current.IntendedAmount()
			outcome, err := AuthorizeWithReallocation(working, source, current.Target(), delta, amount, mode, reason)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			heldOnAccount := heldOnAccountFor(current.Target(), amount)
			next := clonePlan(current)
			if err := next.Authorize(outcome, amount, heldOnAccount); err != nil {
				return lifecycleOutcome{}, err
			}
			return lifecycleOutcome{
				effects: outcome.Effects,
				amount:  &amount,
				source:  TransactionSourcePlannedPurchase,
				commit:  func() { s.plans[id] = next },
			}, nil
		},
	}, func() *PlannedOperation { return clonePlan(s.plans[id]) })
}

// ResizePurchasePlan adjusts an authorized plan's amount. A normal reauthorization
// lands in RESERVED; a reasoned override lands in RESERVED_EXCEPTION. The
// RESERVED -> DRAFT choice the transition contract permits is not taken here:
// the plan keeps holding the (possibly reduced) reservation, so it stays
// authorized. A no-op amount change is INVALID_AMOUNT because it would append an
// effect-free journal operation.
func (s *CommandService) ResizePurchasePlan(env CommandEnvelope, id PlanId, newAmount PositiveAmount, mode AuthorizationMode) (*PlannedOperation, JournalOperation, error) {
	return executeLifecycle(s, env, lifecycleCommand{
		opType: OpReservationResize,
		prepare: func(working *LedgerState) (lifecycleOutcome, error) {
			current, err := s.requirePlanLocked(id)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			switch current.State() {
			case PlanStateReserved, PlanStateReservedException:
			default:
				return lifecycleOutcome{}, NewErrorf(CodeInvalidEntityState, "cannot resize a plan in state %s", current.State())
			}
			comparison, err := newAmount.Money().Compare(current.HeldAmount().Money())
			if err != nil {
				return lifecycleOutcome{}, err
			}
			if comparison == 0 {
				return lifecycleOutcome{}, NewError(CodeInvalidAmount, "resize amount must differ from the currently held amount")
			}
			reason, err := overrideReasonFor(mode, env.Reason)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			target := current.Target()
			outcome, err := ReauthorizeReservation(working, target, current.HeldAmount(), newAmount, mode, reason)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			newState := PlanStateReserved
			if outcome.Mode == AuthorizationModeEmergencyOverride {
				newState = PlanStateReservedException
			}
			heldOnAccount := heldOnAccountFor(target, newAmount)
			next := clonePlan(current)
			if err := next.Adjust(outcome, newState, newAmount, newAmount, heldOnAccount); err != nil {
				return lifecycleOutcome{}, err
			}
			return lifecycleOutcome{
				effects: outcome.Effects,
				amount:  &newAmount,
				source:  TransactionSourcePlannedPurchase,
				commit:  func() { s.plans[id] = next },
			}, nil
		},
	}, func() *PlannedOperation { return clonePlan(s.plans[id]) })
}

// CancelPurchasePlan cancels a non-completed plan, releases holds and appends
// the cancel operation (POSTING_MATRIX "Reservation cancelled"). Cancelling a
// DRAFT plan releases nothing and therefore appends no operation.
func (s *CommandService) CancelPurchasePlan(env CommandEnvelope, id PlanId) (*PlannedOperation, JournalOperation, error) {
	return executeLifecycle(s, env, lifecycleCommand{
		opType:          OpReservationCancel,
		journalOptional: true,
		prepare: func(_ *LedgerState) (lifecycleOutcome, error) {
			current, err := s.requirePlanLocked(id)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			next := clonePlan(current)
			if err := next.Cancel(); err != nil {
				return lifecycleOutcome{}, err
			}
			effects, err := PlanCancelEffects(next)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			var amount *PositiveAmount
			if current.HeldAmount().MinorUnits() > 0 {
				released := current.HeldAmount()
				amount = &released
			}
			return lifecycleOutcome{
				effects: effects,
				amount:  amount,
				source:  TransactionSourcePlannedPurchase,
				commit:  func() { s.plans[id] = next },
			}, nil
		},
	}, func() *PlannedOperation { return clonePlan(s.plans[id]) })
}

// CompletePurchasePlan completes an authorized plan: it validates the final
// amount against the authorization (AuthorizeCompletion), builds the
// payment-method postings, releases the holds and appends ONE journal operation
// atomically (POSTING_MATRIX "Reserved purchase completed"). Replaying the same
// key/fingerprint returns the original operation without posting again.
//
// card is the card-liability account and is required for CARD and REPAYMENT
// postings. paymentEnvelope is the card-payment envelope and is required for a
// CARD purchase; a repayment uses its plan target's payment envelope (Q2).
func (s *CommandService) CompletePurchasePlan(env CommandEnvelope, id PlanId, final PositiveAmount, purchaseKind PurchaseKind, card AccountId, paymentEnvelope EnvelopeId) (*PlannedOperation, JournalOperation, error) {
	return executeLifecycle(s, env, lifecycleCommand{
		opType: OpPurchaseComplete,
		prepare: func(working *LedgerState) (lifecycleOutcome, error) {
			current, err := s.requirePlanLocked(id)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			switch current.State() {
			case PlanStateReserved, PlanStateReservedException:
			default:
				return lifecycleOutcome{}, NewErrorf(CodeInvalidEntityState, "cannot complete a plan in state %s", current.State())
			}
			purchase, err := completionPostings(current, final, purchaseKind, card, paymentEnvelope)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			held := current.HeldAmount()
			heldOnAccount := current.HeldOnAccount()
			effects, err := AuthorizeCompletion(working, current.Target(), held, heldOnAccount, current.AuthorizedAmount(), final, purchase)
			if err != nil {
				return lifecycleOutcome{}, err
			}
			next := clonePlan(current)
			if err := next.Complete(final, held, heldOnAccount); err != nil {
				return lifecycleOutcome{}, err
			}
			return lifecycleOutcome{
				effects: effects,
				amount:  &final,
				source:  TransactionSourcePlannedPurchase,
				commit:  func() { s.plans[id] = next },
			}, nil
		},
	}, func() *PlannedOperation { return clonePlan(s.plans[id]) })
}

// completionPostings selects the payment-method posting shape for a completion
// and enforces consistency between the plan kind and the requested shape.
func completionPostings(plan *PlannedOperation, final PositiveAmount, purchaseKind PurchaseKind, card AccountId, paymentEnvelope EnvelopeId) (Effects, error) {
	if !purchaseKind.IsValid() {
		return Effects{}, NewErrorf(CodeInvalidValue, "unknown purchase kind %q", purchaseKind)
	}
	target := plan.Target()
	switch plan.Kind() {
	case PlanKindRepayment:
		if purchaseKind != PurchaseKindRepayment {
			return Effects{}, NewErrorf(CodeInvalidValue, "a repayment plan must complete with %q postings, got %q", PurchaseKindRepayment, purchaseKind)
		}
	case PlanKindPurchase:
		if purchaseKind == PurchaseKindRepayment {
			return Effects{}, NewErrorf(CodeInvalidValue, "a purchase plan cannot complete with %q postings", PurchaseKindRepayment)
		}
	default:
		return Effects{}, NewErrorf(CodeInvalidValue, "unknown plan kind %q", plan.Kind())
	}

	switch purchaseKind {
	case PurchaseKindDebit, PurchaseKindCash:
		if target.AccountID.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "debit/cash completion requires a source cash account in the plan target")
		}
		if !card.IsZero() || !paymentEnvelope.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "debit/cash completion does not use a card account or payment envelope")
		}
		return DebitPurchaseEffects(target.AccountID, target.EnvelopeID, final)
	case PurchaseKindCard:
		if !target.AccountID.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "a card purchase plan holds only its spending envelope (Q1)")
		}
		if card.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "card completion requires a card account")
		}
		if paymentEnvelope.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "card completion requires a card-payment envelope")
		}
		return CreditCardPurchaseEffects(card, target.EnvelopeID, paymentEnvelope, final)
	case PurchaseKindRepayment:
		if target.AccountID.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "a repayment plan requires a source cash account")
		}
		if card.IsZero() {
			return Effects{}, NewError(CodeInvalidValue, "repayment completion requires a card account")
		}
		if !paymentEnvelope.IsZero() && paymentEnvelope != target.EnvelopeID {
			return Effects{}, NewError(CodeInvalidValue, "repayment payment envelope must match the plan target")
		}
		return CreditCardPaymentEffects(target.AccountID, card, target.EnvelopeID, final)
	default:
		return Effects{}, NewErrorf(CodeInvalidValue, "unknown purchase kind %q", purchaseKind)
	}
}

// requirePlanLocked returns the stored plan or a not-found domain error. The
// caller must hold the service mutex.
func (s *CommandService) requirePlanLocked(id PlanId) (*PlannedOperation, error) {
	plan, known := s.plans[id]
	if !known {
		return nil, NewErrorf(CodeEntityNotFoundOrNotInHousehold, "plan %q is not part of this household", id)
	}
	return plan, nil
}

// validatePlanTargetLocked verifies the target references known household
// entities. The caller must hold the service mutex.
func (s *CommandService) validatePlanTargetLocked(target ReservationTarget) error {
	if target.EnvelopeID.IsZero() {
		return NewError(CodeInvalidValue, "plan target envelope id must not be empty")
	}
	if _, err := s.ledger.EnvelopeBalance(target.EnvelopeID); err != nil {
		return err
	}
	if !target.AccountID.IsZero() {
		if _, err := s.ledger.PostedAccountBalance(target.AccountID); err != nil {
			return err
		}
	}
	return nil
}

// heldOnAccountFor returns the account hold amount for a target: the full amount
// when the target holds a cash account (Q1/Q2), otherwise the zero amount.
func heldOnAccountFor(target ReservationTarget, amount PositiveAmount) PositiveAmount {
	if target.AccountID.IsZero() {
		return PositiveAmount{}
	}
	return amount
}

// overrideReasonFor resolves the reason for an authorization mode. A normal
// authorization ignores the envelope text; an override requires a nonblank
// reason and returns OVERRIDE_REASON_REQUIRED otherwise.
func overrideReasonFor(mode AuthorizationMode, raw string) (OverrideReason, error) {
	if mode != AuthorizationModeEmergencyOverride {
		return "", nil
	}
	return NewOverrideReason(raw)
}

// clonePlan returns an independent snapshot of a plan aggregate. Every field is
// a value, so a shallow copy is complete.
func clonePlan(plan *PlannedOperation) *PlannedOperation {
	if plan == nil {
		return nil
	}
	copied := *plan
	return &copied
}
