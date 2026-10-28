package httpapi

import (
	"time"

	"family-budget/internal/application"
	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

type householdDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Currency  string `json:"currency"`
	Timezone  string `json:"timezone"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type memberDTO struct {
	UserID    string  `json:"user_id"`
	Role      string  `json:"role"`
	Status    string  `json:"status"`
	JoinedAt  string  `json:"joined_at"`
	RemovedAt *string `json:"removed_at"`
}

type accountDTO struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	BudgetEligible bool   `json:"budget_eligible"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type envelopeDTO struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Status              string  `json:"status"`
	LinkedCardAccountID *string `json:"linked_card_account_id"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
}

type accountBalanceDTO struct {
	AccountID      string `json:"account_id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	PostedMinor    int64  `json:"posted_minor"`
	SpendableMinor int64  `json:"spendable_minor"`
	ReservedMinor  int64  `json:"reserved_minor"`
}

type envelopeBalanceDTO struct {
	EnvelopeID     string `json:"envelope_id"`
	Name           string `json:"name"`
	BalanceMinor   int64  `json:"balance_minor"`
	ReservedMinor  int64  `json:"reserved_minor"`
	SpendableMinor int64  `json:"spendable_minor"`
}

type deficitDTO struct {
	Kind        string  `json:"kind"`
	EnvelopeID  *string `json:"envelope_id"`
	AmountMinor int64   `json:"amount_minor"`
}

type balancesDTO struct {
	Currency            string               `json:"currency"`
	BudgetableCashMinor int64                `json:"budgetable_cash_minor"`
	ReadyToAssignMinor  int64                `json:"ready_to_assign_minor"`
	Resolution          string               `json:"resolution"`
	Accounts            []accountBalanceDTO  `json:"accounts"`
	Envelopes           []envelopeBalanceDTO `json:"envelopes"`
	Deficits            []deficitDTO         `json:"deficits"`
}

type resolutionDTO struct {
	Resolution string       `json:"resolution"`
	Deficits   []deficitDTO `json:"deficits"`
}

type postingDTO struct {
	Kind       string `json:"kind"`
	AccountID  string `json:"account_id,omitempty"`
	EnvelopeID string `json:"envelope_id,omitempty"`
	DeltaMinor int64  `json:"delta_minor"`
}

type relatedDTO struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id"`
}

type operationDTO struct {
	ID            string       `json:"id"`
	Type          string       `json:"type"`
	Source        string       `json:"source"`
	EffectiveDate string       `json:"effective_date"`
	Currency      string       `json:"currency"`
	AmountMinor   *int64       `json:"amount_minor"`
	ActorID       string       `json:"actor_id"`
	Reason        *string      `json:"reason"`
	CommittedAt   string       `json:"committed_at"`
	Postings      []postingDTO `json:"postings"`
	Related       []relatedDTO `json:"related"`
}

type planDTO struct {
	ID                    string  `json:"id"`
	Kind                  string  `json:"kind"`
	State                 string  `json:"state"`
	TargetEnvelopeID      string  `json:"target_envelope_id"`
	TargetAccountID       *string `json:"target_account_id"`
	IntendedAmountMinor   int64   `json:"intended_amount_minor"`
	AuthorizedAmountMinor *int64  `json:"authorized_amount_minor"`
	HeldAmountMinor       int64   `json:"held_amount_minor"`
	HeldOnAccountMinor    int64   `json:"held_on_account_minor"`
	Mode                  *string `json:"mode"`
	Reason                *string `json:"reason"`
}

type suggestionDTO struct {
	ID                string  `json:"id"`
	Source            string  `json:"source"`
	AmountMinor       int64   `json:"amount_minor"`
	Merchant          string  `json:"merchant"`
	State             string  `json:"state"`
	CancelReason      *string `json:"cancel_reason"`
	ConfirmationCount int     `json:"confirmation_count"`
}

func toHouseholdDTO(record ports.HouseholdRecord) householdDTO {
	return householdDTO{
		ID:        record.ID.String(),
		Name:      record.Name,
		Currency:  record.Currency.Code(),
		Timezone:  record.Timezone,
		Status:    string(record.Status),
		CreatedAt: formatTimestamp(record.CreatedAt),
		UpdatedAt: formatTimestamp(record.UpdatedAt),
	}
}

func toHouseholdDTOs(records []ports.HouseholdRecord) []householdDTO {
	dtos := make([]householdDTO, 0, len(records))
	for _, record := range records {
		dtos = append(dtos, toHouseholdDTO(record))
	}
	return dtos
}

