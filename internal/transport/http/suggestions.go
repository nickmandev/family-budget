package httpapi

import (
	"net/http"
	"strings"

	"family-budget/internal/application"
	"family-budget/internal/domain"
)

type createSuggestionRequest struct {
	Source      string `json:"source"`
	AmountMinor int64  `json:"amount_minor"`
	Merchant    string `json:"merchant"`
}

type editSuggestionRequest struct {
	AmountMinor int64   `json:"amount_minor"`
	Merchant    *string `json:"merchant"`
}

type cancelSuggestionRequest struct {
	Reason string `json:"reason"`
}

type confirmSuggestionRequest struct {
	financialRequest
	PurchaseKind      string `json:"purchase_kind"`
	AccountID         string `json:"account_id"`
	EnvelopeID        string `json:"envelope_id"`
	PaymentEnvelopeID string `json:"payment_envelope_id"`
}

type convertSuggestionRequest struct {
	Kind             string `json:"kind"`
	TargetEnvelopeID string `json:"target_envelope_id"`
	TargetAccountID  string `json:"target_account_id"`
}

func (s *Server) registerSuggestionRoutes(mux *http.ServeMux) {
	if s.deps.App == nil {
		return
	}
	s.register(mux, "GET /api/v1/households/{id}/suggestions", s.requireAuth(http.HandlerFunc(s.handleListSuggestions)))
	s.register(mux, "POST /api/v1/households/{id}/suggestions", s.requireAuth(http.HandlerFunc(s.handleCreateSuggestion)))
	s.register(mux, "POST /api/v1/households/{id}/suggestions/{suggestionID}/edit", s.requireAuth(http.HandlerFunc(s.handleEditSuggestion)))
	s.register(mux, "POST /api/v1/households/{id}/suggestions/{suggestionID}/cancel", s.requireAuth(http.HandlerFunc(s.handleCancelSuggestion)))
	s.register(mux, "POST /api/v1/households/{id}/suggestions/{suggestionID}/confirm", s.requireAuth(http.HandlerFunc(s.handleConfirmSuggestion)))
	s.register(mux, "POST /api/v1/households/{id}/suggestions/{suggestionID}/convert", s.requireAuth(http.HandlerFunc(s.handleConvertSuggestion)))
}

func suggestionPathID(r *http.Request) (domain.SuggestionId, error) {
	raw, err := pathUUID(r, "suggestionID")
	if err != nil {
		return "", err
	}
	return domain.NewSuggestionId(raw)
}

func (s *Server) handleListSuggestions(w http.ResponseWriter, r *http.Request) {
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
	suggestions, err := s.deps.App.ListSuggestions(r.Context(), userID, household)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ListResponse[suggestionDTO]{Items: toSuggestionDTOs(suggestions)})
}

func (s *Server) handleCreateSuggestion(w http.ResponseWriter, r *http.Request) {
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
	var request createSuggestionRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	source, err := domain.ParseTransactionSource(strings.ToUpper(strings.TrimSpace(request.Source)))
	if err != nil {
		writeError(w, r, err)
		return
	}
	suggestion, err := s.deps.App.CreateSuggestion(r.Context(), userID, household, application.CreateSuggestionInput{
		Source:      source,
		AmountMinor: request.AmountMinor,
		Merchant:    request.Merchant,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[suggestionDTO]{Data: toSuggestionDTO(suggestion)})
}

func (s *Server) handleEditSuggestion(w http.ResponseWriter, r *http.Request) {
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
	suggestionID, err := suggestionPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request editSuggestionRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	suggestion, err := s.deps.App.EditSuggestion(r.Context(), userID, household, suggestionID, request.AmountMinor, request.Merchant)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[suggestionDTO]{Data: toSuggestionDTO(suggestion)})
}

func (s *Server) handleCancelSuggestion(w http.ResponseWriter, r *http.Request) {
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
	suggestionID, err := suggestionPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request cancelSuggestionRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	suggestion, err := s.deps.App.CancelSuggestion(r.Context(), userID, household, suggestionID, request.Reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[suggestionDTO]{Data: toSuggestionDTO(suggestion)})
}

func (s *Server) handleConfirmSuggestion(w http.ResponseWriter, r *http.Request) {
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
	suggestionID, err := suggestionPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request confirmSuggestionRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	kind, err := parsePurchaseKind(request.PurchaseKind)
	if err != nil {
		writeError(w, r, err)
		return
	}
	accountID, err := parseAccountID(request.AccountID, "account_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	envelopeID, err := parseEnvelopeID(request.EnvelopeID, "envelope_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var paymentID domain.EnvelopeId
	if strings.TrimSpace(request.PaymentEnvelopeID) != "" {
		paymentID, err = parseEnvelopeID(request.PaymentEnvelopeID, "payment_envelope_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	suggestion, op, err := s.deps.App.ConfirmSuggestionAsExternalTransaction(r.Context(), mutation, suggestionID, application.ConfirmSuggestionInput{
		PurchaseKind:      kind,
		AccountID:         accountID,
		EnvelopeID:        envelopeID,
		PaymentEnvelopeID: paymentID,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[struct {
		Suggestion suggestionDTO `json:"suggestion"`
		Operation  operationDTO  `json:"operation"`
	}]{Data: struct {
		Suggestion suggestionDTO `json:"suggestion"`
		Operation  operationDTO  `json:"operation"`
	}{Suggestion: toSuggestionDTO(suggestion), Operation: toOperationDTO(op)}})
}

func (s *Server) handleConvertSuggestion(w http.ResponseWriter, r *http.Request) {
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
	suggestionID, err := suggestionPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request convertSuggestionRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	kind, err := domain.ParsePlanKind(strings.ToUpper(strings.TrimSpace(request.Kind)))
	if err != nil {
		writeError(w, r, err)
		return
	}
	envelopeID, err := parseEnvelopeID(request.TargetEnvelopeID, "target_envelope_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var accountID domain.AccountId
	if strings.TrimSpace(request.TargetAccountID) != "" {
		accountID, err = parseAccountID(request.TargetAccountID, "target_account_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
	}
	suggestion, plan, err := s.deps.App.ConvertSuggestionToPlannedPurchase(r.Context(), userID, household, suggestionID, kind, application.PlanTargetInput{
		EnvelopeID: envelopeID,
		AccountID:  accountID,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[struct {
		Suggestion suggestionDTO `json:"suggestion"`
		Plan       planDTO       `json:"plan"`
	}]{Data: struct {
		Suggestion suggestionDTO `json:"suggestion"`
		Plan       planDTO       `json:"plan"`
	}{Suggestion: toSuggestionDTO(suggestion), Plan: toPlanDTO(plan)}})
}
