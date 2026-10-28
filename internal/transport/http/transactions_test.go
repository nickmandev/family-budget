package httpapi

import (
	"net/http"
	"testing"
)

func setupBudget(t *testing.T) (*resourceHarness, householdDTO, accountDTO, envelopeDTO) {
	t.Helper()
	harness := newResourceServer(t)
	household := harness.createHousehold(t)
	account := harness.createAccount(t, household.ID, "CHECKING", true)
	envelope := harness.createEnvelope(t, household.ID, "Spending")
	opening := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/accounts/"+account.ID+"/opening-balance",
		`{"effective_date":"2026-10-01","delta_minor":100000}`, idempotencyHeaders("opening"))
	if opening.Code != http.StatusCreated {
		t.Fatalf("opening balance: %d %s", opening.Code, opening.Body.String())
	}
	allocation := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/allocations",
		`{"effective_date":"2026-10-01","envelope_id":"`+envelope.ID+`","amount_minor":30000}`, idempotencyHeaders("allocate"))
	if allocation.Code != http.StatusCreated {
		t.Fatalf("allocation: %d %s", allocation.Code, allocation.Body.String())
	}
	return harness, household, account, envelope
}

func TestTransactionPurchaseAndCursorPagination(t *testing.T) {
	harness, household, account, envelope := setupBudget(t)

	for i, key := range []string{"income-a", "income-b", "income-c"} {
		body := `{"effective_date":"2026-10-0` + string(rune('2'+i)) + `","account_id":"` + account.ID + `","amount_minor":1000}`
		response := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/transactions/income", body, idempotencyHeaders(key))
		if response.Code != http.StatusCreated {
			t.Fatalf("income %s: %d %s", key, response.Code, response.Body.String())
		}
	}

	purchase := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/transactions/purchases",
		`{"effective_date":"2026-10-05","account_id":"`+account.ID+`","envelope_id":"`+envelope.ID+`","amount_minor":10000}`, idempotencyHeaders("purchase-1"))
	if purchase.Code != http.StatusCreated {
		t.Fatalf("purchase: %d %s", purchase.Code, purchase.Body.String())
	}
	var purchased DataResponse[operationDTO]
	decodeResponse(t, purchase, &purchased)
	if purchased.Data.Type != "DEBIT_PURCHASE" || purchased.Data.AmountMinor == nil || *purchased.Data.AmountMinor != 10000 {
		t.Fatalf("purchase operation = %+v", purchased.Data)
	}

	first := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID+"/transactions?limit=2", "", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first page: %d %s", first.Code, first.Body.String())
	}
	var pageOne ListResponse[operationDTO]
	decodeResponse(t, first, &pageOne)
	if len(pageOne.Items) != 2 || pageOne.NextCursor == nil {
		t.Fatalf("first page = %+v", pageOne)
	}

	second := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID+"/transactions?limit=2&cursor="+*pageOne.NextCursor, "", nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second page: %d %s", second.Code, second.Body.String())
	}
	var pageTwo ListResponse[operationDTO]
	decodeResponse(t, second, &pageTwo)
	if len(pageTwo.Items) == 0 {
		t.Fatal("second page is empty")
	}
	seen := make(map[string]bool)
	for _, op := range append(pageOne.Items, pageTwo.Items...) {
		if seen[op.ID] {
			t.Fatalf("operation %s repeated across pages", op.ID)
		}
		seen[op.ID] = true
	}

	filtered := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID+"/transactions?type=DEBIT_PURCHASE", "", nil)
	if filtered.Code != http.StatusOK {
		t.Fatalf("filtered: %d %s", filtered.Code, filtered.Body.String())
	}
	var purchases ListResponse[operationDTO]
	decodeResponse(t, filtered, &purchases)
	if len(purchases.Items) != 1 || purchases.Items[0].Type != "DEBIT_PURCHASE" {
		t.Fatalf("filtered items = %+v", purchases.Items)
	}

	balances := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID+"/balances", "", nil)
	var report DataResponse[balancesDTO]
	decodeResponse(t, balances, &report)
	if report.Data.Envelopes[0].BalanceMinor != 20000 {
		t.Fatalf("envelope balance = %d, want 20000", report.Data.Envelopes[0].BalanceMinor)
	}
	if report.Data.Accounts[0].PostedMinor != 93000 {
		t.Fatalf("account posted = %d, want 93000", report.Data.Accounts[0].PostedMinor)
	}
}

