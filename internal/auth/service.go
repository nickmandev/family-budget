package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// sessionTokenBytes is the raw bearer-token entropy: 32 bytes = 256 bits.
const sessionTokenBytes = 32

const dummyPassword = "family-budget-timing-equalizer"

// ServiceDeps is the explicit wiring for the auth service. Every seam is
// injected so tests run without PostgreSQL or a real clock.
type ServiceDeps struct {
	Users       ports.UserRepository
	Sessions    ports.SessionRepository
	Memberships ports.MembershipRepository
	Hasher      *Hasher
	Clock       ports.Clock
	IDs         ports.IDGenerator
	SessionTTL  time.Duration
}

// Service implements registration, login, session validation and revocation.
type Service struct {
	users       ports.UserRepository
	sessions    ports.SessionRepository
	memberships ports.MembershipRepository
	hasher      *Hasher
	clock       ports.Clock
	ids         ports.IDGenerator
	sessionTTL  time.Duration

	// dummyHash equalizes login timing when the email is unknown. It is never
	// stored as a user credential.
	dummyHash string
}

// NewService validates dependencies and prepares the timing-equalization hash.
func NewService(deps ServiceDeps) (*Service, error) {
	switch {
	case deps.Users == nil:
		return nil, errors.New("auth: UserRepository is required")
	case deps.Sessions == nil:
		return nil, errors.New("auth: SessionRepository is required")
	case deps.Memberships == nil:
		return nil, errors.New("auth: MembershipRepository is required")
	case deps.Hasher == nil:
		return nil, errors.New("auth: Hasher is required")
	case deps.Clock == nil:
		return nil, errors.New("auth: Clock is required")
	case deps.IDs == nil:
		return nil, errors.New("auth: IDGenerator is required")
	case deps.SessionTTL <= 0:
		return nil, errors.New("auth: SessionTTL must be positive")
	}

	dummyHash, err := deps.Hasher.Hash(dummyPassword)
	if err != nil {
		return nil, fmt.Errorf("auth: prepare timing hash: %w", err)
	}

	return &Service{
		users:       deps.Users,
		sessions:    deps.Sessions,
		memberships: deps.Memberships,
		hasher:      deps.Hasher,
		clock:       deps.Clock,
		ids:         deps.IDs,
		sessionTTL:  deps.SessionTTL,
		dummyHash:   dummyHash,
	}, nil
}

// User is the safe user view. It deliberately has no password field, so a
// response cannot accidentally serialize a hash.
type User struct {
	ID        domain.UserId
	Email     string
	CreatedAt time.Time
}

// Membership is the safe membership view used by /auth/me.
type Membership struct {
	HouseholdID domain.HouseholdId
	Role        ports.MembershipRole
	Status      ports.MembershipStatus
	JoinedAt    time.Time
	RemovedAt   *time.Time
}

// Principal is the authenticated actor attached to a request context.
type Principal struct {
	User        User
	Memberships []Membership
	SessionID   string
	ExpiresAt   time.Time
}

// LoginResult is returned once on successful login. Token is the only place
// the raw session token exists after creation.
type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	User      User
}

// Register creates a global user identity. Household bootstrap (creating the
// first household and owner membership) belongs to the household resource
// endpoints because it needs the household aggregate, not authentication.
func (s *Service) Register(ctx context.Context, email, password string) (User, error) {
	normalized, err := normalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}

	// Friendly duplicate check. A concurrent registration is still caught by
	// the database's case-insensitive unique index and re-checked below.
	if _, err := s.users.FindByEmail(ctx, normalized); err == nil {
		return User{}, ErrEmailAlreadyRegistered
	} else if domain.CodeOf(err) != domain.CodeEntityNotFoundOrNotInHousehold {
		return User{}, err
	}

	passwordHash, err := s.hasher.Hash(password)
	if err != nil {
		return User{}, err
	}

	rawID, err := s.ids.NewID()
	if err != nil {
		return User{}, err
	}
	userID, err := domain.NewUserId(rawID)
	if err != nil {
		return User{}, err
	}
	now := s.clock.Now().UTC()
	record := ports.UserRecord{
		ID:           userID,
		Email:        normalized,
		PasswordHash: passwordHash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.users.Create(ctx, record); err != nil {
		// A concurrent duplicate surfaces as a constraint error. Confirm by
		// lookup so the client still sees the stable registration code.
		if _, lookupErr := s.users.FindByEmail(ctx, normalized); lookupErr == nil {
			return User{}, ErrEmailAlreadyRegistered
		}
		return User{}, err
	}
	return userFromRecord(record), nil
}

