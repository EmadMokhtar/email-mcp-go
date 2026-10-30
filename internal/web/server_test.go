package web

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// instruction is the only text that a browser without a session sees (spec
// section 8.2), as the page shows it.
const instruction = "Run email-mcp setup in a terminal, then open this link again."

// secretDetail stands for the content of a page that needs a session, such as
// the preview of an approval.
const secretDetail = "SECRET-APPROVAL-PREVIEW"

// wantCSP is the Content-Security-Policy of every answer.
const wantCSP = "default-src 'self'; script-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// t0 is the start time of the test clock.
var t0 = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

// testKey is the signing key of the tests.
var testKey = bytes.Repeat([]byte{7}, 32)

var (
	baseURLPattern  = regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+$`)
	setupURLPattern = regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+/setup\?token=[A-Za-z0-9_-]{43}$`)
	// cookiePattern is the whole Set-Cookie header of a new session: host-only
	// (no Domain), not Secure, 30 days.
	cookiePattern = regexp.MustCompile(`^emcp_session=[A-Za-z0-9_-]{43}; Path=/; Max-Age=2592000; HttpOnly; SameSite=Strict$`)
	csrfPattern   = regexp.MustCompile(`<input type="hidden" name="csrf_token" value="([A-Za-z0-9_.-]+)">`)
)

// clock is a test clock that the server goroutines may read.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// syncBuffer is a log buffer that the server goroutines may write.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// fixture is a real store in a temporary directory, a signer, a clock and a
// log, for one or more servers.
type fixture struct {
	t      *testing.T
	dir    string
	store  *store.Store
	signer *signing.Signer
	clock  *clock
	logs   *syncBuffer
	// reached counts the requests that reached a test page.
	reached atomic.Int32
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, err = st.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	return &fixture{
		t:      t,
		dir:    dir,
		store:  st,
		signer: signing.New(testKey),
		clock:  &clock{now: t0},
		logs:   &syncBuffer{},
	}
}

// newServer makes a server over the store of the fixture, adds the test pages,
// and shuts the server down at the end of the test. It does not start it.
func (f *fixture) newServer() *Server {
	f.t.Helper()
	s := New(Deps{
		Store:  f.store,
		Signer: f.signer,
		Now:    f.clock.Now,
		Logger: slog.New(slog.NewTextHandler(f.logs, nil)),
	})
	f.addTestPages(s)
	f.t.Cleanup(func() { require.NoError(f.t, s.Shutdown(context.Background())) })
	return s
}

// start makes a server and starts it, and returns it with its base URL.
func (f *fixture) start() (*Server, string) {
	f.t.Helper()
	s := f.newServer()
	base, err := s.EnsureStarted(context.Background())
	require.NoError(f.t, err)
	return s, base
}

// addTestPages adds pages that stand for the pages of later tasks: a page with
// secret content, a page with a form, and two form targets.
func (f *fixture) addTestPages(s *Server) {
	form := template.Must(template.Must(s.layout.Clone()).Parse(
		`{{define "content"}}<form method="post" action="/test/submit">` +
			`{{template "csrf_field" .CSRFToken}}<button>Go</button></form>{{end}}`))
	s.pages["test_form"] = form
	s.mux.HandleFunc("GET /test/form", func(w http.ResponseWriter, r *http.Request) {
		f.reached.Add(1)
		s.render(w, r, http.StatusOK, "test_form", nil)
	})
	s.mux.HandleFunc("POST /test/submit", func(w http.ResponseWriter, _ *http.Request) {
		f.reached.Add(1)
		_, _ = io.WriteString(w, "accepted")
	})
	s.mux.HandleFunc("GET /test/secret/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.reached.Add(1)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "approval %s: %s", r.PathValue("id"), secretDetail) //nolint:gosec // G705: a plain-text test page that shows which id arrived
	})
	s.mux.HandleFunc("POST /test/secret/{id}/approve", func(w http.ResponseWriter, _ *http.Request) {
		f.reached.Add(1)
		_, _ = io.WriteString(w, "approved")
	})
}

// signIn opens a new setup link in the browser, as "email-mcp setup" does, and
// returns the session cookie value.
func (f *fixture) signIn(s *Server, browser *http.Client) string {
	f.t.Helper()
	setupURL, err := s.NewSetupURL(context.Background())
	require.NoError(f.t, err)
	resp, _ := do(f.t, browser, newRequest(f.t, http.MethodGet, setupURL, nil))
	require.Equal(f.t, http.StatusSeeOther, resp.StatusCode)
	u, err := url.Parse(setupURL)
	require.NoError(f.t, err)
	for _, c := range browser.Jar.Cookies(u) {
		if c.Name == "emcp_session" {
			return c.Value
		}
	}
	f.t.Fatal("the browser got no session cookie")
	return ""
}

