package oauth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenSourceRefreshesAndCaches(t *testing.T) {
	var mu sync.Mutex
	calls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		current := calls
		mu.Unlock()

		require.NoError(t, r.ParseForm())
		assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
		assert.Equal(t, "id", r.Form.Get("client_id"))
		assert.Equal(t, "secret", r.Form.Get("client_secret"))
		assert.Equal(t, "refresh", r.Form.Get("refresh_token"))

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":3600}`, current)
	}))
	defer server.Close()

	src := NewTokenSource(server.URL, "id", "secret", "refresh")

	first, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "token-1", first)

	// A second call inside the lifetime must reuse the cached token.
	second, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "token-1", second)

	mu.Lock()
	assert.Equal(t, 1, calls, "the endpoint should be called once while the token is valid")
	mu.Unlock()
}

func TestTokenSourceRefreshesWhenExpired(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		// One second lifetime, which the refresh skew treats as already stale.
		fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":1}`, calls)
	}))
	defer server.Close()

	src := NewTokenSource(server.URL, "id", "secret", "refresh")

	first, err := src.Token()
	require.NoError(t, err)

	second, err := src.Token()
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "an expiring token must be replaced")
	assert.Equal(t, 2, calls)
}

func TestTokenSourceReportsEndpointErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{
			name:    "rejected refresh token",
			status:  http.StatusBadRequest,
			body:    `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`,
			wantErr: "invalid_grant",
		},
		{
			name:    "no token in response",
			status:  http.StatusOK,
			body:    `{}`,
			wantErr: "no access token",
		},
		{
			name:    "unreadable response",
			status:  http.StatusOK,
			body:    `not json`,
			wantErr: "failed to read the OAuth token response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			_, err := NewTokenSource(server.URL, "id", "secret", "refresh").Token()

			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestNewTokenSourceDefaultsToGoogle(t *testing.T) {
	assert.Equal(t, GoogleTokenURL, NewTokenSource("", "id", "secret", "refresh").tokenURL)
}

func TestXOAuth2InitialResponse(t *testing.T) {
	// The layout is fixed by the mechanism: control-A between the fields and
	// two more at the end.
	want := "user=me@example.com\x01auth=Bearer tok3n\x01\x01"

	mech, ir, err := NewSASLClient("me@example.com", "tok3n").Start()
	require.NoError(t, err)
	assert.Equal(t, "XOAUTH2", mech)
	assert.Equal(t, want, string(ir))

	proto, toServer, err := NewSMTPAuth("me@example.com", "tok3n").Start(nil)
	require.NoError(t, err)
	assert.Equal(t, "XOAUTH2", proto)
	assert.Equal(t, want, string(toServer))
}

func TestXOAuth2ChallengeIsAnsweredEmpty(t *testing.T) {
	// A challenge means the token was refused; an empty reply makes the server
	// report a normal error instead of stalling.
	resp, err := NewSASLClient("me@example.com", "bad").Next([]byte(`{"status":"401"}`))
	require.NoError(t, err)
	assert.Empty(t, resp)

	smtpResp, err := NewSMTPAuth("me@example.com", "bad").Next([]byte(`{"status":"401"}`), true)
	require.NoError(t, err)
	assert.Empty(t, smtpResp)
}

func TestTokenSourceIsConcurrencySafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		fmt.Fprint(w, `{"access_token":"shared","expires_in":3600}`)
	}))
	defer server.Close()

	src := NewTokenSource(server.URL, "id", "secret", "refresh")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := src.Token()
			assert.NoError(t, err)
			assert.Equal(t, "shared", token)
		}()
	}
	wg.Wait()
}
