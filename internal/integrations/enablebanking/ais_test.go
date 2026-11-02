package enablebanking

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newAISClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	_, keyPEM := testPrivateKeyPEM(t)
	server := httptest.NewServer(handler)
	client, err := NewClient(Config{
		ApplicationID: "123e4567-e89b-12d3-a456-426614174000",
		PrivateKeyPEM: keyPEM,
		BaseURL:       server.URL,
		HTTPClient:    server.Client(),
	})
	if err != nil {
		server.Close()
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(server.Close)
	return client, server
}

func TestListASPSPs(t *testing.T) {
	t.Run("parses ASPSPs with signed request and unknown fields", func(t *testing.T) {
		client, _ := newAISClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/aspsps" || r.URL.RawQuery != "country=BG" {
				t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
			authorization := r.Header.Get("Authorization")
			if !strings.HasPrefix(authorization, "Bearer ") {
				t.Errorf("Authorization header = %q, want Bearer token", authorization)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"Bank A","country":"BG","psu_types":["personal"],"unknown":"ignored"},{"name":"Bank B","country":"BG"}]`))
		}))

		aspsps, err := client.ListASPSPs(context.Background(), "bg")
		if err != nil {
			t.Fatalf("ListASPSPs: %v", err)
		}
		if len(aspsps) != 2 || aspsps[0].Name != "Bank A" || aspsps[0].Country != "BG" || len(aspsps[0].PSUTypes) != 1 || aspsps[1].Name != "Bank B" {
			t.Fatalf("ASPSPs = %+v", aspsps)
		}
	})

	t.Run("empty list is non-nil", func(t *testing.T) {
		client, _ := newAISClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[]`))
		}))
		aspsps, err := client.ListASPSPs(context.Background(), "BG")
		if err != nil {
			t.Fatalf("ListASPSPs: %v", err)
		}
		if aspsps == nil || len(aspsps) != 0 {
			t.Fatalf("ASPSPs = %#v, want non-nil empty slice", aspsps)
		}
	})
}

func TestStartAuthorization(t *testing.T) {
	client, _ := newAISClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/auth" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if len(body) != 3 {
			t.Errorf("body fields = %v, want only aspsp, state and redirect_url", body)
		}
		var aspsp map[string]string
		if err := json.Unmarshal(body["aspsp"], &aspsp); err != nil {
			t.Errorf("decode aspsp: %v", err)
		}
		if len(aspsp) != 2 || aspsp["name"] != "Example Bank" || aspsp["country"] != "BG" {
			t.Errorf("aspsp = %v", aspsp)
		}
		var state, redirectURL string
		_ = json.Unmarshal(body["state"], &state)
		_ = json.Unmarshal(body["redirect_url"], &redirectURL)
		if state != "single-use-state" || redirectURL != "https://app.example/callback?source=bank" {
			t.Errorf("state = %q, redirect_url = %q", state, redirectURL)
		}
		if _, exists := body["access"]; exists {
			t.Errorf("body unexpectedly contains access: %s", body["access"])
		}
		_, _ = w.Write([]byte(`{"url":"https://bank.example/authorize","authorization_id":"authorization-123","psu_id_hash":"hash"}`))
	}))

	response, err := client.StartAuthorization(context.Background(), StartAuthorizationRequest{
		ASPSP:       ASPSPRef{Name: "Example Bank", Country: "BG"},
		State:       "single-use-state",
		RedirectURL: "https://app.example/callback?source=bank",
	})
	if err != nil {
		t.Fatalf("StartAuthorization: %v", err)
	}
	if response.URL != "https://bank.example/authorize" || response.AuthorizationID != "authorization-123" || response.PSUIDHash != "hash" {
		t.Fatalf("response = %+v", response)
	}

	t.Run("requires response URL", func(t *testing.T) {
		client, _ := newAISClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"authorization_id":"authorization-123"}`))
		}))
		_, err := client.StartAuthorization(context.Background(), StartAuthorizationRequest{
			ASPSP:       ASPSPRef{Name: "Example Bank", Country: "BG"},
			State:       "single-use-state",
			RedirectURL: "https://app.example/callback",
		})
		if err == nil {
			t.Fatal("StartAuthorization succeeded without URL")
		}
	})
}

func TestAuthorizeSession(t *testing.T) {
	t.Run("parses session and account", func(t *testing.T) {
		client, _ := newAISClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/sessions" {
				t.Errorf("request = %s %s", r.Method, r.URL.Path)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			if len(body) != 1 || body["code"] != "one-time-code" {
				t.Errorf("request body = %v", body)
			}
			_, _ = w.Write([]byte(`{"session_id":"session-123","accounts":[{"uid":"account-1","name":"Main","details":"Checking","currency":"EUR","identification_hash":"account-hash","account_id":{"iban":"BG00TEST"}}],"aspsp":{"name":"Example Bank","country":"BG"},"psu_type":"personal","unknown":true}`))
		}))

		session, err := client.AuthorizeSession(context.Background(), "one-time-code")
		if err != nil {
			t.Fatalf("AuthorizeSession: %v", err)
		}
		if session.SessionID != "session-123" || len(session.Accounts) != 1 || session.Accounts[0].UID != "account-1" || session.Accounts[0].Currency != "EUR" || session.Accounts[0].IdentificationHash != "account-hash" {
			t.Fatalf("session = %+v", session)
		}
	})

	t.Run("requires session id", func(t *testing.T) {
		client, _ := newAISClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"accounts":[]}`))
		}))
		if _, err := client.AuthorizeSession(context.Background(), "code"); err == nil {
			t.Fatal("AuthorizeSession succeeded without session_id")
		}
	})
}