// csrfFrom loads the form page and returns the CSRF token in its form.
func (f *fixture) csrfFrom(browser *http.Client, base string) string {
	f.t.Helper()
	resp, body := do(f.t, browser, newRequest(f.t, http.MethodGet, base+"/test/form", nil))
	require.Equal(f.t, http.StatusOK, resp.StatusCode)
	m := csrfPattern.FindStringSubmatch(body)
	require.NotNil(f.t, m, "the form has a hidden CSRF field")
	return m[1]
}

// dataFiles returns the bytes of every file in the data directory: the
// database and its write-ahead log.
func (f *fixture) dataFiles() []byte {
	f.t.Helper()
	entries, err := os.ReadDir(f.dir)
	require.NoError(f.t, err)
	var all []byte
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(f.dir, e.Name()))
		require.NoError(f.t, err)
		all = append(all, data...)
	}
	return all
}

// newBrowser returns a client that keeps cookies like a browser and does not
// follow redirects.
func newBrowser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{
		Jar:       jar,
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// newRequest makes a request. A form body is sent url-encoded.
func newRequest(t *testing.T, method, target string, form url.Values) *http.Request {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	require.NoError(t, err)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return req
}

// sameOrigin adds the headers that a browser sends with a form post from a page
// of the same origin.
func sameOrigin(req *http.Request, base string) *http.Request {
	req.Header.Set("Origin", base)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return req
}

// do sends the request and returns the response with its whole body.
func do(t *testing.T, c *http.Client, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

// visibleText returns the text of the body of an HTML page, with each run of
// white space made one space.
func visibleText(t *testing.T, page string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	var body *html.Node
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.DataAtom == atom.Body {
			body = n
			break
		}
	}
	require.NotNil(t, body)
	var words []string
	for n := range body.Descendants() {
		if n.Type == html.TextNode {
			words = append(words, strings.Fields(n.Data)...)
		}
	}
	return strings.Join(words, " ")
}

// tokenOf returns the setup token in a setup URL.
func tokenOf(t *testing.T, setupURL string) string {
	t.Helper()
	u, err := url.Parse(setupURL)
	require.NoError(t, err)
	return u.Query().Get("token")
}

// hashOf returns the SHA-256 of a text, as the store keeps it.
func hashOf(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	return sum[:]
}

func TestSetupTokenSingleUse(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	ctx := context.Background()

	setupURL, err := s.NewSetupURL(ctx)
	require.NoError(t, err)
	require.Regexp(t, setupURLPattern, setupURL)
	require.True(t, strings.HasPrefix(setupURL, base+"/setup?"))

	first := newBrowser(t)
	resp, _ := do(t, first, newRequest(t, http.MethodGet, setupURL, nil))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/", resp.Header.Get("Location"), "the redirect takes the token out of the address bar")
	cookies := resp.Header.Values("Set-Cookie")
	require.Len(t, cookies, 1)
	require.Regexp(t, cookiePattern, cookies[0])

	// The same link does not work a second time.
	second := newBrowser(t)
	resp, body := do(t, second, newRequest(t, http.MethodGet, setupURL, nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Empty(t, resp.Header.Values("Set-Cookie"))
	require.Equal(t, instruction, visibleText(t, body))

	// The first browser is signed in, the second is not.
	resp, body = do(t, first, newRequest(t, http.MethodGet, base+"/test/secret/p1", nil))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, secretDetail)
	resp, _ = do(t, second, newRequest(t, http.MethodGet, base+"/test/secret/p1", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Of many browsers that open one link at the same moment, one signs in.
	setupURL, err = s.NewSetupURL(ctx)
	require.NoError(t, err)
	const browsers = 8
	statuses := make([]int, browsers)
	var wg sync.WaitGroup
	for i := range browsers {
		b := newBrowser(t)
		wg.Go(func() {
			resp, err := b.Get(setupURL)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			statuses[i] = resp.StatusCode
		})
	}
	wg.Wait()
	var signedIn, refused int
	for _, status := range statuses {
		switch status {
		case http.StatusSeeOther:
			signedIn++
		case http.StatusUnauthorized:
			refused++
		}
	}
	require.Equal(t, 1, signedIn, "statuses %v", statuses)
	require.Equal(t, browsers-1, refused, "statuses %v", statuses)
}

func TestSetupTokenExpires(t *testing.T) {
	f := newFixture(t)
	s, _ := f.start()
	ctx := context.Background()

	early, err := s.NewSetupURL(ctx)
	require.NoError(t, err)
	late, err := s.NewSetupURL(ctx)
	require.NoError(t, err)

	f.clock.Set(t0.Add(10*time.Minute - time.Millisecond))
	resp, _ := do(t, newBrowser(t), newRequest(t, http.MethodGet, early, nil))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "the token works for 10 minutes")

	f.clock.Set(t0.Add(10 * time.Minute))
	resp, body := do(t, newBrowser(t), newRequest(t, http.MethodGet, late, nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the token does not work after 10 minutes")
	require.Empty(t, resp.Header.Values("Set-Cookie"))
	require.Equal(t, instruction, visibleText(t, body))
}

func TestSetupRefusalsLookTheSame(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	ctx := context.Background()

	used, err := s.NewSetupURL(ctx)
	require.NoError(t, err)
	f.signInWith(used)
	expired, err := s.NewSetupURL(ctx)
	require.NoError(t, err)
	f.clock.Set(t0.Add(time.Minute))
	// A link made one minute later is still valid when the other one expires.
	stillValid, err := s.NewSetupURL(ctx)
	require.NoError(t, err)
	f.clock.Set(t0.Add(10 * time.Minute))

	cases := map[string]string{
		"used":            used,
		"expired":         expired,
		"unknown":         base + "/setup?token=" + randomText(),
		"malformed":       base + "/setup?token=abc",
		"empty":           base + "/setup?token=",
		"missing":         base + "/setup",
		"two tokens":      stillValid + "&token=" + tokenOf(t, stillValid),
		"first of two":    base + "/setup?token=" + tokenOf(t, stillValid) + "&token=" + randomText(),
		"in the fragment": base + "/setup#token=" + tokenOf(t, stillValid),
	}
	var first string
	for name, target := range cases {
		resp, body := do(t, newBrowser(t), newRequest(t, http.MethodGet, target, nil))
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, name)
		require.Empty(t, resp.Header.Values("Set-Cookie"), name)
		require.Equal(t, instruction, visibleText(t, body), name)
		if first == "" {
			first = body
		}
		require.Equal(t, first, body, "%s: every refused setup link gets the same page", name)
	}

	// None of the refused requests used up the valid token.
	resp, _ := do(t, newBrowser(t), newRequest(t, http.MethodGet, stillValid, nil))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}

// signInWith opens a setup link in a new browser and checks that it signs in.
func (f *fixture) signInWith(setupURL string) {
	f.t.Helper()
	resp, _ := do(f.t, newBrowser(f.t), newRequest(f.t, http.MethodGet, setupURL, nil))
	require.Equal(f.t, http.StatusSeeOther, resp.StatusCode)
}

func TestNoSessionShowsOnlyInstruction(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	target := base + "/test/secret/pending-7f3a"

	cases := map[string]func(*http.Request){
		"no cookie":        func(*http.Request) {},
		"unknown session":  func(r *http.Request) { r.Header.Set("Cookie", "emcp_session="+randomText()) },
		"malformed cookie": func(r *http.Request) { r.Header.Set("Cookie", "emcp_session=abc") },
		"empty cookie":     func(r *http.Request) { r.Header.Set("Cookie", "emcp_session=") },
		"other cookie":     func(r *http.Request) { r.Header.Set("Cookie", "session="+randomText()) },
	}
	var first string
	for name, change := range cases {
		req := newRequest(t, http.MethodGet, target, nil)
		change(req)
		resp, body := do(t, newBrowser(t), req)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, name)
		require.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"), name)
		require.Equal(t, instruction, visibleText(t, body), "%s: the page has only the instruction", name)
		require.Contains(t, body, "Run <code>email-mcp setup</code> in a terminal, then open this link again.", name)
		require.NotContains(t, body, "pending-7f3a", name)
		require.NotContains(t, body, secretDetail, name)
		if first == "" {
			first = body
		}
		require.Equal(t, first, body, name)
	}

	// A form post without a session gets the same page, not the action.
	resp, body := do(t, newBrowser(t), sameOrigin(newRequest(t, http.MethodPost,
		target+"/approve", url.Values{"csrf_token": {"x"}}), base))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, first, body)
	require.Zero(t, f.reached.Load(), "no request reached a page")

	// With a session, the same link shows the page.
	browser := newBrowser(t)
	f.signIn(s, browser)
	resp, body = do(t, browser, newRequest(t, http.MethodGet, target, nil))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "approval pending-7f3a: "+secretDetail, body)
}

func TestWrongHostRefused(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	browser := newBrowser(t)
	cookie := f.signIn(s, browser)
	port := strings.TrimPrefix(base, "http://127.0.0.1:")
	setupURL, err := s.NewSetupURL(context.Background())
	require.NoError(t, err)

	bad := []string{
		"evil.example",
		"evil.example:" + port,
		"127.0.0.1",
		"localhost",
		"127.0.0.2:" + port,
		"[::1]:" + port,
		"LOCALHOST:" + port,
		"localhost.:" + port,
		"127.0.0.1.:" + port,
		"127.0.0.1:0" + port,
		"127.0.0.1:" + port + "0",
		"0.0.0.0:" + port,
	}
	for _, host := range bad {
		req := newRequest(t, http.MethodGet, base+"/test/secret/p1", nil)
		req.Host = host
		req.Header.Set("Cookie", "emcp_session="+cookie)
		resp, body := do(t, newBrowser(t), req)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, host)
		require.Equal(t, "Forbidden\n", body, host)

		// A page of an attacker that uses DNS rebinding cannot use a setup link.
		req = newRequest(t, http.MethodGet, setupURL, nil)
		req.Host = host
		resp, _ = do(t, newBrowser(t), req)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, host)
		require.Empty(t, resp.Header.Values("Set-Cookie"), host)
	}
	require.Zero(t, f.reached.Load())

	for _, host := range []string{"127.0.0.1:" + port, "localhost:" + port} {
		req := newRequest(t, http.MethodGet, base+"/test/secret/p1", nil)
		req.Host = host
		req.Header.Set("Cookie", "emcp_session="+cookie)
		resp, _ := do(t, newBrowser(t), req)
		require.Equal(t, http.StatusOK, resp.StatusCode, host)
	}
	// The setup link was not used by the refused requests.
	f.signInWith(setupURL)
}

