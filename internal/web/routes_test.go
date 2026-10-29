package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/tokens"
)

// state is what a request could change: the accounts, the actions, the
// tokens, the log and the sent messages.
type state struct {
	Accounts []*account.Account
	Pending  []*confirm.Pending
	Tokens   []tokens.Info
	Entries  []audit.Entry
	Sent     int
	Forgot   []account.AccountID
}

func (p *pageFixture) state(ids ...string) state {
	p.t.Helper()
	ctx := context.Background()
	accounts, err := p.store.ListAccounts(ctx, p.tenant)
	require.NoError(p.t, err)
	var pending []*confirm.Pending
	for _, id := range ids {
		pending = append(pending, p.pending(id))
	}
	list, err := p.tokens.List(ctx, p.tenant)
	require.NoError(p.t, err)
	return state{
		Accounts: accounts, Pending: pending, Tokens: list, Entries: p.entries(),
		Sent: len(p.sender.sent()), Forgot: p.backends.forgot(),
	}
}

// routeFixture is a page fixture with one item of each kind, and the path of
// each route with the ids of those items.
type routeFixture struct {
	*pageFixture
	account *account.Account
	send    string
	tokenID string
}

func newRouteFixture(t *testing.T) *routeFixture {
	t.Helper()
	p := newPageFixture(t)
	a := p.account()
	send := p.fallbackSend(p.tenant, a, mail.Compose{To: []string{"bob@example.com"}, Subject: "Hi", Body: "Hello"})
	_, err := p.tokens.Create(context.Background(), p.tenant, "laptop", policy.Scopes{"email:read"})
	require.NoError(t, err)
	list, err := p.tokens.List(context.Background(), p.tenant)
	require.NoError(t, err)
	return &routeFixture{pageFixture: p, account: a, send: send, tokenID: list[0].ID}
}

// path returns the path of a route pattern, with the ids of the fixture.
func (r *routeFixture) path(pattern string) string {
	_, path, ok := strings.Cut(pattern, " ")
	require.True(r.t, ok, pattern)
	path = strings.TrimSuffix(path, "{$}")
	switch {
	case strings.HasPrefix(path, "/accounts/"):
		path = strings.Replace(path, "{id}", string(r.account.ID), 1)
	case strings.HasPrefix(path, "/approvals/"):
		path = strings.Replace(path, "{id}", r.send, 1)
	case strings.HasPrefix(path, "/connections/"):
		path = strings.Replace(path, "{id}", r.tokenID, 1)
	}
	require.NotContains(r.t, path, "{", pattern)
	return path
}

// fullForm is a form that each POST route would act on: the right fields, the
// boxes ticked.
func fullForm() url.Values {
	form := settingsForm("confirm_sends", "", "confirm_deletes", "", "accept_autonomy_risk", "1", "mail_level", "5")
	form.Set("confirm_remove", "1")
	form.Set("name", "new token")
	form.Set("scope", "all")
	form.Set("email", "alice@example.org")
	return form
}

// Every route names its method: GET to read, POST to change.
func TestEveryRouteNamesItsMethod(t *testing.T) {
	require.NotEmpty(t, routes)
	posts := 0
	for _, r := range routes {
		method, _, ok := strings.Cut(r.pattern, " ")
		require.True(t, ok, r.pattern)
		require.Contains(t, []string{http.MethodGet, http.MethodPost}, method, r.pattern)
		if method == http.MethodPost {
			posts++
		}
	}
	require.GreaterOrEqual(t, posts, 7)
}

// Each POST route refuses a request without the CSRF token of the session,
// and changes nothing.
func TestEveryPostRouteNeedsTheCSRFToken(t *testing.T) {
	r := newRouteFixture(t)
	other := newBrowser(t)
	r.signIn(r.s, other)
	_, otherBody := do(t, other, newRequest(t, http.MethodGet, r.base+"/connections", nil))
	otherToken := csrfPattern.FindStringSubmatch(otherBody)[1]
	before := r.state(r.send)

	for _, route := range routes {
		if !strings.HasPrefix(route.pattern, "POST ") {
			continue
		}
		path := r.path(route.pattern)
		for name, token := range map[string]string{
			"missing":       "",
			"wrong":         r.csrf + "x",
			"other session": otherToken,
		} {
			form := fullForm()
			if token != "" {
				form.Set(csrfField, token)
			}
			resp, _ := do(t, r.browser, sameOrigin(newRequest(t, http.MethodPost, r.base+path, form), r.base))
			require.Equal(t, http.StatusForbidden, resp.StatusCode, "%s with a %s token", route.pattern, name)
		}
		// The token in the address does not count either.
		form := fullForm()
		resp, _ := do(t, r.browser, sameOrigin(newRequest(t, http.MethodPost,
			r.base+path+"?"+url.Values{csrfField: {r.csrf}}.Encode(), form), r.base))
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "%s with the token in the address", route.pattern)
	}
	require.Equal(t, before, r.state(r.send), "no refused request changed anything")
}

// No GET route changes anything, whatever its address holds. HEAD is answered
// by the GET routes, so it is checked too.
func TestNoGetRouteChangesState(t *testing.T) {
	r := newRouteFixture(t)
	query := "?" + fullForm().Encode() + "&area=mail&level=5&" + url.Values{csrfField: {r.csrf}}.Encode()
	before := r.state(r.send)

	gets := 0
	for _, route := range routes {
		if !strings.HasPrefix(route.pattern, "GET ") {
			continue
		}
		gets++
		path := r.path(route.pattern)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			resp, _ := do(t, r.browser, newRequest(t, method, r.base+path+query, nil))
			require.Less(t, resp.StatusCode, 500, "%s %s", method, path)
		}
	}
	require.GreaterOrEqual(t, gets, 6)
	require.Equal(t, before, r.state(r.send), "no GET or HEAD request changed anything")
}
