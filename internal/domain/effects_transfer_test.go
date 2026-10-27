package domain

import "testing"

// transferEffectsFixture is the shared ledger used to verify the budget effect
// of transfers: two eligible accounts (checking and savings), two excluded
// accounts, and two envelopes. It starts with eligible cash and allocated
// envelope funds so that Ready-to-assign is strictly positive.
type transferEffectsFixture struct {
	currency  Currency
	checking  AccountId
	savings   AccountId
	excluded  AccountId
	excluded2 AccountId
	spend     EnvelopeId
	pay       EnvelopeId
}

func newTransferEffectsFixture(t *testing.T) *transferEffectsFixture {
	t.Helper()
	return &transferEffectsFixture{
		currency:  mustCurrency(t, "USD", 2),
		checking:  mustAccountIDForTest(t, "acct-checking"),
		savings:   mustAccountIDForTest(t, "acct-savings"),
		excluded:  mustAccountIDForTest(t, "acct-excluded"),
		excluded2: mustAccountIDForTest(t, "acct-excluded-2"),
		spend:     mustEnvelopeIDForTest(t, "env-spend"),
		pay:       mustEnvelopeIDForTest(t, "env-pay"),
	}
}

// newLedger builds the ledger and posts the opening state: checking +10,000,
// savings +5,000, excluded +2,000, spend envelope +3,000, pay envelope +1,000.
// Budgetable cash is 15,000, the envelope sum is 4,000, and Ready-to-assign is
// therefore 11,000.
func (f *transferEffectsFixture) newLedger(t *testing.T) *LedgerState {
	t.Helper()
	state := mustLedgerForTest(t, f.currency,
		[]AccountMeta{
			mustAccountMetaForTest(t, f.checking, AccountKindChecking, true),
			mustAccountMetaForTest(t, f.savings, AccountKindSavings, true),
			mustAccountMetaForTest(t, f.excluded, AccountKindSavings, false),
			mustAccountMetaForTest(t, f.excluded2, AccountKindSavings, false),
		},
		[]EnvelopeId{f.spend, f.pay},
	)
	opening := PostingSet{}
	addPostingForTest(t, &opening, f.currency, f.checking, 10_000)
	addPostingForTest(t, &opening, f.currency, f.savings, 5_000)
	addPostingForTest(t, &opening, f.currency, f.excluded, 2_000)
	if err := opening.AddEnvelope(f.spend, mustSignedDeltaForTest(t, 3_000, f.currency)); err != nil {
		t.Fatalf("AddEnvelope(%q, 3000): %v", f.spend, err)
	}
	if err := opening.AddEnvelope(f.pay, mustSignedDeltaForTest(t, 1_000, f.currency)); err != nil {
		t.Fatalf("AddEnvelope(%q, 1000): %v", f.pay, err)
	}
	if err := state.ApplyPostings(opening); err != nil {
		t.Fatalf("ApplyPostings(opening): %v", err)
	}
	return state
}

func addPostingForTest(t *testing.T, postings *PostingSet, currency Currency, id AccountId, minorUnits int64) {
	t.Helper()
	if err := postings.AddAccount(id, mustSignedDeltaForTest(t, minorUnits, currency)); err != nil {
		t.Fatalf("AddAccount(%q, %d): %v", id, minorUnits, err)
	}
}

