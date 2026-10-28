package httpapi

import (
	"context"
	"net/http"
	"time"
)

const readinessTimeout = 2 * time.Second

// healthResponse is deliberately limited to a status word. Liveness and
// readiness endpoints must not expose versions, DSNs, dependency names or
// internal diagnostics (SECURITY_AND_OPERATIONS "Operational telemetry").
type healthResponse struct {
	Status string `json:"status"`
}

// handleHealthz is the liveness probe: the process can serve HTTP. It does not
// touch the database, so a database outage does not restart a healthy process.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// handleReadyz is the readiness probe: the process may receive traffic. When a
// readiness check is wired it is bounded by readinessTimeout; without one the
// server is trivially ready (useful for the skeleton and tests). The probe
// reports only ready/not-ready, never the underlying error.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.deps.Readiness == nil {
		writeJSON(w, http.StatusOK, healthResponse{Status: "ready"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()
	if err := s.deps.Readiness(ctx); err != nil {
		if s.deps.Logger != nil {
			s.deps.Logger.WarnContext(r.Context(), "readiness_check_failed",
				"request_id", RequestIDFromContext(r.Context()),
			)
		}
		writeError(w, r, errServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ready"})
}

// handleAPIVersionHealth is a minimal JSON endpoint under /api/v1 that proves
// the versioned stack (routing, middleware, JSON encoding) is wired end to end.
func (s *Server) handleAPIVersionHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}
