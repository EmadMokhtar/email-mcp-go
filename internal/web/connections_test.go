package web

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
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

// A token from the form may not confirm in band unless the user ticks the box. It
// has the default end date, and the list says all of it.
func TestTokenFormInBandChoice(t *testing.T) {
	p := newPageFixture(t)
	ctx := context.Background()

	resp, body := p.get("/connections")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `name="in_band_confirm"`)
	require.NotContains(t, body, `value="1" checked`, "the box is off by default")

	resp, _ = p.post("/connections", url.Values{"name": {"plain"}, "scope": {"email:send"}})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = p.post("/connections", url.Values{"name": {"form"}, "scope": {"email:read"}, "in_band_confirm": {"1"}})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	list, err := p.tokens.List(ctx, p.tenant)
	require.NoError(t, err)
	require.Len(t, list, 2)
	byName := map[string]tokens.Info{list[0].Name: list[0], list[1].Name: list[1]}
	require.False(t, byName["plain"].InBandConfirm)
	require.True(t, byName["form"].InBandConfirm)
	require.False(t, byName["plain"].ExpiresAt.IsZero())

	_, body = p.get("/connections")
	text := visibleText(t, body)
	require.Contains(t, text, "4 (Send), includes read, organize, draft")
	require.Contains(t, text, byName["plain"].ID)
	require.Contains(t, text, formatTime(byName["plain"].ExpiresAt))
	require.Contains(t, text, "Never")

	_, body = p.post("/connections", url.Values{"name": {""}, "scope": {"email:read"}, "in_band_confirm": {"1"}})
	require.Contains(t, body, `value="1" checked`, "a refused form keeps the choice")

	entries := p.entries()
	require.Len(t, entries, 2)
	require.Equal(t, "token_created_in_band", entries[0].Detail)
	require.Equal(t, "token_created", entries[1].Detail)
}

// An expired token says so and has no revoke button. A token that has no end date
// says that too.
func TestConnectionsShowsTheEndOfAToken(t *testing.T) {
	p := newPageFixture(t)
	ctx := context.Background()
	_, err := p.tokens.Create(ctx, p.tenant, "short", policy.Scopes{"email:read"}, service.TokenOptions{ExpiresIn: time.Minute})
	require.NoError(t, err)
	list, err := p.tokens.List(ctx, p.tenant)
	require.NoError(t, err)
	require.NoError(t, p.store.CreateAPIToken(ctx, &store.APITokenRow{
		ID: "old", Tenant: p.tenant, Name: "old", Hash: bytes.Repeat([]byte{1}, 32),
		Scopes: []string{"email:read"}, CreatedAt: p.clock.Now(),
	}))

	_, body := p.get("/connections")
	require.Contains(t, body, "/connections/"+list[0].ID+"/revoke", "the token is still valid")
	text := visibleText(t, body)
	require.Contains(t, text, "No end date")
	require.Contains(t, text, "A new token stops working after 90 days.")
	require.NotContains(t, text, "A token stops working", "an old token has no end date")
	require.Contains(t, text, "A token with no end date works until you revoke it.")

	p.clock.Set(p.clock.Now().Add(time.Minute))
	_, body = p.get("/connections")
	require.Contains(t, visibleText(t, body), "Expired")
	require.NotContains(t, body, "/connections/"+list[0].ID+"/revoke")
}

func TestRevokeToken(t *testing.T) {
	p := newPageFixture(t)
	ctx := context.Background()
	token, err := p.tokens.Create(ctx, p.tenant, "laptop", policy.Scopes{"email:read"}, service.TokenOptions{})
	require.NoError(t, err)
	list, err := p.tokens.List(ctx, p.tenant)
	require.NoError(t, err)
	require.Len(t, list, 1)
	p.addTenant("other")
	theirToken, err := p.tokens.Create(ctx, "other", "theirs", policy.Scopes{"email:read"}, service.TokenOptions{})
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

// Making and revoking a token are in the activity log, as entries of the
// tenant with a fixed code. The entries never hold the token or its name.
func TestTokenChangesAreAudited(t *testing.T) {
	p := newPageFixture(t)

	_, body := p.post("/connections", url.Values{"name": {"my-laptop"}, "scope": {"email:read"}})
	token := tokenPattern.FindString(body)
	require.NotEmpty(t, token)
	list, err := p.tokens.List(context.Background(), p.tenant)
	require.NoError(t, err)
	resp, _ := p.post("/connections/"+list[0].ID+"/revoke", nil)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	entries := p.entries()
	require.Len(t, entries, 2)
	require.Equal(t, "revoke_token", entries[0].Tool)
	require.Equal(t, "token_revoked", entries[0].Detail)
	require.Equal(t, "create_token", entries[1].Tool)
	require.Equal(t, "token_created", entries[1].Detail)
	for _, e := range entries {
		require.Empty(t, e.Account)
		require.Equal(t, "web", e.Client)
		for _, field := range append([]string{e.Tool, e.Detail, e.Client}, e.Recipients...) {
			require.NotContains(t, field, token)
			require.NotContains(t, field, "my-laptop")
			require.NotContains(t, field, list[0].ID)
		}
	}

	// A refused token writes no entry.
	resp, _ = p.post("/connections", url.Values{"name": {"x"}})
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Len(t, p.entries(), 2)

	// The activity page offers both as a filter.
	_, body = p.get("/activity?tool=create_token")
	require.Equal(t, 1, logRows(t, body))
	require.Contains(t, body, `value="revoke_token"`)
}
