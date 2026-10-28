package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"family-budget/internal/application"
	"family-budget/internal/auth"
	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

type createHouseholdRequest struct {
	Name     string `json:"name"`
	Currency string `json:"currency"`
	Timezone string `json:"timezone"`
}

type addMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type createAccountRequest struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	BudgetEligible *bool  `json:"budget_eligible"`
}

type updateAccountRequest struct {
	Name           *string `json:"name"`
	Status         *string `json:"status"`
	BudgetEligible *bool   `json:"budget_eligible"`
}

type createEnvelopeRequest struct {
	Name                string  `json:"name"`
	LinkedCardAccountID *string `json:"linked_card_account_id"`
}

type updateEnvelopeRequest struct {
	Name   *string `json:"name"`
	Status *string `json:"status"`
}

type financialRequest struct {
	EffectiveDate string `json:"effective_date"`
	Reason        string `json:"reason"`
}

type openingBalanceRequest struct {
	financialRequest
	DeltaMinor int64 `json:"delta_minor"`
}

type allocationRequest struct {
	financialRequest
	EnvelopeID  string `json:"envelope_id"`
	AmountMinor int64  `json:"amount_minor"`
}

type reallocationRequest struct {
	financialRequest
	SourceEnvelopeID      string `json:"source_envelope_id"`
	DestinationEnvelopeID string `json:"destination_envelope_id"`
	AmountMinor           int64  `json:"amount_minor"`
}

func (s *Server) registerResourceRoutes(mux *http.ServeMux) {
	if s.deps.App == nil {
		return
	}
	s.register(mux, "POST /api/v1/households", s.requireAuth(http.HandlerFunc(s.handleCreateHousehold)))
	s.register(mux, "GET /api/v1/households", s.requireAuth(http.HandlerFunc(s.handleListHouseholds)))
	s.register(mux, "GET /api/v1/households/{id}", s.requireAuth(http.HandlerFunc(s.handleGetHousehold)))

	s.register(mux, "GET /api/v1/households/{id}/members", s.requireAuth(http.HandlerFunc(s.handleListMembers)))
	s.register(mux, "POST /api/v1/households/{id}/members", s.requireAuth(http.HandlerFunc(s.handleAddMember)))
	s.register(mux, "DELETE /api/v1/households/{id}/members/{userID}", s.requireAuth(http.HandlerFunc(s.handleRemoveMember)))

	s.register(mux, "GET /api/v1/households/{id}/accounts", s.requireAuth(http.HandlerFunc(s.handleListAccounts)))
	s.register(mux, "POST /api/v1/households/{id}/accounts", s.requireAuth(http.HandlerFunc(s.handleCreateAccount)))
	s.register(mux, "PATCH /api/v1/households/{id}/accounts/{accountID}", s.requireAuth(http.HandlerFunc(s.handleUpdateAccount)))
	s.register(mux, "POST /api/v1/households/{id}/accounts/{accountID}/opening-balance", s.requireAuth(http.HandlerFunc(s.handleOpeningBalance)))

	s.register(mux, "GET /api/v1/households/{id}/envelopes", s.requireAuth(http.HandlerFunc(s.handleListEnvelopes)))
	s.register(mux, "POST /api/v1/households/{id}/envelopes", s.requireAuth(http.HandlerFunc(s.handleCreateEnvelope)))
	s.register(mux, "PATCH /api/v1/households/{id}/envelopes/{envelopeID}", s.requireAuth(http.HandlerFunc(s.handleUpdateEnvelope)))

	s.register(mux, "POST /api/v1/households/{id}/allocations", s.requireAuth(http.HandlerFunc(s.handleAllocate)))
	s.register(mux, "POST /api/v1/households/{id}/reallocations", s.requireAuth(http.HandlerFunc(s.handleReallocate)))
	s.register(mux, "GET /api/v1/households/{id}/balances", s.requireAuth(http.HandlerFunc(s.handleBalances)))
}

func actor(r *http.Request) (domain.UserId, error) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		return "", auth.ErrUnauthenticated
	}
	return principal.User.ID, nil
}

func pathUUID(r *http.Request, name string) (string, error) {
	value, err := parseUUID(r.PathValue(name))
	if err != nil {
		return "", domain.NewErrorf(domain.CodeInvalidValue, "%s must be a UUID", name)
	}
	return value, nil
}

func householdPathID(r *http.Request) (domain.HouseholdId, error) {
	raw, err := pathUUID(r, "id")
	if err != nil {
		return "", err
	}
	return domain.NewHouseholdId(raw)
}

func mutationFor(r *http.Request, actor domain.UserId, household domain.HouseholdId, key domain.IdempotencyKey, fingerprint domain.RequestFingerprint, request financialRequest) (application.Mutation, error) {
	if strings.TrimSpace(request.EffectiveDate) == "" {
		return application.Mutation{}, domain.NewError(domain.CodeInvalidValue, "effective_date is required")
	}
	effectiveDate, err := parseEffectiveDate(request.EffectiveDate)
	if err != nil {
		return application.Mutation{}, err
	}
	return application.Mutation{
		Actor:         actor,
		Household:     household,
		EffectiveDate: effectiveDate,
		Key:           key,
		Fingerprint:   fingerprint,
		Reason:        request.Reason,
	}, nil
}

func includeArchived(r *http.Request) bool {
	value := strings.TrimSpace(r.URL.Query().Get("include_archived"))
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func (s *Server) handleCreateHousehold(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request createHouseholdRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	currency, err := parseCurrency(strings.ToUpper(strings.TrimSpace(request.Currency)))
	if err != nil {
		writeError(w, r, err)
		return
	}
	record, err := s.deps.App.CreateHousehold(r.Context(), userID, request.Name, currency, request.Timezone)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[householdDTO]{Data: toHouseholdDTO(record)})
}

