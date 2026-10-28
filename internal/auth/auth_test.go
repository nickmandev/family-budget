package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// fastHasher uses the smallest allowed cost parameters so the auth unit tests
// stay quick. Production parameters are configured separately and benchmarked
// by BenchmarkHasherHash.
func fastHasher(t *testing.T) *Hasher {
	t.Helper()
	hasher, err := NewHasher(Argon2Params{
		Time:       1,
		MemoryKiB:  8 * 1024,
		Threads:    1,
		KeyLength:  32,
		SaltLength: 16,
	})
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}
	return hasher
}

func TestHasherHashAndVerify(t *testing.T) {
	hasher := fastHasher(t)
	const password = "correct-horse-battery-staple"

	encoded, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$") {
		t.Fatalf("hash is not a PHC Argon2id string: %s", encoded)
	}
	if strings.Contains(encoded, password) {
		t.Fatal("encoded hash contains the plaintext password")
	}

	ok, err := hasher.Verify(password, encoded)
	if err != nil || !ok {
		t.Fatalf("Verify(correct) = %v, %v", ok, err)
	}
	ok, err = hasher.Verify("wrong-password", encoded)
	if err != nil || ok {
		t.Fatalf("Verify(wrong) = %v, %v", ok, err)
	}

	again, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash again: %v", err)
	}
	if again == encoded {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
	ok, err = hasher.Verify(password, again)
	if err != nil || !ok {
		t.Fatalf("Verify(second hash) = %v, %v", ok, err)
	}
}

func TestHasherRejectsMalformedHashes(t *testing.T) {
	hasher := fastHasher(t)
	for _, encoded := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=8192,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=18$m=8192,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=wrong,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8192,t=1,p=1$!!!$aGFzaA",
	} {
		if _, err := hasher.Verify("password", encoded); err == nil {
			t.Errorf("Verify(%q) accepted a malformed hash", encoded)
		}
	}
}

func TestHasherRejectsOutOfRangeParameters(t *testing.T) {
	tests := []Argon2Params{
		{Time: 0, MemoryKiB: 8192, Threads: 1, KeyLength: 32, SaltLength: 16},
		{Time: 1, MemoryKiB: 1024, Threads: 1, KeyLength: 32, SaltLength: 16},
		{Time: 1, MemoryKiB: 8192, Threads: 0, KeyLength: 32, SaltLength: 16},
		{Time: 1, MemoryKiB: 8192, Threads: 1, KeyLength: 8, SaltLength: 16},
		{Time: 1, MemoryKiB: 8192, Threads: 1, KeyLength: 32, SaltLength: 1},
	}
	for _, params := range tests {
		if _, err := NewHasher(params); err == nil {
			t.Errorf("NewHasher(%+v) accepted invalid parameters", params)
		}
	}
}

