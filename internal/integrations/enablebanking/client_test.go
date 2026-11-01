package enablebanking

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testPrivateKeyPEM(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal RSA key: %v", err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestNewClientValidatesConfiguration(t *testing.T) {
	_, keyPEM := testPrivateKeyPEM(t)
	tests := []struct {
		name   string
		config Config
	}{
		{name: "missing application ID", config: Config{PrivateKeyPEM: keyPEM}},
		{name: "missing key", config: Config{ApplicationID: "app-id"}},
		{name: "non-HTTPS remote URL", config: Config{ApplicationID: "app-id", PrivateKeyPEM: keyPEM, BaseURL: "http://bank.example"}},
		{name: "URL path", config: Config{ApplicationID: "app-id", PrivateKeyPEM: keyPEM, BaseURL: "https://api.enablebanking.com/v1"}},
		{name: "overlong token TTL", config: Config{ApplicationID: "app-id", PrivateKeyPEM: keyPEM, TokenTTL: 24*time.Hour + time.Second}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewClient(test.config); err == nil {
				t.Fatal("NewClient succeeded; want validation error")
			}
		})
	}
}

func TestNewClientAllowsLoopbackHTTPForTests(t *testing.T) {
	_, keyPEM := testPrivateKeyPEM(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := NewClient(Config{ApplicationID: "app-id", PrivateKeyPEM: keyPEM, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient(loopback HTTP): %v", err)
	}
	if err := client.Get(context.Background(), "/sessions", nil, nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestJWTSignerCreatesVerifiableEnableBankingJWT(t *testing.T) {
	key, keyPEM := testPrivateKeyPEM(t)
	signer, err := newJWTSigner(keyPEM)
	if err != nil {
		t.Fatalf("newJWTSigner: %v", err)
	}
	issuedAt := time.Date(2026, 11, 1, 12, 30, 0, 0, time.FixedZone("test", 2*60*60))
	token, err := signer.sign("application-id", issuedAt, 3*time.Minute)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d segments, want 3", len(parts))
	}
	decode := func(segment string, target any) {
		t.Helper()
		data, err := base64.RawURLEncoding.DecodeString(segment)
		if err != nil {
			t.Fatalf("decode JWT segment: %v", err)
		}
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatalf("unmarshal JWT segment: %v", err)
		}
	}
	var header jwtHeader
	decode(parts[0], &header)
	if header.Type != "JWT" || header.Algorithm != "RS256" || header.KeyID != "application-id" {
		t.Fatalf("JWT header = %+v", header)
	}
	var claims jwtClaims
	decode(parts[1], &claims)
	wantIssued := issuedAt.UTC().Unix()
	if claims.Issuer != jwtIssuer || claims.Audience != jwtAudience || claims.IssuedAt != wantIssued || claims.Expires != wantIssued+int64((3*time.Minute)/time.Second) {
		t.Fatalf("JWT claims = %+v", claims)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode JWT signature: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("verify JWT signature: %v", err)
	}
}

func TestClientSendsSignedJSONAndDecodesResponse(t *testing.T) {
	key, keyPEM := testPrivateKeyPEM(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		if r.Method != http.MethodPost || r.URL.Path != "/sessions" || r.URL.RawQuery != "" {
			t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected JSON headers: %v", r.Header)
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			t.Errorf("Authorization does not contain a signed JWT")
		} else {
			signature, decodeErr := base64.RawURLEncoding.DecodeString(parts[2])
			if decodeErr != nil {
				t.Errorf("decode signature: %v", decodeErr)
			} else {
				digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
				if verifyErr := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); verifyErr != nil {
					t.Errorf("verify authorization JWT: %v", verifyErr)
				}
			}
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if body.Code != "one-time-code" {
			t.Errorf("request code = %q", body.Code)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"session-123"}`))
	}))
	defer server.Close()

	client, err := NewClient(Config{
		ApplicationID: "application-id",
		PrivateKeyPEM: keyPEM,
		BaseURL:       server.URL,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	var response struct {
		SessionID string `json:"session_id"`
	}
	err = client.Post(context.Background(), "/sessions", map[string]string{"code": "one-time-code"}, &response)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if response.SessionID != "session-123" {
		t.Fatalf("session_id = %q, want session-123", response.SessionID)
	}
}

func TestClientRejectsEscapingPathsAndDoesNotExposeErrorBody(t *testing.T) {
	_, keyPEM := testPrivateKeyPEM(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"sensitive bank details"}`))
	}))
	defer server.Close()

	client, err := NewClient(Config{ApplicationID: "application-id", PrivateKeyPEM: keyPEM, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	for _, path := range []string{"https://evil.example/path", "//evil.example/path", "relative"} {
		if err := client.Get(context.Background(), path, nil, nil); err == nil {
			t.Errorf("Get(%q) succeeded; want invalid path error", path)
		}
	}

	err = client.Get(context.Background(), "/sessions", url.Values{"status": {"AUTHORIZED"}}, nil)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("Get error = %#v, want HTTPError 429", err)
	}
	if strings.Contains(err.Error(), "sensitive bank details") {
		t.Fatalf("provider response body leaked through error: %v", err)
	}
}
