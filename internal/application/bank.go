package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/integrations/enablebanking"
	"family-budget/internal/ports"
)

// BankProvider is the narrow provider seam; tests supply a fake.
type BankProvider interface {
	ListASPSPs(ctx context.Context, country string) ([]enablebanking.ASPSP, error)
	StartAuthorization(ctx context.Context, request enablebanking.StartAuthorizationRequest) (enablebanking.StartAuthorizationResponse, error)
	AuthorizeSession(ctx context.Context, code string) (enablebanking.Session, error)
	DeleteSession(ctx context.Context, sessionID string) error
}

// BankDependencies is the explicit wiring for bank-connection use cases.
type BankDependencies struct {
	UnitOfWork  ports.UnitOfWork
	Clock       ports.Clock
	IDs         ports.IDGenerator
	Provider    BankProvider
	RedirectURL string
	Country     string
	StateTTL    time.Duration
	SecretBox   *SecretBox
}

// BankService implements Enable Banking connection use cases.
type BankService struct {
	uow       ports.UnitOfWork
	clock     ports.Clock
	ids       ports.IDGenerator
	provider  BankProvider
	redirect  string
	country   string
	stateTTL  time.Duration
	secretBox *SecretBox
}

// NewBankService validates its dependencies and returns a bank service.
func NewBankService(deps BankDependencies) (*BankService, error) {
	switch {
	case deps.UnitOfWork == nil:
		return nil, errors.New("application: UnitOfWork is required")
	case deps.Clock == nil:
		return nil, errors.New("application: Clock is required")
	case deps.IDs == nil:
		return nil, errors.New("application: IDGenerator is required")
	case deps.Provider == nil:
		return nil, errors.New("application: bank provider is required")
	case deps.SecretBox == nil:
		return nil, errors.New("application: SecretBox is required")
	}
	if !validBankRedirectURL(deps.RedirectURL) {
		return nil, errors.New("application: RedirectURL must be an absolute HTTPS URL (HTTP is allowed for loopback)")
	}
	country := strings.ToUpper(strings.TrimSpace(deps.Country))
	if country == "" {
		country = "BG"
	}
	if !validCountryCode(country) {
		return nil, errors.New("application: Country must be a two-letter code")
	}
	stateTTL := deps.StateTTL
	if stateTTL == 0 {
		stateTTL = 10 * time.Minute
	}
	if stateTTL < time.Minute || stateTTL > time.Hour {
		return nil, errors.New("application: StateTTL must be between 1m and 1h")
	}
	return &BankService{
		uow:       deps.UnitOfWork,
		clock:     deps.Clock,
		ids:       deps.IDs,
		provider:  deps.Provider,
		redirect:  deps.RedirectURL,
		country:   country,
		stateTTL:  stateTTL,
		secretBox: deps.SecretBox,
	}, nil
}

// StartConnectionResult returns the persisted connection and one-time raw state.
// State is only for the caller to carry through the browser redirect; it is
// never stored or logged by the service.
type StartConnectionResult struct {
	Connection ports.BankConnectionRecord
	State      string
}

// ListASPSPs returns the provider banks for the configured country to a member.
func (s *BankService) ListASPSPs(ctx context.Context, actor domain.UserId, household domain.HouseholdId) ([]enablebanking.ASPSP, error) {
	if err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		_, err := authorizeMember(ctx, repos, household, actor)
		return err
	}); err != nil {
		return nil, err
	}
	aspsps, err := s.provider.ListASPSPs(ctx, s.country)
	if err != nil {
		return nil, errors.New("application: unable to list available banks")
	}
	return aspsps, nil
}

