package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"family-budget/internal/application"
	"family-budget/internal/auth"
	"family-budget/internal/config"
	"family-budget/internal/ports"
)

// Dependencies is the explicit wiring input for the HTTP server. Everything is
// injected so tests can run the real middleware chain without a database.
type Dependencies struct {
	Config config.Config
	Logger *slog.Logger

	// Clock is the application time seam. It is optional in the skeleton and
	// becomes required by the auth service wiring.
	Clock ports.Clock

	// Readiness is optional. A nil check means "trivially ready"; production
	// wiring should ping the database with a short timeout.
	Readiness func(context.Context) error

	// Auth is the authentication service. When nil, auth routes are not
	// registered; the skeleton (health-only) remains usable for tests.
	Auth *auth.Service

	// App is the household/ledger application service. When nil, resource
	// routes are not registered, so health/auth-only tests remain usable.
	App *application.Service

	// AuthRateLimiter overrides the limiter built from Config.Auth. It exists
	// for tests and for deployments that share one limiter across servers.
	AuthRateLimiter *RateLimiter
}

// Server owns the http.Server, the route mux and the middleware chain.
type Server struct {
	deps       Dependencies
	handler    http.Handler
	httpServer *http.Server

	// routes records the method-aware patterns registered on the mux. It is
	// the authoritative route table for contract drift tests, so a pattern
	// cannot be documented without being registered (or vice versa).
	routes []string
}

// NewServer builds the route table and middleware chain. The route set is
// intentionally tiny: health probes now, resource endpoints in the next task
// and auth routes in the authentication task.
func NewServer(deps Dependencies) (*Server, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Clock == nil {
		deps.Clock = systemClock{}
	}

	server := &Server{deps: deps}
	mux := http.NewServeMux()

	server.registerFunc(mux, "GET /healthz", server.handleHealthz)
	server.registerFunc(mux, "GET /readyz", server.handleReadyz)
	server.registerFunc(mux, "GET /api/v1/health", server.handleAPIVersionHealth)
	server.registerAuthRoutes(mux)
	server.registerResourceRoutes(mux)
	server.registerTransactionRoutes(mux)
	server.registerPlanRoutes(mux)
	server.registerSuggestionRoutes(mux)

	// Catch-all so unknown paths use the API error envelope instead of
	// net/http's plain-text 404.
	mux.HandleFunc("/", server.handleNotFound)

	server.handler = Chain(
		mux,
		RequestID(),
		Recovery(deps.Logger),
		// CORS is deliberately ahead of logging, limits, per-route auth and
		// routing so browser preflights are answered before those layers run.
		corsMiddleware(deps.Config.HTTPCORSAllowedOrigins, HeaderRequestID),
		Logging(deps.Logger, newClientIPResolver(deps.Config.HTTP.TrustedProxies)),
		MaxBodyBytes(deps.Config.HTTP.MaxBodyBytes),
		Timeout(deps.Config.HTTP.RequestTimeout),
	)

	server.httpServer = &http.Server{
		Addr:              fmt.Sprintf(":%d", deps.Config.HTTP.Port),
		Handler:           server.handler,
		ReadHeaderTimeout: deps.Config.HTTP.ReadHeaderTimeout,
		ReadTimeout:       deps.Config.HTTP.ReadTimeout,
		WriteTimeout:      deps.Config.HTTP.WriteTimeout,
		IdleTimeout:       deps.Config.HTTP.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(deps.Logger.Handler(), slog.LevelError),
	}

	return server, nil
}

// register installs a route and records its pattern. Every route must go
// through this helper so the contract drift test sees exactly what ServeMux
// serves.
func (s *Server) register(mux *http.ServeMux, pattern string, handler http.Handler) {
	mux.Handle(pattern, handler)
	s.routes = append(s.routes, pattern)
}

// registerFunc is the http.HandlerFunc convenience form of register.
func (s *Server) registerFunc(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	s.register(mux, pattern, handler)
}

// Routes returns a copy of the registered method-aware route patterns.
func (s *Server) Routes() []string {
	return append([]string(nil), s.routes...)
}

// Handler exposes the composed handler for httptest and reverse-proxy setup.
func (s *Server) Handler() http.Handler { return s.handler }

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.httpServer.Addr }

// ListenAndServe starts the blocking HTTP listener.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops accepting requests and waits for in-flight work,
// bounded by the configured shutdown timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Run serves until ctx is canceled, then shuts down gracefully. It returns nil
// for a clean shutdown and the underlying error otherwise.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		timeout := s.deps.Config.HTTP.ShutdownTimeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// handleNotFound is the JSON fallback for unknown routes.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, errNotFound)
}

// systemClock is the stdlib-only production clock.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }
