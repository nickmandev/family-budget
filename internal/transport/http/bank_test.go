package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"family-budget/internal/application"
	"family-budget/internal/config"
	"family-budget/internal/domain"
	"family-budget/internal/integrations/enablebanking"
	"family-budget/internal/ports"
)

type fakeBankUseCases struct {
	aspsps           []enablebanking.ASPSP
	connections      []ports.BankConnectionRecord
	startResult      application.StartConnectionResult
	startErr         error
	listErr          error
	disconnectResult ports.BankConnectionRecord
	disconnectErr    error
	callbackResult   application.CallbackResult
	callbackErr      error
	listActor        domain.UserId
	listHousehold    domain.HouseholdId
	callbackArgs     []string
}

func (f *fakeBankUseCases) ListASPSPs(_ context.Context, actor domain.UserId, household domain.HouseholdId) ([]enablebanking.ASPSP, error) {
	f.listActor, f.listHousehold = actor, household
	return f.aspsps, f.listErr
}

func (f *fakeBankUseCases) StartConnection(context.Context, domain.UserId, domain.HouseholdId, string, string) (application.StartConnectionResult, error) {
	return f.startResult, f.startErr
}

func (f *fakeBankUseCases) ListConnections(context.Context, domain.UserId, domain.HouseholdId) ([]ports.BankConnectionRecord, error) {
	return f.connections, f.listErr
}

func (f *fakeBankUseCases) Disconnect(context.Context, domain.UserId, domain.HouseholdId, string) (ports.BankConnectionRecord, error) {
	return f.disconnectResult, f.disconnectErr
}

func (f *fakeBankUseCases) HandleCallback(_ context.Context, code, state, providerError, providerErrorDescription string) (application.CallbackResult, error) {
	f.callbackArgs = []string{code, state, providerError, providerErrorDescription}
	return f.callbackResult, f.callbackErr
}

