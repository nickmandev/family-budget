package httpapi

import (
	"context"
	"net/http"
	"strings"

	"family-budget/internal/auth"
)

// registerRequest is the transport shape for registration. Passwords are only
// ever held in this request value and handed to the auth service; they are
// never logged or stored by the transport.
type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// userDTO is the safe user representation. It has no password/hash field by
// construction, so no response can serialize one.
type userDTO struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}

type membershipDTO struct {
	HouseholdID string  `json:"household_id"`
	Role        string  `json:"role"`
	Status      string  `json:"status"`
	JoinedAt    string  `json:"joined_at"`
	RemovedAt   *string `json:"removed_at"`
}

type registerResponse struct {
	User userDTO `json:"user"`
}

type loginResponse struct {
	Token     string  `json:"token"`
	ExpiresAt string  `json:"expires_at"`
	User      userDTO `json:"user"`
}

type meResponse struct {
	User        userDTO         `json:"user"`
	Memberships []membershipDTO `json:"memberships"`
}

// registerAuthRoutes attaches the authentication endpoints. Registration
// creates only the global user identity; household bootstrap is a household
// resource operation (7B) because it needs the household aggregate.
//
// Login and registration are rate limited. Logout and /me require a valid
// bearer session and are not rate limited here: they cannot be used to guess
// credentials.
func (s *Server) registerAuthRoutes(mux *http.ServeMux) {
	if s.deps.Auth == nil {
		return
	}

	limiter := s.deps.AuthRateLimiter
	if limiter == nil {
		limiter = NewRateLimiter(s.deps.Config.Auth.RateLimitMax, s.deps.Config.Auth.RateLimitWindow)
	}
	rateLimited := func(next http.Handler) http.Handler {
		// Behind the host nginx (and Cloudflare) the transport peer is
		// loopback; key the limiter by the forwarded client IP from the
		// configured trusted proxies so each user has their own bucket.
		return RateLimit(limiter, authRateLimitKey(newClientIPResolver(s.deps.Config.HTTP.TrustedProxies)))(next)
	}

	s.register(mux, "POST /api/v1/auth/register", rateLimited(http.HandlerFunc(s.handleRegister)))
	s.register(mux, "POST /api/v1/auth/login", rateLimited(http.HandlerFunc(s.handleLogin)))
	s.register(mux, "POST /api/v1/auth/logout", s.requireAuth(http.HandlerFunc(s.handleLogout)))
	s.register(mux, "GET /api/v1/auth/me", s.requireAuth(http.HandlerFunc(s.handleMe)))
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var request registerRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}

	user, err := s.deps.Auth.Register(r.Context(), request.Email, request.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, registerResponse{User: toUserDTO(user)})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}

	result, err := s.deps.Auth.Login(r.Context(), request.Email, request.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}

	// A session token is a credential; it must not be cached by shared caches.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, loginResponse{
		Token:     result.Token,
		ExpiresAt: formatTimestamp(result.ExpiresAt),
		User:      toUserDTO(result.User),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	if err := s.deps.Auth.Logout(r.Context(), principal); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	writeJSON(w, http.StatusOK, meResponse{
		User:        toUserDTO(principal.User),
		Memberships: toMembershipDTOs(principal.Memberships),
	})
}

// requireAuth extracts a bearer token, validates it through the auth service
// and attaches the principal to the request context. All failures are the same
// 401 so an invalid, expired or revoked session is indistinguishable.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok || s.deps.Auth == nil {
			writeError(w, r, auth.ErrUnauthenticated)
			return
		}
		principal, err := s.deps.Auth.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// principalContextKey is a private type so no other package can collide with
// the context value.
type principalContextKey struct{}

// PrincipalFromContext returns the authenticated principal attached by
// requireAuth.
func PrincipalFromContext(ctx context.Context) (auth.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(auth.Principal)
	return principal, ok
}

// bearerToken parses exactly one Bearer credential. Cookies are deliberately
// unsupported: the mobile client uses the Authorization header, which also
// avoids CSRF concerns.
func bearerToken(r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

func toUserDTO(user auth.User) userDTO {
	return userDTO{
		ID:        user.ID.String(),
		Email:     user.Email,
		CreatedAt: formatTimestamp(user.CreatedAt),
	}
}

func toMembershipDTOs(memberships []auth.Membership) []membershipDTO {
	dtos := make([]membershipDTO, 0, len(memberships))
	for _, membership := range memberships {
		var removedAt *string
		if membership.RemovedAt != nil {
			formatted := formatTimestamp(*membership.RemovedAt)
			removedAt = &formatted
		}
		dtos = append(dtos, membershipDTO{
			HouseholdID: membership.HouseholdID.String(),
			Role:        string(membership.Role),
			Status:      string(membership.Status),
			JoinedAt:    formatTimestamp(membership.JoinedAt),
			RemovedAt:   removedAt,
		})
	}
	return dtos
}
