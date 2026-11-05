package web

import (
	"io/fs"
	"net/http"
	"net/url"
	pathpkg "path"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// assets are the static files of the pages, with their Content-Type.
var assets = map[string]string{
	"app.css":  "text/css; charset=utf-8",
	"app.js":   "text/javascript; charset=utf-8",
	"icon.svg": "image/svg+xml",
}

// A static file needs no session, so the page for a browser without one has
// its style too. Its address has the hash of its content, so a browser may
// keep it.
func TestStaticFilesNeedNoSession(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	for name, contentType := range assets {
		want, err := staticFS.ReadFile("static/" + name)
		require.NoError(t, err)
		path, err := s.static.path(name)
		require.NoError(t, err)
		require.Regexp(t, `^/static/[0-9a-f]{16}/`+regexp.QuoteMeta(name)+`$`, path)

		resp, body := do(t, newBrowser(t), newRequest(t, http.MethodGet, base+path, nil))
		require.Equal(t, http.StatusOK, resp.StatusCode, name)
		require.Equal(t, contentType, resp.Header.Get("Content-Type"), name)
		require.Equal(t, staticCacheControl, resp.Header.Get("Cache-Control"), name)
		require.Equal(t, []string{wantCSP}, resp.Header.Values("Content-Security-Policy"), name)
		require.Equal(t, []string{"nosniff"}, resp.Header.Values("X-Content-Type-Options"), name)
		require.Equal(t, []string{"no-referrer"}, resp.Header.Values("Referrer-Policy"), name)
		require.Equal(t, string(want), body, name)

		resp, body = do(t, newBrowser(t), newRequest(t, http.MethodHead, base+path, nil))
		require.Equal(t, http.StatusOK, resp.StatusCode, name)
		require.Empty(t, body, name)
	}
	_, err := s.static.path("nothing.css")
	require.Error(t, err)
}

// Only the exact address of a file is served, and only to GET and HEAD. Any
// other address under /static/ is not found, and that answer is not kept.
func TestStaticServesOnlyTheExactAddresses(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	path, err := s.static.path("app.css")
	require.NoError(t, err)
	dir, name := pathpkg.Split(path)
	for _, p := range []string{
		"/static/",
		"/static/" + name,
		"/static/0000000000000000/" + name,
		dir,
		dir + "nothing.css",
		dir + "../" + name,
		path + "/",
		path + "x",
	} {
		resp, _ := do(t, newBrowser(t), newRequest(t, http.MethodGet, base+p, nil))
		require.Equal(t, http.StatusNotFound, resp.StatusCode, p)
		require.Equal(t, "no-store", resp.Header.Get("Cache-Control"), p)
	}

	browser := newBrowser(t)
	f.signIn(s, browser)
	token := f.csrfFrom(browser, base)
	resp, _ := do(t, browser, sameOrigin(newRequest(t, http.MethodPost, base+path,
		url.Values{csrfField: {token}}), base))
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	require.Equal(t, "GET, HEAD", resp.Header.Get("Allow"))

	badHost := newRequest(t, http.MethodGet, base+path, nil)
	badHost.Host = "evil.example"
	resp, _ = do(t, newBrowser(t), badHost)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "the Host check runs first")
}

// Every page, also the page for a browser without a session, loads the
// stylesheet, the script and the icon.
func TestPagesLoadTheStaticFiles(t *testing.T) {
	p := newPageFixture(t)
	_, signedIn := p.get("/accounts")
	resp, noSession := do(t, newBrowser(t), newRequest(t, http.MethodGet, p.base+"/", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	path := func(name string) string {
		address, err := p.s.static.path(name)
		require.NoError(t, err)
		return address
	}
	for page, body := range map[string]string{"accounts": signedIn, "no session": noSession} {
		require.Contains(t, body, `<link rel="stylesheet" href="`+path("app.css")+`">`, page)
		require.Contains(t, body, `<script src="`+path("app.js")+`" defer></script>`, page)
		require.Contains(t, body, `<link rel="icon" href="`+path("icon.svg")+`" type="image/svg+xml">`, page)
	}
}

// Every form works without the script: the script never posts anything by
// itself, so it has no address and no request of its own. It also never moves
// or reloads the page: a reload could post a form again.
func TestScriptOnlyShowsThatAFormWorks(t *testing.T) {
	body, err := staticFS.ReadFile("static/app.js")
	require.NoError(t, err)
	require.NotRegexp(t, regexp.MustCompile(`fetch\(|XMLHttpRequest|sendBeacon|WebSocket|\.submit\(\)|\.click\(\)|innerHTML|eval\(|location\.`), string(body))
}

// The Content-Security-Policy runs no inline style and no inline script, so a
// template must not have them: the browser would drop them without an error.
// The only script tag is the one that loads the file of the binary.
func TestTemplatesHaveNoInlineStyleOrScript(t *testing.T) {
	files, err := fs.Glob(templateFS, "templates/*.html")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	inline := regexp.MustCompile(`(?i)<style|\sstyle=|\son[a-z]+=|javascript:`)
	script := regexp.MustCompile(`(?i)<script[^>]*>`)
	for _, file := range files {
		body, err := templateFS.ReadFile(file)
		require.NoError(t, err)
		require.NotRegexp(t, inline, string(body), file)
		for _, tag := range script.FindAllString(string(body), -1) {
			require.Equal(t, `<script src="{{static "app.js"}}" defer>`, tag, file)
		}
	}
}

// The menu marks the link of the part of the site that the page belongs to,
// and only that link.
func TestMenuMarksTheCurrentPart(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	send := p.fallbackSend(p.tenant, a, mail.Compose{To: []string{"bob@example.com"}, Subject: "Hi", Body: "Hello"})
	current := regexp.MustCompile(`<a class="menu-link" href="([^"]*)" aria-current="page">`)
	for path, want := range map[string]string{
		"/accounts": "/accounts",
		"/connect":  "/accounts",
		"/accounts/" + string(a.ID) + "/settings":  "/accounts",
		"/accounts/" + string(a.ID) + "/reconnect": "/accounts",
		"/approvals":         "/approvals",
		"/approvals/" + send: "/approvals",
		"/activity":          "/activity",
		"/connections":       "/connections",
	} {
		resp, body := p.get(path)
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		m := current.FindAllStringSubmatch(body, -1)
		require.Len(t, m, 1, path)
		require.Equal(t, want, m[0][1], path)
	}
}

// Every page with the menu belongs to a section, so the menu marks one of its
// links. Only the message page and the page without a session have none.
func TestEveryMenuPageHasASection(t *testing.T) {
	static, err := loadStatic()
	require.NoError(t, err)
	_, pages, err := parsePages(static)
	require.NoError(t, err)
	for name := range pages {
		if name == pageMessage || name == pageNoSession {
			continue
		}
		require.Contains(t, sectionOf, name, "page %s", name)
	}
	for name := range sectionOf {
		require.Contains(t, pages, name, "sectionOf names a page that does not exist")
	}
}
