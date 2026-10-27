package domain

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// commandFixture is the shared household shape for command-service and command
// handler tests. It is deliberately larger than journalFixture: it has two
// eligible cash accounts, two excluded accounts, a card liability, and three
// envelopes so every handler family can be exercised without juggling ids.
type commandFixture struct {
	household HouseholdId
	actor     UserId
	currency  Currency
	checking  AccountId
	savings   AccountId
	excluded  AccountId
	excluded2 AccountId
	card      AccountId
	spend     EnvelopeId
	pay       EnvelopeId
	fee       EnvelopeId
	date      EffectiveDate
	committed RecordedAt
	service   *CommandService
}

func newCommandFixture(t *testing.T) *commandFixture {
	t.Helper()
	household, err := NewHouseholdId("household-cmd")
	if err != nil {
		t.Fatalf("NewHouseholdId: %v", err)
	}
	actor, err := NewUserId("user-cmd")
	if err != nil {
		t.Fatalf("NewUserId: %v", err)
	}
	date, err := ParseEffectiveDate("2026-02-01")
	if err != nil {
		t.Fatalf("ParseEffectiveDate: %v", err)
	}
	f := &commandFixture{
		household: household,
		actor:     actor,
		currency:  mustCurrency(t, "USD", 2),
		checking:  mustAccountIDForTest(t, "acct-checking"),
		savings:   mustAccountIDForTest(t, "acct-savings"),
		excluded:  mustAccountIDForTest(t, "acct-excluded"),
		excluded2: mustAccountIDForTest(t, "acct-excluded-2"),
		card:      mustAccountIDForTest(t, "acct-card"),
		spend:     mustEnvelopeIDForTest(t, "env-spend"),
		pay:       mustEnvelopeIDForTest(t, "env-pay"),
		fee:       mustEnvelopeIDForTest(t, "env-fee"),
		date:      date,
		committed: NewRecordedAt(time.Date(2026, time.February, 1, 9, 0, 0, 0, time.UTC)),
	}
	service, err := NewCommandService(household, f.currency, f.accountMetas(t), f.envelopeIDs())
	if err != nil {
		t.Fatalf("NewCommandService: %v", err)
	}
	f.service = service
	return f
}

func (f *commandFixture) accountMetas(t *testing.T) []AccountMeta {
	t.Helper()
	return []AccountMeta{
		mustAccountMetaForTest(t, f.checking, AccountKindChecking, true),
		mustAccountMetaForTest(t, f.savings, AccountKindSavings, true),
		mustAccountMetaForTest(t, f.excluded, AccountKindSavings, false),
		mustAccountMetaForTest(t, f.excluded2, AccountKindSavings, false),
		mustAccountMetaForTest(t, f.card, AccountKindCardLiability, false),
	}
}

func (f *commandFixture) envelopeIDs() []EnvelopeId {
	return []EnvelopeId{f.spend, f.pay, f.fee}
}

// envelope builds a command envelope with the fixture timestamp and a caller
// supplied idempotency key.
func (f *commandFixture) envelope(t *testing.T, key string) CommandEnvelope {
	t.Helper()
	return f.envelopeWithFingerprint(t, key, "abcdef0123456789")
}

// envelopeWithFingerprint builds a command envelope with an explicit key and
// fingerprint.
func (f *commandFixture) envelopeWithFingerprint(t *testing.T, key, fingerprint string) CommandEnvelope {
	t.Helper()
	validKey, err := NewIdempotencyKey(key)
	if err != nil {
		t.Fatalf("NewIdempotencyKey(%q): %v", key, err)
	}
	validFingerprint, err := NewRequestFingerprint(fingerprint)
	if err != nil {
		t.Fatalf("NewRequestFingerprint(%q): %v", fingerprint, err)
	}
	return CommandEnvelope{
		Actor:         f.actor,
		EffectiveDate: f.date,
		CommittedAt:   f.committed,
		Key:           validKey,
		Fingerprint:   validFingerprint,
	}
}

// ledger returns the current ledger snapshot, failing the test on error.
func (f *commandFixture) ledger(t *testing.T) *LedgerState {
	t.Helper()
	state, err := f.service.Ledger()
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	return state
}

// mustMoneyForCommandTest builds a Money for a fixture currency.
func mustMoneyForCommandTest(t *testing.T, minorUnits int64, currency Currency) Money {
	t.Helper()
	money, err := NewMoney(minorUnits, currency)
	if err != nil {
		t.Fatalf("NewMoney(%d): %v", minorUnits, err)
	}
	return money
}