// StartConnection creates a pending attempt, then asks the provider for its
// redirect URL outside the transaction.
func (s *BankService) StartConnection(ctx context.Context, actor domain.UserId, household domain.HouseholdId, aspspName, aspspCountry string) (StartConnectionResult, error) {
	name := strings.TrimSpace(aspspName)
	country := strings.ToUpper(strings.TrimSpace(aspspCountry))
	if country == "" {
		country = s.country
	}
	if name == "" || !validCountryCode(country) {
		return StartConnectionResult{}, domain.NewError(domain.CodeInvalidValue, "bank name and country must be valid")
	}
	if err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		_, err := authorizeOwner(ctx, repos, household, actor)
		return err
	}); err != nil {
		return StartConnectionResult{}, err
	}
	aspsps, err := s.provider.ListASPSPs(ctx, country)
	if err != nil {
		return StartConnectionResult{}, errors.New("application: unable to validate selected bank")
	}
	var canonicalName string
	for _, aspsp := range aspsps {
		if strings.EqualFold(strings.TrimSpace(aspsp.Name), name) && strings.EqualFold(strings.TrimSpace(aspsp.Country), country) {
			canonicalName = strings.TrimSpace(aspsp.Name)
			break
		}
	}
	if canonicalName == "" {
		return StartConnectionResult{}, domain.NewError(domain.CodeInvalidValue, "selected bank is not available")
	}

	rawStateBytes := make([]byte, 32)
	if _, err := rand.Read(rawStateBytes); err != nil {
		return StartConnectionResult{}, errors.New("application: unable to generate authorization state")
	}
	state := base64.RawURLEncoding.EncodeToString(rawStateBytes)
	stateDigest := sha256.Sum256(rawStateBytes)
	id, err := s.ids.NewID()
	if err != nil {
		return StartConnectionResult{}, err
	}
	if !isUUIDText(id) {
		return StartConnectionResult{}, domain.NewError(domain.CodeInvalidValue, "generated id is not a UUID")
	}
	id = strings.ToLower(id)
	now := s.clock.Now().UTC()
	record := ports.BankConnectionRecord{
		ID:             id,
		HouseholdID:    household,
		ASPSPName:      canonicalName,
		ASPSPCountry:   country,
		Status:         ports.BankConnectionPending,
		StateHash:      hex.EncodeToString(stateDigest[:]),
		StateExpiresAt: now.Add(s.stateTTL),
		InitiatedBy:    actor,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeOwner(ctx, repos, household, actor); err != nil {
			return err
		}
		return repos.BankConnections.Create(ctx, household, record)
	}); err != nil {
		return StartConnectionResult{}, err
	}

	response, providerErr := s.provider.StartAuthorization(ctx, enablebanking.StartAuthorizationRequest{
		ASPSP:       enablebanking.ASPSPRef{Name: canonicalName, Country: country},
		State:       state,
		RedirectURL: s.redirect,
	})
	if providerErr != nil {
		record.Status = ports.BankConnectionFailed
		record.FailureSummary = stringPointer("Bank authorization could not be started")
		record.UpdatedAt = s.clock.Now().UTC()
		if err := s.saveConnectionAsOwner(ctx, actor, household, record); err != nil {
			return StartConnectionResult{}, err
		}
		return StartConnectionResult{}, errors.New("application: bank authorization could not be started")
	}
	record.AuthorizationID = stringPointer(response.AuthorizationID)
	record.ProviderRedirectURL = stringPointer(response.URL)
	record.UpdatedAt = s.clock.Now().UTC()
	if err := s.saveConnectionAsOwner(ctx, actor, household, record); err != nil {
		return StartConnectionResult{}, err
	}
	return StartConnectionResult{Connection: record, State: state}, nil
}

// ListConnections returns only the caller's household bank connections.
func (s *BankService) ListConnections(ctx context.Context, actor domain.UserId, household domain.HouseholdId) ([]ports.BankConnectionRecord, error) {
	var records []ports.BankConnectionRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		var err error
		records, err = repos.BankConnections.ListByHousehold(ctx, household)
		return err
	})
	return records, err
}

// Disconnect marks an authorized or failed connection disconnected. Provider
// session revocation is intentionally deferred to phase 2.
func (s *BankService) Disconnect(ctx context.Context, actor domain.UserId, household domain.HouseholdId, connectionID string) (ports.BankConnectionRecord, error) {
	var record ports.BankConnectionRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeOwner(ctx, repos, household, actor); err != nil {
			return err
		}
		current, err := repos.BankConnections.Get(ctx, household, connectionID)
		if err != nil {
			return err
		}
		if current.Status != ports.BankConnectionAuthorized && current.Status != ports.BankConnectionFailed {
			return domain.ErrInvalidEntityState
		}
		current.Status = ports.BankConnectionDisconnected
		current.SessionIDEncrypted = nil
		current.SessionExpiresAt = nil
		current.UpdatedAt = s.clock.Now().UTC()
		if err := repos.BankConnections.Save(ctx, current); err != nil {
			return err
		}
		record = current
		return nil
	})
	return record, err
}

// CallbackOutcome is the result class for an unauthenticated provider callback.
type CallbackOutcome string

