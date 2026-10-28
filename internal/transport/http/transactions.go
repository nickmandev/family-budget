package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"family-budget/internal/application"
	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

type incomeRequest struct {
	financialRequest
	AccountID   string `json:"account_id"`
	AmountMinor int64  `json:"amount_minor"`
}

type purchaseRequest struct {
	financialRequest
	PurchaseKind      string `json:"purchase_kind"`
	AccountID         string `json:"account_id"`
	CardAccountID     string `json:"card_account_id"`
	EnvelopeID        string `json:"envelope_id"`
	PaymentEnvelopeID string `json:"payment_envelope_id"`
	AmountMinor       int64  `json:"amount_minor"`
}

type cardPurchaseRequest struct {
	financialRequest
	CardAccountID      string `json:"card_account_id"`
	SpendingEnvelopeID string `json:"spending_envelope_id"`
	PaymentEnvelopeID  string `json:"payment_envelope_id"`
	AmountMinor        int64  `json:"amount_minor"`
}

type cardPaymentRequest struct {
	financialRequest
	AccountID         string `json:"account_id"`
	CardAccountID     string `json:"card_account_id"`
	PaymentEnvelopeID string `json:"payment_envelope_id"`
	AmountMinor       int64  `json:"amount_minor"`
}

type cardFeeRequest struct {
	financialRequest
	CardAccountID     string `json:"card_account_id"`
	FeeEnvelopeID     string `json:"fee_envelope_id"`
	PaymentEnvelopeID string `json:"payment_envelope_id"`
	AmountMinor       int64  `json:"amount_minor"`
}

type fundingDTO struct {
	FromReadyMinor *int64               `json:"from_ready_minor"`
	Envelopes      []envelopeFundingDTO `json:"envelopes"`
}

type envelopeFundingDTO struct {
	EnvelopeID  string `json:"envelope_id"`
	AmountMinor int64  `json:"amount_minor"`
}

type transferRequest struct {
	financialRequest
	SourceAccountID      string      `json:"source_account_id"`
	DestinationAccountID string      `json:"destination_account_id"`
	AmountMinor          int64       `json:"amount_minor"`
	Funding              *fundingDTO `json:"funding"`
}

type refundRequest struct {
	financialRequest
	OriginalOperationID string `json:"original_operation_id"`
	AccountID           string `json:"account_id"`
	CardAccountID       string `json:"card_account_id"`
	EnvelopeID          string `json:"envelope_id"`
	PaymentEnvelopeID   string `json:"payment_envelope_id"`
	AmountMinor         int64  `json:"amount_minor"`
}

type reversalRequest struct {
	financialRequest
	OriginalOperationID string `json:"original_operation_id"`
	AmountMinor         int64  `json:"amount_minor"`
}

type replacementDTO struct {
	PurchaseKind      string `json:"purchase_kind"`
	AccountID         string `json:"account_id"`
	EnvelopeID        string `json:"envelope_id"`
	PaymentEnvelopeID string `json:"payment_envelope_id"`
	AmountMinor       int64  `json:"amount_minor"`
}

type correctionRequest struct {
	financialRequest
	Kind                string          `json:"kind"`
	OriginalOperationID string          `json:"original_operation_id"`
	OriginalEnvelopeID  string          `json:"original_envelope_id"`
	CorrectedEnvelopeID string          `json:"corrected_envelope_id"`
	AmountMinor         int64           `json:"amount_minor"`
	Replacement         *replacementDTO `json:"replacement"`
}

type reconciliationRequest struct {
	financialRequest
	AccountID  string      `json:"account_id"`
	DeltaMinor int64       `json:"delta_minor"`
	Funding    *fundingDTO `json:"funding"`
}

