package enablebanking

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type ASPSP struct {
	Name     string   `json:"name"`
	Country  string   `json:"country"`
	PSUTypes []string `json:"psu_types,omitempty"`
}

type ASPSPRef struct {
	Name    string `json:"name"`
	Country string `json:"country"`
}

type Access struct {
	ValidUntil *time.Time `json:"valid_until,omitempty"`
}

type StartAuthorizationRequest struct {
	Access      *Access  `json:"access,omitempty"`
	ASPSP       ASPSPRef `json:"aspsp"`
	State       string   `json:"state"`
	RedirectURL string   `json:"redirect_url"`
}

type StartAuthorizationResponse struct {
	URL             string `json:"url"`
	AuthorizationID string `json:"authorization_id"`
	PSUIDHash       string `json:"psu_id_hash"`
}

type AccountIdentification struct {
	IBAN string `json:"iban"`
}

type SessionAccount struct {
	UID                  string                `json:"uid"`
	Name                 string                `json:"name"`
	Details              string                `json:"details"`
	Currency             string                `json:"currency"`
	CashAccountType      string                `json:"cash_account_type"`
	AccountID            AccountIdentification `json:"account_id"`
	IdentificationHash   string                `json:"identification_hash"`
	IdentificationHashes []string              `json:"identification_hashes"`
}

type Session struct {
	SessionID string           `json:"session_id"`
	Accounts  []SessionAccount `json:"accounts"`
	ASPSP     ASPSPRef         `json:"aspsp"`
	PSUType   string           `json:"psu_type"`
	Access    *Access          `json:"access"`
}

// ListASPSPs returns the ASPSPs available for a two-letter country code.
func (c *Client) ListASPSPs(ctx context.Context, country string) ([]ASPSP, error) {
	country, err := normalizeCountry(country)
	if err != nil {
		return nil, err
	}

	var aspsps []ASPSP
	if err := c.Get(ctx, "/aspsps", url.Values{"country": {country}}, &aspsps); err != nil {
		return nil, err
	}
	if aspsps == nil {
		aspsps = []ASPSP{}
	}
	return aspsps, nil
}

// StartAuthorization begins a user authorisation and returns the redirect URL
// the PSU must be sent to. The caller supplies the exact registered redirect
// URL and a single-use state.
func (c *Client) StartAuthorization(ctx context.Context, request StartAuthorizationRequest) (StartAuthorizationResponse, error) {
	if strings.TrimSpace(request.ASPSP.Name) == "" {
		return StartAuthorizationResponse{}, errors.New("Enable Banking ASPSP name is required")
	}
	if _, err := normalizeCountry(request.ASPSP.Country); err != nil {
		return StartAuthorizationResponse{}, fmt.Errorf("Enable Banking ASPSP country: %w", err)
	}
	if strings.TrimSpace(request.State) == "" {
		return StartAuthorizationResponse{}, errors.New("Enable Banking authorization state is required")
	}
	if err := validateRedirectURL(request.RedirectURL); err != nil {
		return StartAuthorizationResponse{}, err
	}
	var response StartAuthorizationResponse
	if err := c.Post(ctx, "/auth", request, &response); err != nil {
		return StartAuthorizationResponse{}, err
	}
	if response.URL == "" {
		return StartAuthorizationResponse{}, errors.New("Enable Banking authorization response is missing URL")
	}
	return response, nil
}

// AuthorizeSession exchanges a one-time authorisation code for a session.
func (c *Client) AuthorizeSession(ctx context.Context, code string) (Session, error) {
	if strings.TrimSpace(code) == "" {
		return Session{}, errors.New("Enable Banking authorization code is required")
	}
	var session Session
	if err := c.Post(ctx, "/sessions", map[string]string{"code": code}, &session); err != nil {
		return Session{}, err
	}
	if session.SessionID == "" {
		return Session{}, errors.New("Enable Banking session response is missing session_id")
	}
	return session, nil
}

// DeleteSession revokes a provider session (best effort at the ASPSP).
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	if !isUUID(sessionID) {
		return errors.New("Enable Banking session ID must be a UUID")
	}
	return c.Delete(ctx, "/sessions/"+url.PathEscape(sessionID), nil)
}

func normalizeCountry(country string) (string, error) {
	if len(country) != 2 || !isASCIILetter(country[0]) || !isASCIILetter(country[1]) {
		return "", errors.New("country must be exactly two ASCII letters")
	}
	return strings.ToUpper(country), nil
}

func isASCIILetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func validateRedirectURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || strings.Contains(rawURL, "#") {
		return errors.New("Enable Banking redirect URL must be an absolute URL with a host and no fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return errors.New("Enable Banking redirect URL must use HTTPS (HTTP is permitted for loopback tests only)")
	}
	return nil
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F') {
			return false
		}
	}
	return true
}
