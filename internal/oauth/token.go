// Package oauth obtains OAuth2 access tokens for IMAP and SMTP, and provides
// the XOAUTH2 authentication mechanism both protocols use.
package oauth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// GoogleTokenURL is the token endpoint used when none is configured.
const GoogleTokenURL = "https://oauth2.googleapis.com/token"

// refreshSkew renews a token slightly before it expires, so a token does not
// lapse between the check and the server reading it.
const refreshSkew = 30 * time.Second

// TokenSource exchanges a long-lived refresh token for short-lived access
// tokens and caches the result until shortly before it expires. It is safe for
// concurrent use.
type TokenSource struct {
	tokenURL     string
	clientID     string
	clientSecret string
	refreshToken string
	httpClient   *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// NewTokenSource creates a source for the given credentials. An empty tokenURL
// falls back to Google's endpoint.
func NewTokenSource(tokenURL, clientID, clientSecret, refreshToken string) *TokenSource {
	if tokenURL == "" {
		tokenURL = GoogleTokenURL
	}

	return &TokenSource{
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		refreshToken: refreshToken,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// Token returns a valid access token, refreshing it when the cached one is
// missing or about to expire.
func (s *TokenSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && time.Now().Before(s.expiry.Add(-refreshSkew)) {
		return s.token, nil
	}

	token, lifetime, err := s.refresh()
	if err != nil {
		return "", err
	}

	s.token = token
	s.expiry = time.Now().Add(lifetime)

	return token, nil
}

// tokenResponse covers both the success and the error shape of the endpoint.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (s *TokenSource) refresh() (string, time.Duration, error) {
	form := url.Values{
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
		"refresh_token": {s.refreshToken},
		"grant_type":    {"refresh_token"},
	}

	resp, err := s.httpClient.Post(
		s.tokenURL,
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", 0, fmt.Errorf("failed to reach the OAuth token endpoint: %w", err)
	}
	defer resp.Body.Close()

	var parsed tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", 0, fmt.Errorf("failed to read the OAuth token response (status %d): %w", resp.StatusCode, err)
	}

	if parsed.Error != "" {
		return "", 0, fmt.Errorf("OAuth token refresh rejected: %s: %s", parsed.Error, parsed.ErrorDescription)
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("OAuth token endpoint returned status %d", resp.StatusCode)
	}

	if parsed.AccessToken == "" {
		return "", 0, fmt.Errorf("OAuth token endpoint returned no access token")
	}

	lifetime := time.Duration(parsed.ExpiresIn) * time.Second
	if lifetime <= 0 {
		// Endpoints may omit expires_in. Assume the common one-hour lifetime.
		lifetime = time.Hour
	}

	return parsed.AccessToken, lifetime, nil
}
