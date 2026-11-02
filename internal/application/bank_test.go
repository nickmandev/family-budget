package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/integrations/enablebanking"
	"family-budget/internal/ports"
)

const (
	testOwnerID      domain.UserId      = "owner"
	testMemberID     domain.UserId      = "member"
	testHousehold    domain.HouseholdId = "household-one"
	testHousehold2   domain.HouseholdId = "household-two"
	testConnectionID                    = "123e4567-e89b-12d3-a456-426614174000"
)

type bankTestClock struct{ now time.Time }

func (c *bankTestClock) Now() time.Time { return c.now }

type bankTestIDs struct{ next int }

func (g *bankTestIDs) NewID() (string, error) {
	g.next++
	return fmt.Sprintf("123e4567-e89b-12d3-a456-%012d", g.next), nil
}

type bankTestProvider struct {
	aspsps          []enablebanking.ASPSP
	startRequest    enablebanking.StartAuthorizationRequest
	startResponse   enablebanking.StartAuthorizationResponse
	startErr        error
	session         enablebanking.Session
	sessionErr      error
	startCalls      int
	listCalls       int
	authorizeCalls  int
	listedCountries []string
}

func (p *bankTestProvider) ListASPSPs(_ context.Context, country string) ([]enablebanking.ASPSP, error) {
	p.listCalls++
	p.listedCountries = append(p.listedCountries, country)
	return p.aspsps, nil
}

func (p *bankTestProvider) StartAuthorization(_ context.Context, request enablebanking.StartAuthorizationRequest) (enablebanking.StartAuthorizationResponse, error) {
	p.startCalls++
	p.startRequest = request
	return p.startResponse, p.startErr
}

func (p *bankTestProvider) AuthorizeSession(_ context.Context, code string) (enablebanking.Session, error) {
	p.authorizeCalls++
	if code == "" {
		return enablebanking.Session{}, errors.New("missing code")
	}
	return p.session, p.sessionErr
}

func (*bankTestProvider) DeleteSession(context.Context, string) error { return nil }

type bankTestUOW struct {
	memberships map[string]ports.MembershipRecord
	connections map[string]ports.BankConnectionRecord
}

func newBankTestUOW() *bankTestUOW {
	return &bankTestUOW{
		memberships: map[string]ports.MembershipRecord{
			membershipKey(testHousehold, testOwnerID):  {UserID: testOwnerID, Role: ports.MembershipRoleOwner, Status: ports.MembershipStatusActive},
			membershipKey(testHousehold, testMemberID): {UserID: testMemberID, Role: ports.MembershipRoleMember, Status: ports.MembershipStatusActive},
			membershipKey(testHousehold2, testOwnerID): {UserID: testOwnerID, Role: ports.MembershipRoleOwner, Status: ports.MembershipStatusActive},
		},
		connections: make(map[string]ports.BankConnectionRecord),
	}
}

func (u *bankTestUOW) WithinTransaction(ctx context.Context, fn func(context.Context, ports.Repositories) error) error {
	return fn(ctx, ports.Repositories{
		Households:      bankTestHouseholds{},
		Memberships:     bankTestMemberships{u: u},
		BankConnections: bankTestConnections{u: u},
	})
}

type bankTestHouseholds struct{}

func (bankTestHouseholds) Create(context.Context, ports.HouseholdRecord) error { return nil }
func (bankTestHouseholds) Get(context.Context, domain.HouseholdId) (ports.HouseholdRecord, error) {
	return ports.HouseholdRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
}
func (bankTestHouseholds) SaveSettings(context.Context, ports.HouseholdRecord) error { return nil }

type bankTestMemberships struct{ u *bankTestUOW }

func (m bankTestMemberships) Add(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}
func (m bankTestMemberships) Get(_ context.Context, household domain.HouseholdId, user domain.UserId) (ports.MembershipRecord, error) {
	if record, ok := m.u.memberships[membershipKey(household, user)]; ok {
		return record, nil
	}
	return ports.MembershipRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
}
func (bankTestMemberships) List(context.Context, domain.HouseholdId) ([]ports.MembershipRecord, error) {
	return nil, nil
}
func (bankTestMemberships) ListForUser(context.Context, domain.UserId) ([]ports.UserMembershipRecord, error) {
	return nil, nil
}
func (bankTestMemberships) Save(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}

type bankTestConnections struct{ u *bankTestUOW }

