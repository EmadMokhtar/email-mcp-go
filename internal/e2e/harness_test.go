// Package e2e holds the end-to-end tests of the security checklist of the spec
// (section 15.4). Each test opens the whole program with app.Open, as the
// "serve" command opens it, and drives it as an agent and as a browser would:
// through a go-sdk MCP client on an in-memory connection (stdio), through the
// local HTTP MCP endpoint, and through the local web interface. The mail
// servers are the test servers imaptest and smtptest on the loopback address.
// No test uses the network outside this computer.
//
// This file holds the helpers. security_test.go holds the tests.
package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/app"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/httpserve"
	"github.com/EmadMokhtar/email-mcp-go/internal/mcpserver"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
)

// The test secrets. Each one is long and unusual, so a match in a log or a file
// cannot be a chance match with a random id or with ciphertext. No secret is a
// part of another one. Addresses are in lower case, and display names are one
// word, so no encoding of a header changes them.
const (
	// testUser is the login name of the test mail servers. It is not a secret:
	// the account settings keep it in plain text.
	testUser = "e2e-login"
	// secretPassword is the password of the account.
	secretPassword = "pw-Zx7Qk2Lm9Vr4Tn8B" //nolint:gosec // G101: a test password, used to check that it is never shown
	// secretWrongPassword is a password that the mail servers refuse.
	secretWrongPassword = "pw-wrong-Hd4Ny8Ks1Fe6" //nolint:gosec // G101: a test password, used to check that it is never shown

	// accountAddress is the address of the connected account. It is the only
	// test address that the database keeps in plain text, by design: the
	// accounts table has an address column (spec section 9.1). The logs must
	// not have it either.
	accountAddress = "acct-r8k2w5@example.net"

	// The sender of the message in the inbox, and the text of that message.
	inSenderAddress = "sndr-h4p9x2@example.org"
	inSenderName    = "SndrNameQ7v3Kp"
	inSubject       = "InSubjMarkerX2c9Lq6"
	inBody          = "InBodyMarkerJ5v1Zm8"
	inAttachName    = "inattnamey3m7.txt"
	inAttachBody    = "InAttBodyMarkerC4r9"

	// The recipients of the messages that the agent writes.
	rcptAddress     = "rcpt-m3t8z6@example.org"
	rcptName        = "RcptNameW5j2Dx"
	ccAddress       = "ccpy-b6n1y4@example.org"
	bccAddress      = "bccx-f2q7c9@example.org"
	rejectedAddress = "rjct-p5s3u7@example.org"

	// The text of the messages that the agent writes.
	outSubject    = "SubjMarkerK8d3Ry5Pw"
	outBody       = "BodyMarkerT4g7Hs2Nv"
	outAttachName = "outattnamee5k2.txt"
	outAttachBody = "OutAttBodyMarkerV7q3"
	draftSubject  = "DraftSubjMarkerG6w1"
	draftBody     = "DraftBodyMarkerN9b5"
)

// markedSubject is a subject with the marker three times. The tests look for
// the marker alone, so the copies must not hide each other.
func markedSubject(marker string) string {
	return marker + " re " + marker + " fw " + marker
}

// markedBody is a body with the marker three times, about 700 and 1900 bytes
// apart. A large value in SQLite is split over overflow pages, and each page
// starts with a 4-byte pointer, so the split can cut a marker in two. The
// pages are 4096 bytes, so a split can cut at most one copy, and a scan of the
// raw bytes still finds another one when the body is stored in plain text.
func markedBody(marker string) string {
	filler := strings.Repeat("words that move the next copy along ", 60)
	return marker + "\n" + filler[:700] + "\n" + marker + "\n" + filler[:1900] + "\n" + marker + "\n"
}

// localPrincipal is the caller of a tool call on stdio, as "serve" sets it: the
// local tenant, with no scope limit, and the name that the client gave.
func localPrincipal(_ context.Context, req *mcp.CallToolRequest) (service.Principal, error) {
	var client string
	if req != nil {
		if info := req.ClientInfo(); info != nil {
			client = info.Name
		}
	}
	return service.Principal{Tenant: store.LocalTenantID, Client: client}, nil
}

