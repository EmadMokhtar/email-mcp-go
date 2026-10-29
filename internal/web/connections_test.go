package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/tokens"
)

// tokenPattern is the form of an access token.
var tokenPattern = regexp.MustCompile(`emcp_[a-z2-7]{52}`)

// The fixed texts of the connections page.
const (
	textNoScope  = "Choose at least one permission for the token."
	textBadName  = "The name must be 1 to 128 characters of text."
	textNoToken  = "This token was not found."
	textCopyOnce = "Copy this token now. It is shown only once."
)

func TestTokenShownOnce(t *testing.T) {
	p := newPageFixture(t)

	resp, body := p.post("/connections", url.Values{"name": {"laptop"}, "scope": {"email:read", "email:draft"}})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, []string{"no-store"}, resp.Header.Values("Cache-Control"))
	require.Empty(t, resp.Header.Get("Location"))
	require.Contains(t, visibleText(t, body), textCopyOnce)
	found := tokenPattern.FindAllString(body, -1)
	require.NotEmpty(t, found)
	token := found[0]
	for _, other := range found {
		require.Equal(t, token, other)
	}

	principal, err := p.tokens.Verify(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, p.tenant, principal.Tenant)
	require.Equal(t, policy.Scopes{"email:draft", "email:read"}, principal.Scopes)

	// The list shows the token by its name, never the token itself again.
	resp, body = p.get("/connections")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "laptop")
	require.NotContains(t, body, token)
	require.False(t, tokenPattern.MatchString(body))
	require.NotContains(t, p.logs.String(), token)
	require.NotContains(t, string(p.dataFiles()), token, "the store keeps only the hash")
}

// "all" stands for every known scope. The token gets the explicit list.
func TestTokenScopeAllExpands(t *testing.T) {
	p := newPageFixture(t)

	resp, body := p.post("/connections", url.Values{"name": {"ci"}, "scope": {"all"}})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	token := tokenPattern.FindString(body)
	require.NotEmpty(t, token)
	principal, err := p.tokens.Verify(context.Background(), token)
	require.NoError(t, err)
	require.ElementsMatch(t, policy.KnownScopes(), []string(principal.Scopes))
}

func TestTokenRefusesBadInput(t *testing.T) {
	p := newPageFixture(t)
	cases := map[string]struct {
		form url.Values
		text string
	}{
		"no scope":      {url.Values{"name": {"x"}}, textNoScope},
		"unknown scope": {url.Values{"name": {"x"}, "scope": {"email:read", "email:everything"}}, textNoScope},
		"empty name":    {url.Values{"name": {"  "}, "scope": {"email:read"}}, textBadName},
		"control":       {url.Values{"name": {"a\u0007b"}, "scope": {"email:read"}}, textBadName},
	}
	for name, c := range cases {
		resp, body := p.post("/connections", c.form)
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, name)
		require.Contains(t, visibleText(t, body), c.text, name)
		require.False(t, tokenPattern.MatchString(body), name)
	}
	list, err := p.tokens.List(context.Background(), p.tenant)
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestRevokeToken(t *testing.T) {
	p := newPageFixture(t)
	ctx := context.Background()
	token, err := p.tokens.Create(ctx, p.tenant, "laptop", policy.Scopes{"email:read"})
	require.NoError(t, err)
	list, err := p.tokens.List(ctx, p.tenant)
	require.NoError(t, err)
	require.Len(t, list, 1)
	p.addTenant("other")
	theirToken, err := p.tokens.Create(ctx, "other", "theirs", policy.Scopes{"email:read"})
	require.NoError(t, err)
	theirs, err := p.tokens.List(ctx, "other")
	require.NoError(t, err)

	resp, body := p.post("/connections/"+theirs[0].ID+"/revoke", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textNoToken)
	_, err = p.tokens.Verify(ctx, theirToken)
	require.NoError(t, err, "a token of another tenant is not revoked")

	resp, _ = p.post("/connections/"+list[0].ID+"/revoke", nil)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/connections", resp.Header.Get("Location"))
	_, err = p.tokens.Verify(ctx, token)
	require.ErrorIs(t, err, tokens.ErrInvalidToken)

	_, body = p.get("/connections")
	require.Contains(t, visibleText(t, body), "Revoked")
	require.NotContains(t, body, "/connections/"+list[0].ID+"/revoke", "a revoked token has no revoke button")
}