func (r bankTestConnections) Create(_ context.Context, household domain.HouseholdId, record ports.BankConnectionRecord) error {
	record.HouseholdID = household
	r.u.connections[record.ID] = record
	return nil
}
func (r bankTestConnections) Get(_ context.Context, household domain.HouseholdId, id string) (ports.BankConnectionRecord, error) {
	if record, ok := r.u.connections[id]; ok && record.HouseholdID == household {
		return record, nil
	}
	return ports.BankConnectionRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
}
func (r bankTestConnections) ListByHousehold(_ context.Context, household domain.HouseholdId) ([]ports.BankConnectionRecord, error) {
	var records []ports.BankConnectionRecord
	for _, record := range r.u.connections {
		if record.HouseholdID == household {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
	return records, nil
}
func (r bankTestConnections) ClaimPendingState(_ context.Context, stateHash string, now time.Time) (ports.BankConnectionRecord, error) {
	for id, record := range r.u.connections {
		if record.StateHash == stateHash && record.Status == ports.BankConnectionPending && record.StateExpiresAt.After(now) {
			record.Status = ports.BankConnectionAuthorized
			record.UpdatedAt = now
			r.u.connections[id] = record
			return record, nil
		}
	}
	return ports.BankConnectionRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
}
func (r bankTestConnections) Save(_ context.Context, record ports.BankConnectionRecord) error {
	current, ok := r.u.connections[record.ID]
	if !ok || current.HouseholdID != record.HouseholdID {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	current.Status = record.Status
	current.AuthorizationID = record.AuthorizationID
	current.ProviderRedirectURL = record.ProviderRedirectURL
	current.SessionIDEncrypted = record.SessionIDEncrypted
	current.SessionExpiresAt = record.SessionExpiresAt
	current.FailureSummary = record.FailureSummary
	current.UpdatedAt = record.UpdatedAt
	r.u.connections[record.ID] = current
	return nil
}

type bankTestFixture struct {
	service  *BankService
	uow      *bankTestUOW
	clock    *bankTestClock
	provider *bankTestProvider
	box      *SecretBox
}

func newBankTestFixture(t *testing.T) *bankTestFixture {
	t.Helper()
	clock := &bankTestClock{now: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)}
	uow := newBankTestUOW()
	provider := &bankTestProvider{
		aspsps:        []enablebanking.ASPSP{{Name: "Example Bank", Country: "BG"}},
		startResponse: enablebanking.StartAuthorizationResponse{AuthorizationID: "auth-123", URL: "https://provider.example/authorize"},
		session: enablebanking.Session{
			SessionID: "session-secret-value",
			Accounts:  []enablebanking.SessionAccount{{UID: "account-1", Name: "Current account"}},
			Access:    &enablebanking.Access{ValidUntil: timePointer(clock.now.Add(24 * time.Hour))},
		},
	}
	box, err := NewSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewSecretBox: %v", err)
	}
	service, err := NewBankService(BankDependencies{
		UnitOfWork: uow, Clock: clock, IDs: &bankTestIDs{}, Provider: provider,
		RedirectURL: "https://budget-api.example/callbacks/enablebanking", Country: "BG", SecretBox: box,
	})
	if err != nil {
		t.Fatalf("NewBankService: %v", err)
	}
	return &bankTestFixture{service: service, uow: uow, clock: clock, provider: provider, box: box}
}

func (f *bankTestFixture) start(t *testing.T) StartConnectionResult {
	t.Helper()
	result, err := f.service.StartConnection(context.Background(), testOwnerID, testHousehold, " Example Bank ", " bg ")
	if err != nil {
		t.Fatalf("StartConnection: %v", err)
	}
	return result
}

func TestStartConnectionPersistsBeforeProviderCallAndReturnsHashedState(t *testing.T) {
	f := newBankTestFixture(t)
	result := f.start(t)
	if result.Connection.Status != ports.BankConnectionPending {
		t.Fatalf("status = %s, want PENDING", result.Connection.Status)
	}
	if result.Connection.AuthorizationID == nil || *result.Connection.AuthorizationID != "auth-123" {
		t.Fatalf("authorization ID = %v", result.Connection.AuthorizationID)
	}
	if result.Connection.ProviderRedirectURL == nil || *result.Connection.ProviderRedirectURL != "https://provider.example/authorize" {
		t.Fatalf("redirect URL = %v", result.Connection.ProviderRedirectURL)
	}
	// State is base64url encoding of 32 random bytes; hash those bytes, not the text.
	rawState, err := decodeRawState(result.State)
	if err != nil {
		t.Fatalf("decode state: %v", err)
	}
	stateHash := sha256.Sum256(rawState)
	if result.Connection.StateHash != hex.EncodeToString(stateHash[:]) {
		t.Fatalf("state hash = %s, does not match returned state", result.Connection.StateHash)
	}
	request := f.provider.startRequest
	if request.State != result.State || request.RedirectURL != "https://budget-api.example/callbacks/enablebanking" ||
		request.ASPSP.Name != "Example Bank" || request.ASPSP.Country != "BG" {
		t.Fatalf("provider request = %+v", request)
	}
	if got := f.uow.connections[result.Connection.ID]; got.Status != ports.BankConnectionPending {
		t.Fatalf("persisted status before start provider call = %s", got.Status)
	}
}

func TestStartConnectionAuthorizationValidationAndProviderFailure(t *testing.T) {
	t.Run("non-owner member", func(t *testing.T) {
		f := newBankTestFixture(t)
		_, err := f.service.StartConnection(context.Background(), testMemberID, testHousehold, "Example Bank", "BG")
		assertBankCode(t, err, domain.CodeHouseholdAccessDenied)
	})
	t.Run("non-member", func(t *testing.T) {
		f := newBankTestFixture(t)
		_, err := f.service.StartConnection(context.Background(), "outsider", testHousehold, "Example Bank", "BG")
		assertBankCode(t, err, domain.CodeEntityNotFoundOrNotInHousehold)
	})
	t.Run("unknown bank", func(t *testing.T) {
		f := newBankTestFixture(t)
		_, err := f.service.StartConnection(context.Background(), testOwnerID, testHousehold, "Injected Bank", "BG")
		assertBankCode(t, err, domain.CodeInvalidValue)
		if len(f.uow.connections) != 0 || f.provider.startCalls != 0 {
			t.Fatal("unknown bank created a connection or started provider authorization")
		}
	})
	t.Run("provider failure", func(t *testing.T) {
		f := newBankTestFixture(t)
		f.provider.startErr = errors.New("provider response contains private body")
		_, err := f.service.StartConnection(context.Background(), testOwnerID, testHousehold, "Example Bank", "BG")
		if err == nil || strings.Contains(err.Error(), "private body") {
			t.Fatalf("provider failure error = %v", err)
		}
		if len(f.uow.connections) != 1 {
			t.Fatalf("persisted rows = %d, want failed attempt", len(f.uow.connections))
		}
		for _, record := range f.uow.connections {
			if record.Status != ports.BankConnectionFailed || record.FailureSummary == nil || strings.Contains(*record.FailureSummary, "private body") {
				t.Fatalf("failed connection = %+v", record)
			}
		}
	})
}

func TestHandleCallbackCompletesOnceWithEncryptedSession(t *testing.T) {
	f := newBankTestFixture(t)
	started := f.start(t)
	result, err := f.service.HandleCallback(context.Background(), "one-time-code", started.State, "", "")
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if result.Outcome != CallbackCompleted || len(result.Accounts) != 1 || result.Connection.Status != ports.BankConnectionAuthorized {
		t.Fatalf("callback result = %+v", result)
	}
	stored := f.uow.connections[started.Connection.ID]
	if stored.SessionIDEncrypted == nil || *stored.SessionIDEncrypted == f.provider.session.SessionID {
		t.Fatalf("stored session id was not encrypted: %v", stored.SessionIDEncrypted)
	}
	opened, err := f.box.Open(*stored.SessionIDEncrypted)
	if err != nil || opened != f.provider.session.SessionID {
		t.Fatalf("decrypt stored session: value=%q err=%v", opened, err)
	}
	before := stored
	replay, err := f.service.HandleCallback(context.Background(), "one-time-code", started.State, "", "")
	if err != nil || replay.Outcome != CallbackInvalid || f.provider.authorizeCalls != 1 {
		t.Fatalf("replay result = %+v, calls=%d, err=%v", replay, f.provider.authorizeCalls, err)
	}
	if after := f.uow.connections[started.Connection.ID]; after.Status != before.Status || after.SessionIDEncrypted != before.SessionIDEncrypted {
		t.Fatalf("replay changed connection: before=%+v after=%+v", before, after)
	}
}

func TestHandleCallbackExpiredStateIsInvalid(t *testing.T) {
	f := newBankTestFixture(t)
	started := f.start(t)
	f.clock.now = started.Connection.StateExpiresAt
	result, err := f.service.HandleCallback(context.Background(), "code", started.State, "", "")
	if err != nil || result.Outcome != CallbackInvalid || f.provider.authorizeCalls != 0 {
		t.Fatalf("expired callback = %+v, exchange calls=%d, err=%v", result, f.provider.authorizeCalls, err)
	}
	if f.uow.connections[started.Connection.ID].Status != ports.BankConnectionPending {
		t.Fatal("expired attempt was mutated")
	}
}

func TestHandleCallbackProviderDenialAndExchangeFailure(t *testing.T) {
	t.Run("access denied", func(t *testing.T) {
		f := newBankTestFixture(t)
		started := f.start(t)
		result, err := f.service.HandleCallback(context.Background(), "unused-code", started.State, "access_denied", "raw provider details")
		if err != nil || result.Outcome != CallbackDenied || result.Connection.Status != ports.BankConnectionFailed {
			t.Fatalf("denied callback = %+v, err=%v", result, err)
		}
		if f.provider.authorizeCalls != 0 || f.uow.connections[started.Connection.ID].FailureSummary == nil {
			t.Fatal("denial was exchanged or not persisted")
		}
	})
	t.Run("exchange failure is generic", func(t *testing.T) {
		f := newBankTestFixture(t)
		started := f.start(t)
		f.provider.sessionErr = errors.New("provider returned sensitive error")
		code := "sensitive-code-value"
		result, err := f.service.HandleCallback(context.Background(), code, started.State, "", "")
		if err != nil || result.Outcome != CallbackFailed || result.Connection.Status != ports.BankConnectionFailed {
			t.Fatalf("failed callback = %+v, err=%v", result, err)
		}
		failure := f.uow.connections[started.Connection.ID].FailureSummary
		if failure == nil || strings.Contains(*failure, code) || strings.Contains(*failure, "sensitive") {
			t.Fatalf("failure summary leaked sensitive input: %v", failure)
		}
	})
}

func TestDisconnectRequiresOwnerAndAllowedState(t *testing.T) {
	t.Run("authorized clears session", func(t *testing.T) {
		f := newBankTestFixture(t)
		started := f.start(t)
		if _, err := f.service.HandleCallback(context.Background(), "code", started.State, "", ""); err != nil {
			t.Fatalf("HandleCallback: %v", err)
		}
		disconnected, err := f.service.Disconnect(context.Background(), testOwnerID, testHousehold, started.Connection.ID)
		if err != nil || disconnected.Status != ports.BankConnectionDisconnected || disconnected.SessionIDEncrypted != nil || disconnected.SessionExpiresAt != nil {
			t.Fatalf("Disconnect = %+v, err=%v", disconnected, err)
		}
	})
	t.Run("pending invalid state", func(t *testing.T) {
		f := newBankTestFixture(t)
		started := f.start(t)
		_, err := f.service.Disconnect(context.Background(), testOwnerID, testHousehold, started.Connection.ID)
		assertBankCode(t, err, domain.CodeInvalidEntityState)
	})
	t.Run("non-owner rejected", func(t *testing.T) {
		f := newBankTestFixture(t)
		started := f.start(t)
		_, err := f.service.Disconnect(context.Background(), testMemberID, testHousehold, started.Connection.ID)
		assertBankCode(t, err, domain.CodeHouseholdAccessDenied)
	})
}

func TestListConnectionsIsHouseholdScoped(t *testing.T) {
	f := newBankTestFixture(t)
	first := f.start(t)
	other := first.Connection
	other.ID = "123e4567-e89b-12d3-a456-999999999999"
	other.HouseholdID = testHousehold2
	other.CreatedAt = other.CreatedAt.Add(time.Minute)
	f.uow.connections[other.ID] = other
	records, err := f.service.ListConnections(context.Background(), testOwnerID, testHousehold)
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	if len(records) != 1 || records[0].ID != first.Connection.ID {
		t.Fatalf("household connections = %+v, want only %s", records, first.Connection.ID)
	}
}

func decodeRawState(state string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(state)
}

func assertBankCode(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	if !domain.HasCode(err, code) {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func membershipKey(household domain.HouseholdId, user domain.UserId) string {
	return household.String() + "/" + user.String()
}

func timePointer(value time.Time) *time.Time { return &value }
