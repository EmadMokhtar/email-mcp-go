package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// fallbackHosts are the other names that reach the server. A browser that
// holds a cookie of 127.0.0.1 sends it to every program on that host, so no
// session may start or work there.
func fallbackHosts(port string) []string {
	return []string{"127.0.0.1:" + port, "localhost:" + port}
}

// Only the name of the web interface gives a cookie. It is sent to this name
// alone, not to other programs on 127.0.0.1.
func TestCookieOnlyOnTheDedicatedHost(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	setupURL, err := s.NewSetupURL(context.Background())
	require.NoError(t, err)
	require.Regexp(t, setupURLPattern, setupURL)

	for _, host := range fallbackHosts(portOf(t, base)) {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			req := newRequest(t, method, setupURL, nil)
			req.Host = host
			resp, _ := do(t, newBrowser(t), req)
			require.Equal(t, http.StatusForbidden, resp.StatusCode, host)
			require.Empty(t, resp.Header.Values("Set-Cookie"), "%s sets no cookie", host)
		}
	}

	// The refused requests did not use the one-time token up.
	browser := newBrowser(t)
	resp, _ := do(t, browser, newRequest(t, http.MethodGet, setupURL, nil))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	cookies := resp.Header.Values("Set-Cookie")
	require.Len(t, cookies, 1)
	require.Regexp(t, cookiePattern, cookies[0])
	require.NotContains(t, cookies[0], "Domain", "the cookie goes to this host only")

	u, err := url.Parse(setupURL)
	require.NoError(t, err)
	require.Len(t, browser.Jar.Cookies(u), 1)
	u.Host = "127.0.0.1:" + u.Port()
	require.Empty(t, browser.Jar.Cookies(u), "a browser does not send it to 127.0.0.1")
}

// The page on 127.0.0.1 says what to do, and it holds no secret.
func TestFallbackHostShowsHowToContinue(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	setupURL, err := s.NewSetupURL(context.Background())
	require.NoError(t, err)
	help, err := s.SignInHelpURL(context.Background())
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:"+portOf(t, base)+"/setup", help)

	for _, target := range []string{help, setupURL} {
		req := newRequest(t, http.MethodGet, target, nil)
		req.Host = "127.0.0.1:" + portOf(t, base)
		resp, body := do(t, newBrowser(t), req)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		text := visibleText(t, body)
		require.Contains(t, text, base+"/")
		require.Contains(t, text, "127.0.0.1 email-mcp.localhost", "it shows the line for the hosts file")
		require.Contains(t, text, "/etc/hosts")
		require.NotContains(t, body, tokenOf(t, setupURL), "the page holds no token")
		require.Contains(t, body, `href="`+base+`/"`)
	}
	require.NotContains(t, f.logs.String(), tokenOf(t, setupURL))

	// Static files work, so the page has its style.
	u, err := s.static.path("app.css")
	require.NoError(t, err)
	req := newRequest(t, http.MethodGet, "http://127.0.0.1:"+portOf(t, base)+u, nil)
	resp, _ := do(t, newBrowser(t), req)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// A cookie that arrives on 127.0.0.1 opens nothing, for no method. It is the
// host where a cookie of an older version was set.
func TestSessionRefusedOnFallbackHost(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	browser := newBrowser(t)
	cookie := f.signIn(s, browser)
	token := f.csrfFrom(browser, base)
	f.reached.Store(0)
	f.clock.Set(t0.Add(10 * time.Minute))

	for _, host := range fallbackHosts(portOf(t, base)) {
		get := newRequest(t, http.MethodGet, base+"/test/secret/p1", nil)
		get.Host = host
		get.Header.Set("Cookie", "emcp_session="+cookie)
		resp, body := do(t, newBrowser(t), get)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, host)
		require.NotContains(t, body, secretDetail, host)

		post := newRequest(t, http.MethodPost, base+"/test/submit", url.Values{csrfField: {token}})
		post.Host = host
		post.Header.Set("Cookie", "emcp_session="+cookie)
		post.Header.Set("Origin", "http://"+host)
		post.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, _ = do(t, newBrowser(t), post)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, host)
	}
	require.Zero(t, f.reached.Load(), "no request reached a page")

	row, err := f.store.WebSessionByHash(context.Background(), hashOf(cookie))
	require.NoError(t, err)
	require.Equal(t, t0, row.LastSeenAt, "a refused request does not keep the session alive")
}