const (
	CallbackCompleted CallbackOutcome = "COMPLETED"
	CallbackDenied    CallbackOutcome = "DENIED"
	CallbackFailed    CallbackOutcome = "FAILED"
	CallbackInvalid   CallbackOutcome = "INVALID"
)

// CallbackResult contains a connection result and, only on success, provider
// accounts. Provider credentials and the callback code are never exposed.
type CallbackResult struct {
	Outcome    CallbackOutcome
	Connection ports.BankConnectionRecord
	Accounts   []enablebanking.SessionAccount
}

// HandleCallback consumes a one-use state and completes or rejects the provider
// authorization. Invalid, replayed and expired state is reported without error.
func (s *BankService) HandleCallback(ctx context.Context, code, state, providerError, providerErrorDescription string) (CallbackResult, error) {
	_ = providerErrorDescription
	rawState, decodeErr := base64.RawURLEncoding.DecodeString(state)
	if decodeErr != nil || len(rawState) != 32 {
		return CallbackResult{Outcome: CallbackInvalid}, nil
	}
	stateDigest := sha256.Sum256(rawState)
	stateHash := hex.EncodeToString(stateDigest[:])
	var record ports.BankConnectionRecord
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		claimed, err := repos.BankConnections.ClaimPendingState(ctx, stateHash, s.clock.Now().UTC())
		if err != nil {
			return err
		}
		record = claimed
		return nil
	})
	if err != nil {
		return CallbackResult{Outcome: CallbackInvalid}, nil
	}
	if providerError != "" {
		outcome := CallbackFailed
		summary := "Bank authorization was rejected"
		if strings.EqualFold(strings.TrimSpace(providerError), "access_denied") {
			outcome = CallbackDenied
		}
		record, err = s.failConnection(ctx, record, summary)
		if err != nil {
			return CallbackResult{}, err
		}
		return CallbackResult{Outcome: outcome, Connection: record}, nil
	}

	session, providerErr := s.provider.AuthorizeSession(ctx, code)
	if providerErr != nil || strings.TrimSpace(session.SessionID) == "" {
		record, err = s.failConnection(ctx, record, "Bank session authorization failed")
		if err != nil {
			return CallbackResult{}, err
		}
		return CallbackResult{Outcome: CallbackFailed, Connection: record}, nil
	}
	encryptedSessionID, err := s.secretBox.Seal(session.SessionID)
	if err != nil {
		record, err = s.failConnection(ctx, record, "Bank session authorization failed")
		if err != nil {
			return CallbackResult{}, err
		}
		return CallbackResult{Outcome: CallbackFailed, Connection: record}, nil
	}
	record.Status = ports.BankConnectionAuthorized
	record.SessionIDEncrypted = stringPointer(encryptedSessionID)
	if session.Access != nil && session.Access.ValidUntil != nil {
		expiresAt := session.Access.ValidUntil.UTC()
		record.SessionExpiresAt = &expiresAt
	}
	record.FailureSummary = nil
	record.UpdatedAt = s.clock.Now().UTC()
	if err := s.saveConnection(ctx, record); err != nil {
		return CallbackResult{}, err
	}
	accounts := session.Accounts
	if accounts == nil {
		accounts = []enablebanking.SessionAccount{}
	}
	return CallbackResult{Outcome: CallbackCompleted, Connection: record, Accounts: accounts}, nil
}

func (s *BankService) saveConnection(ctx context.Context, record ports.BankConnectionRecord) error {
	return s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		return repos.BankConnections.Save(ctx, record)
	})
}

func (s *BankService) saveConnectionAsOwner(ctx context.Context, actor domain.UserId, household domain.HouseholdId, record ports.BankConnectionRecord) error {
	return s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeOwner(ctx, repos, household, actor); err != nil {
			return err
		}
		return repos.BankConnections.Save(ctx, record)
	})
}

func (s *BankService) failConnection(ctx context.Context, record ports.BankConnectionRecord, summary string) (ports.BankConnectionRecord, error) {
	record.Status = ports.BankConnectionFailed
	record.SessionIDEncrypted = nil
	record.SessionExpiresAt = nil
	record.FailureSummary = stringPointer(summary)
	record.UpdatedAt = s.clock.Now().UTC()
	return record, s.saveConnection(ctx, record)
}

func validBankRedirectURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname()
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func validCountryCode(country string) bool {
	return len(country) == 2 && country[0] >= 'A' && country[0] <= 'Z' && country[1] >= 'A' && country[1] <= 'Z'
}

func stringPointer(value string) *string { return &value }