// localCtx returns a context with the local user as the caller, as the web
// interface and the command line tool call the service.
func localCtx() context.Context {
	return service.WithPrincipal(context.Background(), service.Principal{Tenant: store.LocalTenantID, Client: "e2e-user"})
}

// syncBuffer is a bytes.Buffer that many goroutines may write to. It collects
// the log of the program.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---------------------------------------------------------------- the program

// harness is one program, opened with app.Open on a new data directory with a
// master key file, as "serve" opens it on a local install. Its log goes to a
// buffer at Debug level, so a test sees every line of every part.
type harness struct {
	t       *testing.T
	app     *app.App
	dataDir string
	keyPath string
	logs    *syncBuffer

	// stdio is the MCP server of stdio, made on first use.
	stdio *mcp.Server

	// mu guards the fields below.
	mu     sync.Mutex
	closed bool
	db     *sql.DB
}

// newHarness opens a program. The test cleanup closes it without draining the
// outbox (CloseNow), so a message that a test leaves queued does not hold the
// test up. A test that wants the whole stop calls close.
func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, harnessOptions{})
}

// harnessOptions change how newHarnessWith opens the program.
type harnessOptions struct {
	// hosted opens a hosted install (app.Config.Local is false): plain text is
	// refused even to a server on this computer.
	hosted bool
	// now is the clock of the program (app.Config.Now). Nil means the real
	// clock.
	now func() time.Time
}

// newHarnessWith opens a program with options.
func newHarnessWith(t *testing.T, opts harnessOptions) *harness {
	t.Helper()
	h := &harness{
		t:       t,
		dataDir: filepath.Join(t.TempDir(), "data"),
		// The key file is outside the data directory, so every file of the data
		// directory is the database and its files.
		keyPath: filepath.Join(t.TempDir(), "master.key"),
		logs:    &syncBuffer{},
	}
	a, err := app.Open(context.Background(), app.Config{
		DataDir:   h.dataDir,
		KEKSource: "file:" + h.keyPath,
		Local:     !opts.hosted,
		LogOutput: h.logs,
		LogLevel:  slog.LevelDebug,
		Now:       opts.now,
	})
	require.NoError(t, err)
	h.app = a
	t.Cleanup(func() {
		h.mu.Lock()
		closed := h.closed
		h.closed = true
		h.mu.Unlock()
		if !closed {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = a.CloseNow(ctx)
		}
	})
	return h
}

// close stops the program with app.App.Close, the stop of "serve", with the
// same time budget (app.StopTimeout). Nothing may be queued: Close would wait
// for it.
func (h *harness) close() {
	h.t.Helper()
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), app.StopTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.app.Close(ctx) }()
	select {
	case err := <-done:
		require.NoError(h.t, err)
	case <-time.After(30 * time.Second):
		h.t.Fatal("the program did not stop within 30 seconds")
	}
}

// mailServers are a test IMAP server and a test SMTP server that accept the
// same login: testUser and secretPassword.
type mailServers struct {
	imap *imaptest.Server
	smtp *smtptest.Server
}

// newMailServers starts the two servers. The IMAP server has the folders Trash,
// Drafts, Sent and Archive.
func newMailServers(t *testing.T, smtpOpts ...smtptest.Option) mailServers {
	t.Helper()
	imapSrv := imaptest.New(t, imaptest.WithLogin(testUser, secretPassword))
	imapSrv.Create("Trash", imap.MailboxAttrTrash)
	imapSrv.Create("Drafts", imap.MailboxAttrDrafts)
	imapSrv.Create("Sent", imap.MailboxAttrSent)
	imapSrv.Create("Archive", imap.MailboxAttrArchive)
	smtpSrv := smtptest.New(t, append([]smtptest.Option{smtptest.WithLogin(testUser, secretPassword)}, smtpOpts...)...)
	return mailServers{imap: imapSrv, smtp: smtpSrv}
}