func TestVerifyRejectsOverlongPasswordWithoutHashing(t *testing.T) {
	hasher := fastHasher(t)
	encoded, err := hasher.Hash("short-password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	ok, err := hasher.Verify(strings.Repeat("x", maxPasswordBytes+1), encoded)
	if err != nil {
		t.Fatalf("Verify(overlong): %v", err)
	}
	if ok {
		t.Fatal("overlong password matched")
	}
}

type fakeUserRepository struct {
	mu      sync.Mutex
	byID    map[domain.UserId]ports.UserRecord
	byEmail map[string]domain.UserId
}

func newFakeUserRepository() *fakeUserRepository {
	return &fakeUserRepository{
		byID:    make(map[domain.UserId]ports.UserRecord),
		byEmail: make(map[string]domain.UserId),
	}
}

func (r *fakeUserRepository) Create(_ context.Context, user ports.UserRecord) error {
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

func (r *fakeUserRepository) Get(_ context.Context, id domain.UserId) (ports.UserRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	user, ok := r.byID[id]
	if !ok {
		return ports.UserRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return user, nil
}

func (r *fakeUserRepository) FindByEmail(_ context.Context, email string) (ports.UserRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byEmail[strings.ToLower(strings.TrimSpace(email))]
	if !ok {
		return ports.UserRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return r.byID[id], nil
}

func (r *fakeUserRepository) get(id domain.UserId) (ports.UserRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	user, ok := r.byID[id]
	return user, ok
}

type fakeSessionRepository struct {
	mu      sync.Mutex
	byHash  map[string]ports.SessionRecord
	ordered []ports.SessionRecord
}

func newFakeSessionRepository() *fakeSessionRepository {
	return &fakeSessionRepository{byHash: make(map[string]ports.SessionRecord)}
}

func (r *fakeSessionRepository) Create(_ context.Context, session ports.SessionRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byHash[session.TokenHash] = session
	r.ordered = append(r.ordered, session)
	return nil
}

func (r *fakeSessionRepository) FindByTokenHash(_ context.Context, tokenHash string) (ports.SessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.byHash[tokenHash]
	if !ok {
		return ports.SessionRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
	}
	return session, nil
}

func (r *fakeSessionRepository) Revoke(_ context.Context, id string) error {
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

func (r *fakeSessionRepository) RevokeAllForUser(_ context.Context, user domain.UserId, now time.Time) error {
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

func (r *fakeSessionRepository) snapshot() []ports.SessionRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ports.SessionRecord, len(r.ordered))
	copy(out, r.ordered)
	return out
}

type fakeMembershipRepository struct {
	mu     sync.Mutex
	byUser map[domain.UserId][]ports.UserMembershipRecord
}

func newFakeMembershipRepository() *fakeMembershipRepository {
	return &fakeMembershipRepository{byUser: make(map[domain.UserId][]ports.UserMembershipRecord)}
}

func (r *fakeMembershipRepository) set(user domain.UserId, record ports.UserMembershipRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byUser[user] = append(r.byUser[user], record)
}

func (r *fakeMembershipRepository) Add(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}

func (r *fakeMembershipRepository) Get(context.Context, domain.HouseholdId, domain.UserId) (ports.MembershipRecord, error) {
	return ports.MembershipRecord{}, domain.ErrEntityNotFoundOrNotInHousehold
}

func (r *fakeMembershipRepository) List(context.Context, domain.HouseholdId) ([]ports.MembershipRecord, error) {
	return nil, nil
}

func (r *fakeMembershipRepository) ListForUser(_ context.Context, user domain.UserId) ([]ports.UserMembershipRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	records := r.byUser[user]
	out := make([]ports.UserMembershipRecord, len(records))
	copy(out, records)
	return out, nil
}

func (r *fakeMembershipRepository) Save(context.Context, domain.HouseholdId, ports.MembershipRecord) error {
	return nil
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

type sequenceIDs struct {
	mu   sync.Mutex
	next uint64
}

func (g *sequenceIDs) NewID() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return fmt.Sprintf("00000000-0000-7000-8000-%012x", g.next), nil
}

type serviceHarness struct {
	users       *fakeUserRepository
	sessions    *fakeSessionRepository
	memberships *fakeMembershipRepository
	clock       *testClock
	ids         *sequenceIDs
}

func newServiceHarness(t *testing.T) (*Service, *serviceHarness) {
	t.Helper()
	harness := &serviceHarness{
		users:       newFakeUserRepository(),
		sessions:    newFakeSessionRepository(),
		memberships: newFakeMembershipRepository(),
		clock:       &testClock{now: time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)},
		ids:         &sequenceIDs{},
	}
	service, err := NewService(ServiceDeps{
		Users:       harness.users,
		Sessions:    harness.sessions,
		Memberships: harness.memberships,
		Hasher:      fastHasher(t),
		Clock:       harness.clock,
		IDs:         harness.ids,
		SessionTTL:  time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service, harness
}

func TestRegisterLoginMeRoundTrip(t *testing.T) {
	service, harness := newServiceHarness(t)
	ctx := context.Background()

	user, err := service.Register(ctx, " Owner@Example.COM ", "correct-horse")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if user.Email != "owner@example.com" {
		t.Fatalf("email = %q, want normalized owner@example.com", user.Email)
	}
	if user.ID.IsZero() {
		t.Fatal("registered user has a zero id")
	}

	stored, ok := harness.users.get(user.ID)
	if !ok {
		t.Fatal("user was not stored")
	}
	if !strings.HasPrefix(stored.PasswordHash, "$argon2id$v=19$") {
		t.Fatalf("stored hash is not Argon2id: %q", stored.PasswordHash)
	}
	if strings.Contains(stored.PasswordHash, "correct-horse") {
		t.Fatal("stored hash contains the plaintext password")
	}

	harness.memberships.set(user.ID, ports.UserMembershipRecord{
		HouseholdID: domain.HouseholdId("household-1"),
		Role:        ports.MembershipRoleOwner,
		Status:      ports.MembershipStatusActive,
		JoinedAt:    harness.clock.Now(),
	})

	login, err := service.Login(ctx, "OWNER@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login.Token == "" {
		t.Fatal("login returned an empty session token")
	}
	if !login.ExpiresAt.Equal(harness.clock.Now().Add(time.Hour)) {
		t.Fatalf("expires at = %v", login.ExpiresAt)
	}

	sessions := harness.sessions.snapshot()
	if len(sessions) != 1 {
		t.Fatalf("stored sessions = %d, want 1", len(sessions))
	}
	wantHash := hashToken(login.Token)
	if sessions[0].TokenHash != wantHash {
		t.Fatalf("stored token hash = %q, want SHA-256 %q", sessions[0].TokenHash, wantHash)
	}
	if sessions[0].TokenHash == login.Token {
		t.Fatal("raw session token was stored")
	}

	principal, err := service.Authenticate(ctx, login.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if principal.User.ID != user.ID || principal.User.Email != user.Email {
		t.Fatalf("principal user = %+v", principal.User)
	}
	if len(principal.Memberships) != 1 || principal.Memberships[0].HouseholdID != "household-1" {
		t.Fatalf("principal memberships = %+v", principal.Memberships)
	}
	if principal.SessionID == "" {
		t.Fatal("principal has no session id")
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	service, _ := newServiceHarness(t)
	ctx := context.Background()

	if _, err := service.Register(ctx, "owner@example.com", "correct-horse"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	wrongPassword, wrongErr := service.Login(ctx, "owner@example.com", "wrong-password")
	unknownEmail, unknownErr := service.Login(ctx, "nobody@example.com", "wrong-password")
	malformed, malformedErr := service.Login(ctx, "not-an-email", "wrong-password")

	for name, err := range map[string]error{
		"wrong password":  wrongErr,
		"unknown email":   unknownErr,
		"malformed email": malformedErr,
	} {
		if err != ErrInvalidCredentials {
			t.Errorf("%s error = %v, want ErrInvalidCredentials", name, err)
		}
	}
	if wrongPassword.Token != unknownEmail.Token || malformed.Token != "" {
		t.Fatal("a failed login returned a token")
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	service, harness := newServiceHarness(t)
	ctx := context.Background()

	if _, err := service.Register(ctx, "owner@example.com", "correct-horse"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	login, err := service.Login(ctx, "owner@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	harness.clock.advance(time.Hour + time.Second)
	if _, err := service.Authenticate(ctx, login.Token); err != ErrUnauthenticated {
		t.Fatalf("Authenticate(expired) = %v, want ErrUnauthenticated", err)
	}
}

func TestLogoutRevokesOnlyPresentedSession(t *testing.T) {
	service, _ := newServiceHarness(t)
	ctx := context.Background()

	if _, err := service.Register(ctx, "owner@example.com", "correct-horse"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	first, err := service.Login(ctx, "owner@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("Login first: %v", err)
	}
	second, err := service.Login(ctx, "owner@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("Login second: %v", err)
	}

	firstPrincipal, err := service.Authenticate(ctx, first.Token)
	if err != nil {
		t.Fatalf("Authenticate first: %v", err)
	}
	if err := service.Logout(ctx, firstPrincipal); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if _, err := service.Authenticate(ctx, first.Token); err != ErrUnauthenticated {
		t.Fatalf("Authenticate(logged out) = %v, want ErrUnauthenticated", err)
	}
	if _, err := service.Authenticate(ctx, second.Token); err != nil {
		t.Fatalf("logout must not revoke the other session: %v", err)
	}
}

func TestRevokeAllForUser(t *testing.T) {
	service, harness := newServiceHarness(t)
	ctx := context.Background()

	user, err := service.Register(ctx, "owner@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	first, err := service.Login(ctx, "owner@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("Login first: %v", err)
	}
	second, err := service.Login(ctx, "owner@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("Login second: %v", err)
	}

	if err := service.RevokeAllForUser(ctx, user.ID); err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}
	for name, token := range map[string]string{"first": first.Token, "second": second.Token} {
		if _, err := service.Authenticate(ctx, token); err != ErrUnauthenticated {
			t.Errorf("Authenticate(%s) = %v, want ErrUnauthenticated", name, err)
		}
	}
	if harness.clock.Now().IsZero() {
		t.Fatal("test clock is zero")
	}
}

func TestRegisterRejectsDuplicateEmailCaseInsensitively(t *testing.T) {
	service, _ := newServiceHarness(t)
	ctx := context.Background()

	if _, err := service.Register(ctx, "owner@example.com", "correct-horse"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := service.Register(ctx, "OWNER@example.com", "another-password"); err != ErrEmailAlreadyRegistered {
		t.Fatalf("duplicate Register = %v, want ErrEmailAlreadyRegistered", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	service, _ := newServiceHarness(t)
	ctx := context.Background()

	if _, err := service.Register(ctx, "not-an-email", "correct-horse"); err != ErrEmailInvalid {
		t.Fatalf("invalid email = %v, want ErrEmailInvalid", err)
	}
	if _, err := service.Register(ctx, "owner@example.com", "short"); err != ErrPasswordInvalid {
		t.Fatalf("short password = %v, want ErrPasswordInvalid", err)
	}
}

func TestNewServiceRequiresDependencies(t *testing.T) {
	hasher := fastHasher(t)
	clock := &testClock{now: time.Now()}
	ids := &sequenceIDs{}
	users := newFakeUserRepository()
	sessions := newFakeSessionRepository()
	memberships := newFakeMembershipRepository()

	base := ServiceDeps{
		Users:       users,
		Sessions:    sessions,
		Memberships: memberships,
		Hasher:      hasher,
		Clock:       clock,
		IDs:         ids,
		SessionTTL:  time.Hour,
	}

	tests := []struct {
		name   string
		mutate func(*ServiceDeps)
	}{
		{"missing users", func(d *ServiceDeps) { d.Users = nil }},
		{"missing sessions", func(d *ServiceDeps) { d.Sessions = nil }},
		{"missing memberships", func(d *ServiceDeps) { d.Memberships = nil }},
		{"missing hasher", func(d *ServiceDeps) { d.Hasher = nil }},
		{"missing clock", func(d *ServiceDeps) { d.Clock = nil }},
		{"missing ids", func(d *ServiceDeps) { d.IDs = nil }},
		{"zero ttl", func(d *ServiceDeps) { d.SessionTTL = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := base
			test.mutate(&deps)
			if _, err := NewService(deps); err == nil {
				t.Fatal("NewService accepted missing dependency")
			}
		})
	}
}

func TestHashTokenIsSHA256Hex(t *testing.T) {
	raw := "some-raw-token"
	sum := sha256.Sum256([]byte(raw))
	want := hex.EncodeToString(sum[:])
	if got := hashToken(raw); got != want {
		t.Fatalf("hashToken = %q, want %q", got, want)
	}
	if hashToken(raw) == raw {
		t.Fatal("hashToken returned the raw token")
	}
}

func TestSessionTokenHasEntropy(t *testing.T) {
	first, firstHash, err := newSessionToken()
	if err != nil {
		t.Fatalf("newSessionToken: %v", err)
	}
	second, secondHash, err := newSessionToken()
	if err != nil {
		t.Fatalf("newSessionToken: %v", err)
	}
	if first == second || firstHash == secondHash {
		t.Fatal("two session tokens collided")
	}
	if len(first) < 43 {
		t.Fatalf("token length = %d, want at least 43 base64url characters", len(first))
	}
	if firstHash != hashToken(first) {
		t.Fatal("returned hash does not match the token")
	}
}

func TestErrorsAreTyped(t *testing.T) {
	var authErr *Error
	if !errors.As(ErrInvalidCredentials, &authErr) {
		t.Fatal("ErrInvalidCredentials is not an *Error")
	}
	if authErr.ErrorCode() != CodeInvalidCredentials {
		t.Fatalf("code = %q", authErr.ErrorCode())
	}
	if ErrEmailInvalid.ErrorField() != "email" || ErrPasswordInvalid.ErrorField() != "password" {
		t.Fatal("validation errors must name their field")
	}
}

// BenchmarkHasherHash measures the configured production default. Run it on the
// deployment hardware before changing AUTH_ARGON2_* values; the result should
// stay within an interactive-login budget (roughly 100-500 ms per hash).
func BenchmarkHasherHash(b *testing.B) {
	hasher, err := NewHasher(Argon2Params{
		Time:       3,
		MemoryKiB:  64 * 1024,
		Threads:    4,
		KeyLength:  32,
		SaltLength: 16,
	})
	if err != nil {
		b.Fatalf("NewHasher: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := hasher.Hash("benchmark-password"); err != nil {
			b.Fatalf("Hash: %v", err)
		}
	}
}
