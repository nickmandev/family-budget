// Package enablebanking contains the server-side transport boundary for the
// Enable Banking AIS API. It deliberately has no dependency on the application,
// domain, or HTTP transport packages.
package enablebanking

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL        = "https://api.enablebanking.com"
	defaultTokenTTL       = 5 * time.Minute
	maxTokenTTL           = 24 * time.Hour
	defaultRequestTimeout = 15 * time.Second
	maxResponseBytes      = 8 << 20
)

// Config contains secrets and runtime dependencies for the provider client.
// PrivateKeyPEM must be loaded by trusted server-side wiring from a mounted
// secret; it must never be passed to a client or logged.
type Config struct {
	ApplicationID string
	PrivateKeyPEM []byte
	BaseURL       string
	TokenTTL      time.Duration
	HTTPClient    *http.Client
	Now           func() time.Time
}

// Client signs one short-lived application JWT per request and calls the
// configured Enable Banking API endpoint.
type Client struct {
	applicationID string
	signer        *jwtSigner
	baseURL       *url.URL
	tokenTTL      time.Duration
	httpClient    *http.Client
	now           func() time.Time
}

// HTTPError reports a provider HTTP status without retaining or exposing the
// response body, which may contain sensitive account information.
type HTTPError struct {
	StatusCode int
	RequestID  string
}

func (e *HTTPError) Error() string {
	if e.RequestID == "" {
		return fmt.Sprintf("Enable Banking API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("Enable Banking API returned HTTP %d (request %s)", e.StatusCode, e.RequestID)
}

// NewClient validates configuration and constructs a provider client. An HTTP
// URL is allowed only for loopback test servers; production endpoints must use
// HTTPS.
func NewClient(config Config) (*Client, error) {
	applicationID := strings.TrimSpace(config.ApplicationID)
	if applicationID == "" {
		return nil, errors.New("Enable Banking application ID is required")
	}
	signer, err := newJWTSigner(config.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("Enable Banking private key: %w", err)
	}

	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" || (parsedURL.Path != "" && parsedURL.Path != "/") {
		return nil, errors.New("Enable Banking base URL must be an origin without credentials, path, query or fragment")
	}
	if parsedURL.Scheme != "https" && !(parsedURL.Scheme == "http" && isLoopbackHost(parsedURL.Hostname())) {
		return nil, errors.New("Enable Banking base URL must use HTTPS (HTTP is permitted for loopback tests only)")
	}
	parsedURL.Path = ""
	parsedURL.RawPath = ""

	tokenTTL := config.TokenTTL
	if tokenTTL == 0 {
		tokenTTL = defaultTokenTTL
	}
	if tokenTTL < time.Second || tokenTTL > maxTokenTTL {
		return nil, fmt.Errorf("Enable Banking JWT TTL must be at least 1s and no longer than %s", maxTokenTTL)
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	return &Client{
		applicationID: applicationID,
		signer:        signer,
		baseURL:       parsedURL,
		tokenTTL:      tokenTTL,
		httpClient:    httpClient,
		now:           now,
	}, nil
}

// Get sends an authenticated JSON GET request. path must be an absolute API
// path (for example, "/aspsps"); query values are encoded by net/url.
func (c *Client) Get(ctx context.Context, path string, query url.Values, response any) error {
	return c.doJSON(ctx, http.MethodGet, path, query, nil, response)
}

// Post sends an authenticated JSON POST request.
func (c *Client) Post(ctx context.Context, path string, body, response any) error {
	return c.doJSON(ctx, http.MethodPost, path, nil, body, response)
}

// Delete sends an authenticated DELETE request. A nil response is allowed for
// endpoints whose successful response body is not needed by the caller.
func (c *Client) Delete(ctx context.Context, path string, response any) error {
	return c.doJSON(ctx, http.MethodDelete, path, nil, nil, response)
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body, response any) error {
	endpoint, err := c.endpoint(path, query)
	if err != nil {
		return err
	}

	token, err := c.signer.sign(c.applicationID, c.now(), c.tokenTTL)
	if err != nil {
		return fmt.Errorf("sign Enable Banking request: %w", err)
	}

	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode Enable Banking request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), requestBody)
	if err != nil {
		return fmt.Errorf("create Enable Banking request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	result, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send Enable Banking request: %w", err)
	}
	defer result.Body.Close()

	if result.StatusCode < http.StatusOK || result.StatusCode >= http.StatusMultipleChoices {
		return &HTTPError{StatusCode: result.StatusCode, RequestID: result.Header.Get("X-Request-ID")}
	}
	if response == nil || result.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(result.Body, maxResponseBytes))
		return nil
	}
	responseBytes, err := io.ReadAll(io.LimitReader(result.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read Enable Banking response: %w", err)
	}
	if len(responseBytes) > maxResponseBytes {
		return fmt.Errorf("Enable Banking response exceeds %d bytes", maxResponseBytes)
	}
	if err := json.Unmarshal(responseBytes, response); err != nil {
		return fmt.Errorf("decode Enable Banking response: %w", err)
	}
	return nil
}

func (c *Client) endpoint(path string, query url.Values) (*url.URL, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("Enable Banking API path must begin with a single slash")
	}
	relative, err := url.Parse(path)
	if err != nil || relative.IsAbs() || relative.Host != "" || relative.User != nil || relative.RawQuery != "" || relative.Fragment != "" {
		return nil, errors.New("invalid Enable Banking API path")
	}
	endpoint := c.baseURL.ResolveReference(relative)
	if endpoint.Scheme != c.baseURL.Scheme || endpoint.Host != c.baseURL.Host {
		return nil, errors.New("Enable Banking API path escaped the configured origin")
	}
	if query != nil {
		endpoint.RawQuery = query.Encode()
	}
	return endpoint, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