func TestPlanLifecycle(t *testing.T) {
	harness, household, account, envelope := setupBudget(t)

	created := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/plans",
		`{"kind":"PURCHASE","target_envelope_id":"`+envelope.ID+`","target_account_id":"`+account.ID+`","amount_minor":5000}`, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create plan: %d %s", created.Code, created.Body.String())
	}
	var planResponse DataResponse[planDTO]
	decodeResponse(t, created, &planResponse)
	if planResponse.Data.State != "DRAFT" {
		t.Fatalf("plan state = %s", planResponse.Data.State)
	}

	authorized := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/plans/"+planResponse.Data.ID+"/authorize",
		`{"effective_date":"2026-10-02","mode":"NORMAL"}`, idempotencyHeaders("plan-authorize"))
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorize plan: %d %s", authorized.Code, authorized.Body.String())
	}
	var authorizedResponse DataResponse[struct {
		Plan      planDTO      `json:"plan"`
		Operation operationDTO `json:"operation"`
	}]
	decodeResponse(t, authorized, &authorizedResponse)
	if authorizedResponse.Data.Plan.State != "RESERVED" {
		t.Fatalf("authorized plan = %+v", authorizedResponse.Data.Plan)
	}

	completed := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/plans/"+planResponse.Data.ID+"/complete",
		`{"effective_date":"2026-10-03","final_amount_minor":5000,"purchase_kind":"DEBIT"}`, idempotencyHeaders("plan-complete"))
	if completed.Code != http.StatusOK {
		t.Fatalf("complete plan: %d %s", completed.Code, completed.Body.String())
	}
	var completedResponse DataResponse[struct {
		Plan      planDTO      `json:"plan"`
		Operation operationDTO `json:"operation"`
	}]
	decodeResponse(t, completed, &completedResponse)
	if completedResponse.Data.Plan.State != "COMPLETED" {
		t.Fatalf("completed plan = %+v", completedResponse.Data.Plan)
	}
	if completedResponse.Data.Operation.Type != "PURCHASE_COMPLETE" {
		t.Fatalf("completion operation = %+v", completedResponse.Data.Operation)
	}
}

func TestSuggestionLifecycle(t *testing.T) {
	harness, household, account, envelope := setupBudget(t)

	created := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/suggestions",
		`{"source":"EXTERNAL_ENTRY","amount_minor":2500,"merchant":"Cafe"}`, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create suggestion: %d %s", created.Code, created.Body.String())
	}
	var suggestion DataResponse[suggestionDTO]
	decodeResponse(t, created, &suggestion)

	edited := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/suggestions/"+suggestion.Data.ID+"/edit",
		`{"amount_minor":2000,"merchant":"Cafe Updated"}`, nil)
	if edited.Code != http.StatusOK {
		t.Fatalf("edit suggestion: %d %s", edited.Code, edited.Body.String())
	}

	confirmed := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/suggestions/"+suggestion.Data.ID+"/confirm",
		`{"effective_date":"2026-10-04","purchase_kind":"DEBIT","account_id":"`+account.ID+`","envelope_id":"`+envelope.ID+`"}`, idempotencyHeaders("suggestion-confirm"))
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm suggestion: %d %s", confirmed.Code, confirmed.Body.String())
	}
	var confirmedResponse DataResponse[struct {
		Suggestion suggestionDTO `json:"suggestion"`
		Operation  operationDTO  `json:"operation"`
	}]
	decodeResponse(t, confirmed, &confirmedResponse)
	if confirmedResponse.Data.Suggestion.State != "CONFIRMED" {
		t.Fatalf("confirmed suggestion = %+v", confirmedResponse.Data.Suggestion)
	}
	if confirmedResponse.Data.Operation.Type != "EXTERNAL_TRANSACTION" {
		t.Fatalf("confirmed operation = %+v", confirmedResponse.Data.Operation)
	}

	replay := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/suggestions/"+suggestion.Data.ID+"/confirm",
		`{"effective_date":"2026-10-04","purchase_kind":"DEBIT","account_id":"`+account.ID+`","envelope_id":"`+envelope.ID+`"}`, idempotencyHeaders("suggestion-confirm"))
	if replay.Code != http.StatusOK {
		t.Fatalf("confirm replay: %d %s", replay.Code, replay.Body.String())
	}
	var replayResponse DataResponse[struct {
		Suggestion suggestionDTO `json:"suggestion"`
		Operation  operationDTO  `json:"operation"`
	}]
	decodeResponse(t, replay, &replayResponse)
	if replayResponse.Data.Operation.ID != confirmedResponse.Data.Operation.ID {
		t.Fatalf("replay operation = %s, want %s", replayResponse.Data.Operation.ID, confirmedResponse.Data.Operation.ID)
	}
}