func TestDeleteSession(t *testing.T) {
	const sessionID = "123e4567-e89b-12d3-a456-426614174000"
	var calls atomic.Int32
	client, _ := newAISClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodDelete || r.URL.Path != "/sessions/"+sessionID {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if err := client.DeleteSession(context.Background(), sessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("server calls = %d, want 1", calls.Load())
	}
	if err := client.DeleteSession(context.Background(), "not-a-uuid/segment"); err == nil {
		t.Fatal("DeleteSession accepted a non-UUID session id")
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid session id sent a request; calls = %d", calls.Load())
	}
}

func TestAISHTTPErrorDoesNotExposeResponseBody(t *testing.T) {
	client, _ := newAISClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"sensitive provider response"}`))
	}))
	_, err := client.ListASPSPs(context.Background(), "BG")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("ListASPSPs error = %#v, want HTTPError 502", err)
	}
	if strings.Contains(err.Error(), "sensitive provider response") {
		t.Fatalf("provider response body leaked through error: %v", err)
	}
}

func TestAISValidation(t *testing.T) {
	var calls atomic.Int32
	client, _ := newAISClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"url":"https://bank.example/authorize","session_id":"session"}`))
	}))
	tests := []struct {
		name string
		call func() error
	}{
		{name: "bad list country", call: func() error { _, err := client.ListASPSPs(context.Background(), "B1"); return err }},
		{name: "bad authorization country", call: func() error {
			_, err := client.StartAuthorization(context.Background(), StartAuthorizationRequest{ASPSP: ASPSPRef{Name: "Bank", Country: "B"}, State: "state", RedirectURL: "https://app.example/callback"})
			return err
		}},
		{name: "blank ASPSP name", call: func() error {
			_, err := client.StartAuthorization(context.Background(), StartAuthorizationRequest{ASPSP: ASPSPRef{Name: "  ", Country: "BG"}, State: "state", RedirectURL: "https://app.example/callback"})
			return err
		}},
		{name: "blank state", call: func() error {
			_, err := client.StartAuthorization(context.Background(), StartAuthorizationRequest{ASPSP: ASPSPRef{Name: "Bank", Country: "BG"}, State: " \t", RedirectURL: "https://app.example/callback"})
			return err
		}},
		{name: "relative redirect URL", call: func() error {
			_, err := client.StartAuthorization(context.Background(), StartAuthorizationRequest{ASPSP: ASPSPRef{Name: "Bank", Country: "BG"}, State: "state", RedirectURL: "/callback"})
			return err
		}},
		{name: "insecure redirect URL", call: func() error {
			_, err := client.StartAuthorization(context.Background(), StartAuthorizationRequest{ASPSP: ASPSPRef{Name: "Bank", Country: "BG"}, State: "state", RedirectURL: "http://app.example/callback"})
			return err
		}},
		{name: "redirect URL fragment", call: func() error {
			_, err := client.StartAuthorization(context.Background(), StartAuthorizationRequest{ASPSP: ASPSPRef{Name: "Bank", Country: "BG"}, State: "state", RedirectURL: "https://app.example/callback#fragment"})
			return err
		}},
		{name: "blank authorization code", call: func() error { _, err := client.AuthorizeSession(context.Background(), " \t"); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("operation succeeded; want validation error")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("validation failures sent %d requests", calls.Load())
	}
}
