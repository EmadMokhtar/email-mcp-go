package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EmadMokhtar/email-mcp-go/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestRequireAuth(t *testing.T) {
	s := &EmailMCPServer{config: &config.Config{AuthToken: "s3cret"}}

	// StatusTeapot marks "the request reached the protected handler".
	reached := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := s.requireAuth(reached)

	tests := []struct {
		name       string
		method     string
		authHeader string
		wantStatus int
	}{
		{"no header", http.MethodPost, "", http.StatusUnauthorized},
		{"wrong token", http.MethodPost, "Bearer wrong", http.StatusUnauthorized},
		{"token without scheme", http.MethodPost, "s3cret", http.StatusUnauthorized},
		{"token as basic auth", http.MethodPost, "Basic s3cret", http.StatusUnauthorized},
		{"prefix of the token", http.MethodPost, "Bearer s3cre", http.StatusUnauthorized},
		{"correct token", http.MethodPost, "Bearer s3cret", http.StatusTeapot},
		// A browser never sends credentials on a preflight request.
		{"preflight without token", http.MethodOptions, "", http.StatusTeapot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/mcp", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestStartHTTPRefusesWithoutToken(t *testing.T) {
	s := &EmailMCPServer{config: &config.Config{}}

	// Must fail before it ever binds a port.
	err := s.StartHTTP(t.Context(), "127.0.0.1:0")

	assert.ErrorContains(t, err, "MCP_AUTH_TOKEN")
}