func TestHostCheckUsesConnectionPort(t *testing.T) {
	f := newFixture(t)
	_, baseA := f.start()
	_, baseB := f.start()
	require.NotEqual(t, baseA, baseB)

	for _, pair := range [][2]string{{baseA, baseB}, {baseB, baseA}} {
		to, hostOf := pair[0], pair[1]
		for _, name := range []string{"127.0.0.1:", "localhost:"} {
			req := newRequest(t, http.MethodGet, to+"/test/secret/p1", nil)
			req.Host = name + strings.TrimPrefix(hostOf, "http://127.0.0.1:")
			resp, _ := do(t, newBrowser(t), req)
			require.Equal(t, http.StatusForbidden, resp.StatusCode,
				"a Host with the port of the other server is refused")
		}
	}
}

func TestCrossOriginPostRefused(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	browser := newBrowser(t)
	f.signIn(s, browser)
	token := f.csrfFrom(browser, base)
	f.reached.Store(0)
	form := url.Values{"csrf_token": {token}}

	refused := map[string]map[string]string{
		"cross-site":            {"Sec-Fetch-Site": "cross-site", "Origin": "http://evil.example"},
		"same-site other port":  {"Sec-Fetch-Site": "same-site", "Origin": "http://127.0.0.1:1"},
		"old browser, evil":     {"Origin": "http://evil.example"},
		"old browser, other":    {"Origin": "http://127.0.0.1:1"},
		"old browser, null":     {"Origin": "null"},
		"old browser, localhst": {"Origin": strings.Replace(base, "127.0.0.1", "localhost", 1)},
	}
	for name, headers := range refused {
		req := newRequest(t, http.MethodPost, base+"/test/submit", form)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, body := do(t, browser, req)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, name)
		require.Equal(t, "Forbidden\n", body, name)
	}
	require.Zero(t, f.reached.Load(), "no cross-origin post reached the page")

	// The same post from the same origin is accepted.
	resp, body := do(t, browser, sameOrigin(newRequest(t, http.MethodPost, base+"/test/submit", form), base))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "accepted", body)
}