func TestTransferEffects(t *testing.T) {
	f := newTransferEffectsFixture(t)
	usd := f.currency
	zeroReady := mustMoney(t, 0, usd)

	tests := []struct {
		name                 string
		effects              func(t *testing.T) (Effects, error)
		wantAccounts         map[AccountId]int64
		wantEnvelopes        map[EnvelopeId]int64
		wantAccountBalances  map[AccountId]int64
		wantEnvelopeBalances map[EnvelopeId]int64
		wantBudgetable       int64
		wantReady            int64
		wantEnvelopeSum      int64
	}{
		{
			name: "eligible to eligible",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleToEligibleEffects(f.checking, f.savings, mustPositiveAmountForTest(t, 1_000, usd))
			},
			wantAccounts:         map[AccountId]int64{f.checking: -1_000, f.savings: 1_000},
			wantAccountBalances:  map[AccountId]int64{f.checking: 9_000, f.savings: 6_000, f.excluded: 2_000, f.excluded2: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 3_000, f.pay: 1_000},
			wantBudgetable:       15_000,
			wantReady:            11_000,
			wantEnvelopeSum:      4_000,
		},
		{
			name: "excluded to eligible",
			effects: func(t *testing.T) (Effects, error) {
				return TransferExcludedToEligibleEffects(f.excluded, f.checking, mustPositiveAmountForTest(t, 1_000, usd))
			},
			wantAccounts:         map[AccountId]int64{f.excluded: -1_000, f.checking: 1_000},
			wantAccountBalances:  map[AccountId]int64{f.checking: 11_000, f.savings: 5_000, f.excluded: 1_000, f.excluded2: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 3_000, f.pay: 1_000},
			wantBudgetable:       16_000,
			wantReady:            12_000,
			wantEnvelopeSum:      4_000,
		},
		{
			name: "eligible to excluded funded from ready and envelopes",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 2_000, usd), FundingSource{
					FromReady:     mustMoney(t, 1_200, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, 800, usd)}},
				})
			},
			wantAccounts:         map[AccountId]int64{f.checking: -2_000, f.excluded: 2_000},
			wantEnvelopes:        map[EnvelopeId]int64{f.spend: -800},
			wantAccountBalances:  map[AccountId]int64{f.checking: 8_000, f.savings: 5_000, f.excluded: 4_000, f.excluded2: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 2_200, f.pay: 1_000},
			wantBudgetable:       13_000,
			wantReady:            9_800,
			wantEnvelopeSum:      3_200,
		},
		{
			name: "eligible to excluded funded entirely from ready",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 1_000, usd), FundingSource{
					FromReady: mustMoney(t, 1_000, usd),
				})
			},
			wantAccounts:         map[AccountId]int64{f.checking: -1_000, f.excluded: 1_000},
			wantAccountBalances:  map[AccountId]int64{f.checking: 9_000, f.savings: 5_000, f.excluded: 3_000, f.excluded2: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 3_000, f.pay: 1_000},
			wantBudgetable:       14_000,
			wantReady:            10_000,
			wantEnvelopeSum:      4_000,
		},
		{
			name: "eligible to excluded funded entirely from envelopes",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.savings, f.excluded, mustPositiveAmountForTest(t, 1_000, usd), FundingSource{
					FromReady:     zeroReady,
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.pay, Amount: mustMoney(t, 1_000, usd)}},
				})
			},
			wantAccounts:         map[AccountId]int64{f.savings: -1_000, f.excluded: 1_000},
			wantEnvelopes:        map[EnvelopeId]int64{f.pay: -1_000},
			wantAccountBalances:  map[AccountId]int64{f.checking: 10_000, f.savings: 4_000, f.excluded: 3_000, f.excluded2: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 3_000, f.pay: 0},
			wantBudgetable:       14_000,
			wantReady:            11_000,
			wantEnvelopeSum:      3_000,
		},
		{
			name: "wholly excluded",
			effects: func(t *testing.T) (Effects, error) {
				return TransferExcludedToExcludedEffects(f.excluded, f.excluded2, mustPositiveAmountForTest(t, 500, usd))
			},
			wantAccounts:         map[AccountId]int64{f.excluded: -500, f.excluded2: 500},
			wantAccountBalances:  map[AccountId]int64{f.checking: 10_000, f.savings: 5_000, f.excluded: 1_500, f.excluded2: 500},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 3_000, f.pay: 1_000},
			wantBudgetable:       15_000,
			wantReady:            11_000,
			wantEnvelopeSum:      4_000,
		},
		{
			name: "eligible to excluded leaves ready negative",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 12_000, usd), FundingSource{
					FromReady: mustMoney(t, 12_000, usd),
				})
			},
			wantAccounts:         map[AccountId]int64{f.checking: -12_000, f.excluded: 12_000},
			wantAccountBalances:  map[AccountId]int64{f.checking: -2_000, f.savings: 5_000, f.excluded: 14_000, f.excluded2: 0},
			wantEnvelopeBalances: map[EnvelopeId]int64{f.spend: 3_000, f.pay: 1_000},
			wantBudgetable:       3_000,
			wantReady:            -1_000,
			wantEnvelopeSum:      4_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effects, err := tt.effects(t)
			if err != nil {
				t.Fatalf("effects: %v", err)
			}
			assertEffectsShape(t, effects, tt.wantAccounts, tt.wantEnvelopes)

			state := f.newLedger(t)
			if err := state.ApplyPostings(effects.Postings); err != nil {
				t.Fatalf("ApplyPostings: %v", err)
			}

			assertAccountBalances(t, state, tt.wantAccountBalances)
			assertEnvelopeBalances(t, state, tt.wantEnvelopeBalances)

			assertMoney(t, (*LedgerState).BudgetableCash, state, tt.wantBudgetable)
			assertMoney(t, (*LedgerState).ReadyToAssign, state, tt.wantReady)
			assertMoney(t, (*LedgerState).SumEnvelopeBalances, state, tt.wantEnvelopeSum)
			if err := state.CheckInvariants(); err != nil {
				t.Errorf("CheckInvariants: %v", err)
			}
		})
	}
}