// settings are the settings of an account of the two servers: TLS, with the
// certificates pinned.
func (m mailServers) settings() account.MailServer {
	return account.MailServer{
		IMAPHost: m.imap.Host, IMAPPort: m.imap.Port, IMAPSecurity: account.SecurityTLS,
		SMTPHost: m.smtp.Host, SMTPPort: m.smtp.Port, SMTPSecurity: account.SecurityTLS,
		Username:       testUser,
		PinnedIMAPCert: m.imap.CertSHA256, PinnedSMTPCert: m.smtp.CertSHA256,
	}
}

// connect connects an account of the servers with the service, as the web page
// and the command line tool do, in the tenant of ctx. The probe logs in to
// both servers. A new account has Mail 4 and both confirmations on.
func (h *harness) connect(ctx context.Context, address string, m mailServers) *account.Account {
	h.t.Helper()
	a, err := h.app.Service.CreateIMAPAccount(ctx, address, m.settings(), secrets.Secret(secretPassword))
	require.NoError(h.t, err)
	return a
}

// settingsOf is a settings update with the default limits and both
// confirmations on.
func settingsOf(level account.Level) service.SettingsUpdate {
	return service.SettingsUpdate{MailLevel: level, ConfirmSends: true, ConfirmDeletes: true, Limits: account.DefaultLimits()}
}

// update saves the settings of an account as the user does on the settings
// page.
func (h *harness) update(id account.AccountID, u service.SettingsUpdate) {
	h.t.Helper()
	require.NoError(h.t, h.app.Service.UpdateSettings(localCtx(), id, u))
}

// ---------------------------------------------------------------- MCP clients

// stdioServer returns the MCP server of stdio, as "serve" makes it: in-band
// confirmation for older clients too, and the local principal.
func (h *harness) stdioServer() *mcp.Server {
	if h.stdio == nil {
		h.stdio = h.app.MCPServer(mcpserver.Options{InBandLegacyElicitation: true, Principal: localPrincipal})
	}
	return h.stdio
}

// client connects a go-sdk client to the stdio server over an in-memory
// connection. Nil options give a client without elicitation.
func (h *harness) client(opts *mcp.ClientOptions) *mcp.ClientSession {
	h.t.Helper()
	return connectClient(h.t, h.stdioServer(), opts)
}

// connectClient connects a go-sdk client to srv over an in-memory connection.
func connectClient(t *testing.T, srv *mcp.Server, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "1"}, opts)
	cs, err := c.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Wait()
	})
	return cs
}

// asker answers every elicitation with a fixed answer and counts the questions.
type asker struct {
	mu     sync.Mutex
	asked  []*mcp.ElicitParams
	answer mcp.ElicitResult
}

func (a *asker) handle(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, req.Params)
	answer := a.answer
	if a.answer.Content != nil {
		answer.Content = map[string]any{}
		for k, v := range a.answer.Content {
			answer.Content[k] = v
		}
	}
	return &answer, nil
}

func (a *asker) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.asked)
}

// acceptingClient returns client options of a person who confirms every
// question.
func acceptingClient() (*mcp.ClientOptions, *asker) {
	ask := &asker{answer: mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}
	return &mcp.ClientOptions{ElicitationHandler: ask.handle}, ask
}

// rawClient returns the options of a client that declares form elicitation but
// does not answer input requests by itself, so a test sends its own second
// call with any request state it likes.
func rawClient(t *testing.T) *mcp.ClientOptions {
	return &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			t.Error("the raw client must not be asked directly")
			return &mcp.ElicitResult{Action: "decline"}, nil
		},
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	}
}

// call calls a tool. A protocol error fails the test: every problem that the
// tools report must be a tool result.
func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	return callParams(t, cs, &mcp.CallToolParams{Name: tool, Arguments: args})
}