func (s *Server) registerTransactionRoutes(mux *http.ServeMux) {
	if s.deps.App == nil {
		return
	}
	s.register(mux, "GET /api/v1/households/{id}/transactions", s.requireAuth(http.HandlerFunc(s.handleListTransactions)))
	s.register(mux, "POST /api/v1/households/{id}/transactions/income", s.requireAuth(http.HandlerFunc(s.handleIncome)))
	s.register(mux, "POST /api/v1/households/{id}/transactions/purchases", s.requireAuth(http.HandlerFunc(s.handlePurchase)))
	s.register(mux, "POST /api/v1/households/{id}/transactions/card-payments", s.requireAuth(http.HandlerFunc(s.handleCardPayment)))
	s.register(mux, "POST /api/v1/households/{id}/transactions/card-fees", s.requireAuth(http.HandlerFunc(s.handleCardFee)))
	s.register(mux, "POST /api/v1/households/{id}/transactions/transfers", s.requireAuth(http.HandlerFunc(s.handleTransfer)))
	s.register(mux, "POST /api/v1/households/{id}/transactions/refunds", s.requireAuth(http.HandlerFunc(s.handleRefund)))
	s.register(mux, "POST /api/v1/households/{id}/transactions/reversals", s.requireAuth(http.HandlerFunc(s.handleReversal)))
	s.register(mux, "POST /api/v1/households/{id}/transactions/corrections", s.requireAuth(http.HandlerFunc(s.handleCorrection)))
	s.register(mux, "POST /api/v1/households/{id}/transactions/reconciliations", s.requireAuth(http.HandlerFunc(s.handleReconciliation)))
	s.register(mux, "GET /api/v1/households/{id}/resolution", s.requireAuth(http.HandlerFunc(s.handleResolution)))
}

func parseAccountID(raw, field string) (domain.AccountId, error) {
	value, err := parseUUID(raw)
	if err != nil {
		return "", domain.NewErrorf(domain.CodeInvalidValue, "%s must be a UUID", field)
	}
	return domain.NewAccountId(value)
}

func parseEnvelopeID(raw, field string) (domain.EnvelopeId, error) {
	value, err := parseUUID(raw)
	if err != nil {
		return "", domain.NewErrorf(domain.CodeInvalidValue, "%s must be a UUID", field)
	}
	return domain.NewEnvelopeId(value)
}

func parseOperationID(raw string) (domain.JournalOperationId, error) {
	value, err := parseUUID(raw)
	if err != nil {
		return "", domain.NewErrorf(domain.CodeInvalidValue, "original_operation_id must be a UUID")
	}
	return domain.NewJournalOperationId(value)
}

func fundingInput(input *fundingDTO) (*application.FundingInput, error) {
	if input == nil {
		return nil, nil
	}
	result := &application.FundingInput{FromReadyMinor: input.FromReadyMinor}
	for _, item := range input.Envelopes {
		id, err := parseEnvelopeID(item.EnvelopeID, "funding.envelopes[].envelope_id")
		if err != nil {
			return nil, err
		}
		result.Envelopes = append(result.Envelopes, application.EnvelopeFundingInput{
			EnvelopeID:  id,
			AmountMinor: item.AmountMinor,
		})
	}
	return result, nil
}

func (s *Server) handleListTransactions(w http.ResponseWriter, r *http.Request) {
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
	query := r.URL.Query()
	filter := ports.JournalFilter{}
	for _, raw := range query["type"] {
		for _, part := range strings.Split(raw, ",") {
			if strings.TrimSpace(part) == "" {
				continue
			}
			opType, err := domain.ParseOperationType(strings.TrimSpace(part))
			if err != nil {
				writeError(w, r, err)
				return
			}
			filter.Types = append(filter.Types, opType)
		}
	}
	if raw := strings.TrimSpace(query.Get("source")); raw != "" {
		source, err := domain.ParseTransactionSource(raw)
		if err != nil {
			writeError(w, r, err)
			return
		}
		filter.Source = source
	}
	if raw := strings.TrimSpace(query.Get("from")); raw != "" {
		date, err := parseEffectiveDate(raw)
		if err != nil {
			writeError(w, r, err)
			return
		}
		filter.From = &date
	}
	if raw := strings.TrimSpace(query.Get("to")); raw != "" {
		date, err := parseEffectiveDate(raw)
		if err != nil {
			writeError(w, r, err)
			return
		}
		filter.To = &date
	}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			writeError(w, r, domain.NewError(domain.CodeInvalidValue, "limit must be a positive integer"))
			return
		}
		filter.Limit = limit
	}
	if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
		cursor, err := decodeJournalCursor(raw)
		if err != nil {
			writeError(w, r, err)
			return
		}
		filter.Cursor = cursor
	}
	page, err := s.deps.App.ListTransactions(r.Context(), userID, household, filter)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ListResponse[operationDTO]{
		Items:      toOperationDTOs(page.Items),
		NextCursor: encodeJournalCursor(page.NextCursor),
	})
}