func TestCSRFTokenRequired(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	alice := newBrowser(t)
	aliceCookie := f.signIn(s, alice)
	bob := newBrowser(t)
	f.signIn(s, bob)
	aliceToken := f.csrfFrom(alice, base)
	bobToken := f.csrfFrom(bob, base)
	require.NotEqual(t, aliceToken, bobToken)

	otherKey, err := signing.New(bytes.Repeat([]byte{8}, 32)).Seal(signing.PurposeCSRF, hashOf(aliceCookie), time.Hour)
	require.NoError(t, err)
	otherPurpose, err := f.signer.Seal(signing.PurposePage, hashOf(aliceCookie), time.Hour)
	require.NoError(t, err)
	otherValue, err := f.signer.Seal(signing.PurposeCSRF, hashOf(randomText()), time.Hour)
	require.NoError(t, err)
	oldSigner := signing.New(testKey)
	oldSigner.Now = func() time.Time { return time.Now().Add(-31 * 24 * time.Hour) }
	expired, err := oldSigner.Seal(signing.PurposeCSRF, hashOf(aliceCookie), 30*24*time.Hour)
	require.NoError(t, err)
	// changed is the token of alice with another last character.
	flip := "A"
	if strings.HasSuffix(aliceToken, "A") {
		flip = "B"
	}
	changed := aliceToken[:len(aliceToken)-1] + flip

	f.reached.Store(0)
	refused := map[string]*http.Request{
		"no token":            newRequest(t, http.MethodPost, base+"/test/submit", url.Values{"other": {"1"}}),
		"no body":             newRequest(t, http.MethodPost, base+"/test/submit", nil),
		"empty token":         newRequest(t, http.MethodPost, base+"/test/submit", url.Values{"csrf_token": {""}}),
		"token of bob":        newRequest(t, http.MethodPost, base+"/test/submit", url.Values{"csrf_token": {bobToken}}),
		"changed token":       newRequest(t, http.MethodPost, base+"/test/submit", url.Values{"csrf_token": {changed}}),
		"other key":           newRequest(t, http.MethodPost, base+"/test/submit", url.Values{"csrf_token": {otherKey}}),
		"other purpose":       newRequest(t, http.MethodPost, base+"/test/submit", url.Values{"csrf_token": {otherPurpose}}),
		"other session hash":  newRequest(t, http.MethodPost, base+"/test/submit", url.Values{"csrf_token": {otherValue}}),
		"expired token":       newRequest(t, http.MethodPost, base+"/test/submit", url.Values{"csrf_token": {expired}}),
		"token in the query":  newRequest(t, http.MethodPost, base+"/test/submit?csrf_token="+url.QueryEscape(aliceToken), url.Values{"other": {"1"}}),
		"token as a header":   withHeader(newRequest(t, http.MethodPost, base+"/test/submit", nil), "X-CSRF-Token", aliceToken),
		"put without token":   newRequest(t, http.MethodPut, base+"/test/submit", nil),
		"delete":              newRequest(t, http.MethodDelete, base+"/test/submit", nil),
		"options":             newRequest(t, http.MethodOptions, base+"/test/submit", nil),
		"multipart has token": multipartRequest(t, base+"/test/submit", aliceToken),
	}
	for name, req := range refused {
		resp, _ := do(t, alice, sameOrigin(req, base))
		require.Equal(t, http.StatusForbidden, resp.StatusCode, name)
	}
	require.Zero(t, f.reached.Load(), "no post without a valid token reached the page")

	// Each browser's own token works.
	resp, body := do(t, alice, sameOrigin(newRequest(t, http.MethodPost, base+"/test/submit",
		url.Values{"csrf_token": {aliceToken}}), base))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "accepted", body)
	resp, _ = do(t, bob, sameOrigin(newRequest(t, http.MethodPost, base+"/test/submit",
		url.Values{"csrf_token": {bobToken}}), base))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// A body over 15 MB is refused before the token is checked.
	big := url.Values{"csrf_token": {aliceToken}, "data": {strings.Repeat("a", 15<<20)}}
	resp, _ = do(t, alice, sameOrigin(newRequest(t, http.MethodPost, base+"/test/submit", big), base))
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)

	// HEAD is a safe method like GET, and the mux sends it to the GET page. It
	// needs a session but no form token.
	f.reached.Store(0)
	resp, body = do(t, alice, newRequest(t, http.MethodHead, base+"/test/form", nil))
	require.Equal(t, http.StatusOK, resp.StatusCode, "head without a token")
	require.Empty(t, body)
	require.Equal(t, int32(1), f.reached.Load())
	resp, _ = do(t, newBrowser(t), newRequest(t, http.MethodHead, base+"/test/form", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "head without a session")
	require.Equal(t, int32(1), f.reached.Load())
}

