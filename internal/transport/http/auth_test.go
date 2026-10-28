package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"family-budget/internal/auth"
	"family-budget/internal/config"
	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

type httpTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *httpTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *httpTestClock) advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

type httpSequenceIDs struct {
	mu   sync.Mutex
	next uint64
}

func (g *httpSequenceIDs) NewID() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return fmt.Sprintf("00000000-0000-7000-8000-%012x", g.next), nil
}

type memoryUsers struct {
	mu      sync.Mutex
	byID    map[domain.UserId]ports.UserRecord
	byEmail map[string]domain.UserId
}

func newMemoryUsers() *memoryUsers {
	return &memoryUsers{
		byID:    make(map[domain.UserId]ports.UserRecord),
		byEmail: make(map[string]domain.UserId),
	}
}

func (r *memoryUsers) Create(_ context.Context, user ports.UserRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := strings.ToLower(user.Email)
	if _, exists := r.byEmail[key]; exists {
		return domain.NewError(domain.CodeInvalidValue, "duplicate email")
	}
	r.byID[user.ID] = user
	r.byEmail[key] = user.ID
	return nil
}

func (r *memoryUsers) Get(_ context.Context, id domain.UserId) (ports.UserRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	user, ok := r.byID[id]
	if !ok {
		return ports.UserRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return user, nil
}

func (r *memoryUsers) FindByEmail(_ context.Context, email string) (ports.UserRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byEmail[strings.ToLower(strings.TrimSpace(email))]
	if !ok {
		return ports.UserRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return r.byID[id], nil
}

func (r *memoryUsers) record(id domain.UserId) (ports.UserRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	user, ok := r.byID[id]
	return user, ok
}

type memorySessions struct {
	mu      sync.Mutex
	byHash  map[string]ports.SessionRecord
	ordered []ports.SessionRecord
}

func newMemorySessions() *memorySessions {
	return &memorySessions{byHash: make(map[string]ports.SessionRecord)}
}

func (r *memorySessions) Create(_ context.Context, session ports.SessionRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byHash[session.TokenHash] = session
	r.ordered = append(r.ordered, session)
	return nil
}

func (r *memorySessions) FindByTokenHash(_ context.Context, tokenHash string) (ports.SessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.byHash[tokenHash]
	if !ok {
		return ports.SessionRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return session, nil
}

func (r *memorySessions) Revoke(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for hash, session := range r.byHash {
		if session.ID == id && session.RevokedAt == nil {
			now := time.Now().UTC()
			session.RevokedAt = &now
			r.byHash[hash] = session
		}
	}
	return nil
}

func (r *memorySessions) RevokeAllForUser(_ context.Context, user domain.UserId, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for hash, session := range r.byHash {
		if session.UserID == user && session.RevokedAt == nil {
			revokedAt := now.UTC()
			session.RevokedAt = &revokedAt
			r.byHash[hash] = session
		}
	}
	return nil
}

func (r *memorySessions) snapshot() []ports.SessionRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ports.SessionRecord, len(r.ordered))
	copy(out, r.ordered)
	return out
}

// storesRawToken would be true if any persisted value equalled the raw token.
func (r *memorySessions) storesRawToken(raw string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for hash, session := range r.byHash {
		if hash == raw || session.TokenHash == raw {
			return true
		}
	}
	return false
}

type memoryMemberships struct {
	mu     sync.Mutex
	byUser map[domain.UserId][]ports.UserMembershipRecord
}

func newMemoryMemberships() *memoryMemberships {
	return &memoryMemberships{byUser: make(map[domain.UserId][]ports.UserMembershipRecord)}
}

func (r *memoryMemberships) set(user domain.UserId, record ports.UserMembershipRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byUser[user] = append(r.byUser[user], record)
}

func (r *memoryMemberships) Add(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}

func (r *memoryMemberships) Get(context.Context, domain.HouseholdId, domain.UserId) (ports.MembershipRecord, error) {
	return ports.MembershipRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
}

func (r *memoryMemberships) List(context.Context, domain.HouseholdId) ([]ports.MembershipRecord, error) {
	return nil, nil
}

func (r *memoryMemberships) ListForUser(_ context.Context, user domain.UserId) ([]ports.UserMembershipRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	records := r.byUser[user]
	out := make([]ports.UserMembershipRecord, len(records))
	copy(out, records)
	return out, nil
}

func (r *memoryMemberships) Save(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}

type authHarness struct {
	users       *memoryUsers
	sessions    *memorySessions
	memberships *memoryMemberships
	clock       *httpTestClock
}

func newAuthServer(t *testing.T, rateLimitMax int, rateLimitWindow time.Duration) (*Server, *authHarness) {
	t.Helper()

	harness := &authHarness{
		users:       newMemoryUsers(),
		sessions:    newMemorySessions(),
		memberships: newMemoryMemberships(),
		clock:       &httpTestClock{now: time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)},
	}

	hasher, err := auth.NewHasher(auth.Argon2Params{
		Time:       1,
		MemoryKiB:  8 * 1024,
		Threads:    1,
		KeyLength:  32,
		SaltLength: 16,
	})
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}

	service, err := auth.NewService(auth.ServiceDeps{
		Users:       harness.users,
		Sessions:    harness.sessions,
		Memberships: harness.memberships,
		Hasher:      hasher,
		Clock:       harness.clock,
		IDs:         &httpSequenceIDs{},
		SessionTTL:  time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	cfg := config.Default()
	cfg.HTTP.Port = 0
	cfg.HTTP.RequestTimeout = 2 * time.Second
	cfg.HTTP.MaxBodyBytes = 1 << 20
	cfg.Auth.RateLimitMax = rateLimitMax
	cfg.Auth.RateLimitWindow = rateLimitWindow

	server, err := NewServer(Dependencies{
		Config: cfg,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Auth:   service,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server, harness
}

func performJSON(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeUser(t *testing.T, recorder *httptest.ResponseRecorder) userDTO {
	t.Helper()
	var response struct {
		User userDTO `json:"user"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode user response: %v (body %q)", err, recorder.Body.String())
	}
	return response.User
}

func decodeLogin(t *testing.T, recorder *httptest.ResponseRecorder) loginResponse {
	t.Helper()
	var response loginResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode login response: %v (body %q)", err, recorder.Body.String())
	}
	return response
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestHTTPAuthRoundTrip(t *testing.T) {
	server, harness := newAuthServer(t, 1000, time.Minute)
	handler := server.Handler()

	register := performJSON(handler, http.MethodPost, "/api/v1/auth/register",
		`{"email":"owner@example.com","password":"correct-horse"}`, "")
	if register.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", register.Code, register.Body.String())
	}
	user := decodeUser(t, register)
	if user.ID == "" || user.Email != "owner@example.com" {
		t.Fatalf("register user = %+v", user)
	}

	stored, ok := harness.users.record(domain.UserId(user.ID))
	if !ok {
		t.Fatal("registered user was not persisted")
	}
	if strings.Contains(stored.PasswordHash, "correct-horse") {
		t.Fatal("persisted hash contains the plaintext password")
	}

	harness.memberships.set(domain.UserId(user.ID), ports.UserMembershipRecord{
		HouseholdID: domain.HouseholdId("household-1"),
		Role:        ports.MembershipRoleOwner,
		Status:      ports.MembershipStatusActive,
		JoinedAt:    harness.clock.Now(),
	})

	login := performJSON(handler, http.MethodPost, "/api/v1/auth/login",
		`{"email":"owner@example.com","password":"correct-horse"}`, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	if got := login.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("login Cache-Control = %q, want no-store", got)
	}
	loginBody := decodeLogin(t, login)
	if loginBody.Token == "" || loginBody.User.ID != user.ID {
		t.Fatalf("login response = %+v", loginBody)
	}
	if _, err := time.Parse(time.RFC3339, loginBody.ExpiresAt); err != nil {
		t.Fatalf("expires_at %q is not RFC 3339: %v", loginBody.ExpiresAt, err)
	}

	sessions := harness.sessions.snapshot()
	if len(sessions) != 1 {
		t.Fatalf("stored sessions = %d, want 1", len(sessions))
	}
	if want := sha256Hex(loginBody.Token); sessions[0].TokenHash != want {
		t.Fatalf("stored token hash = %q, want %q", sessions[0].TokenHash, want)
	}
	if harness.sessions.storesRawToken(loginBody.Token) {
		t.Fatal("the raw session token was stored")
	}

	me := performJSON(handler, http.MethodGet, "/api/v1/auth/me", "", loginBody.Token)
	if me.Code != http.StatusOK {
		t.Fatalf("me status = %d, body = %s", me.Code, me.Body.String())
	}
	var meBody meResponse
	if err := json.Unmarshal(me.Body.Bytes(), &meBody); err != nil {
		t.Fatalf("decode me response: %v", err)
	}
	if meBody.User.ID != user.ID {
		t.Fatalf("me user = %+v", meBody.User)
	}
	if len(meBody.Memberships) != 1 || meBody.Memberships[0].HouseholdID != "household-1" {
		t.Fatalf("me memberships = %+v", meBody.Memberships)
	}

	// No response body may contain the password hash or the raw password.
	for name, body := range map[string]string{
		"register": register.Body.String(),
		"login":    login.Body.String(),
		"me":       me.Body.String(),
	} {
		for _, secret := range []string{stored.PasswordHash, "correct-horse"} {
			if strings.Contains(body, secret) {
				t.Fatalf("%s response leaked %q: %s", name, secret, body)
			}
		}
	}

	logout := performJSON(handler, http.MethodPost, "/api/v1/auth/logout", "", loginBody.Token)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, body = %s", logout.Code, logout.Body.String())
	}
	afterLogout := performJSON(handler, http.MethodGet, "/api/v1/auth/me", "", loginBody.Token)
	if afterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout status = %d, want 401", afterLogout.Code)
	}
}

func TestHTTPLoginFailuresAreGeneric(t *testing.T) {
	server, _ := newAuthServer(t, 1000, time.Minute)
	handler := server.Handler()

	if register := performJSON(handler, http.MethodPost, "/api/v1/auth/register",
		`{"email":"owner@example.com","password":"correct-horse"}`, ""); register.Code != http.StatusCreated {
		t.Fatalf("register status = %d", register.Code)
	}

	wrong := performJSON(handler, http.MethodPost, "/api/v1/auth/login",
		`{"email":"owner@example.com","password":"wrong-password"}`, "")
	unknown := performJSON(handler, http.MethodPost, "/api/v1/auth/login",
		`{"email":"nobody@example.com","password":"wrong-password"}`, "")

	if wrong.Code != http.StatusUnauthorized || unknown.Code != http.StatusUnauthorized {
		t.Fatalf("statuses = %d and %d, want 401/401", wrong.Code, unknown.Code)
	}
	wrongEnvelope := decodeErrorEnvelope(t, wrong)
	unknownEnvelope := decodeErrorEnvelope(t, unknown)

	if wrongEnvelope.Error.Code != auth.CodeInvalidCredentials || unknownEnvelope.Error.Code != auth.CodeInvalidCredentials {
		t.Fatalf("codes = %q and %q, want %s", wrongEnvelope.Error.Code, unknownEnvelope.Error.Code, auth.CodeInvalidCredentials)
	}
	if wrongEnvelope.Error.Message != "Invalid credentials." || unknownEnvelope.Error.Message != "Invalid credentials." {
		t.Fatalf("messages = %q and %q", wrongEnvelope.Error.Message, unknownEnvelope.Error.Message)
	}
	if wrongEnvelope.Error.Field != nil || unknownEnvelope.Error.Field != nil {
		t.Fatal("credential failures must not identify a field")
	}
	if wrongEnvelope.Error.Details != nil || unknownEnvelope.Error.Details != nil {
		t.Fatal("credential failures must not carry details")
	}
	if wrongEnvelope.Error.RequestID == "" || unknownEnvelope.Error.RequestID == "" {
		t.Fatal("credential failures must carry a request id")
	}
	if wrong.Header().Get("WWW-Authenticate") != `Bearer realm="family-budget"` {
		t.Fatalf("WWW-Authenticate = %q", wrong.Header().Get("WWW-Authenticate"))
	}
}

func TestHTTPExpiredAndRevokedSessions(t *testing.T) {
	server, harness := newAuthServer(t, 1000, time.Minute)
	handler := server.Handler()

	if register := performJSON(handler, http.MethodPost, "/api/v1/auth/register",
		`{"email":"owner@example.com","password":"correct-horse"}`, ""); register.Code != http.StatusCreated {
		t.Fatalf("register status = %d", register.Code)
	}
	login := decodeLogin(t, performJSON(handler, http.MethodPost, "/api/v1/auth/login",
		`{"email":"owner@example.com","password":"correct-horse"}`, ""))

	harness.clock.advance(2 * time.Hour)
	expired := performJSON(handler, http.MethodGet, "/api/v1/auth/me", "", login.Token)
	if expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired session status = %d, want 401", expired.Code)
	}
	if envelope := decodeErrorEnvelope(t, expired); envelope.Error.Code != auth.CodeUnauthenticated {
		t.Fatalf("expired session code = %q", envelope.Error.Code)
	}

	// A fresh login is valid and can be revoked explicitly.
	fresh := decodeLogin(t, performJSON(handler, http.MethodPost, "/api/v1/auth/login",
		`{"email":"owner@example.com","password":"correct-horse"}`, ""))
	if me := performJSON(handler, http.MethodGet, "/api/v1/auth/me", "", fresh.Token); me.Code != http.StatusOK {
		t.Fatalf("fresh session status = %d", me.Code)
	}
	performJSON(handler, http.MethodPost, "/api/v1/auth/logout", "", fresh.Token)
	if me := performJSON(handler, http.MethodGet, "/api/v1/auth/me", "", fresh.Token); me.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d, want 401", me.Code)
	}
}

func TestHTTPLogoutRevokesOnlyPresentedSession(t *testing.T) {
	server, _ := newAuthServer(t, 1000, time.Minute)
	handler := server.Handler()

	if register := performJSON(handler, http.MethodPost, "/api/v1/auth/register",
		`{"email":"owner@example.com","password":"correct-horse"}`, ""); register.Code != http.StatusCreated {
		t.Fatalf("register status = %d", register.Code)
	}
	first := decodeLogin(t, performJSON(handler, http.MethodPost, "/api/v1/auth/login",
		`{"email":"owner@example.com","password":"correct-horse"}`, ""))
	second := decodeLogin(t, performJSON(handler, http.MethodPost, "/api/v1/auth/login",
		`{"email":"owner@example.com","password":"correct-horse"}`, ""))

	if logout := performJSON(handler, http.MethodPost, "/api/v1/auth/logout", "", first.Token); logout.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", logout.Code)
	}
	if me := performJSON(handler, http.MethodGet, "/api/v1/auth/me", "", first.Token); me.Code != http.StatusUnauthorized {
		t.Fatalf("first session after logout = %d, want 401", me.Code)
	}
	if me := performJSON(handler, http.MethodGet, "/api/v1/auth/me", "", second.Token); me.Code != http.StatusOK {
		t.Fatalf("second session after first logout = %d, want 200", me.Code)
	}
}

func TestHTTPAuthRateLimitTrips(t *testing.T) {
	server, _ := newAuthServer(t, 2, time.Hour)
	handler := server.Handler()

	body := `{"email":"nobody@example.com","password":"wrong-password"}`
	for attempt := 1; attempt <= 2; attempt++ {
		recorder := performJSON(handler, http.MethodPost, "/api/v1/auth/login", body, "")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", attempt, recorder.Code)
		}
	}

	limited := performJSON(handler, http.MethodPost, "/api/v1/auth/login", body, "")
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited status = %d, want 429", limited.Code)
	}
	if envelope := decodeErrorEnvelope(t, limited); envelope.Error.Code != CodeRateLimited {
		t.Fatalf("rate-limited code = %q, want %s", envelope.Error.Code, CodeRateLimited)
	}
	if got := limited.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}

	// The register bucket is independent of the login bucket.
	register := performJSON(handler, http.MethodPost, "/api/v1/auth/register",
		`{"email":"new@example.com","password":"correct-horse"}`, "")
	if register.Code != http.StatusCreated {
		t.Fatalf("register after login limit status = %d, want 201", register.Code)
	}
}

func TestHTTPRequiresBearerToken(t *testing.T) {
	server, _ := newAuthServer(t, 1000, time.Minute)
	handler := server.Handler()

	for name, token := range map[string]string{
		"missing": "",
		"bogus":   "not-a-real-session-token",
	} {
		t.Run(name, func(t *testing.T) {
			recorder := performJSON(handler, http.MethodGet, "/api/v1/auth/me", "", token)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", recorder.Code)
			}
			envelope := decodeErrorEnvelope(t, recorder)
			if envelope.Error.Code != auth.CodeUnauthenticated {
				t.Fatalf("code = %q, want %s", envelope.Error.Code, auth.CodeUnauthenticated)
			}
			if got := recorder.Header().Get("WWW-Authenticate"); got != `Bearer realm="family-budget"` {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	request.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("Basic auth status = %d, want 401", recorder.Code)
	}
}

func TestHTTPRegisterValidationNamesField(t *testing.T) {
	server, _ := newAuthServer(t, 1000, time.Minute)
	handler := server.Handler()

	invalidEmail := performJSON(handler, http.MethodPost, "/api/v1/auth/register",
		`{"email":"not-an-email","password":"correct-horse"}`, "")
	if invalidEmail.Code != http.StatusBadRequest {
		t.Fatalf("invalid email status = %d, want 400", invalidEmail.Code)
	}
	emailEnvelope := decodeErrorEnvelope(t, invalidEmail)
	if emailEnvelope.Error.Code != auth.CodeInvalidValue || emailEnvelope.Error.Field == nil || *emailEnvelope.Error.Field != "email" {
		t.Fatalf("invalid email envelope = %+v", emailEnvelope.Error)
	}

	shortPassword := performJSON(handler, http.MethodPost, "/api/v1/auth/register",
		`{"email":"owner@example.com","password":"short"}`, "")
	if shortPassword.Code != http.StatusBadRequest {
		t.Fatalf("short password status = %d, want 400", shortPassword.Code)
	}
	passwordEnvelope := decodeErrorEnvelope(t, shortPassword)
	if passwordEnvelope.Error.Field == nil || *passwordEnvelope.Error.Field != "password" {
		t.Fatalf("short password envelope = %+v", passwordEnvelope.Error)
	}
}

func TestHTTPDuplicateRegistration(t *testing.T) {
	server, _ := newAuthServer(t, 1000, time.Minute)
	handler := server.Handler()

	body := `{"email":"owner@example.com","password":"correct-horse"}`
	if register := performJSON(handler, http.MethodPost, "/api/v1/auth/register", body, ""); register.Code != http.StatusCreated {
		t.Fatalf("first register status = %d", register.Code)
	}
	duplicate := performJSON(handler, http.MethodPost, "/api/v1/auth/register", body, "")
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate register status = %d, want 409", duplicate.Code)
	}
	if envelope := decodeErrorEnvelope(t, duplicate); envelope.Error.Code != auth.CodeEmailAlreadyRegistered {
		t.Fatalf("duplicate register code = %q", envelope.Error.Code)
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	limiter := NewRateLimiter(2, time.Second)
	now := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }

	if !limiter.Allow("key") || !limiter.Allow("key") {
		t.Fatal("initial burst was rejected")
	}
	if limiter.Allow("key") {
		t.Fatal("third immediate request was allowed")
	}
	now = now.Add(time.Second)
	if !limiter.Allow("key") {
		t.Fatal("bucket did not refill after a window")
	}
}