func TestTransferEffectsRejections(t *testing.T) {
	f := newTransferEffectsFixture(t)
	usd := f.currency

	tests := []struct {
		name     string
		effects  func(t *testing.T) (Effects, error)
		wantCode ErrorCode
	}{
		{
			name: "eligible to eligible same account",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleToEligibleEffects(f.checking, f.checking, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "excluded to eligible same account",
			effects: func(t *testing.T) (Effects, error) {
				return TransferExcludedToEligibleEffects(f.excluded, f.excluded, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "eligible to excluded same account",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.checking, mustPositiveAmountForTest(t, 100, usd), FundingSource{
					FromReady: mustMoney(t, 100, usd),
				})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "wholly excluded same account",
			effects: func(t *testing.T) (Effects, error) {
				return TransferExcludedToExcludedEffects(f.excluded, f.excluded, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "zero source account",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleToEligibleEffects("", f.savings, mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "zero destination account",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleToEligibleEffects(f.checking, "", mustPositiveAmountForTest(t, 100, usd))
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "funding total below amount",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 1_000, usd), FundingSource{
					FromReady:     mustMoney(t, 500, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, 400, usd)}},
				})
			},
			wantCode: CodeInvalidAmount,
		},
		{
			name: "funding total above amount",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 1_000, usd), FundingSource{
					FromReady: mustMoney(t, 1_100, usd),
				})
			},
			wantCode: CodeInvalidAmount,
		},
		{
			name: "zero-value ready money",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 1_000, usd), FundingSource{
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, 1_000, usd)}},
				})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "negative ready money",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 1_000, usd), FundingSource{
					FromReady:     mustMoney(t, -1_000, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, 2_000, usd)}},
				})
			},
			wantCode: CodeInvalidAmount,
		},
		{
			name: "zero funding envelope id",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 100, usd), FundingSource{
					FromReady:     mustMoney(t, 0, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: "", Amount: mustMoney(t, 100, usd)}},
				})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "zero-value funding amount",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 100, usd), FundingSource{
					FromReady:     mustMoney(t, 0, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: Money{}}},
				})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "zero funding amount",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 100, usd), FundingSource{
					FromReady:     mustMoney(t, 100, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, 0, usd)}},
				})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "negative funding amount",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 100, usd), FundingSource{
					FromReady:     mustMoney(t, 200, usd),
					FromEnvelopes: []EnvelopeFunding{{EnvelopeID: f.spend, Amount: mustMoney(t, -100, usd)}},
				})
			},
			wantCode: CodeInvalidValue,
		},
		{
			name: "duplicate funding envelope",
			effects: func(t *testing.T) (Effects, error) {
				return TransferEligibleOutToExcludedEffects(f.checking, f.excluded, mustPositiveAmountForTest(t, 200, usd), FundingSource{
					FromReady: mustMoney(t, 0, usd),
					FromEnvelopes: []EnvelopeFunding{
						{EnvelopeID: f.spend, Amount: mustMoney(t, 100, usd)},
						{EnvelopeID: f.spend, Amount: mustMoney(t, 100, usd)},
					},
				})
			},
			wantCode: CodeInvalidValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effects, err := tt.effects(t)
			if !HasCode(err, tt.wantCode) {
				t.Fatalf("effects error = %v, want code %s", err, tt.wantCode)
			}
			if !effects.IsEmpty() {
				t.Errorf("rejected effects = %+v, want empty", effects)
			}
		})
	}
}

// assertAccountBalances compares every expected posted account balance.
func assertAccountBalances(t *testing.T, state *LedgerState, wants map[AccountId]int64) {
	t.Helper()
	for id, want := range wants {
		assertMoney(t, state.PostedAccountBalance, id, want)
	}
}

// assertEnvelopeBalances compares every expected posted envelope balance.
func assertEnvelopeBalances(t *testing.T, state *LedgerState, wants map[EnvelopeId]int64) {
	t.Helper()
	for id, want := range wants {
		assertMoney(t, state.EnvelopeBalance, id, want)
	}
}
