package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"family-budget/internal/application"
	"family-budget/internal/auth"
	"family-budget/internal/config"
	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

type memoryLedgerStore struct {
	mu               sync.Mutex
	households       map[domain.HouseholdId]ports.HouseholdRecord
	accounts         map[domain.HouseholdId]map[domain.AccountId]ports.AccountRecord
	envelopes        map[domain.HouseholdId]map[domain.EnvelopeId]ports.EnvelopeRecord
	journal          map[domain.HouseholdId][]domain.JournalOperation
	versions         map[domain.HouseholdId]uint64
	idempotency      map[string]domain.IdempotencyRecord
	plans            map[domain.HouseholdId]map[domain.PlanId]*domain.PlannedOperation
	planStates       map[string]domain.PlanState
	suggestions      map[domain.HouseholdId]map[domain.SuggestionId]*domain.Suggestion
	suggestionStates map[string]domain.SuggestionState
}

func newMemoryLedgerStore() *memoryLedgerStore {
	return &memoryLedgerStore{
		households:       make(map[domain.HouseholdId]ports.HouseholdRecord),
		accounts:         make(map[domain.HouseholdId]map[domain.AccountId]ports.AccountRecord),
		envelopes:        make(map[domain.HouseholdId]map[domain.EnvelopeId]ports.EnvelopeRecord),
		journal:          make(map[domain.HouseholdId][]domain.JournalOperation),
		versions:         make(map[domain.HouseholdId]uint64),
		idempotency:      make(map[string]domain.IdempotencyRecord),
		plans:            make(map[domain.HouseholdId]map[domain.PlanId]*domain.PlannedOperation),
		planStates:       make(map[string]domain.PlanState),
		suggestions:      make(map[domain.HouseholdId]map[domain.SuggestionId]*domain.Suggestion),
		suggestionStates: make(map[string]domain.SuggestionState),
	}
}

func (s *memoryLedgerStore) WithinTransaction(_ context.Context, fn func(context.Context, ports.Repositories) error) error {
	return fn(context.Background(), ports.Repositories{
		Households:   &memoryHouseholds{store: s},
		Memberships:  nil,
		Accounts:     &memoryAccounts{store: s},
		Envelopes:    &memoryEnvelopes{store: s},
		Journal:      &memoryJournal{store: s},
		Reservations: &memoryReservations{store: s},
		Plans:        &memoryPlans{store: s},
		Suggestions:  &memorySuggestions{store: s},
		Idempotency:  &memoryIdempotency{store: s},
	})
}

type memoryHouseholds struct{ store *memoryLedgerStore }

func (r *memoryHouseholds) Create(_ context.Context, household ports.HouseholdRecord) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if _, ok := r.store.households[household.ID]; ok {
		return domain.NewError(domain.CodeInvalidValue, "duplicate household")
	}
	r.store.households[household.ID] = household
	return nil
}

func (r *memoryHouseholds) Get(_ context.Context, id domain.HouseholdId) (ports.HouseholdRecord, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	record, ok := r.store.households[id]
	if !ok {
		return ports.HouseholdRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return record, nil
}

func (r *memoryHouseholds) SaveSettings(_ context.Context, household ports.HouseholdRecord) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if _, ok := r.store.households[household.ID]; !ok {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	r.store.households[household.ID] = household
	return nil
}

type memoryAccounts struct{ store *memoryLedgerStore }

func (r *memoryAccounts) Create(_ context.Context, household domain.HouseholdId, account ports.AccountRecord) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if r.store.accounts[household] == nil {
		r.store.accounts[household] = make(map[domain.AccountId]ports.AccountRecord)
	}
	if _, ok := r.store.accounts[household][account.ID]; ok {
		return domain.NewError(domain.CodeInvalidValue, "duplicate account")
	}
	r.store.accounts[household][account.ID] = account
	return nil
}