// callParams calls a tool with the whole parameters.
func callParams(t *testing.T, cs *mcp.ClientSession, params *mcp.CallToolParams) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), params)
	require.NoError(t, err, "tool %s must answer with a result, not a protocol error", params.Name)
	return res
}

// mustOK calls a tool and fails the test when the result is an error. It
// returns the structured content.
func mustOK(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) map[string]any {
	t.Helper()
	res := call(t, cs, tool, args)
	require.False(t, res.IsError, "%s: %s", tool, textOf(t, res))
	return structured(t, res)
}

// textOf returns the text of the first content block of a result.
func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		return ""
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok, "the first content block must be text, got %T", res.Content[0])
	return tc.Text
}

// structured returns the structured content of a result as a JSON object.
func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	m, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "structured content must be an object, got %T", res.StructuredContent)
	return m
}

// firstCall makes the first call of a tool that needs a confirmation, on a raw
// client, and returns the request state.
func firstCall(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res := call(t, cs, tool, args)
	require.False(t, res.IsError, textOf(t, res))
	require.True(t, res.NeedsInput(), "the first call of %s must ask for the confirmation", tool)
	require.NotEmpty(t, res.RequestState)
	return res.RequestState
}

// answerCall is the second call: the arguments, an answer and a request state.
func answerCall(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, answers mcp.InputResponseMap, state string) *mcp.CallToolResult {
	t.Helper()
	return callParams(t, cs, &mcp.CallToolParams{Name: tool, Arguments: args, InputResponses: answers, RequestState: state})
}

// accept is the answer of a person who confirmed.
func accept() mcp.InputResponseMap {
	return mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}
}

// refsIn returns the refs of the messages in a folder.
func refsIn(t *testing.T, cs *mcp.ClientSession, id account.AccountID, folder string) []any {
	t.Helper()
	out := mustOK(t, cs, "search_messages", map[string]any{"account": string(id), "folder": folder})
	msgs, _ := out["messages"].([]any)
	refs := make([]any, len(msgs))
	for i, m := range msgs {
		refs[i] = m.(map[string]any)["ref"]
	}
	return refs
}

// sendArgs are the arguments of a send with the test secrets in every field
// that a person writes.
func sendArgs(id account.AccountID) map[string]any {
	return map[string]any{
		"account": string(id),
		"to":      []any{rcptName + " <" + rcptAddress + ">"},
		"cc":      []any{ccAddress},
		"bcc":     []any{bccAddress},
		"subject": markedSubject(outSubject),
		"body":    markedBody(outBody),
		"attachments": []any{map[string]any{
			"filename":     outAttachName,
			"content_type": "text/plain",
			"data_base64":  base64.StdEncoding.EncodeToString([]byte(outAttachBody)),
		}},
	}
}

// ---------------------------------------------------------------- the HTTP MCP endpoint

// httpEndpoint serves the MCP server over local HTTP, as "serve --http" does:
// stateless, with the bearer token check and no allowed browser origin.
func (h *harness) httpEndpoint() *httptest.Server {
	h.t.Helper()
	srv := h.app.MCPServer(mcpserver.Options{InBandLegacyElicitation: false, Principal: httpserve.Principal})
	ts := httptest.NewServer(httpserve.Handler(func() *mcp.Server { return srv }, h.app.Tokens, nil,
		httpserve.WithLogger(h.app.Logger())))
	h.t.Cleanup(ts.Close)
	return ts
}

// bearer is an http.RoundTripper that adds the token to every request.
type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

// httpClient connects a go-sdk client to the HTTP endpoint with a token.
func httpClient(t *testing.T, ts *httptest.Server, token string) *mcp.ClientSession {
	t.Helper()
	transport := &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: bearer{token: token, base: http.DefaultTransport}},
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e-http-client", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// toolCallBody is a JSON-RPC tools/call request in the oldest protocol, which
// a stateless server answers without a session.
func toolCallBody(t *testing.T, tool string, args map[string]any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	})
	require.NoError(t, err)
	return string(body)
}