// A session ends when it is not used for two hours, and each use moves that
// limit. It never passes the limit of 24 hours (see TestSessionExpires).
func TestIdleSessionRefused(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	browser := newBrowser(t)
	cookie := f.signIn(s, browser)
	target := base + "/test/secret/p1"

	f.clock.Set(t0.Add(2*time.Hour - time.Millisecond))
	resp, _ := do(t, browser, newRequest(t, http.MethodGet, target, nil))
	require.Equal(t, http.StatusOK, resp.StatusCode, "the use moves the limit")

	f.clock.Set(t0.Add(4*time.Hour - 2*time.Millisecond))
	resp, _ = do(t, browser, newRequest(t, http.MethodGet, target, nil))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	f.clock.Set(t0.Add(6*time.Hour - 2*time.Millisecond))
	resp, body := do(t, browser, newRequest(t, http.MethodGet, target, nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "two hours without a use end the session")
	require.Equal(t, instruction, visibleText(t, body))

	_, err := f.store.WebSessionByHash(context.Background(), hashOf(cookie))
	require.ErrorIs(t, err, store.ErrNotFound, "the idle session was deleted")
}

// The time of the last use is written at most once a minute, and never for a
// static file, so a page load does not write to the database many times.
func TestLastSeenIsWrittenAtMostOnceAMinute(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	browser := newBrowser(t)
	hash := hashOf(f.signIn(s, browser))
	seen := func() time.Time {
		row, err := f.store.WebSessionByHash(context.Background(), hash)
		require.NoError(t, err)
		return row.LastSeenAt
	}

	f.clock.Set(t0.Add(time.Minute - time.Millisecond))
	do(t, browser, newRequest(t, http.MethodGet, base+"/test/secret/p1", nil))
	require.Equal(t, t0, seen(), "less than a minute later, nothing is written")

	f.clock.Set(t0.Add(time.Minute))
	css, err := s.static.path("app.css")
	require.NoError(t, err)
	do(t, browser, newRequest(t, http.MethodGet, base+css, nil))
	require.Equal(t, t0, seen(), "a static file is not a use of the session")

	do(t, browser, newRequest(t, http.MethodGet, base+"/test/secret/p1", nil))
	require.Equal(t, t0.Add(time.Minute), seen())
}

func TestSignOutDeletesTheSession(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	browser := newBrowser(t)
	cookie := f.signIn(s, browser)
	other := newBrowser(t)
	f.signIn(s, other)
	token := f.csrfFrom(browser, base)

	resp, _ := do(t, browser, sameOrigin(newRequest(t, http.MethodPost, base+"/signout",
		url.Values{csrfField: {token}}), base))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/", resp.Header.Get("Location"))
	cookies := resp.Header.Values("Set-Cookie")
	require.Len(t, cookies, 1)
	require.Regexp(t, regexp.MustCompile(`^emcp_session=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax$`), cookies[0])

	_, err := f.store.WebSessionByHash(context.Background(), hashOf(cookie))
	require.ErrorIs(t, err, store.ErrNotFound, "the row is gone")

	// The cookie does not work any more, also when a program kept a copy.
	req := newRequest(t, http.MethodGet, base+"/test/secret/p1", nil)
	req.Header.Set("Cookie", "emcp_session="+cookie)
	resp, _ = do(t, newBrowser(t), req)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	resp, _ = do(t, other, newRequest(t, http.MethodGet, base+"/test/secret/p1", nil))
	require.Equal(t, http.StatusOK, resp.StatusCode, "the other browser stays signed in")
}

// A GET does not sign out, so a link in a mail or a page of another program
// cannot do it.
func TestSignOutNeedsAPost(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	browser := newBrowser(t)
	cookie := f.signIn(s, browser)

	resp, _ := do(t, browser, newRequest(t, http.MethodGet, base+"/signout", nil))
	require.NotEqual(t, http.StatusSeeOther, resp.StatusCode)
	_, err := f.store.WebSessionByHash(context.Background(), hashOf(cookie))
	require.NoError(t, err)
}

// Every page with the menu has the sign-out button, with the form token.
func TestEveryPageHasTheSignOutButton(t *testing.T) {
	p := newPageFixture(t)
	for _, path := range []string{"/accounts", "/approvals", "/activity", "/connections", "/connect"} {
		resp, body := p.get(path)
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		require.Contains(t, body, `action="/signout"`, path)
		form := body[strings.Index(body, `action="/signout"`):]
		require.Contains(t, form[:strings.Index(form, "</form>")], `name="csrf_token"`, path)
	}
}

func TestRevokeRevokesAllSessions(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	first := newBrowser(t)
	f.signIn(s, first)
	second := newBrowser(t)
	f.signIn(s, second)

	n, err := s.RevokeSessions(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, n)

	for _, browser := range []*http.Client{first, second} {
		resp, _ := do(t, browser, newRequest(t, http.MethodGet, base+"/test/secret/p1", nil))
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}
}

// sessionIDs returns the numbers of the sessions on the connections page.
func sessionIDs(body string) []string {
	var ids []string
	for _, m := range regexp.MustCompile(`action="/sessions/([0-9]+)/revoke"`).FindAllStringSubmatch(body, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// The connections page lists the sessions of the tenant, marks this browser
// and never shows a hash or a cookie. The button signs one browser out.
func TestConnectionsPageListsAndRevokesSessions(t *testing.T) {
	p := newPageFixture(t)
	_, err := p.tokens.Create(context.Background(), p.tenant, "laptop", policy.Scopes{"email:read"})
	require.NoError(t, err)
	other := newBrowser(t)
	otherCookie := p.signIn(p.s, other)

	resp, body := p.get("/connections")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	ids := sessionIDs(body)
	require.Len(t, ids, 2)
	require.Contains(t, visibleText(t, body), "This browser")
	require.NotContains(t, body, otherCookie)
	require.NotContains(t, body, string(hashOf(otherCookie)))

	row, err := p.store.WebSessionByHash(context.Background(), hashOf(otherCookie))
	require.NoError(t, err)
	id := strconv.FormatInt(row.ID, 10)
	require.Contains(t, ids, id)

	resp, _ = p.post("/sessions/"+id+"/revoke", url.Values{})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/connections", resp.Header.Get("Location"))
	require.Empty(t, resp.Header.Values("Set-Cookie"), "this browser stays signed in")
	_, err = p.store.WebSessionByHash(context.Background(), hashOf(otherCookie))
	require.ErrorIs(t, err, store.ErrNotFound)
	resp, _ = do(t, other, newRequest(t, http.MethodGet, p.base+"/connections", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	for _, bad := range []string{id, "999999", "abc"} {
		resp, _ = p.post("/sessions/"+bad+"/revoke", url.Values{})
		require.Equal(t, http.StatusNotFound, resp.StatusCode, bad)
	}
}

// Revoking the session of this browser is the same as signing out.
func TestRevokingThisBrowserClearsTheCookie(t *testing.T) {
	p := newPageFixture(t)
	_, body := p.get("/connections")
	ids := sessionIDs(body)
	require.Len(t, ids, 1)

	resp, _ := p.post("/sessions/"+ids[0]+"/revoke", url.Values{})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/", resp.Header.Get("Location"))
	require.Len(t, resp.Header.Values("Set-Cookie"), 1)
	require.Contains(t, resp.Header.Get("Set-Cookie"), "Max-Age=0")
	resp, _ = p.get("/connections")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// A session of another tenant cannot be listed or revoked.
func TestSessionsOfAnotherTenantAreHidden(t *testing.T) {
	p := newPageFixture(t)
	ctx := context.Background()
	p.addTenant("other")
	require.NoError(t, p.store.CreateWebSetupToken(ctx, "other", hashOf("t"), t0, t0.Add(time.Minute)))
	_, err := p.store.UseWebSetupToken(ctx, hashOf("t"), hashOf("s"), t0, t0.Add(time.Hour))
	require.NoError(t, err)
	row, err := p.store.WebSessionByHash(ctx, hashOf("s"))
	require.NoError(t, err)

	_, body := p.get("/connections")
	require.Len(t, sessionIDs(body), 1)
	resp, _ := p.post("/sessions/"+strconv.FormatInt(row.ID, 10)+"/revoke", url.Values{})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	_, err = p.store.WebSessionByHash(ctx, hashOf("s"))
	require.NoError(t, err)
}