func (r *memoryAccounts) Get(_ context.Context, household domain.HouseholdId, id domain.AccountId) (ports.AccountRecord, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	account, ok := r.store.accounts[household][id]
	if !ok {
		return ports.AccountRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return account, nil
}

func (r *memoryAccounts) List(_ context.Context, household domain.HouseholdId, filter ports.AccountFilter) ([]ports.AccountRecord, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	records := make([]ports.AccountRecord, 0, len(r.store.accounts[household]))
	for _, record := range r.store.accounts[household] {
		if !filter.IncludeArchived && record.Status != ports.LifecycleActive {
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID.String() < records[j].ID.String() })
	return records, nil
}

func (r *memoryAccounts) Save(_ context.Context, household domain.HouseholdId, account ports.AccountRecord) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if _, ok := r.store.accounts[household][account.ID]; !ok {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	r.store.accounts[household][account.ID] = account
	return nil
}

func (r *memoryAccounts) HasActiveReservations(context.Context, domain.HouseholdId, domain.AccountId) (bool, error) {
	return false, nil
}

type memoryEnvelopes struct{ store *memoryLedgerStore }

func (r *memoryEnvelopes) Create(_ context.Context, household domain.HouseholdId, envelope ports.EnvelopeRecord) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if r.store.envelopes[household] == nil {
		r.store.envelopes[household] = make(map[domain.EnvelopeId]ports.EnvelopeRecord)
	}
	if _, ok := r.store.envelopes[household][envelope.ID]; ok {
		return domain.NewError(domain.CodeInvalidValue, "duplicate envelope")
	}
	r.store.envelopes[household][envelope.ID] = envelope
	return nil
}

