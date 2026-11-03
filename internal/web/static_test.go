package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/url"
	pathpkg "path"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// bulmaSHA256 is the SHA-256 of css/bulma.min.css in the npm package
// bulma@1.0.4. The package matched the integrity value of the npm registry,
// sha512-Ffb6YGXDiZYX3cqvSbHWqQ8+LkX6tVoTcZuVB3lm93sbAVXlO0D6QlOTMnV6g18gILpAXqkG2z9hf9z4hCjz2g==.
// An update of Bulma changes the file and this value together.
const bulmaSHA256 = "67fa26df1ca9e95d8f2adc7c04fa1b15fa3d24257470ebc10cc68b9aab914bee"

// stylesheets are the files that every page loads.
var stylesheets = []string{"bulma.min.css", "app.css"}

// static/bulma.min.css is the released file, not changed by hand.
func TestBulmaIsTheReleasedFile(t *testing.T) {
	body, err := staticFS.ReadFile("static/bulma.min.css")
	require.NoError(t, err)
	sum := sha256.Sum256(body)
	require.Equal(t, bulmaSHA256, hex.EncodeToString(sum[:]))
}

// A stylesheet needs no session, so the page for a browser without one has
// its style too. Its address has the hash of its content, so a browser may
// keep it.
func TestStylesheetsNeedNoSession(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	for _, name := range stylesheets {
		want, err := staticFS.ReadFile("static/" + name)
		require.NoError(t, err)
		path, err := s.static.path(name)
		require.NoError(t, err)
		require.Regexp(t, `^/static/[0-9a-f]{16}/`+regexp.QuoteMeta(name)+`$`, path)

		resp, body := do(t, newBrowser(t), newRequest(t, http.MethodGet, base+path, nil))
		require.Equal(t, http.StatusOK, resp.StatusCode, name)
		require.Equal(t, "text/css; charset=utf-8", resp.Header.Get("Content-Type"), name)
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

// Every page, also the page for a browser without a session, loads both
// stylesheets.
func TestPagesLoadTheStylesheets(t *testing.T) {
	p := newPageFixture(t)
	_, signedIn := p.get("/")
	resp, noSession := do(t, newBrowser(t), newRequest(t, http.MethodGet, p.base+"/", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	for page, body := range map[string]string{"accounts": signedIn, "no session": noSession} {
		for _, name := range stylesheets {
			path, err := p.s.static.path(name)
			require.NoError(t, err)
			require.Contains(t, body, `<link rel="stylesheet" href="`+path+`">`, page)
		}
	}
}

// The Content-Security-Policy runs no inline style and no script, so a
// template must not have them: the browser would drop them without an error.
func TestTemplatesHaveNoInlineStyleOrScript(t *testing.T) {
	files, err := fs.Glob(templateFS, "templates/*.html")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	inline := regexp.MustCompile(`(?i)<style|<script|\sstyle=|\son[a-z]+=|javascript:`)
	for _, file := range files {
		body, err := templateFS.ReadFile(file)
		require.NoError(t, err)
		require.NotRegexp(t, inline, string(body), file)
	}
}

// The menu marks the link of the part of the site that the page belongs to,
// and only that link.
func TestMenuMarksTheCurrentPart(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	send := p.fallbackSend(p.tenant, a, mail.Compose{To: []string{"bob@example.com"}, Subject: "Hi", Body: "Hello"})
	current := regexp.MustCompile(`<a class="navbar-item is-tab is-active" href="([^"]*)" aria-current="page">`)
	for path, want := range map[string]string{
		"/":        "/",
		"/connect": "/",
		"/accounts/" + string(a.ID) + "/settings": "/",
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
