package httpapi

import (
	"net/http"
	"strings"

	"family-budget/internal/application"
	"family-budget/internal/domain"
)

type createPlanRequest struct {
	Kind             string `json:"kind"`
	TargetEnvelopeID string `json:"target_envelope_id"`
	TargetAccountID  string `json:"target_account_id"`
	AmountMinor      int64  `json:"amount_minor"`
}

type reallocationDTO struct {
	SourceEnvelopeID string `json:"source_envelope_id"`
	AmountMinor      int64  `json:"amount_minor"`
}

type authorizePlanRequest struct {
	financialRequest
	Mode         string           `json:"mode"`
	Reallocation *reallocationDTO `json:"reallocation"`
}

type resizePlanRequest struct {
	financialRequest
	AmountMinor int64  `json:"amount_minor"`
	Mode        string `json:"mode"`
}

type cancelPlanRequest struct {
	financialRequest
	Reason string `json:"reason"`
}

type completePlanRequest struct {
	financialRequest
	FinalAmountMinor  int64  `json:"final_amount_minor"`
	PurchaseKind      string `json:"purchase_kind"`
	CardAccountID     string `json:"card_account_id"`
	PaymentEnvelopeID string `json:"payment_envelope_id"`
}

func (s *Server) registerPlanRoutes(mux *http.ServeMux) {
	if s.deps.App == nil {
		return
	}
	s.register(mux, "GET /api/v1/households/{id}/plans", s.requireAuth(http.HandlerFunc(s.handleListPlans)))
	s.register(mux, "POST /api/v1/households/{id}/plans", s.requireAuth(http.HandlerFunc(s.handleCreatePlan)))
	s.register(mux, "GET /api/v1/households/{id}/plans/{planID}", s.requireAuth(http.HandlerFunc(s.handleGetPlan)))
	s.register(mux, "POST /api/v1/households/{id}/plans/{planID}/authorize", s.requireAuth(http.HandlerFunc(s.handleAuthorizePlan)))
	s.register(mux, "POST /api/v1/households/{id}/plans/{planID}/resize", s.requireAuth(http.HandlerFunc(s.handleResizePlan)))
	s.register(mux, "POST /api/v1/households/{id}/plans/{planID}/cancel", s.requireAuth(http.HandlerFunc(s.handleCancelPlan)))
	s.register(mux, "POST /api/v1/households/{id}/plans/{planID}/complete", s.requireAuth(http.HandlerFunc(s.handleCompletePlan)))
}

func planPathID(r *http.Request) (domain.PlanId, error) {
	raw, err := pathUUID(r, "planID")
	if err != nil {
		return "", err
	}
	return domain.NewPlanId(raw)
}

func authorizationMode(raw string) (domain.AuthorizationMode, error) {
	if strings.TrimSpace(raw) == "" {
		return domain.AuthorizationModeNormal, nil
	}
	return domain.ParseAuthorizationMode(raw)
}

func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
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
	plans, err := s.deps.App.ListPurchasePlans(r.Context(), userID, household)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ListResponse[planDTO]{Items: toPlanDTOs(plans)})
}

func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
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
	var request createPlanRequest
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
	plan, err := s.deps.App.CreatePurchasePlan(r.Context(), userID, household, application.CreatePlanInput{
		Kind:        kind,
		Target:      application.PlanTargetInput{EnvelopeID: envelopeID, AccountID: accountID},
		AmountMinor: request.AmountMinor,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[planDTO]{Data: toPlanDTO(plan)})
}

func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
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
	planID, err := planPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	plan, err := s.deps.App.GetPurchasePlan(r.Context(), userID, household, planID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[planDTO]{Data: toPlanDTO(plan)})
}

func (s *Server) handleAuthorizePlan(w http.ResponseWriter, r *http.Request) {
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
	planID, err := planPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request authorizePlanRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mode, err := authorizationMode(request.Mode)
	if err != nil {
		writeError(w, r, err)
		return
	}
	input := application.AuthorizePlanInput{Mode: mode}
	if request.Reallocation != nil {
		sourceID, err := parseEnvelopeID(request.Reallocation.SourceEnvelopeID, "reallocation.source_envelope_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
		input.Reallocation = &application.ReallocationInput{
			Source:      sourceID,
			AmountMinor: request.Reallocation.AmountMinor,
		}
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	plan, op, err := s.deps.App.AuthorizePurchasePlan(r.Context(), mutation, planID, input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[struct {
		Plan      planDTO      `json:"plan"`
		Operation operationDTO `json:"operation"`
	}]{Data: struct {
		Plan      planDTO      `json:"plan"`
		Operation operationDTO `json:"operation"`
	}{Plan: toPlanDTO(plan), Operation: toOperationDTO(op)}})
}

func (s *Server) handleResizePlan(w http.ResponseWriter, r *http.Request) {
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
	planID, err := planPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request resizePlanRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mode, err := authorizationMode(request.Mode)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	plan, op, err := s.deps.App.ResizePurchasePlan(r.Context(), mutation, planID, request.AmountMinor, mode)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[struct {
		Plan      planDTO      `json:"plan"`
		Operation operationDTO `json:"operation"`
	}]{Data: struct {
		Plan      planDTO      `json:"plan"`
		Operation operationDTO `json:"operation"`
	}{Plan: toPlanDTO(plan), Operation: toOperationDTO(op)}})
}

func (s *Server) handleCancelPlan(w http.ResponseWriter, r *http.Request) {
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
	planID, err := planPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request cancelPlanRequest
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
	mutation.Reason = request.Reason
	plan, op, err := s.deps.App.CancelPurchasePlan(r.Context(), mutation, planID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[struct {
		Plan      planDTO       `json:"plan"`
		Operation *operationDTO `json:"operation"`
	}]{Data: struct {
		Plan      planDTO       `json:"plan"`
		Operation *operationDTO `json:"operation"`
	}{Plan: toPlanDTO(plan), Operation: optionalOperationDTO(op)}})
}

func optionalOperationDTO(op domain.JournalOperation) *operationDTO {
	if op.ID().IsZero() {
		return nil
	}
	dto := toOperationDTO(op)
	return &dto
}

func (s *Server) handleCompletePlan(w http.ResponseWriter, r *http.Request) {
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
	planID, err := planPathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request completePlanRequest
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
	var cardID domain.AccountId
	if strings.TrimSpace(request.CardAccountID) != "" {
		cardID, err = parseAccountID(request.CardAccountID, "card_account_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
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
	plan, op, err := s.deps.App.CompletePurchasePlan(r.Context(), mutation, planID, application.CompletePlanInput{
		FinalMinor:        request.FinalAmountMinor,
		PurchaseKind:      kind,
		CardAccountID:     cardID,
		PaymentEnvelopeID: paymentID,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[struct {
		Plan      planDTO      `json:"plan"`
		Operation operationDTO `json:"operation"`
	}]{Data: struct {
		Plan      planDTO      `json:"plan"`
		Operation operationDTO `json:"operation"`
	}{Plan: toPlanDTO(plan), Operation: toOperationDTO(op)}})
}