func (r *memoryEnvelopes) Get(_ context.Context, household domain.HouseholdId, id domain.EnvelopeId) (ports.EnvelopeRecord, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	envelope, ok := r.store.envelopes[household][id]
	if !ok {
		return ports.EnvelopeRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return envelope, nil
}

func (r *memoryEnvelopes) List(_ context.Context, household domain.HouseholdId, filter ports.EnvelopeFilter) ([]ports.EnvelopeRecord, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	records := make([]ports.EnvelopeRecord, 0, len(r.store.envelopes[household]))
	for _, record := range r.store.envelopes[household] {
		if !filter.IncludeArchived && record.Status != ports.LifecycleActive {
			continue
		}
		if filter.LinkedCardAccountID != nil {
			if record.LinkedCardAccountID == nil || *record.LinkedCardAccountID != *filter.LinkedCardAccountID {
				continue
			}
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID.String() < records[j].ID.String() })
	return records, nil
}

func (r *memoryEnvelopes) Save(_ context.Context, household domain.HouseholdId, envelope ports.EnvelopeRecord) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if _, ok := r.store.envelopes[household][envelope.ID]; !ok {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	r.store.envelopes[household][envelope.ID] = envelope
	return nil
}

func (r *memoryEnvelopes) HasActiveReservations(context.Context, domain.HouseholdId, domain.EnvelopeId) (bool, error) {
	return false, nil
}

type memoryJournal struct{ store *memoryLedgerStore }

func (r *memoryJournal) Append(_ context.Context, household domain.HouseholdId, op domain.JournalOperation) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if op.Household() != household {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	for _, existing := range r.store.journal[household] {
		if existing.ID() == op.ID() {
			return domain.NewError(domain.CodeInvalidValue, "duplicate operation")
		}
	}
	r.store.journal[household] = append(r.store.journal[household], op)
	r.store.versions[household]++
	return nil
}

func (r *memoryJournal) Get(_ context.Context, household domain.HouseholdId, id domain.JournalOperationId) (domain.JournalOperation, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	for _, op := range r.store.journal[household] {
		if op.ID() == id {
			return op, nil
		}
	}
	return domain.JournalOperation{}, domain.ErrEntityNotFoundOrNotInHousehold
}

func (r *memoryJournal) List(_ context.Context, household domain.HouseholdId, filter ports.JournalFilter) ([]domain.JournalOperation, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	ops := append([]domain.JournalOperation(nil), r.store.journal[household]...)
	sort.Slice(ops, func(i, j int) bool {
		left, right := ops[i], ops[j]
		if !left.CommittedAt().Time().Equal(right.CommittedAt().Time()) {
			return left.CommittedAt().Time().Before(right.CommittedAt().Time())
		}
		return left.ID().String() < right.ID().String()
	})
	result := make([]domain.JournalOperation, 0)
	for _, op := range ops {
		if len(filter.Types) > 0 {
			found := false
			for _, opType := range filter.Types {
				if op.Type() == opType {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if filter.Source != "" && op.Source() != filter.Source {
			continue
		}
		if filter.From != nil && op.EffectiveDate().String() < filter.From.String() {
			continue
		}
		if filter.To != nil && op.EffectiveDate().String() > filter.To.String() {
			continue
		}
		if filter.Cursor != nil {
			after := op.CommittedAt().Time().After(filter.Cursor.CommittedAt)
			if !after && !(op.CommittedAt().Time().Equal(filter.Cursor.CommittedAt) && op.ID().String() > filter.Cursor.ID.String()) {
				continue
			}
		}
		result = append(result, op)
		limit := filter.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 200 {
			limit = 200
		}
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (r *memoryJournal) Version(_ context.Context, household domain.HouseholdId) (uint64, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	return r.store.versions[household], nil
}

type memoryReservations struct{ store *memoryLedgerStore }

func (r *memoryReservations) zero(household domain.HouseholdId) (domain.Money, error) {
	record, ok := r.store.households[household]
	if !ok {
		return domain.Money{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return domain.NewMoney(0, record.Currency)
}

func (r *memoryReservations) ActiveEnvelopeHolds(_ context.Context, household domain.HouseholdId, _ domain.EnvelopeId) (domain.Money, error) {
	return r.zero(household)
}

func (r *memoryReservations) ActiveAccountHolds(_ context.Context, household domain.HouseholdId, _ domain.AccountId) (domain.Money, error) {
	return r.zero(household)
}

func (r *memoryReservations) HoldsForPlan(_ context.Context, household domain.HouseholdId, _ domain.PlanId) (domain.Money, domain.Money, error) {
	envelope, err := r.zero(household)
	if err != nil {
		return domain.Money{}, domain.Money{}, err
	}
	account, err := r.zero(household)
	return envelope, account, err
}

type memoryPlans struct{ store *memoryLedgerStore }

func (r *memoryPlans) Create(_ context.Context, household domain.HouseholdId, plan *domain.PlannedOperation) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if r.store.plans[household] == nil {
		r.store.plans[household] = make(map[domain.PlanId]*domain.PlannedOperation)
	}
	if _, ok := r.store.plans[household][plan.ID()]; ok {
		return domain.NewError(domain.CodeInvalidValue, "duplicate plan")
	}
	r.store.plans[household][plan.ID()] = plan
	r.store.planStates[household.String()+"|"+plan.ID().String()] = plan.State()
	return nil
}

func (r *memoryPlans) Get(_ context.Context, household domain.HouseholdId, id domain.PlanId) (*domain.PlannedOperation, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	plan, ok := r.store.plans[household][id]
	if !ok {
		return nil, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return plan, nil
}

func (r *memoryPlans) List(_ context.Context, household domain.HouseholdId) ([]*domain.PlannedOperation, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	plans := make([]*domain.PlannedOperation, 0, len(r.store.plans[household]))
	for _, plan := range r.store.plans[household] {
		plans = append(plans, plan)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].ID().String() < plans[j].ID().String() })
	return plans, nil
}

func (r *memoryPlans) Save(_ context.Context, household domain.HouseholdId, plan *domain.PlannedOperation, expected domain.PlanState) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	_, ok := r.store.plans[household][plan.ID()]
	if !ok {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	key := household.String() + "|" + plan.ID().String()
	if _, ok := r.store.plans[household][plan.ID()]; !ok {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	if r.store.planStates[key] != expected {
		return domain.NewError(domain.CodeConcurrencyConflict, "plan changed concurrently")
	}
	r.store.plans[household][plan.ID()] = plan
	r.store.planStates[key] = plan.State()
	return nil
}

type memorySuggestions struct{ store *memoryLedgerStore }

func (r *memorySuggestions) Create(_ context.Context, household domain.HouseholdId, suggestion *domain.Suggestion) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if r.store.suggestions[household] == nil {
		r.store.suggestions[household] = make(map[domain.SuggestionId]*domain.Suggestion)
	}
	if _, ok := r.store.suggestions[household][suggestion.ID()]; ok {
		return domain.NewError(domain.CodeInvalidValue, "duplicate suggestion")
	}
	r.store.suggestions[household][suggestion.ID()] = suggestion
	r.store.suggestionStates[household.String()+"|"+suggestion.ID().String()] = suggestion.State()
	return nil
}

func (r *memorySuggestions) Get(_ context.Context, household domain.HouseholdId, id domain.SuggestionId) (*domain.Suggestion, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	suggestion, ok := r.store.suggestions[household][id]
	if !ok {
		return nil, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return suggestion, nil
}

func (r *memorySuggestions) List(_ context.Context, household domain.HouseholdId) ([]*domain.Suggestion, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	suggestions := make([]*domain.Suggestion, 0, len(r.store.suggestions[household]))
	for _, suggestion := range r.store.suggestions[household] {
		suggestions = append(suggestions, suggestion)
	}
	sort.Slice(suggestions, func(i, j int) bool { return suggestions[i].ID().String() < suggestions[j].ID().String() })
	return suggestions, nil
}

func (r *memorySuggestions) Save(_ context.Context, household domain.HouseholdId, suggestion *domain.Suggestion, expected domain.SuggestionState) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	_, ok := r.store.suggestions[household][suggestion.ID()]
	if !ok {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	key := household.String() + "|" + suggestion.ID().String()
	if _, ok := r.store.suggestions[household][suggestion.ID()]; !ok {
		return domain.ErrEntityNotFoundOrNotInHousehold
	}
	if r.store.suggestionStates[key] != expected {
		return domain.NewError(domain.CodeConcurrencyConflict, "suggestion changed concurrently")
	}
	r.store.suggestions[household][suggestion.ID()] = suggestion
	r.store.suggestionStates[key] = suggestion.State()
	return nil
}

type memoryIdempotency struct{ store *memoryLedgerStore }

func keyFor(household domain.HouseholdId, key domain.IdempotencyKey) string {
	return household.String() + "|" + key.String()
}

func (r *memoryIdempotency) Begin(_ context.Context, household domain.HouseholdId, key domain.IdempotencyKey, fingerprint domain.RequestFingerprint, operationID domain.JournalOperationId, now time.Time) (domain.IdempotencyRecord, bool, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	compound := keyFor(household, key)
	if existing, ok := r.store.idempotency[compound]; ok {
		if existing.Fingerprint() != fingerprint {
			return domain.IdempotencyRecord{}, false, domain.ErrIdempotencyKeyPayloadMismatch
		}
		return existing, false, nil
	}
	record, err := domain.NewIdempotencyRecord(key, fingerprint, operationID, domain.NewRecordedAt(now.UTC()))
	if err != nil {
		return domain.IdempotencyRecord{}, false, err
	}
	r.store.idempotency[compound] = record
	return record, true, nil
}

func (r *memoryIdempotency) Lookup(_ context.Context, household domain.HouseholdId, key domain.IdempotencyKey) (domain.IdempotencyRecord, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	record, ok := r.store.idempotency[keyFor(household, key)]
	if !ok {
		return domain.IdempotencyRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return record, nil
}

type resourceHarness struct {
	store       *memoryLedgerStore
	users       *memoryUsers
	sessions    *memorySessions
	memberships *memoryMemberships
	clock       *httpTestClock
	ids         *httpSequenceIDs
	auth        *auth.Service
	app         *application.Service
	server      *Server
	token       string
	userID      domain.UserId
}

func newResourceServer(t *testing.T) *resourceHarness {
	t.Helper()
	harness := &resourceHarness{
		store:       newMemoryLedgerStore(),
		users:       newMemoryUsers(),
		sessions:    newMemorySessions(),
		memberships: newMemoryMemberships(),
		clock:       &httpTestClock{now: time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)},
		ids:         &httpSequenceIDs{},
	}
	hasher, err := auth.NewHasher(auth.Argon2Params{Time: 1, MemoryKiB: 8 * 1024, Threads: 1, KeyLength: 32, SaltLength: 16})
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}
	harness.auth, err = auth.NewService(auth.ServiceDeps{
		Users:       harness.users,
		Sessions:    harness.sessions,
		Memberships: harness.memberships,
		Hasher:      hasher,
		Clock:       harness.clock,
		IDs:         harness.ids,
		SessionTTL:  time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	harness.app, err = application.New(application.Dependencies{
		UnitOfWork: &transactionalMemoryStore{harness: harness},
		Clock:      harness.clock,
		IDs:        harness.ids,
	})
	if err != nil {
		t.Fatalf("application.New: %v", err)
	}
	user, err := harness.auth.Register(context.Background(), "owner@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	harness.userID = user.ID
	login, err := harness.auth.Login(context.Background(), "owner@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	harness.token = login.Token

	cfg := config.Default()
	cfg.HTTP.Port = 0
	cfg.HTTP.RequestTimeout = 2 * time.Second
	cfg.HTTP.MaxBodyBytes = 1 << 20
	cfg.Auth.RateLimitMax = 1000
	cfg.Auth.RateLimitWindow = time.Minute

	harness.server, err = NewServer(Dependencies{
		Config: cfg,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Auth:   harness.auth,
		App:    harness.app,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return harness
}

// transactionalMemoryStore binds the shared store plus the auth repositories so
// the application service and the auth middleware see one consistent state.
type transactionalMemoryStore struct{ harness *resourceHarness }

func (t *transactionalMemoryStore) WithinTransaction(ctx context.Context, fn func(context.Context, ports.Repositories) error) error {
	return fn(ctx, ports.Repositories{
		Households:   &memoryHouseholds{store: t.harness.store},
		Users:        t.harness.users,
		Sessions:     t.harness.sessions,
		Memberships:  t.harness.memberships,
		Accounts:     &memoryAccounts{store: t.harness.store},
		Envelopes:    &memoryEnvelopes{store: t.harness.store},
		Journal:      &memoryJournal{store: t.harness.store},
		Reservations: &memoryReservations{store: t.harness.store},
		Plans:        &memoryPlans{store: t.harness.store},
		Suggestions:  &memorySuggestions{store: t.harness.store},
		Idempotency:  &memoryIdempotency{store: t.harness.store},
	})
}

func (h *resourceHarness) do(t *testing.T, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+h.token)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func (h *resourceHarness) createHousehold(t *testing.T) householdDTO {
	t.Helper()
	recorder := h.do(t, http.MethodPost, "/api/v1/households", `{"name":"Home","currency":"USD","timezone":"UTC"}`, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create household: %d %s", recorder.Code, recorder.Body.String())
	}
	var response DataResponse[householdDTO]
	decodeResponse(t, recorder, &response)
	return response.Data
}

func (h *resourceHarness) createAccount(t *testing.T, householdID, kind string, eligible bool) accountDTO {
	t.Helper()
	body := fmt.Sprintf(`{"name":"%s","kind":"%s","budget_eligible":%t}`, kind, kind, eligible)
	recorder := h.do(t, http.MethodPost, "/api/v1/households/"+householdID+"/accounts", body, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create account: %d %s", recorder.Code, recorder.Body.String())
	}
	var response DataResponse[accountDTO]
	decodeResponse(t, recorder, &response)
	return response.Data
}

func (h *resourceHarness) createEnvelope(t *testing.T, householdID, name string) envelopeDTO {
	t.Helper()
	recorder := h.do(t, http.MethodPost, "/api/v1/households/"+householdID+"/envelopes", fmt.Sprintf(`{"name":%q}`, name), nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create envelope: %d %s", recorder.Code, recorder.Body.String())
	}
	var response DataResponse[envelopeDTO]
	decodeResponse(t, recorder, &response)
	return response.Data
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode response: %v (body %q)", err, recorder.Body.String())
	}
}

func idempotencyHeaders(key string) map[string]string {
	return map[string]string{"Idempotency-Key": key}
}