func TestPlanCancelAndReplay(t *testing.T) {
	harness, household, account, envelope := setupBudget(t)

	created := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/plans",
		`{"kind":"PURCHASE","target_envelope_id":"`+envelope.ID+`","target_account_id":"`+account.ID+`","amount_minor":1000}`, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create plan: %d %s", created.Code, created.Body.String())
	}
	var planResponse DataResponse[planDTO]
	decodeResponse(t, created, &planResponse)

	authorized := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/plans/"+planResponse.Data.ID+"/authorize",
		`{"effective_date":"2026-10-02","mode":"NORMAL"}`, idempotencyHeaders("cancel-authorize"))
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorize plan: %d %s", authorized.Code, authorized.Body.String())
	}

	cancelled := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/plans/"+planResponse.Data.ID+"/cancel",
		`{"effective_date":"2026-10-03","reason":"no longer needed"}`, idempotencyHeaders("cancel-plan"))
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel plan: %d %s", cancelled.Code, cancelled.Body.String())
	}
	var cancelledResponse DataResponse[struct {
		Plan      planDTO       `json:"plan"`
		Operation *operationDTO `json:"operation"`
	}]
	decodeResponse(t, cancelled, &cancelledResponse)
	if cancelledResponse.Data.Plan.State != "CANCELLED" {
		t.Fatalf("cancelled plan = %+v", cancelledResponse.Data.Plan)
	}
	if cancelledResponse.Data.Operation == nil || cancelledResponse.Data.Operation.Type != "RESERVATION_CANCEL" {
		t.Fatalf("cancel operation = %+v", cancelledResponse.Data.Operation)
	}

	replay := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/plans/"+planResponse.Data.ID+"/cancel",
		`{"effective_date":"2026-10-03","reason":"no longer needed"}`, idempotencyHeaders("cancel-plan"))
	if replay.Code != http.StatusOK {
		t.Fatalf("cancel replay: %d %s", replay.Code, replay.Body.String())
	}
	var replayResponse DataResponse[struct {
		Plan      planDTO       `json:"plan"`
		Operation *operationDTO `json:"operation"`
	}]
	decodeResponse(t, replay, &replayResponse)
	if replayResponse.Data.Operation == nil || replayResponse.Data.Operation.ID != cancelledResponse.Data.Operation.ID {
		t.Fatalf("replay operation = %+v", replayResponse.Data.Operation)
	}
}

func TestCardPurchaseUsesCardPostingShape(t *testing.T) {
	harness, household, _, envelope := setupBudget(t)
	card := harness.createAccount(t, household.ID, "CREDIT_CARD_LIABILITY", false)
	payment := harness.createEnvelope(t, household.ID, "Card payment")

	response := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/transactions/purchases",
		`{"effective_date":"2026-10-03","purchase_kind":"CARD","account_id":"`+card.ID+`","envelope_id":"`+envelope.ID+`","payment_envelope_id":"`+payment.ID+`","amount_minor":5000}`, idempotencyHeaders("card-purchase"))
	if response.Code != http.StatusCreated {
		t.Fatalf("card purchase: %d %s", response.Code, response.Body.String())
	}
	var operation DataResponse[operationDTO]
	decodeResponse(t, response, &operation)
	if operation.Data.Type != "CARD_PURCHASE" {
		t.Fatalf("card purchase type = %s", operation.Data.Type)
	}
}