// incomePlan builds the synthetic plan used by the command-service tests. It is
// intentionally independent of the command handlers added in commands.go so
// this commit's substrate can be tested on its own.
func (f *commandFixture) incomePlan(t *testing.T, minorUnits int64) commandPlan {
	t.Helper()
	amount := mustPositiveAmountForTest(t, minorUnits, f.currency)
	return commandPlan{
		opType: OpIncome,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			return IncomeReceivedEffects(f.checking, amount)
		},
	}
}

func TestNewCommandServiceValidation(t *testing.T) {
	f := newCommandFixture(t)

	if f.service.Household() != f.household {
		t.Errorf("Household() = %q, want %q", f.service.Household(), f.household)
	}
	if !f.service.Currency().Equal(f.currency) {
		t.Errorf("Currency() = %q, want %q", f.service.Currency(), f.currency)
	}
	if f.service.Version() != 0 {
		t.Errorf("Version() = %d, want 0", f.service.Version())
	}
	if _, err := f.service.Ledger(); err != nil {
		t.Errorf("Ledger() on fresh service: %v", err)
	}

	accounts := f.accountMetas(t)
	envelopes := f.envelopeIDs()
	tests := []struct {
		name      string
		household HouseholdId
		currency  Currency
		accounts  []AccountMeta
		envelopes []EnvelopeId
		wantCode  ErrorCode
	}{
		{name: "valid", household: f.household, currency: f.currency, accounts: accounts, envelopes: envelopes},
		{name: "zero household", household: "", currency: f.currency, accounts: accounts, envelopes: envelopes, wantCode: CodeInvalidValue},
		{name: "zero currency", household: f.household, currency: Currency{}, accounts: accounts, envelopes: envelopes, wantCode: CodeInvalidValue},
		{
			name:      "duplicate account",
			household: f.household,
			currency:  f.currency,
			accounts:  []AccountMeta{accounts[0], accounts[0]},
			envelopes: envelopes,
			wantCode:  CodeInvalidValue,
		},
		{
			name:      "eligible card liability",
			household: f.household,
			currency:  f.currency,
			accounts:  []AccountMeta{{ID: f.card, Kind: AccountKindCardLiability, BudgetEligible: true}},
			envelopes: nil,
			wantCode:  CodeInvalidValue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCommandService(tt.household, tt.currency, tt.accounts, tt.envelopes)
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("NewCommandService: %v", err)
				}
				return
			}
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("NewCommandService error = %v, want code %s", err, tt.wantCode)
			}
		})
	}
}

func TestCommandServiceFailingCommandIsAtomic(t *testing.T) {
	f := newCommandFixture(t)
	env := f.envelope(t, "key-atomic-failure")

	// A currency-mismatched effect set fails during working-copy validation.
	eur := mustCurrency(t, "EUR", 2)
	amount := mustPositiveAmountForTest(t, 1_000, eur)
	delta, err := NewSignedMoneyDelta(1_000, eur)
	if err != nil {
		t.Fatalf("NewSignedMoneyDelta: %v", err)
	}
	plan := commandPlan{
		opType: OpIncome,
		source: TransactionSourceManual,
		amount: &amount,
		build: func(_ *LedgerState) (Effects, error) {
			postings := PostingSet{}
			if addErr := postings.AddAccount(f.checking, delta); addErr != nil {
				return Effects{}, addErr
			}
			return Effects{Postings: postings}, nil
		},
	}

	if _, err := f.service.execute(env, plan); !HasCode(err, CodeCurrencyMismatch) {
		t.Fatalf("execute error = %v, want CURRENCY_MISMATCH", err)
	}
	if f.service.Version() != 0 {
		t.Errorf("Version() = %d after failed command, want 0", f.service.Version())
	}
	state := f.ledger(t)
	balance, err := state.PostedAccountBalance(f.checking)
	if err != nil {
		t.Fatalf("PostedAccountBalance: %v", err)
	}
	if !balance.IsZero() {
		t.Errorf("checking balance = %d after failed command, want 0", balance.MinorUnits())
	}
	if err := state.CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants after failed command: %v", err)
	}
	if _, err := f.service.Idempotency().Lookup(env.Key); !HasCode(err, CodeEntityNotFoundOrNotInHousehold) {
		t.Errorf("idempotency lookup after failed command = %v, want ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD", err)
	}
}