// Login verifies credentials and issues one opaque session. Every failure
// returns ErrInvalidCredentials; unknown email and wrong password are
// indistinguishable, including approximate timing.
func (s *Service) Login(ctx context.Context, email, password string) (LoginResult, error) {
	normalized, emailErr := normalizeEmail(email)
	if emailErr != nil || len(password) == 0 || len(password) > maxPasswordBytes {
		// Spend the same Argon2id work on malformed input so it is not a fast
		// path for probing account existence.
		_, _ = s.hasher.Verify(password, s.dummyHash)
		return LoginResult{}, ErrInvalidCredentials
	}

	user, err := s.users.FindByEmail(ctx, normalized)
	if err != nil {
		if domain.CodeOf(err) == domain.CodeEntityNotFoundOrNotInHousehold {
			_, _ = s.hasher.Verify(password, s.dummyHash)
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, err
	}
	if user.PasswordHash == "" {
		_, _ = s.hasher.Verify(password, s.dummyHash)
		return LoginResult{}, ErrInvalidCredentials
	}

	matches, err := s.hasher.Verify(password, user.PasswordHash)
	if err != nil {
		return LoginResult{}, err
	}
	if !matches {
		return LoginResult{}, ErrInvalidCredentials
	}

	token, tokenHash, err := newSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	rawSessionID, err := s.ids.NewID()
	if err != nil {
		return LoginResult{}, err
	}
	now := s.clock.Now().UTC()
	expiresAt := now.Add(s.sessionTTL)
	session := ports.SessionRecord{
		ID:        rawSessionID,
		UserID:    user.ID,
		TokenHash: tokenHash,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}
	if err := s.sessions.Create(ctx, session); err != nil {
		return LoginResult{}, err
	}

	return LoginResult{
		Token:     token,
		ExpiresAt: expiresAt,
		User:      userFromRecord(user),
	}, nil
}

// Authenticate validates a raw bearer token by hashing it and looking up the
// digest. Revoked, expired and unknown sessions all return ErrUnauthenticated.
func (s *Service) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	token := strings.TrimSpace(rawToken)
	if token == "" {
		return Principal{}, ErrUnauthenticated
	}

	session, err := s.sessions.FindByTokenHash(ctx, hashToken(token))
	if err != nil {
		if domain.CodeOf(err) == domain.CodeEntityNotFoundOrNotInHousehold {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, err
	}

	now := s.clock.Now().UTC()
	if session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
		return Principal{}, ErrUnauthenticated
	}

	user, err := s.users.Get(ctx, session.UserID)
	if err != nil {
		if domain.CodeOf(err) == domain.CodeEntityNotFoundOrNotInHousehold {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, err
	}

	memberships, err := s.memberships.ListForUser(ctx, session.UserID)
	if err != nil {
		return Principal{}, err
	}

	return Principal{
		User:        userFromRecord(user),
		Memberships: membershipsFromRecords(memberships),
		SessionID:   session.ID,
		ExpiresAt:   session.ExpiresAt,
	}, nil
}

// Logout revokes exactly the session presented on the current request.
func (s *Service) Logout(ctx context.Context, principal Principal) error {
	if principal.SessionID == "" {
		return ErrUnauthenticated
	}
	return s.sessions.Revoke(ctx, principal.SessionID)
}

// RevokeAllForUser revokes every live session for a user. It is not exposed by
// a route in this task; it exists for credential-change and recovery flows.
func (s *Service) RevokeAllForUser(ctx context.Context, user domain.UserId) error {
	return s.sessions.RevokeAllForUser(ctx, user, s.clock.Now().UTC())
}

func normalizeEmail(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 254 {
		return "", ErrEmailInvalid
	}
	address, err := mail.ParseAddress(trimmed)
	if err != nil || address.Address != trimmed {
		return "", ErrEmailInvalid
	}
	local, host, found := strings.Cut(address.Address, "@")
	if !found || local == "" || host == "" || strings.ContainsAny(address.Address, " \t\r\n") {
		return "", ErrEmailInvalid
	}
	return strings.ToLower(address.Address), nil
}

func validatePassword(password string) error {
	if len(password) < minPasswordBytes || len(password) > maxPasswordBytes {
		return ErrPasswordInvalid
	}
	return nil
}

func newSessionToken() (raw string, hash string, err error) {
	buffer := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", "", fmt.Errorf("generate session token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buffer)
	return raw, hashToken(raw), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func userFromRecord(record ports.UserRecord) User {
	return User{ID: record.ID, Email: record.Email, CreatedAt: record.CreatedAt.UTC()}
}

func membershipsFromRecords(records []ports.UserMembershipRecord) []Membership {
	memberships := make([]Membership, 0, len(records))
	for _, record := range records {
		memberships = append(memberships, Membership{
			HouseholdID: record.HouseholdID,
			Role:        record.Role,
			Status:      record.Status,
			JoinedAt:    record.JoinedAt.UTC(),
			RemovedAt:   record.RemovedAt,
		})
	}
	return memberships
}
