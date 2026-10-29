package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func forwardedRequest(remoteAddr, forwardedFor, realIP string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	request.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		request.Header.Set("X-Forwarded-For", forwardedFor)
	}
	if realIP != "" {
		request.Header.Set("X-Real-IP", realIP)
	}
	return request
}

func TestClientIPResolvesForwardedAddressFromTrustedProxy(t *testing.T) {
	resolver := newClientIPResolver([]string{"127.0.0.0/8", "::1/128"})

	cases := []struct {
		name       string
		remoteAddr string
		forwarded  string
		realIP     string
		want       string
	}{
		{
			name:       "single forwarded hop",
			remoteAddr: "127.0.0.1:4321",
			forwarded:  "203.0.113.7",
			want:       "203.0.113.7",
		},
		{
			name:       "rightmost non-trusted hop wins",
			remoteAddr: "127.0.0.1:4321",
			forwarded:  "198.51.100.99, 203.0.113.7",
			want:       "203.0.113.7",
		},
		{
			name:       "trusted loopback hops are skipped",
			remoteAddr: "127.0.0.1:4321",
			forwarded:  "203.0.113.99, 127.0.0.1",
			want:       "203.0.113.99",
		},
		{
			name:       "X-Real-IP fallback",
			remoteAddr: "127.0.0.1:4321",
			realIP:     "203.0.113.7",
			want:       "203.0.113.7",
		},
		{
			name:       "loopback peer without headers",
			remoteAddr: "127.0.0.1:4321",
			want:       "127.0.0.1",
		},
		{
			name:       "IPv6 loopback peer",
			remoteAddr: "[::1]:4321",
			forwarded:  "203.0.113.7",
			want:       "203.0.113.7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolver.resolve(forwardedRequest(tc.remoteAddr, tc.forwarded, tc.realIP))
			if got != tc.want {
				t.Fatalf("resolved client IP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientIPIgnoresForwardedHeadersFromUntrustedPeer(t *testing.T) {
	resolver := newClientIPResolver([]string{"127.0.0.0/8"})

	request := forwardedRequest("203.0.113.50:1234", "198.51.100.99", "198.51.100.99")
	if got := resolver.resolve(request); got != "203.0.113.50" {
		t.Fatalf("resolved client IP = %q, want the untrusted transport peer", got)
	}
}

func TestAuthRateLimitBucketsPerForwardedClientIP(t *testing.T) {
	limiter := NewRateLimiter(1, time.Hour)
	handler := RateLimit(limiter, authRateLimitKey(newClientIPResolver([]string{"127.0.0.0/8"})))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	)
	call := func(forwarded string) int {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, forwardedRequest("127.0.0.1:4321", forwarded, ""))
		return recorder.Code
	}

	if got := call("203.0.113.7"); got != http.StatusNoContent {
		t.Fatalf("first client request = %d, want 204", got)
	}
	if got := call("203.0.113.7"); got != http.StatusTooManyRequests {
		t.Fatalf("second request from the same client = %d, want 429", got)
	}
	if got := call("203.0.113.8"); got != http.StatusNoContent {
		t.Fatalf("request from a different client = %d, want its own bucket (204)", got)
	}
	// The loopback nginx hop must not consume a bucket shared by every user:
	// a request with no forwarding header is independent of the client keys.
	if got := call(""); got != http.StatusNoContent {
		t.Fatalf("loopback-only request = %d, want an independent bucket (204)", got)
	}
}

func TestAuthRateLimitIgnoresSpoofedForwardedIPFromUntrustedPeer(t *testing.T) {
	limiter := NewRateLimiter(1, time.Hour)
	handler := RateLimit(limiter, authRateLimitKey(newClientIPResolver([]string{"127.0.0.0/8"})))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	)
	call := func(remoteAddr, forwarded string) int {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, forwardedRequest(remoteAddr, forwarded, ""))
		return recorder.Code
	}

	if got := call("203.0.113.50:1", "198.51.100.1"); got != http.StatusNoContent {
		t.Fatalf("first direct request = %d, want 204", got)
	}
	// A different spoofed header from the same untrusted peer must not open a
	// new bucket: the transport address is the key.
	if got := call("203.0.113.50:2", "198.51.100.2"); got != http.StatusTooManyRequests {
		t.Fatalf("second direct request with a new spoofed header = %d, want 429", got)
	}
}

func TestLoggingRecordsResolvedClientIP(t *testing.T) {
	logger, logs := testLogger()
	handler := Chain(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		RequestID(),
		Logging(logger, newClientIPResolver([]string{"127.0.0.0/8"})),
	)

	handler.ServeHTTP(httptest.NewRecorder(), forwardedRequest("127.0.0.1:4321", "203.0.113.7", ""))
	if !strings.Contains(logs.String(), `"client_ip":"203.0.113.7"`) {
		t.Fatalf("log does not record the forwarded client IP: %s", logs.String())
	}
}
