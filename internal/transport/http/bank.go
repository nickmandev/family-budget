package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"family-budget/internal/application"
	"family-budget/internal/domain"
	"family-budget/internal/integrations/enablebanking"
	"family-budget/internal/ports"
)

// BankUseCases is the narrow seam over application.BankService so handlers can
// be tested with a fake without a database.
type BankUseCases interface {
	ListASPSPs(ctx context.Context, actor domain.UserId, household domain.HouseholdId) ([]enablebanking.ASPSP, error)
	StartConnection(ctx context.Context, actor domain.UserId, household domain.HouseholdId, aspspName, aspspCountry string) (application.StartConnectionResult, error)
	ListConnections(ctx context.Context, actor domain.UserId, household domain.HouseholdId) ([]ports.BankConnectionRecord, error)
	Disconnect(ctx context.Context, actor domain.UserId, household domain.HouseholdId, connectionID string) (ports.BankConnectionRecord, error)
	HandleCallback(ctx context.Context, code, state, providerError, providerErrorDescription string) (application.CallbackResult, error)
}

type aspspDTO struct {
	Name    string `json:"name"`
	Country string `json:"country"`
}

type bankConnectionDTO struct {
	ID               string  `json:"id"`
	ASPSPName        string  `json:"aspsp_name"`
	ASPSPCountry     string  `json:"aspsp_country"`
	Status           string  `json:"status"`
	AuthorizationID  *string `json:"authorization_id"`
	SessionExpiresAt *string `json:"session_expires_at"`
	FailureSummary   *string `json:"failure_summary"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type bankStartDTO struct {
	Connection  bankConnectionDTO `json:"connection"`
	RedirectURL string            `json:"redirect_url"`
}

type listASPSPsResponse struct {
	ASPSPs []aspspDTO `json:"aspsps"`
}

type startBankConnectionRequest struct {
	ASPSPName    string `json:"aspsp_name"`
	ASPSPCountry string `json:"aspsp_country"`
}

func (s *Server) registerBankRoutes(mux *http.ServeMux) {
	if s.deps.Bank == nil {
		return
	}
	s.register(mux, "GET /api/v1/households/{id}/bank/aspsps", s.requireAuth(http.HandlerFunc(s.handleListBankASPSPs)))
	s.register(mux, "POST /api/v1/households/{id}/bank/connections", s.requireAuth(http.HandlerFunc(s.handleStartBankConnection)))
	s.register(mux, "GET /api/v1/households/{id}/bank/connections", s.requireAuth(http.HandlerFunc(s.handleListBankConnections)))
	s.register(mux, "DELETE /api/v1/households/{id}/bank/connections/{connectionID}", s.requireAuth(http.HandlerFunc(s.handleDisconnectBankConnection)))
	s.registerFunc(mux, "GET /callbacks/enablebanking", s.handleEnableBankingCallback)
}

func (s *Server) handleListBankASPSPs(w http.ResponseWriter, r *http.Request) {
	userID, household, err := bankRequestScope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	aspsps, err := s.deps.Bank.ListASPSPs(r.Context(), userID, household)
	if err != nil {
		s.logBankFailure(r, err)
		writeError(w, r, err)
		return
	}
	items := make([]aspspDTO, 0, len(aspsps))
	for _, aspsp := range aspsps {
		items = append(items, aspspDTO{Name: aspsp.Name, Country: aspsp.Country})
	}
	writeJSON(w, http.StatusOK, DataResponse[listASPSPsResponse]{Data: listASPSPsResponse{ASPSPs: items}})
}

func (s *Server) handleStartBankConnection(w http.ResponseWriter, r *http.Request) {
	userID, household, err := bankRequestScope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request startBankConnectionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := s.deps.Bank.StartConnection(r.Context(), userID, household, request.ASPSPName, request.ASPSPCountry)
	if err != nil {
		s.logBankFailure(r, err)
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[bankStartDTO]{Data: bankStartDTO{
		Connection:  toBankConnectionDTO(result.Connection),
		RedirectURL: stringValue(result.Connection.ProviderRedirectURL),
	}})
}

func (s *Server) handleListBankConnections(w http.ResponseWriter, r *http.Request) {
	userID, household, err := bankRequestScope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	records, err := s.deps.Bank.ListConnections(r.Context(), userID, household)
	if err != nil {
		s.logBankFailure(r, err)
		writeError(w, r, err)
		return
	}
	items := make([]bankConnectionDTO, 0, len(records))
	for _, record := range records {
		items = append(items, toBankConnectionDTO(record))
	}
	writeJSON(w, http.StatusOK, ListResponse[bankConnectionDTO]{Items: items})
}

func (s *Server) handleDisconnectBankConnection(w http.ResponseWriter, r *http.Request) {
	userID, household, err := bankRequestScope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	connectionID, err := pathUUID(r, "connectionID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	record, err := s.deps.Bank.Disconnect(r.Context(), userID, household, connectionID)
	if err != nil {
		s.logBankFailure(r, err)
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[bankConnectionDTO]{Data: toBankConnectionDTO(record)})
}

func (s *Server) handleEnableBankingCallback(w http.ResponseWriter, r *http.Request) {
	// Enable Banking sends error/error_description on rejection; those values are untrusted.
	query := r.URL.Query()
	result, err := s.deps.Bank.HandleCallback(r.Context(), query.Get("code"), query.Get("state"), query.Get("error"), query.Get("error_description"))
	if err != nil {
		s.logBankFailure(r, err)
		writeError(w, r, err)
		return
	}
	outcome := strings.ToLower(string(result.Outcome))
	if resultURL := s.deps.Config.EnableBanking.ResultURL; resultURL != "" {
		parsed, err := url.Parse(resultURL)
		if err != nil {
			writeError(w, r, errInternal)
			return
		}
		values := url.Values{}
		values.Set("status", outcome)
		parsed.RawQuery = values.Encode()
		http.Redirect(w, r, parsed.String(), http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[struct {
		Outcome string `json:"outcome"`
	}]{Data: struct {
		Outcome string `json:"outcome"`
	}{Outcome: outcome}})
}

func (s *Server) logBankFailure(r *http.Request, err error) {
	if mapped := toAPIError(err); mapped != nil && mapped.Status >= http.StatusInternalServerError &&
		s.deps.Logger != nil {
		s.deps.Logger.Error("bank request failed",
			"request_id", RequestIDFromContext(r.Context()),
			"error", err,
		)
	}
}

func bankRequestScope(r *http.Request) (domain.UserId, domain.HouseholdId, error) {
	userID, err := actor(r)
	if err != nil {
		return "", "", err
	}
	household, err := householdPathID(r)
	if err != nil {
		return "", "", err
	}
	return userID, household, nil
}

func toBankConnectionDTO(record ports.BankConnectionRecord) bankConnectionDTO {
	var expiresAt *string
	if record.SessionExpiresAt != nil {
		formatted := formatTimestamp(*record.SessionExpiresAt)
		expiresAt = &formatted
	}
	return bankConnectionDTO{
		ID:               record.ID,
		ASPSPName:        record.ASPSPName,
		ASPSPCountry:     record.ASPSPCountry,
		Status:           string(record.Status),
		AuthorizationID:  record.AuthorizationID,
		SessionExpiresAt: expiresAt,
		FailureSummary:   record.FailureSummary,
		CreatedAt:        formatTimestamp(record.CreatedAt),
		UpdatedAt:        formatTimestamp(record.UpdatedAt),
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