func (s *Server) handleIncome(w http.ResponseWriter, r *http.Request) {
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
	var request incomeRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	accountID, err := parseAccountID(request.AccountID, "account_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.RecordIncome(r.Context(), mutation, accountID, request.AmountMinor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handlePurchase(w http.ResponseWriter, r *http.Request) {
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
	var request purchaseRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	envelopeID, err := parseEnvelopeID(request.EnvelopeID, "envelope_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}

	isCard := strings.TrimSpace(request.PaymentEnvelopeID) != "" ||
		strings.EqualFold(strings.TrimSpace(request.PurchaseKind), string(domain.PurchaseKindCard))
	if isCard {
		cardRaw := request.CardAccountID
		if strings.TrimSpace(cardRaw) == "" {
			cardRaw = request.AccountID
		}
		cardID, err := parseAccountID(cardRaw, "card_account_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
		paymentID, err := parseEnvelopeID(request.PaymentEnvelopeID, "payment_envelope_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
		op, err := s.deps.App.RecordCardPurchase(r.Context(), mutation, cardID, envelopeID, paymentID, request.AmountMinor)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
		return
	}

	accountID, err := parseAccountID(request.AccountID, "account_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.RecordDebitPurchase(r.Context(), mutation, accountID, envelopeID, request.AmountMinor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleCardPurchase(w http.ResponseWriter, r *http.Request) {
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
	var request cardPurchaseRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	cardID, err := parseAccountID(request.CardAccountID, "card_account_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	spendingID, err := parseEnvelopeID(request.SpendingEnvelopeID, "spending_envelope_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	paymentID, err := parseEnvelopeID(request.PaymentEnvelopeID, "payment_envelope_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.RecordCardPurchase(r.Context(), mutation, cardID, spendingID, paymentID, request.AmountMinor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleCardPayment(w http.ResponseWriter, r *http.Request) {
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
	var request cardPaymentRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	cashID, err := parseAccountID(request.AccountID, "account_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	cardID, err := parseAccountID(request.CardAccountID, "card_account_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	paymentID, err := parseEnvelopeID(request.PaymentEnvelopeID, "payment_envelope_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.RecordCardPayment(r.Context(), mutation, cashID, cardID, paymentID, request.AmountMinor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleCardFee(w http.ResponseWriter, r *http.Request) {
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
	var request cardFeeRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	cardID, err := parseAccountID(request.CardAccountID, "card_account_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	feeID, err := parseEnvelopeID(request.FeeEnvelopeID, "fee_envelope_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	paymentID, err := parseEnvelopeID(request.PaymentEnvelopeID, "payment_envelope_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.RecordCardFee(r.Context(), mutation, cardID, feeID, paymentID, request.AmountMinor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
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
	var request transferRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	sourceID, err := parseAccountID(request.SourceAccountID, "source_account_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	destinationID, err := parseAccountID(request.DestinationAccountID, "destination_account_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	funding, err := fundingInput(request.Funding)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.RecordTransfer(r.Context(), mutation, sourceID, destinationID, request.AmountMinor, funding)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleRefund(w http.ResponseWriter, r *http.Request) {
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
	var request refundRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	original, err := parseOperationID(request.OriginalOperationID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var op domain.JournalOperation
	if strings.TrimSpace(request.CardAccountID) != "" {
		cardID, err := parseAccountID(request.CardAccountID, "card_account_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
		spendingID, err := parseEnvelopeID(request.EnvelopeID, "envelope_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
		paymentID, err := parseEnvelopeID(request.PaymentEnvelopeID, "payment_envelope_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
		op, err = s.deps.App.RecordCardRefund(r.Context(), mutation, cardID, original, spendingID, paymentID, request.AmountMinor)
		if err != nil {
			writeError(w, r, err)
			return
		}
	} else {
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
		op, err = s.deps.App.RecordCashRefund(r.Context(), mutation, accountID, original, envelopeID, request.AmountMinor)
		if err != nil {
			writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleReversal(w http.ResponseWriter, r *http.Request) {
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
	var request reversalRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	original, err := parseOperationID(request.OriginalOperationID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.ReverseOperation(r.Context(), mutation, original, request.AmountMinor, request.Reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleCorrection(w http.ResponseWriter, r *http.Request) {
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
	var request correctionRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	original, err := parseOperationID(request.OriginalOperationID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	kind := application.CorrectionKind(strings.ToUpper(strings.TrimSpace(request.Kind)))
	switch kind {
	case application.CorrectionTransaction, application.CorrectionCategory, application.CorrectionMetadata:
	default:
		writeError(w, r, domain.NewErrorf(domain.CodeInvalidValue, "unknown correction kind %q", request.Kind))
		return
	}
	var originalEnvelope, correctedEnvelope domain.EnvelopeId
	if strings.TrimSpace(request.OriginalEnvelopeID) != "" {
		originalEnvelope, err = parseEnvelopeID(request.OriginalEnvelopeID, "original_envelope_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
	}
	if strings.TrimSpace(request.CorrectedEnvelopeID) != "" {
		correctedEnvelope, err = parseEnvelopeID(request.CorrectedEnvelopeID, "corrected_envelope_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
	}
	var replacement *application.ReplacementPurchase
	if request.Replacement != nil {
		method, err := parsePurchaseKind(request.Replacement.PurchaseKind)
		if err != nil {
			writeError(w, r, err)
			return
		}
		accountID, err := parseAccountID(request.Replacement.AccountID, "replacement.account_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
		envelopeID, err := parseEnvelopeID(request.Replacement.EnvelopeID, "replacement.envelope_id")
		if err != nil {
			writeError(w, r, err)
			return
		}
		var paymentEnvelopeID domain.EnvelopeId
		if strings.TrimSpace(request.Replacement.PaymentEnvelopeID) != "" {
			paymentEnvelopeID, err = parseEnvelopeID(request.Replacement.PaymentEnvelopeID, "replacement.payment_envelope_id")
			if err != nil {
				writeError(w, r, err)
				return
			}
		}
		replacement = &application.ReplacementPurchase{
			Method:          method,
			Account:         accountID,
			Envelope:        envelopeID,
			PaymentEnvelope: paymentEnvelopeID,
			AmountMinor:     request.Replacement.AmountMinor,
		}
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.Correct(r.Context(), mutation, kind, original, request.Reason, originalEnvelope, correctedEnvelope, request.AmountMinor, replacement)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleReconciliation(w http.ResponseWriter, r *http.Request) {
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
	var request reconciliationRequest
	key, fingerprint, err := decodeIdempotentRequest(r, &request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	accountID, err := parseAccountID(request.AccountID, "account_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	funding, err := fundingInput(request.Funding)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mutation, err := mutationFor(r, userID, household, key, fingerprint, request.financialRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	op, err := s.deps.App.Reconcile(r.Context(), mutation, accountID, request.DeltaMinor, funding, request.Reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, DataResponse[operationDTO]{Data: toOperationDTO(op)})
}

func (s *Server) handleResolution(w http.ResponseWriter, r *http.Request) {
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
	status, deficits, err := s.deps.App.Resolution(r.Context(), userID, household)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DataResponse[resolutionDTO]{
		Data: resolutionDTO{Resolution: string(status), Deficits: toDeficitDTOs(deficits)},
	})
}

func parsePurchaseKind(raw string) (domain.PurchaseKind, error) {
	switch domain.PurchaseKind(strings.ToUpper(strings.TrimSpace(raw))) {
	case domain.PurchaseKindDebit, domain.PurchaseKindCash, domain.PurchaseKindCard, domain.PurchaseKindRepayment:
		return domain.PurchaseKind(strings.ToUpper(strings.TrimSpace(raw))), nil
	default:
		return "", domain.NewErrorf(domain.CodeInvalidValue, "unknown purchase kind %q", raw)
	}
}