func TestCommandServiceIdempotentReplay(t *testing.T) {
	f := newCommandFixture(t)
	env := f.envelope(t, "key-replay")
	plan := f.incomePlan(t, 1_000)

	first, err := f.service.execute(env, plan)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	second, err := f.service.execute(env, plan)
	if err != nil {
		t.Fatalf("replay execute: %v", err)
	}
	if first.ID() != second.ID() {
		t.Errorf("replay operation id = %q, want original %q", second.ID(), first.ID())
	}
	if f.service.Version() != 1 {
		t.Errorf("Version() = %d after replay, want 1", f.service.Version())
	}
	balance, err := f.ledger(t).PostedAccountBalance(f.checking)
	if err != nil {
		t.Fatalf("PostedAccountBalance: %v", err)
	}
	if balance.MinorUnits() != 1_000 {
		t.Errorf("checking balance = %d after replay, want 1000 (not doubled)", balance.MinorUnits())
	}
	record, err := f.service.Idempotency().Lookup(env.Key)
	if err != nil {
		t.Fatalf("Idempotency Lookup: %v", err)
	}
	if record.OperationID() != first.ID() {
		t.Errorf("idempotency record operation = %q, want %q", record.OperationID(), first.ID())
	}
}

func TestCommandServiceFingerprintMismatch(t *testing.T) {
	f := newCommandFixture(t)
	env := f.envelope(t, "key-mismatch")
	plan := f.incomePlan(t, 1_000)
	if _, err := f.service.execute(env, plan); err != nil {
		t.Fatalf("first execute: %v", err)
	}

	conflicting := f.envelopeWithFingerprint(t, "key-mismatch", "1234567890abcdef")
	if _, err := f.service.execute(conflicting, plan); !HasCode(err, CodeIdempotencyKeyPayloadMismatch) {
		t.Fatalf("execute with mismatched fingerprint error = %v, want IDEMPOTENCY_KEY_PAYLOAD_MISMATCH", err)
	}
	if f.service.Version() != 1 {
		t.Errorf("Version() = %d after mismatch, want 1", f.service.Version())
	}
	balance, err := f.ledger(t).PostedAccountBalance(f.checking)
	if err != nil {
		t.Fatalf("PostedAccountBalance: %v", err)
	}
	if balance.MinorUnits() != 1_000 {
		t.Errorf("checking balance = %d after mismatch, want unchanged 1000", balance.MinorUnits())
	}
}

func TestCommandServiceConcurrentDistinctKeys(t *testing.T) {
	f := newCommandFixture(t)
	const commands = 25

	plans := make([]commandPlan, commands)
	envelopes := make([]CommandEnvelope, commands)
	for i := 0; i < commands; i++ {
		plans[i] = f.incomePlan(t, 100)
		envelopes[i] = f.envelope(t, fmt.Sprintf("key-concurrent-%d", i))
	}

	var wg sync.WaitGroup
	errs := make([]error, commands)
	for i := 0; i < commands; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, errs[index] = f.service.execute(envelopes[index], plans[index])
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("command %d failed: %v", i, err)
		}
	}
	if got := f.service.Version(); got != commands {
		t.Errorf("Version() = %d, want %d", got, commands)
	}
	balance, err := f.ledger(t).PostedAccountBalance(f.checking)
	if err != nil {
		t.Fatalf("PostedAccountBalance: %v", err)
	}
	if balance.MinorUnits() != int64(commands)*100 {
		t.Errorf("checking balance = %d, want %d", balance.MinorUnits(), commands*100)
	}
	if err := f.ledger(t).CheckInvariants(); err != nil {
		t.Errorf("CheckInvariants after concurrent commands: %v", err)
	}
}

func TestLedgerStateCloneIsIndependent(t *testing.T) {
	f := newCommandFixture(t)
	original := f.ledger(t)
	cloned, err := original.Clone()
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	amount := mustPositiveAmountForTest(t, 500, f.currency)
	effects, err := IncomeReceivedEffects(f.checking, amount)
	if err != nil {
		t.Fatalf("IncomeReceivedEffects: %v", err)
	}
	if err := cloned.ApplyPostings(effects.Postings); err != nil {
		t.Fatalf("ApplyPostings on clone: %v", err)
	}

	originalBalance, err := original.PostedAccountBalance(f.checking)
	if err != nil {
		t.Fatalf("original PostedAccountBalance: %v", err)
	}
	clonedBalance, err := cloned.PostedAccountBalance(f.checking)
	if err != nil {
		t.Fatalf("cloned PostedAccountBalance: %v", err)
	}
	if !originalBalance.IsZero() {
		t.Errorf("original balance = %d, want 0", originalBalance.MinorUnits())
	}
	if clonedBalance.MinorUnits() != 500 {
		t.Errorf("cloned balance = %d, want 500", clonedBalance.MinorUnits())
	}

	var nilLedger *LedgerState
	if _, err := nilLedger.Clone(); !HasCode(err, CodeInvalidValue) {
		t.Errorf("nil Clone error = %v, want INVALID_VALUE", err)
	}
}