func toMemberResourceDTO(record ports.MembershipRecord) memberDTO {
	dto := memberDTO{
		UserID:   record.UserID.String(),
		Role:     string(record.Role),
		Status:   string(record.Status),
		JoinedAt: formatTimestamp(record.JoinedAt),
	}
	if record.RemovedAt != nil {
		value := formatTimestamp(*record.RemovedAt)
		dto.RemovedAt = &value
	}
	return dto
}

func toAccountDTO(record ports.AccountRecord) accountDTO {
	return accountDTO{
		ID:             record.ID.String(),
		Name:           record.Name,
		Kind:           string(record.Kind),
		BudgetEligible: record.BudgetEligible,
		Status:         string(record.Status),
		CreatedAt:      formatTimestamp(record.CreatedAt),
		UpdatedAt:      formatTimestamp(record.UpdatedAt),
	}
}

func toEnvelopeDTO(record ports.EnvelopeRecord) envelopeDTO {
	dto := envelopeDTO{
		ID:        record.ID.String(),
		Name:      record.Name,
		Status:    string(record.Status),
		CreatedAt: formatTimestamp(record.CreatedAt),
		UpdatedAt: formatTimestamp(record.UpdatedAt),
	}
	if record.LinkedCardAccountID != nil && !record.LinkedCardAccountID.IsZero() {
		value := record.LinkedCardAccountID.String()
		dto.LinkedCardAccountID = &value
	}
	return dto
}

func toBalancesDTO(report application.BalanceReport) balancesDTO {
	dto := balancesDTO{
		Currency:            report.BudgetableCash.Currency().Code(),
		BudgetableCashMinor: report.BudgetableCash.MinorUnits(),
		ReadyToAssignMinor:  report.ReadyToAssign.MinorUnits(),
		Resolution:          string(report.Resolution),
		Accounts:            make([]accountBalanceDTO, 0, len(report.Accounts)),
		Envelopes:           make([]envelopeBalanceDTO, 0, len(report.Envelopes)),
		Deficits:            toDeficitDTOs(report.Deficits),
	}
	for _, item := range report.Accounts {
		dto.Accounts = append(dto.Accounts, accountBalanceDTO{
			AccountID:      item.Account.ID.String(),
			Name:           item.Account.Name,
			Kind:           string(item.Account.Kind),
			PostedMinor:    item.Posted.MinorUnits(),
			SpendableMinor: item.Spendable.MinorUnits(),
			ReservedMinor:  item.Reserved.MinorUnits(),
		})
	}
	for _, item := range report.Envelopes {
		dto.Envelopes = append(dto.Envelopes, envelopeBalanceDTO{
			EnvelopeID:     item.Envelope.ID.String(),
			Name:           item.Envelope.Name,
			BalanceMinor:   item.Balance.MinorUnits(),
			ReservedMinor:  item.Reserved.MinorUnits(),
			SpendableMinor: item.Spendable.MinorUnits(),
		})
	}
	return dto
}

func toDeficitDTOs(deficits []domain.BudgetDeficit) []deficitDTO {
	dtos := make([]deficitDTO, 0, len(deficits))
	for _, deficit := range deficits {
		dto := deficitDTO{
			Kind:        string(deficit.Kind),
			AmountMinor: deficit.Amount.MinorUnits(),
		}
		if !deficit.EnvelopeID.IsZero() {
			value := deficit.EnvelopeID.String()
			dto.EnvelopeID = &value
		}
		dtos = append(dtos, dto)
	}
	return dtos
}