func newBankHarness(t *testing.T) (*resourceHarness, *fakeBankUseCases) {
	t.Helper()
	harness := newResourceServer(t)
	fake := &fakeBankUseCases{}
	cfg := config.Default()
	cfg.HTTP.Port = 0
	cfg.HTTP.RequestTimeout = 2 * time.Second
	cfg.HTTP.MaxBodyBytes = 1 << 20
	cfg.Auth.RateLimitMax = 1000
	cfg.Auth.RateLimitWindow = time.Minute
	server, err := NewServer(Dependencies{
		Config: cfg,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Auth:   harness.auth,
		App:    harness.app,
		Bank:   fake,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	harness.server = server
	return harness, fake
}

func bankConnectionFixture() ports.BankConnectionRecord {
	expiresAt := time.Date(2026, time.November, 1, 12, 0, 0, 0, time.UTC)
	authorizationID := "authorization-1"
	sessionID := "encrypted-session-secret"
	stateHash := "state-hash-secret"
	failure := "provider failure"
	now := time.Date(2026, time.November, 1, 10, 0, 0, 0, time.UTC)
	return ports.BankConnectionRecord{
		ID:                  "123e4567-e89b-12d3-a456-426614174001",
		HouseholdID:         "123e4567-e89b-12d3-a456-426614174002",
		ASPSPName:           "Example Bank",
		ASPSPCountry:        "BG",
		Status:              ports.BankConnectionAuthorized,
		StateHash:           stateHash,
		StateExpiresAt:      now.Add(time.Hour),
		AuthorizationID:     &authorizationID,
		ProviderRedirectURL: bankStringPointer("https://provider.example/authorize?token=private"),
		SessionIDEncrypted:  &sessionID,
		SessionExpiresAt:    &expiresAt,
		FailureSummary:      &failure,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
}

func bankStringPointer(value string) *string { return &value }

func TestBankASPSPListRequiresAuthAndScopesHousehold(t *testing.T) {
	harness, bank := newBankHarness(t)
	household := harness.createHousehold(t)
	bank.aspsps = []enablebanking.ASPSP{{Name: "Example Bank", Country: "BG"}}
	path := "/api/v1/households/" + household.ID + "/bank/aspsps"

	request := httptest.NewRequest(http.MethodGet, path, nil)
	unauthenticated := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(unauthenticated, request)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", unauthenticated.Code)
	}

	response := harness.do(t, http.MethodGet, path, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("ASPSP list: %d %s", response.Code, response.Body.String())
	}
	if bank.listActor != harness.userID || bank.listHousehold.String() != household.ID {
		t.Fatalf("forwarded actor/household = %q/%q", bank.listActor, bank.listHousehold)
	}
	var listed DataResponse[listASPSPsResponse]
	decodeResponse(t, response, &listed)
	if len(listed.Data.ASPSPs) != 1 || listed.Data.ASPSPs[0].Name != "Example Bank" {
		t.Fatalf("ASPSPs = %+v", listed.Data.ASPSPs)
	}

	bank.listErr = domain.ErrEntityNotFoundOrNotInHousehold
	nonMember := harness.do(t, http.MethodGet, path, "", nil)
	if nonMember.Code != http.StatusNotFound || !strings.Contains(nonMember.Body.String(), `"code":"ENTITY_NOT_FOUND_OR_NOT_IN_HOUSEHOLD"`) {
		t.Fatalf("non-member response = %d %s", nonMember.Code, nonMember.Body.String())
	}
}

func TestBankStartConnectionReturnsSafeDTOAndMapsErrors(t *testing.T) {
	harness, bank := newBankHarness(t)
	household := harness.createHousehold(t)
	record := bankConnectionFixture()
	bank.startResult = application.StartConnectionResult{Connection: record, State: "do-not-expose-state"}
	path := "/api/v1/households/" + household.ID + "/bank/connections"
	response := harness.do(t, http.MethodPost, path, `{"aspsp_name":"Example Bank","aspsp_country":"BG"}`, nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("start connection: %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"redirect_url":"https://provider.example/authorize?token=private"`) {
		t.Fatalf("redirect_url missing: %s", body)
	}
	for _, secret := range []string{"state_hash", "session_id", "session_id_encrypted", record.StateHash, "encrypted-session-secret", "do-not-expose-state"} {
		if strings.Contains(body, secret) {
			t.Errorf("response exposed %q: %s", secret, body)
		}
	}
	var started DataResponse[bankStartDTO]
	decodeResponse(t, response, &started)
	if started.Data.Connection.ID != record.ID {
		t.Fatalf("connection = %+v", started.Data.Connection)
	}

	invalid := harness.do(t, http.MethodPost, path, "{not json", nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid body status = %d, want 400", invalid.Code)
	}

	bank.startErr = errors.New("unmapped bank provider failure")
	unknown := harness.do(t, http.MethodPost, path, `{"aspsp_name":"Example Bank","aspsp_country":"BG"}`, nil)
	if unknown.Code != http.StatusInternalServerError || !strings.Contains(unknown.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("unknown bank error = %d %s", unknown.Code, unknown.Body.String())
	}
}

func TestBankProviderFailureIsLoggedWithRequestID(t *testing.T) {
	harness, bank := newBankHarness(t)
	household := harness.createHousehold(t)
	var logs bytes.Buffer
	harness.server.deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	bank.startErr = errors.New("upstream provider contract failure")
	requestID := "bank-provider-request-123"
	response := harness.do(t, http.MethodPost,
		"/api/v1/households/"+household.ID+"/bank/connections",
		`{"aspsp_name":"Example Bank","aspsp_country":"BG"}`,
		map[string]string{HeaderRequestID: requestID},
	)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("provider failure response = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "upstream provider contract failure") {
		t.Fatalf("provider error leaked in response: %s", response.Body.String())
	}
	if !strings.Contains(logs.String(), "upstream provider contract failure") || !strings.Contains(logs.String(), requestID) {
		t.Fatalf("bank failure log = %s, want provider error and request id %q", logs.String(), requestID)
	}
}

func TestBankListConnectionsAndDisconnect(t *testing.T) {
	harness, bank := newBankHarness(t)
	household := harness.createHousehold(t)
	record := bankConnectionFixture()
	bank.connections = []ports.BankConnectionRecord{record}
	path := "/api/v1/households/" + household.ID + "/bank/connections"
	list := harness.do(t, http.MethodGet, path, "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list connections: %d %s", list.Code, list.Body.String())
	}
	var listed ListResponse[bankConnectionDTO]
	decodeResponse(t, list, &listed)
	if len(listed.Items) != 1 || listed.Items[0].ID != record.ID || listed.NextCursor != nil {
		t.Fatalf("connections response = %+v", listed)
	}
	if !strings.Contains(list.Body.String(), `"next_cursor":null`) {
		t.Fatalf("next_cursor is not null: %s", list.Body.String())
	}

	updated := record
	updated.Status = ports.BankConnectionDisconnected
	updated.UpdatedAt = updated.UpdatedAt.Add(time.Minute)
	bank.disconnectResult = updated
	deleted := harness.do(t, http.MethodDelete, path+"/"+record.ID, "", nil)
	if deleted.Code != http.StatusOK {
		t.Fatalf("disconnect: %d %s", deleted.Code, deleted.Body.String())
	}
	var disconnected DataResponse[bankConnectionDTO]
	decodeResponse(t, deleted, &disconnected)
	if disconnected.Data.Status != string(ports.BankConnectionDisconnected) {
		t.Fatalf("disconnected connection = %+v", disconnected.Data)
	}
}

func TestEnableBankingCallbackRedirectAndJSON(t *testing.T) {
	harness, bank := newBankHarness(t)
	bank.callbackResult = application.CallbackResult{Outcome: application.CallbackCompleted}
	harness.server.deps.Config.EnableBanking.ResultURL = "https://budget.example/bank/callback"
	request := httptest.NewRequest(http.MethodGet, "/callbacks/enablebanking?code=secret-code&state=secret-state", nil)
	redirect := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(redirect, request)
	if redirect.Code != http.StatusSeeOther {
		t.Fatalf("callback status = %d, want 303: %s", redirect.Code, redirect.Body.String())
	}
	location, err := url.Parse(redirect.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if got := location.Query(); len(got) != 1 || got.Get("status") != "completed" {
		t.Fatalf("redirect query = %v, want only status=completed", got)
	}
	if strings.Contains(redirect.Header().Get("Location"), "secret-code") || strings.Contains(redirect.Header().Get("Location"), "secret-state") {
		t.Fatalf("redirect exposed callback secrets: %q", redirect.Header().Get("Location"))
	}
	if strings.Join(bank.callbackArgs, "|") != "secret-code|secret-state||" {
		t.Fatalf("callback args = %q", bank.callbackArgs)
	}

	bank.callbackResult = application.CallbackResult{Outcome: application.CallbackDenied}
	deniedRequest := httptest.NewRequest(http.MethodGet, "/callbacks/enablebanking?error=access_denied&error_description=untrusted+description", nil)
	denied := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusSeeOther {
		t.Fatalf("denied callback status = %d", denied.Code)
	}
	deniedLocation, err := url.Parse(denied.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse denied Location: %v", err)
	}
	if got := deniedLocation.Query(); len(got) != 1 || got.Get("status") != "denied" {
		t.Fatalf("denied redirect query = %v, want only status=denied", got)
	}
	if strings.Join(bank.callbackArgs, "|") != "||access_denied|untrusted description" {
		t.Fatalf("provider error callback args = %q", bank.callbackArgs)
	}

	harness.server.deps.Config.EnableBanking.ResultURL = ""
	bank.callbackResult = application.CallbackResult{Outcome: application.CallbackCompleted}
	jsonRequest := httptest.NewRequest(http.MethodGet, "/callbacks/enablebanking?code=secret-code&state=secret-state", nil)
	jsonResponse := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(jsonResponse, jsonRequest)
	if jsonResponse.Code != http.StatusOK || strings.TrimSpace(jsonResponse.Body.String()) != `{"data":{"outcome":"completed"}}` {
		t.Fatalf("JSON callback = %d %s", jsonResponse.Code, jsonResponse.Body.String())
	}
}

func TestEnableBankingCallbackIsPublic(t *testing.T) {
	harness, bank := newBankHarness(t)
	bank.callbackResult = application.CallbackResult{Outcome: application.CallbackCompleted}
	request := httptest.NewRequest(http.MethodGet, "/callbacks/enablebanking?state=opaque", nil)
	response := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("public callback status = %d: %s", response.Code, response.Body.String())
	}
}