func (s *Server) handleListHouseholds(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	records, err := s.deps.App.ListHouseholds(r.Context(), userID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ListResponse[householdDTO]{Items: toHouseholdDTOs(records)})
}

func (s *Server) handleGetHousehold(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	record, err := s.deps.App.GetHousehold(r.Context(), userID, household)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[householdDTO]{Data: toHouseholdDTO(record)})
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	records, err := s.deps.App.ListMembers(r.Context(), userID, household)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]memberDTO, 0, len(records))
	for _, record := range records {
		items = append(items, toMemberResourceDTO(record))
	}
	writeJSON(w, http.StatusOK, ListResponse[memberDTO]{Items: items})
}

func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request addMemberRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	role := ports.MembershipRole(strings.ToUpper(strings.TrimSpace(request.Role)))
	record, err := s.deps.App.AddMember(r.Context(), userID, household, request.Email, role)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[memberDTO]{Data: toMemberResourceDTO(record)})
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rawTarget, err := pathUUID(r, "userID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	target, err := domain.NewUserId(rawTarget)
	if err != nil {
		writeError(w, r, err)
		return
	}
	record, err := s.deps.App.RemoveMember(r.Context(), userID, household, target)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[memberDTO]{Data: toMemberResourceDTO(record)})
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	records, err := s.deps.App.ListAccounts(r.Context(), userID, household, includeArchived(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]accountDTO, 0, len(records))
	for _, record := range records {
		items = append(items, toAccountDTO(record))
	}
	writeJSON(w, http.StatusOK, ListResponse[accountDTO]{Items: items})
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request createAccountRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	kind, err := domain.ParseAccountKind(strings.ToUpper(strings.TrimSpace(request.Kind)))
	if err != nil {
		writeError(w, r, err)
		return
	}
	eligible := true
	if request.BudgetEligible != nil {
		eligible = *request.BudgetEligible
	}
	record, err := s.deps.App.CreateAccount(r.Context(), userID, household, request.Name, kind, eligible)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[accountDTO]{Data: toAccountDTO(record)})
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rawAccount, err := pathUUID(r, "accountID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	accountID, err := domain.NewAccountId(rawAccount)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request updateAccountRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	var status *ports.LifecycleStatus
	if request.Status != nil {
		parsed, err := parseLifecycleStatus(strings.ToUpper(strings.TrimSpace(*request.Status)))
		if err != nil {
			writeError(w, r, err)
			return
		}
		status = &parsed
	}
	record, err := s.deps.App.UpdateAccount(r.Context(), userID, household, accountID, request.Name, status, request.BudgetEligible)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[accountDTO]{Data: toAccountDTO(record)})
}

func (s *Server) handleOpeningBalance(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rawAccount, err := pathUUID(r, "accountID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	accountID, err := domain.NewAccountId(rawAccount)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request openingBalanceRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.RecordOpeningBalance(r.Context(), mutation, accountID, request.DeltaMinor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleListEnvelopes(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	records, err := s.deps.App.ListEnvelopes(r.Context(), userID, household, includeArchived(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]envelopeDTO, 0, len(records))
	for _, record := range records {
		items = append(items, toEnvelopeDTO(record))
	}
	writeJSON(w, http.StatusOK, ListResponse[envelopeDTO]{Items: items})
}

func (s *Server) handleCreateEnvelope(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request createEnvelopeRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	var linkedCard *domain.AccountId
	if request.LinkedCardAccountID != nil {
		raw, err := parseUUID(*request.LinkedCardAccountID)
		if err != nil {
			writeError(w, r, domain.NewError(domain.CodeInvalidValue, "linked_card_account_id must be a UUID"))
			return
		}
		id, err := domain.NewAccountId(raw)
		if err != nil {
			writeError(w, r, err)
			return
		}
		linkedCard = &id
	}
	record, err := s.deps.App.CreateEnvelope(r.Context(), userID, household, request.Name, linkedCard)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[envelopeDTO]{Data: toEnvelopeDTO(record)})
}

func (s *Server) handleUpdateEnvelope(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rawEnvelope, err := pathUUID(r, "envelopeID")
	if err != nil {
		writeError(w, r, err)
		return
	}
	envelopeID, err := domain.NewEnvelopeId(rawEnvelope)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request updateEnvelopeRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	var status *ports.LifecycleStatus
	if request.Status != nil {
		parsed, err := parseLifecycleStatus(strings.ToUpper(strings.TrimSpace(*request.Status)))
		if err != nil {
			writeError(w, r, err)
			return
		}
		status = &parsed
	}
	record, err := s.deps.App.UpdateEnvelope(r.Context(), userID, household, envelopeID, request.Name, status)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[envelopeDTO]{Data: toEnvelopeDTO(record)})
}

func (s *Server) handleAllocate(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request allocationRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	envelopeID, err := domain.NewEnvelopeId(request.EnvelopeID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.AllocateFunds(r.Context(), mutation, envelopeID, request.AmountMinor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleReallocate(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request reallocationRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	source, err := domain.NewEnvelopeId(request.SourceEnvelopeID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	destination, err := domain.NewEnvelopeId(request.DestinationEnvelopeID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.ReallocateFunds(r.Context(), mutation, source, destination, request.AmountMinor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleBalances(w http.ResponseWriter, r *http.Request) {
	userID, err := actor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	household, err := householdPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	report, err := s.deps.App.Balances(r.Context(), userID, household)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[balancesDTO]{Data: toBalancesDTO(report)})
}