// postMCP sends a raw JSON-RPC request to the HTTP endpoint. change can set the
// Host or more headers. It returns the status and the body.
func postMCP(t *testing.T, ts *httptest.Server, body string, change func(r *http.Request)) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if change != nil {
		change(req)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(data)
}

// ---------------------------------------------------------------- the browser

// The patterns of the pages of the web interface.
var (
	csrfPattern  = regexp.MustCompile(`<input type="hidden" name="csrf_token" value="([A-Za-z0-9_.-]+)">`)
	offerPattern = regexp.MustCompile(`<input type="hidden" name="(imap|smtp)_offer" value="([A-Za-z0-9_.-]+)">`)
	tokenPattern = regexp.MustCompile(`<p><code>([^<]+)</code></p>`)
)

// browser is a browser of the user: a session cookie (empty for a browser
// that never signed in) and the CSRF token of a form of its session.
type browser struct {
	t      *testing.T
	base   string
	client *http.Client
	cookie string
	csrf   string
}

// newClient returns an HTTP client that does not follow redirects and keeps no
// cookies by itself.
func newClient(t *testing.T) *http.Client {
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// webBase starts the web interface if it does not run yet, and returns its base
// URL.
func (h *harness) webBase() string {
	h.t.Helper()
	base, err := h.app.Web.EnsureStarted(context.Background())
	require.NoError(h.t, err)
	return base
}

// stranger returns a browser without a session: an agent that fetches a link,
// or a browser that never ran "email-mcp setup".
func (h *harness) stranger() *browser {
	return &browser{t: h.t, base: h.webBase(), client: newClient(h.t)}
}

// signIn signs a new browser in as "email-mcp setup" does: it opens a new setup
// link, keeps the session cookie, and reads the CSRF token of a form. It also
// returns the setup URL.
func (h *harness) signIn() (*browser, string) {
	h.t.Helper()
	setupURL, err := h.app.Web.NewSetupURL(context.Background())
	require.NoError(h.t, err)
	b := h.stranger()
	resp, _ := b.do(b.request(http.MethodGet, setupURL, nil))
	require.Equal(h.t, http.StatusSeeOther, resp.StatusCode)
	for _, c := range resp.Cookies() {
		if c.Name == "emcp_session" {
			b.cookie = c.Value
		}
	}
	require.NotEmpty(h.t, b.cookie, "the setup link gives a session cookie")
	status, page := b.get("/connect")
	require.Equal(h.t, http.StatusOK, status)
	m := csrfPattern.FindStringSubmatch(page)
	require.NotNil(h.t, m, "the connect page has a form with the CSRF token")
	b.csrf = m[1]
	return b, setupURL
}

// request makes a request. A form body is sent url-encoded. A target that is a
// path is on the web interface. The session cookie goes with it when the
// browser has one.
func (b *browser) request(method, target string, form url.Values) *http.Request {
	b.t.Helper()
	if strings.HasPrefix(target, "/") {
		target = b.base + target
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	require.NoError(b.t, err)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if b.cookie != "" {
		req.Header.Set("Cookie", "emcp_session="+b.cookie)
	}
	return req
}

// do sends a request and returns the response with its whole body.
func (b *browser) do(req *http.Request) (*http.Response, string) {
	b.t.Helper()
	resp, err := b.client.Do(req)
	require.NoError(b.t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(b.t, err)
	return resp, string(data)
}

// get loads a page (a path or a whole link) and returns the status and the
// body.
func (b *browser) get(target string) (int, string) {
	b.t.Helper()
	resp, body := b.do(b.request(http.MethodGet, target, nil))
	return resp.StatusCode, body
}

// post sends a form from a page of the same origin, as a browser does when the
// user presses a button. The CSRF token of the session is added unless the form
// has one (an empty value is sent as it is).
func (b *browser) post(target string, form url.Values) (int, string) {
	b.t.Helper()
	form = cloneForm(form)
	if _, ok := form["csrf_token"]; !ok && b.csrf != "" {
		form.Set("csrf_token", b.csrf)
	}
	req := b.request(http.MethodPost, target, form)
	req.Header.Set("Origin", b.base)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, body := b.do(req)
	return resp.StatusCode, body
}

// cloneForm returns a copy of a form. A nil form gives an empty one.
func cloneForm(form url.Values) url.Values {
	out := url.Values{}
	for k, v := range form {
		out[k] = slices.Clone(v)
	}
	return out
}

// pathOf returns the path and the query of a link of the web interface.
func pathOf(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", u.Hostname(), "a link points at the loopback address")
	return u.RequestURI()
}

// connectForm is the form of the connect page for the two test servers.
func connectForm(address string, m mailServers, password string) url.Values {
	return url.Values{
		"email":         {address},
		"imap_host":     {m.imap.Host},
		"imap_port":     {fmt.Sprint(m.imap.Port)},
		"imap_security": {string(account.SecurityTLS)},
		"smtp_host":     {m.smtp.Host},
		"smtp_port":     {fmt.Sprint(m.smtp.Port)},
		"smtp_security": {string(account.SecurityTLS)},
		"username":      {testUser},
		"password":      {password},
	}
}

// ---------------------------------------------------------------- the database

// sqlDB returns a second connection to the database of the program, as a
// second local process would have, opened on first use. A test uses it to look
// at rows and, for the cross-tenant test, to add a tenant (no code path of a
// local install adds one).
func (h *harness) sqlDB() *sql.DB {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.db != nil {
		return h.db
	}
	path := filepath.ToSlash(filepath.Join(h.dataDir, store.DBFileName))
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: url.Values{
		"_pragma": {"busy_timeout(10000)", "foreign_keys(1)"},
	}.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	require.NoError(h.t, err)
	h.t.Cleanup(func() { _ = db.Close() })
	h.db = db
	return db
}

// count returns the result of a SELECT COUNT(*) query.
func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(query, args...).Scan(&n))
	return n
}

// dump returns every row of every table of the database as text, in a fixed
// order. Two dumps are equal when nothing in the database changed.
func dump(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	require.NoError(t, err)
	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	var out strings.Builder
	for _, table := range tables {
		fmt.Fprintf(&out, "== %s\n", table)
		// The table names come from sqlite_master, never from input.
		rows, err := db.Query(`SELECT * FROM "` + table + `"`) //nolint:gosec // G202: the name is a table of the schema, never input
		require.NoError(t, err)
		cols, err := rows.Columns()
		require.NoError(t, err)
		var lines []string
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			require.NoError(t, rows.Scan(ptrs...))
			var line strings.Builder
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					v = hex.EncodeToString(b)
				}
				fmt.Fprintf(&line, "%s=%v;", cols[i], v)
			}
			lines = append(lines, line.String())
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		sort.Strings(lines)
		out.WriteString(strings.Join(lines, "\n"))
		out.WriteString("\n")
	}
	return out.String()
}