func toOperationDTO(op domain.JournalOperation) operationDTO {
	dto := operationDTO{
		ID:            op.ID().String(),
		Type:          string(op.Type()),
		Source:        string(op.Source()),
		EffectiveDate: formatEffectiveDate(op.EffectiveDate()),
		Currency:      op.Currency().Code(),
		ActorID:       op.Actor().String(),
		CommittedAt:   formatTimestamp(op.CommittedAt().Time()),
		Postings:      make([]postingDTO, 0, len(op.Postings().Accounts)+len(op.Postings().Envelopes)),
		Related:       make([]relatedDTO, 0, len(op.Related())),
	}
	if amount, ok := op.Amount(); ok {
		value := amount.MinorUnits()
		dto.AmountMinor = &value
	}
	if reason := op.Reason(); reason != "" {
		value := reason
		dto.Reason = &value
	}
	for _, posting := range op.Postings().Accounts {
		delta, err := posting.Delta.Money()
		if err != nil {
			continue
		}
		dto.Postings = append(dto.Postings, postingDTO{
			Kind:       "ACCOUNT",
			AccountID:  posting.AccountID.String(),
			DeltaMinor: delta.MinorUnits(),
		})
	}
	for _, posting := range op.Postings().Envelopes {
		delta, err := posting.Delta.Money()
		if err != nil {
			continue
		}
		dto.Postings = append(dto.Postings, postingDTO{
			Kind:       "ENVELOPE",
			EnvelopeID: posting.EnvelopeID.String(),
			DeltaMinor: delta.MinorUnits(),
		})
	}
	for _, related := range op.Related() {
		dto.Related = append(dto.Related, relatedDTO{
			Kind:        related.Kind.String(),
			OperationID: related.OperationID.String(),
		})
	}
	return dto
}

func toOperationDTOs(ops []domain.JournalOperation) []operationDTO {
	dtos := make([]operationDTO, 0, len(ops))
	for _, op := range ops {
		dtos = append(dtos, toOperationDTO(op))
	}
	return dtos
}

func toPlanDTO(plan *domain.PlannedOperation) planDTO {
	target := plan.Target()
	dto := planDTO{
		ID:                  plan.ID().String(),
		Kind:                string(plan.Kind()),
		State:               string(plan.State()),
		TargetEnvelopeID:    target.EnvelopeID.String(),
		IntendedAmountMinor: plan.IntendedAmount().MinorUnits(),
		HeldAmountMinor:     plan.HeldAmount().MinorUnits(),
		HeldOnAccountMinor:  plan.HeldOnAccount().MinorUnits(),
	}
	if !target.AccountID.IsZero() {
		value := target.AccountID.String()
		dto.TargetAccountID = &value
	}
	if authorized := plan.AuthorizedAmount(); authorized.MinorUnits() > 0 {
		value := authorized.MinorUnits()
		dto.AuthorizedAmountMinor = &value
	}
	if mode := plan.Mode(); mode != "" {
		value := string(mode)
		dto.Mode = &value
	}
	if reason := plan.Reason(); reason != "" {
		value := string(reason)
		dto.Reason = &value
	}
	return dto
}

func toPlanDTOs(plans []*domain.PlannedOperation) []planDTO {
	dtos := make([]planDTO, 0, len(plans))
	for _, plan := range plans {
		dtos = append(dtos, toPlanDTO(plan))
	}
	return dtos
}

func toSuggestionDTO(suggestion *domain.Suggestion) suggestionDTO {
	dto := suggestionDTO{
		ID:                suggestion.ID().String(),
		Source:            string(suggestion.Source()),
		AmountMinor:       suggestion.Amount().MinorUnits(),
		Merchant:          suggestion.Merchant(),
		State:             string(suggestion.State()),
		ConfirmationCount: suggestion.ConfirmationCount(),
	}
	if reason := suggestion.CancelReason(); reason != "" {
		value := reason
		dto.CancelReason = &value
	}
	return dto
}

func toSuggestionDTOs(suggestions []*domain.Suggestion) []suggestionDTO {
	dtos := make([]suggestionDTO, 0, len(suggestions))
	for _, suggestion := range suggestions {
		dtos = append(dtos, toSuggestionDTO(suggestion))
	}
	return dtos
}

// parseCurrency maps an API currency code to a domain currency. The contract
// currently exposes the common two-decimal currencies; exponent exceptions can
// be added here without touching the domain.
func parseCurrency(code string) (domain.Currency, error) {
	exponent := 2
	switch code {
	case "JPY", "KRW", "VND", "CLP", "ISK":
		exponent = 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		exponent = 3
	}
	currency, err := domain.NewCurrency(code, exponent)
	if err != nil {
		return domain.Currency{}, domain.NewError(domain.CodeInvalidValue, "currency must be a supported ISO-4217 code")
	}
	return currency, nil
}

func parseLifecycleStatus(raw string) (ports.LifecycleStatus, error) {
	switch ports.LifecycleStatus(raw) {
	case ports.LifecycleActive, ports.LifecycleArchived:
		return ports.LifecycleStatus(raw), nil
	default:
		return "", domain.NewErrorf(domain.CodeInvalidValue, "unknown lifecycle status %q", raw)
	}
}

func timeOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
