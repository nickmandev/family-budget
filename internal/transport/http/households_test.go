package httpapi

import (
	"net/http"
	"testing"
)

func TestHouseholdAndBudgetEndpoints(t *testing.T) {
	harness := newResourceServer(t)
	household := harness.createHousehold(t)
	if household.ID == "" || household.Currency != "USD" || household.Timezone != "UTC" {
		t.Fatalf("household = %+v", household)
	}

	list := harness.do(t, http.MethodGet, "/api/v1/households", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list households: %d %s", list.Code, list.Body.String())
	}
	var households ListResponse[householdDTO]
	decodeResponse(t, list, &households)
	if len(households.Items) != 1 || households.Items[0].ID != household.ID {
		t.Fatalf("households = %+v", households.Items)
	}

	checking := harness.createAccount(t, household.ID, "CHECKING", true)
	envelope := harness.createEnvelope(t, household.ID, "Groceries")

	openingBody := `{"effective_date":"2026-10-01","delta_minor":100000}`
	opening := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/accounts/"+checking.ID+"/opening-balance", openingBody, idempotencyHeaders("opening-1"))
	if opening.Code != http.StatusCreated {
		t.Fatalf("opening balance: %d %s", opening.Code, opening.Body.String())
	}
	var opened DataResponse[operationDTO]
	decodeResponse(t, opening, &opened)
	if opened.Data.Type != "OPENING_BALANCE" || opened.Data.AmountMinor != nil {
		t.Fatalf("opening operation = %+v", opened.Data)
	}

	replay := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/accounts/"+checking.ID+"/opening-balance", openingBody, idempotencyHeaders("opening-1"))
	if replay.Code != http.StatusCreated {
		t.Fatalf("opening replay: %d %s", replay.Code, replay.Body.String())
	}
	var replayed DataResponse[operationDTO]
	decodeResponse(t, replay, &replayed)
	if replayed.Data.ID != opened.Data.ID {
		t.Fatalf("replay operation id = %s, want %s", replayed.Data.ID, opened.Data.ID)
	}

	mismatch := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/accounts/"+checking.ID+"/opening-balance",
		`{"effective_date":"2026-10-01","delta_minor":99999}`, idempotencyHeaders("opening-1"))
	if mismatch.Code != http.StatusConflict {
		t.Fatalf("idempotency mismatch status = %d %s", mismatch.Code, mismatch.Body.String())
	}

	allocation := harness.do(t, http.MethodPost, "/api/v1/households/"+household.ID+"/allocations",
		`{"effective_date":"2026-10-03","envelope_id":"`+envelope.ID+`","amount_minor":30000}`, idempotencyHeaders("allocate-1"))
	if allocation.Code != http.StatusCreated {
		t.Fatalf("allocation: %d %s", allocation.Code, allocation.Body.String())
	}

	balances := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID+"/balances", "", nil)
	if balances.Code != http.StatusOK {
		t.Fatalf("balances: %d %s", balances.Code, balances.Body.String())
	}
	var report DataResponse[balancesDTO]
	decodeResponse(t, balances, &report)
	if report.Data.ReadyToAssignMinor != 70000 {
		t.Fatalf("ready to assign = %d, want 70000", report.Data.ReadyToAssignMinor)
	}
	if len(report.Data.Envelopes) != 1 || report.Data.Envelopes[0].BalanceMinor != 30000 {
		t.Fatalf("envelope balances = %+v", report.Data.Envelopes)
	}
	if len(report.Data.Accounts) != 1 || report.Data.Accounts[0].PostedMinor != 100000 {
		t.Fatalf("account balances = %+v", report.Data.Accounts)
	}
}

func TestNonMemberCannotSeeHousehold(t *testing.T) {
	harness := newResourceServer(t)
	household := harness.createHousehold(t)

	stranger, err := harness.auth.Register(t.Context(), "stranger@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("register stranger: %v", err)
	}
	_ = stranger
	login, err := harness.auth.Login(t.Context(), "stranger@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("login stranger: %v", err)
	}

	request := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID, "", nil)
	_ = request
	// Replace the harness token for one request.
	original := harness.token
	harness.token = login.Token
	response := harness.do(t, http.MethodGet, "/api/v1/households/"+household.ID, "", nil)
	harness.token = original
	if response.Code != http.StatusNotFound {
		t.Fatalf("non-member get status = %d %s", response.Code, response.Body.String())
	}
}