func TestSetupTakesOnlyGetAndHead(t *testing.T) {
	f := newFixture(t)
	s := f.newServer()
	// A catch-all route stands for any later route that matches every path.
	var caught atomic.Int32
	s.mux.HandleFunc("/{path...}", func(w http.ResponseWriter, _ *http.Request) {
		caught.Add(1)
		_, _ = io.WriteString(w, "caught")
	})
	base, err := s.EnsureStarted(context.Background())
	require.NoError(t, err)
	browser := newBrowser(t)
	f.signIn(s, browser)
	token := f.csrfFrom(browser, base)
	setupURL, err := s.NewSetupURL(context.Background())
	require.NoError(t, err)

	clients := map[string]*http.Client{"no session": newBrowser(t), "session": browser}
	methods := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}
	for name, client := range clients {
		for _, method := range methods {
			for _, target := range []string{base + "/setup", setupURL} {
				req := sameOrigin(newRequest(t, method, target, url.Values{"csrf_token": {token}}), base)
				resp, body := do(t, client, req)
				require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, "%s %s", name, method)
				require.Equal(t, "GET, HEAD", resp.Header.Get("Allow"), "%s %s", name, method)
				require.NotContains(t, body, "caught")
				require.Empty(t, resp.Header.Values("Set-Cookie"))
			}
		}
	}
	require.Zero(t, caught.Load(), "no request to /setup reached another route")

	// GET and HEAD go to the setup page, never to the catch-all.
	resp, body := do(t, newBrowser(t), newRequest(t, http.MethodGet, base+"/setup", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, instruction, visibleText(t, body))
	resp, _ = do(t, newBrowser(t), newRequest(t, http.MethodHead, base+"/setup", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Zero(t, caught.Load())

	// The setup link was not used by the refused requests.
	f.signInWith(setupURL)

	// Other paths still reach the catch-all, after the session check.
	resp, _ = do(t, newBrowser(t), newRequest(t, http.MethodGet, base+"/setup/x", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp, body = do(t, browser, newRequest(t, http.MethodGet, base+"/setup/x", nil))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "caught", body)
}

func TestServeFailureStartsANewListener(t *testing.T) {
	f := newFixture(t)
	s := f.newServer()
	var (
		mu        sync.Mutex
		listeners []net.Listener
	)
	s.listen = func(ctx context.Context, network, address string) (net.Listener, error) {
		var lc net.ListenConfig
		ln, err := lc.Listen(ctx, network, address)
		if err == nil {
			mu.Lock()
			listeners = append(listeners, ln)
			mu.Unlock()
		}
		return ln, err
	}
	ctx := context.Background()
	first, err := s.EnsureStarted(ctx)
	require.NoError(t, err)

	// A browser keeps an idle connection open to the first server.
	addr := strings.TrimPrefix(first, "http://")
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = io.WriteString(conn, "GET /test/secret/p1 HTTP/1.1\r\nHost: "+addr+"\r\n\r\n")
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Closing the listener under the server makes Serve fail with an error that
	// is not http.ErrServerClosed.
	mu.Lock()
	require.Len(t, listeners, 1)
	require.NoError(t, listeners[0].Close())
	mu.Unlock()
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.baseURL == "" && s.srv == nil
	}, 5*time.Second, 5*time.Millisecond, "a failed server is forgotten")

	// The failed server is closed too: it ends its open connections, so
	// nothing of it runs on after Shutdown.
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = reader.ReadByte()
	require.ErrorIs(t, err, io.EOF, "the failed server closed its idle connection")

	// The next call starts a new listener, and it serves.
	base, err := s.EnsureStarted(ctx)
	require.NoError(t, err)
	require.Regexp(t, baseURLPattern, base)
	mu.Lock()
	require.Len(t, listeners, 2)
	mu.Unlock()
	var body string
	resp, body = do(t, newBrowser(t), newRequest(t, http.MethodGet, base+"/test/secret/p1", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, instruction, visibleText(t, body))
	require.Contains(t, f.logs.String(), "code=serve_failed")

	// A shutdown after the restart still stops the new listener.
	require.NoError(t, s.Shutdown(ctx))
	_, err = http.Get(base + "/")
	require.Error(t, err)
}

// withHeader sets a header of the request and returns the request.
func withHeader(req *http.Request, key, value string) *http.Request {
	req.Header.Set(key, value)
	return req
}

// multipartRequest returns a multipart form post with the CSRF token as a
// field. The UI uses only url-encoded forms, so the guard does not read it.
func multipartRequest(t *testing.T, target, token string) *http.Request {
	t.Helper()
	body := "--b\r\nContent-Disposition: form-data; name=\"csrf_token\"\r\n\r\n" + token + "\r\n--b--\r\n"
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	return req
}

func TestSessionWorksAcrossPorts(t *testing.T) {
	f := newFixture(t)
	a, baseA := f.start()
	_, baseB := f.start()
	require.NotEqual(t, baseA, baseB, "the two servers listen on different ports")

	browser := newBrowser(t)
	f.signIn(a, browser)

	// The browser sends the cookie of 127.0.0.1 to both ports, and both
	// servers accept it, because the session is in the shared store.
	for _, base := range []string{baseA, baseB} {
		resp, body := do(t, browser, newRequest(t, http.MethodGet, base+"/test/secret/p1", nil))
		require.Equal(t, http.StatusOK, resp.StatusCode, base)
		require.Contains(t, body, secretDetail, base)
	}
	resp, _ := do(t, newBrowser(t), newRequest(t, http.MethodGet, baseB+"/test/secret/p1", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a browser without the cookie is refused")

	// A form of one process can post to another: the token belongs to the
	// session, not to the port.
	token := f.csrfFrom(browser, baseA)
	resp, body := do(t, browser, sameOrigin(newRequest(t, http.MethodPost, baseB+"/test/submit",
		url.Values{"csrf_token": {token}}), baseB))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "accepted", body)
}

func TestSessionExpires(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	browser := newBrowser(t)
	cookie := f.signIn(s, browser)

	f.clock.Set(t0.Add(30*24*time.Hour - time.Millisecond))
	resp, _ := do(t, browser, newRequest(t, http.MethodGet, base+"/test/secret/p1", nil))
	require.Equal(t, http.StatusOK, resp.StatusCode, "the session works for 30 days")

	f.clock.Set(t0.Add(30 * 24 * time.Hour))
	resp, body := do(t, browser, newRequest(t, http.MethodGet, base+"/test/secret/p1", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the session ends after 30 days")
	require.Equal(t, instruction, visibleText(t, body))

	_, err := f.store.WebSessionByHash(context.Background(), hashOf(cookie))
	require.ErrorIs(t, err, store.ErrNotFound, "the expired session was deleted")
}

func TestSecretsStoredHashed(t *testing.T) {
	f := newFixture(t)
	s, _ := f.start()

	setupURL, err := s.NewSetupURL(context.Background())
	require.NoError(t, err)
	setupToken := tokenOf(t, setupURL)
	all := f.dataFiles()
	require.True(t, bytes.Contains(all, hashOf(setupToken)), "the SHA-256 of the setup token is stored")
	requireNotStored(t, all, setupToken)

	browser := newBrowser(t)
	resp, _ := do(t, browser, newRequest(t, http.MethodGet, setupURL, nil))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	cookie := strings.TrimPrefix(strings.SplitN(resp.Header.Get("Set-Cookie"), ";", 2)[0], "emcp_session=")
	require.Len(t, cookie, 43)

	all = f.dataFiles()
	require.True(t, bytes.Contains(all, hashOf(cookie)), "the SHA-256 of the session id is stored")
	requireNotStored(t, all, cookie)
	requireNotStored(t, all, setupToken)
}

// requireNotStored checks that neither the text of a secret nor its random
// bytes are in the data files.
func requireNotStored(t *testing.T, all []byte, secret string) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	require.NoError(t, err)
	require.Len(t, raw, 32)
	require.False(t, bytes.Contains(all, []byte(secret)), "the secret text is not stored")
	require.False(t, bytes.Contains(all, raw), "the random bytes of the secret are not stored")
}

func TestLinksHaveNoToken(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Each link starts the server the first time, as in a stdio process.
	starters := map[string]func(Links) (string, error){
		"connect":  func(l Links) (string, error) { return l.Connect(ctx) },
		"settings": func(l Links) (string, error) { return l.Settings(ctx, "a1", account.AreaMail, account.MailRead) },
		"approval": func(l Links) (string, error) { return l.Approval(ctx, "p1") },
	}
	for name, link := range starters {
		s := f.newServer()
		s.mu.Lock()
		require.Empty(t, s.baseURL, "%s: nothing listens before a link is needed", name)
		s.mu.Unlock()
		got, err := link(s.Links())
		require.NoError(t, err, name)
		resp, body := do(t, newBrowser(t), newRequest(t, http.MethodGet, got, nil))
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, name)
		require.Equal(t, instruction, visibleText(t, body), name)
	}

	s, base := f.start()
	browser := newBrowser(t)
	cookie := f.signIn(s, browser)
	unusedSetupURL, err := s.NewSetupURL(ctx)
	require.NoError(t, err)

	var links service.Links = s.Links()
	connect, err := links.Connect(ctx)
	require.NoError(t, err)
	settings, err := links.Settings(ctx, "acc/1 x?y#z", account.AreaMail, account.MailSend)
	require.NoError(t, err)
	approval, err := links.Approval(ctx, "p/1?token=x")
	require.NoError(t, err)

	require.Equal(t, base+"/connect", connect)
	require.Equal(t, base+"/accounts/acc%2F1%20x%3Fy%23z/settings?area=mail&level=4", settings)
	require.Equal(t, base+"/approvals/p%2F1%3Ftoken=x", approval)
	for _, link := range []string{connect, settings, approval} {
		require.True(t, strings.HasPrefix(link, base+"/"), link)
		require.Regexp(t, baseURLPattern, base)
		u, err := url.Parse(link)
		require.NoError(t, err)
		require.Empty(t, u.Query().Get("token"), link)
		require.Empty(t, u.Fragment, link)
		require.NotContains(t, link, cookie)
		require.NotContains(t, link, tokenOf(t, unusedSetupURL))
		require.NotContains(t, link, "emcp_session")
		require.NotContains(t, link, "csrf")
	}

	// An escaped id reaches the page as the same id. The test page stands for
	// the approval page, at another path.
	secretPage := strings.Replace(approval, base+"/approvals/", base+"/test/secret/", 1)
	resp, body := do(t, browser, newRequest(t, http.MethodGet, secretPage, nil))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "approval p/1?token=x: "+secretDetail, body)

	_, err = links.Settings(ctx, "", account.AreaMail, account.MailSend)
	require.Error(t, err)
	_, err = links.Approval(ctx, "")
	require.Error(t, err)
}

func TestEnsureStartedOnce(t *testing.T) {
	f := newFixture(t)
	s := f.newServer()

	const callers = 16
	urls := make([]string, callers)
	errs := make([]error, callers)
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			<-ready
			urls[i], errs[i] = s.EnsureStarted(context.Background())
		})
	}
	close(ready)
	wg.Wait()

	for i := range callers {
		require.NoError(t, errs[i])
		require.Equal(t, urls[0], urls[i], "every caller gets the one listener")
	}
	require.Regexp(t, baseURLPattern, urls[0])
	again, err := s.EnsureStarted(context.Background())
	require.NoError(t, err)
	require.Equal(t, urls[0], again)
	resp, _ := do(t, newBrowser(t), newRequest(t, http.MethodGet, urls[0]+"/", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestShutdown(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, base := f.start()

	require.NoError(t, s.Shutdown(ctx))
	_, err := http.Get(base + "/")
	require.Error(t, err, "nothing listens after the shutdown")
	_, err = s.EnsureStarted(ctx)
	require.ErrorIs(t, err, ErrClosed, "a shut down server does not start again")
	_, err = s.Links().Connect(ctx)
	require.ErrorIs(t, err, ErrClosed)
	_, err = s.NewSetupURL(ctx)
	require.ErrorIs(t, err, ErrClosed)
	require.NoError(t, s.Shutdown(ctx), "a second shutdown does nothing")

	never := f.newServer()
	require.NoError(t, never.Shutdown(ctx), "a server that never started shuts down")
	_, err = never.EnsureStarted(ctx)
	require.ErrorIs(t, err, ErrClosed)
}

func TestSecurityHeaders(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	browser := newBrowser(t)
	f.signIn(s, browser)
	token := f.csrfFrom(browser, base)
	setupURL, err := s.NewSetupURL(context.Background())
	require.NoError(t, err)

	badHost := newRequest(t, http.MethodGet, base+"/", nil)
	badHost.Host = "evil.example"
	crossOrigin := newRequest(t, http.MethodPost, base+"/test/submit", url.Values{"csrf_token": {token}})
	crossOrigin.Header.Set("Sec-Fetch-Site", "cross-site")

	requests := map[string]struct {
		client *http.Client
		req    *http.Request
		status int
	}{
		"setup redirect": {newBrowser(t), newRequest(t, http.MethodGet, setupURL, nil), http.StatusSeeOther},
		"bad setup":      {newBrowser(t), newRequest(t, http.MethodGet, base+"/setup?token=x", nil), http.StatusUnauthorized},
		"post to setup":  {browser, sameOrigin(newRequest(t, http.MethodPost, base+"/setup", nil), base), http.StatusMethodNotAllowed},
		"no session":     {newBrowser(t), newRequest(t, http.MethodGet, base+"/test/secret/p1", nil), http.StatusUnauthorized},
		"page":           {browser, newRequest(t, http.MethodGet, base+"/test/form", nil), http.StatusOK},
		"plain page":     {browser, newRequest(t, http.MethodGet, base+"/test/secret/p1", nil), http.StatusOK},
		"form post":      {browser, sameOrigin(newRequest(t, http.MethodPost, base+"/test/submit", url.Values{"csrf_token": {token}}), base), http.StatusOK},
		"unknown page":   {browser, newRequest(t, http.MethodGet, base+"/nothing", nil), http.StatusNotFound},
		"bad host":       {newBrowser(t), badHost, http.StatusForbidden},
		"cross origin":   {browser, crossOrigin, http.StatusForbidden},
		"no csrf token":  {browser, sameOrigin(newRequest(t, http.MethodPost, base+"/test/submit", url.Values{}), base), http.StatusForbidden},
	}
	for name, r := range requests {
		resp, _ := do(t, r.client, r.req)
		require.Equal(t, r.status, resp.StatusCode, name)
		require.Equal(t, []string{wantCSP}, resp.Header.Values("Content-Security-Policy"), name)
		require.Equal(t, []string{"nosniff"}, resp.Header.Values("X-Content-Type-Options"), name)
		require.Equal(t, []string{"no-referrer"}, resp.Header.Values("Referrer-Policy"), name)
		require.Equal(t, []string{"no-store"}, resp.Header.Values("Cache-Control"), name)
	}
}

func TestLogsHoldNoSecrets(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	browser := newBrowser(t)
	cookie := f.signIn(s, browser)
	token := f.csrfFrom(browser, base)
	setupURL, err := s.NewSetupURL(context.Background())
	require.NoError(t, err)

	badHost := newRequest(t, http.MethodGet, setupURL, nil)
	badHost.Host = "evil.example"
	do(t, newBrowser(t), badHost)
	crossOrigin := newRequest(t, http.MethodPost, base+"/test/secret/pending-7f3a/approve?csrf_token="+token,
		url.Values{"csrf_token": {token}})
	crossOrigin.Header.Set("Sec-Fetch-Site", "cross-site")
	do(t, browser, crossOrigin)
	do(t, browser, sameOrigin(newRequest(t, http.MethodPost, base+"/test/secret/pending-7f3a/approve",
		url.Values{"csrf_token": {token + "x"}}), base))
	do(t, newBrowser(t), newRequest(t, http.MethodGet, base+"/test/secret/pending-7f3a?q=secret-query", nil))
	do(t, newBrowser(t), newRequest(t, http.MethodGet, base+"/setup?token="+randomText(), nil))
	f.signInWith(setupURL)
	do(t, newBrowser(t), newRequest(t, http.MethodGet, setupURL, nil))

	logs := f.logs.String()
	for _, code := range []string{"bad_host", "cross_origin", "bad_csrf", "no_session", "bad_setup_token"} {
		require.Contains(t, logs, "code="+code)
	}
	for _, secret := range []string{
		cookie, token, tokenOf(t, setupURL), "token=", "pending-7f3a", "secret-query", "evil.example",
		"emcp_session", base64.RawURLEncoding.EncodeToString(hashOf(cookie)),
	} {
		require.NotContains(t, logs, secret)
	}
}

func TestStoreFailureIsAnError(t *testing.T) {
	f := newFixture(t)
	s, base := f.start()
	browser := newBrowser(t)
	cookie := f.signIn(s, browser)
	require.NoError(t, f.store.Close())

	resp, body := do(t, browser, newRequest(t, http.MethodGet, base+"/test/secret/p1", nil))
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Equal(t, "Internal Server Error\n", body)
	resp, _ = do(t, newBrowser(t), newRequest(t, http.MethodGet, base+"/setup?token="+randomText(), nil))
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	_, err := s.NewSetupURL(context.Background())
	require.Error(t, err)

	logs := f.logs.String()
	require.Contains(t, logs, "code=session_check_failed")
	require.Contains(t, logs, "code=setup_failed")
	require.Contains(t, logs, "error_types=")
	require.NotContains(t, logs, "closed", "the text of the error is not logged")
	require.NotContains(t, logs, cookie)
}
