package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

// corsMiddleware applies the configured allowlist. Exact-origin matching only
// (no wildcards); CORS is a browser-enforcement mechanism, never an
// authorization check — the API still requires a bearer token on every
// protected route.
func corsMiddleware(allowed []string, requestIDHeader string) func(http.Handler) http.Handler {
	if len(allowed) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	allowlist := make(map[string]struct{}, len(allowed))
	for _, origin := range allowed {
		if normalized, ok := normalizeRequestOrigin(origin); ok {
			allowlist[normalized] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			normalized, validOrigin := normalizeRequestOrigin(origin)
			_, allowedOrigin := allowlist[normalized]
			allowedOrigin = allowedOrigin && validOrigin

			preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
			if preflight {
				if allowedOrigin {
					setCORSOriginHeaders(w.Header(), origin, requestIDHeader)
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
					w.Header().Set("Access-Control-Max-Age", "600")
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			if origin != "" && allowedOrigin {
				setCORSOriginHeaders(w.Header(), origin, requestIDHeader)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func setCORSOriginHeaders(header http.Header, origin, requestIDHeader string) {
	header.Set("Access-Control-Allow-Origin", origin)
	header.Add("Vary", "Origin")
	header.Set("Access-Control-Expose-Headers", requestIDHeader)
}

func normalizeRequestOrigin(value string) (string, bool) {
	if value == "" || strings.Contains(value, "*") {
		return "", false
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Opaque != "" || u.User != nil ||
		u.Host == "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") || u.Hostname() == "" {
		return "", false
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", false
	}
	if _, err := url.Parse(scheme + "://" + u.Host); err != nil {
		return "", false
	}
	return scheme + "://" + strings.ToLower(u.Host), true
}