// dataFiles returns the bytes of every file in the data directory, by name.
// The master key file is never there (see newHarness), and the test checks
// that.
func (h *harness) dataFiles() map[string][]byte {
	h.t.Helper()
	entries, err := os.ReadDir(h.dataDir)
	require.NoError(h.t, err)
	files := map[string][]byte{}
	for _, e := range entries {
		require.False(h.t, e.IsDir(), "the data directory has no sub-directory")
		require.NotEqual(h.t, filepath.Clean(h.keyPath), filepath.Join(h.dataDir, e.Name()), "the master key is not in the data directory")
		data, err := os.ReadFile(filepath.Join(h.dataDir, e.Name()))
		require.NoError(h.t, err)
		files[e.Name()] = data
	}
	return files
}

// ---------------------------------------------------------------- finding secrets

// minCore is the shortest base64 part that the search uses: 16 characters.
// The search ignores case, so each character still carries more than 5 bits,
// more than 80 bits in all. A random match in a few megabytes of ciphertext is
// then out of reach.
const minCore = 16

// base64Cores returns the parts of the base64 forms of text (standard and URL
// alphabets) that do not depend on the bytes around it: groups of 3 bytes that
// are all bytes of text encode to the same 4 characters wherever text starts,
// once for each of the 3 ways that text can sit in the groups.
func base64Cores(text string) []string {
	var cores []string
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
		for shift := 0; shift < 3; shift++ {
			padded := append(make([]byte, shift), text...)
			encoded := enc.EncodeToString(padded)
			first, last := (shift+2)/3, (shift+len(text))/3
			if 4*(last-first) >= minCore {
				cores = append(cores, encoded[4*first:4*last])
			}
		}
	}
	return cores
}

// secretForms returns the forms of a secret to look for:
//   - the text itself;
//   - its hex form (the search ignores case, so upper-case hex too);
//   - its percent-encoded form, as in a URL query or a form body;
//   - its base64 forms (base64Cores). A JSON encoding of []byte is base64, so a
//     message inside a JSON document shows only that way;
//   - the base64 forms of those: base64 inside base64, such as an attachment
//     of a message that is itself in a JSON document.
func secretForms(secret string) []string {
	forms := []string{secret, hex.EncodeToString([]byte(secret)), url.QueryEscape(secret)}
	first := base64Cores(secret)
	forms = append(forms, first...)
	for _, core := range first {
		forms = append(forms, base64Cores(core)...)
	}
	slices.Sort(forms)
	return slices.Compact(forms)
}

// secretForm is one form of one secret, in lower case.
type secretForm struct {
	name  string
	plain bool
	form  string
}

// secretFinder looks for many secrets in all their forms in one pass over the
// data. It compares without regard to case. Each form is at least 8 bytes, so
// the forms are indexed by their first 8 bytes.
type secretFinder struct {
	byPrefix map[string][]secretForm
}

func newSecretFinder(t *testing.T, secrets map[string]string) *secretFinder {
	t.Helper()
	f := &secretFinder{byPrefix: map[string][]secretForm{}}
	for name, secret := range secrets {
		require.GreaterOrEqual(t, len(secret), 8, "secret %s is too short to look for", name)
		for _, form := range secretForms(secret) {
			lower := strings.ToLower(form)
			f.byPrefix[lower[:8]] = append(f.byPrefix[lower[:8]], secretForm{name: name, plain: form == secret, form: lower})
		}
	}
	return f
}

// find returns the names of the secrets that are in data, with the form in
// which each one was found.
func (f *secretFinder) find(data []byte) map[string]string {
	found := map[string]string{}
	lower := bytes.ToLower(data)
	for i := 0; i+8 <= len(lower); i++ {
		forms, ok := f.byPrefix[string(lower[i:i+8])]
		if !ok {
			continue
		}
		for _, sf := range forms {
			if _, seen := found[sf.name]; seen || !bytes.HasPrefix(lower[i:], []byte(sf.form)) {
				continue
			}
			if sf.plain {
				found[sf.name] = "plain text"
			} else {
				found[sf.name] = fmt.Sprintf("encoded as %q", sf.form)
			}
		}
	}
	return found
}

// findSecret reports the form in which a secret is in data, or "" when it is
// not there.
func findSecret(t *testing.T, data []byte, secret string) string {
	t.Helper()
	return newSecretFinder(t, map[string]string{"secret": secret}).find(data)["secret"]
}

// requireNoSecrets fails for each secret that is in data, in any form of
// secretForms.
func requireNoSecrets(t *testing.T, where string, data []byte, secrets map[string]string) {
	t.Helper()
	found := newSecretFinder(t, secrets).find(data)
	names := slices.Sorted(maps.Keys(found))
	for _, name := range names {
		t.Errorf("%s holds the %s (%s)", where, name, found[name])
	}
}
