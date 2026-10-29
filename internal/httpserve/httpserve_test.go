package httpserve

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mcpserver"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/tokens"
)

// testStart is the clock of the service in these tests.
var testStart = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

// clientName is the name that the test clients give for themselves.
const clientName = "http-test-client"

// syncBuffer is a bytes.Buffer that many goroutines may write to. It collects
// the log lines of a test.
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

// fakeLinks is a service.Links for tests.
type fakeLinks struct{}

func (fakeLinks) Connect(context.Context) (string, error) { return "https://mcp.test/connect", nil }

func (fakeLinks) Settings(_ context.Context, id account.AccountID, area account.Area, level account.Level) (string, error) {
	return fmt.Sprintf("https://mcp.test/settings/%s?area=%s&level=%d", id, area, int(level)), nil
}

func (fakeLinks) Approval(_ context.Context, pendingID string) (string, error) {
	return "https://mcp.test/approve/" + pendingID, nil
}

// httpEnv is the MCP server of the program behind the HTTP handler, on a test
// HTTP server on 127.0.0.1. The service has a real store, vault, outbox and
// confirm manager, and one account on test IMAP and SMTP servers.
type httpEnv struct {
	t      *testing.T
	store  *store.Store
	tenant account.TenantID
	tokens *tokens.Manager
	srv    *httptest.Server
	acct   *account.Account
	smtp   *smtptest.Server
	logs   *syncBuffer

	// servers counts the calls of newServer: each one is a request that reached
	// the MCP handler of the SDK.
	servers atomic.Int32
	// tools counts the tool calls that reached the Principal function.
	tools atomic.Int32
}

// newHTTPEnv builds the env. allowedOrigins is passed to Handler.
func newHTTPEnv(t *testing.T, allowedOrigins []string) *httpEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	// A test may close the store itself, so the second close is not checked.
	t.Cleanup(func() { _ = st.Close() })
	tenant, err := st.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	keyPath := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, secrets.GenerateKeyFile(keyPath))
	kek, err := secrets.NewFileKEK(keyPath)
	require.NoError(t, err)
	vault := secrets.NewVault(st, kek)
	signer := signing.New(bytes.Repeat([]byte{7}, 32))
	backends := service.NewBackendFactory(st, vault, signer, true, nil)
	t.Cleanup(backends.Close)
	clock := func() time.Time { return testStart }
	send := func(ctx context.Context, tenant account.TenantID, id account.AccountID, msg *mail.RawMessage, _ mail.Ref) error {
		a, err := st.GetAccount(ctx, tenant, id)
		if err != nil {
			return err
		}
		b, err := backends.Mail(ctx, a)
		if err != nil {
			return err
		}
		return b.Send(ctx, msg)
	}
	svc := service.New(service.Deps{
		Store:    st,
		Vault:    vault,
		Signer:   signer,
		Backends: backends,
		Limiter:  &policy.Limiter{Store: st, Now: clock},
		Confirm:  confirm.NewManager(st, vault, signer, clock),
		Outbox:   outbox.New(st, vault, send, clock),
		Audit:    audit.New(st, vault, clock),
		Links:    fakeLinks{},
		Now:      clock,
	})

	h := &httpEnv{t: t, store: st, tenant: tenant, tokens: tokens.New(st, clock), logs: &syncBuffer{}}
	logger := slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server := mcpserver.New(svc, mcpserver.Options{
		Version:                 "test",
		InBandLegacyElicitation: false,
		Principal: func(ctx context.Context, req *mcp.CallToolRequest) (service.Principal, error) {
			h.tools.Add(1)
			return Principal(ctx, req)
		},
		Logger: logger,
	})
	newServer := func() *mcp.Server {
		h.servers.Add(1)
		return server
	}
	h.srv = httptest.NewServer(Handler(newServer, h.tokens, allowedOrigins, WithLogger(logger)))
	t.Cleanup(h.srv.Close)

	imapSrv := imaptest.New(t)
	h.smtp = smtptest.New(t)
	h.acct = h.addAccount(imapSrv, h.smtp, vault)
	return h
}

// addAccount stores an account of the local tenant on the test servers, with a
// sealed password. A new account is at Mail 4 and confirms sends.
func (h *httpEnv) addAccount(imapSrv *imaptest.Server, smtpSrv *smtptest.Server, vault *secrets.Vault) *account.Account {
	h.t.Helper()
	ctx := context.Background()
	a := account.NewIMAPAccount(h.tenant, "me@example.com", account.MailServer{
		IMAPHost: imapSrv.Host, IMAPPort: imapSrv.Port, IMAPSecurity: account.SecurityTLS,
		SMTPHost: smtpSrv.Host, SMTPPort: smtpSrv.Port, SMTPSecurity: account.SecurityTLS,
		Username: imapSrv.User, PinnedIMAPCert: imapSrv.CertSHA256, PinnedSMTPCert: smtpSrv.CertSHA256,
	}, testStart)
	require.NoError(h.t, h.store.CreateAccount(ctx, a))
	aad := secrets.AAD(string(a.TenantID), string(a.ID), "password")
	version, sealed, err := vault.Encrypt(ctx, a.TenantID, aad, []byte(imapSrv.Pass))
	require.NoError(h.t, err)
	require.NoError(h.t, h.store.PutAccountSecret(ctx, a.TenantID, a.ID, "password", version, sealed))
	return a
}

// token makes a token of the local tenant with the scopes.
func (h *httpEnv) token(scopes ...string) string {
	h.t.Helper()
	token, err := h.tokens.Create(context.Background(), h.tenant, "test", policy.Scopes(scopes))
	require.NoError(h.t, err)
	return token
}

// port is the port of the test server.
func (h *httpEnv) port() string {
	h.t.Helper()
	u, err := url.Parse(h.srv.URL)
	require.NoError(h.t, err)
	return u.Port()
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

// connect connects a go-sdk client over the streamable client transport, with
// the token on every request.
func (h *httpEnv) connect(token string, opts *mcp.ClientOptions, session *mcp.ClientSessionOptions) *mcp.ClientSession {
	h.t.Helper()
	transport := &mcp.StreamableClientTransport{
		Endpoint:   h.srv.URL,
		HTTPClient: &http.Client{Transport: bearer{token: token, base: http.DefaultTransport}},
	}
	c := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "1"}, opts)
	cs, err := c.Connect(context.Background(), transport, session)
	require.NoError(h.t, err)
	h.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call calls a tool and returns the result. A protocol error fails the test.
func call(t *testing.T, cs *mcp.ClientSession, params *mcp.CallToolParams) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), params)
	require.NoError(t, err, "tool %s must answer with a result", params.Name)
	return res
}

// textOf returns the text of the first content block of a result.
func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
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

// sendArgs are the arguments of a simple send.
func (h *httpEnv) sendArgs() map[string]any {
	return map[string]any{
		"account": string(h.acct.ID),
		"to":      []any{"you@example.com"},
		"subject": "Lunch",
		"body":    "Shall we meet at noon?",
	}
}

// listBody is a small JSON-RPC request in the oldest protocol, which a stateless
// server answers without a session.
const listBody = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

// post sends a JSON-RPC request with the headers that the transport needs.
// change can set more headers or another Host. It returns the status and the
// body of the answer.
func (h *httpEnv) post(body string, change func(r *http.Request)) (int, string) {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.srv.URL, strings.NewReader(body))
	require.NoError(h.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if change != nil {
		change(req)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	return resp.StatusCode, string(data)
}

// withToken sets the Authorization header.
func withToken(token string) func(r *http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

// both runs two changes of a request.
func both(a, b func(r *http.Request)) func(r *http.Request) {
	return func(r *http.Request) { a(r); b(r) }
}

// rawPost sends a request over a plain TCP connection, so the test controls
// every byte: the Host (empty for the address of the server), the other
// headers, and a body that writeBody writes (nil for none). The body is written
// while the answer is read, because the server can answer before it has read
// the body. It returns the status of the answer.
func (h *httpEnv) rawPost(host string, headers []string, writeBody func(w io.Writer)) int {
	h.t.Helper()
	u, err := url.Parse(h.srv.URL)
	require.NoError(h.t, err)
	if host == "" {
		host = u.Host
	}
	conn, err := net.Dial("tcp", u.Host)
	require.NoError(h.t, err)
	defer func() { _ = conn.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w := bufio.NewWriter(conn)
		_, _ = fmt.Fprintf(w, "POST / HTTP/1.1\r\nHost: %s\r\n", host)
		for _, line := range headers {
			_, _ = fmt.Fprintf(w, "%s\r\n", line)
		}
		_, _ = w.WriteString("\r\n")
		if writeBody != nil {
			writeBody(w)
		}
		_ = w.Flush()
	}()

	require.NoError(h.t, conn.SetReadDeadline(time.Now().Add(time.Minute)))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(h.t, err)
	_ = resp.Body.Close()
	// Closing the connection ends a writer that the server stopped reading.
	_ = conn.Close()
	<-done
	return resp.StatusCode
}

// jsonHeaders are the headers of an MCP request over the transport.
var jsonHeaders = []string{"Content-Type: application/json", "Accept: application/json, text/event-stream"}

// ---------------------------------------------------------------- tokens

func TestHTTPRejectsMissingToken(t *testing.T) {
	h := newHTTPEnv(t, nil)
	valid := h.token("email:read")
	unknown := "emcp_" + strings.Repeat("a", 52)

	for name, change := range map[string]func(r *http.Request){
		"no header":          nil,
		"empty bearer":       func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") },
		"basic auth":         func(r *http.Request) { r.SetBasicAuth("user", valid) },
		"token without type": func(r *http.Request) { r.Header.Set("Authorization", valid) },
		"malformed token":    withToken("emcp_not-a-token"),
		"unknown token":      withToken(unknown),
		"upper-case token":   withToken(strings.ToUpper(valid)),
	} {
		t.Run(name, func(t *testing.T) {
			status, body := h.post(listBody, change)
			require.Equal(t, http.StatusUnauthorized, status)
			require.NotContains(t, body, valid)
			require.NotContains(t, body, unknown)
			require.NotContains(t, body, "not-a-token", "the answer does not repeat the token")
		})
	}
	require.Zero(t, h.servers.Load(), "no request without a valid token reached the MCP handler")

	status, _ := h.post(listBody, withToken(valid))
	require.Equal(t, http.StatusOK, status, "the valid token works")
}

// A malformed, an unknown and a revoked token get the same answer, so the answer
// does not tell a caller which one it had.
func TestHTTPBadTokensGetTheSameAnswer(t *testing.T) {
	h := newHTTPEnv(t, nil)
	revoked := h.token("email:read")
	list, err := h.tokens.List(context.Background(), h.tenant)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NoError(t, h.tokens.Revoke(context.Background(), h.tenant, list[0].ID))

	malformedStatus, malformedBody := h.post(listBody, withToken("emcp_not-a-token"))
	unknownStatus, unknownBody := h.post(listBody, withToken("emcp_"+strings.Repeat("a", 52)))
	revokedStatus, revokedBody := h.post(listBody, withToken(revoked))

	require.Equal(t, http.StatusUnauthorized, malformedStatus)
	require.Equal(t, malformedStatus, unknownStatus)
	require.Equal(t, malformedStatus, revokedStatus)
	require.Equal(t, malformedBody, unknownBody)
	require.Equal(t, malformedBody, revokedBody)
}

func TestHTTPRejectsRevokedToken(t *testing.T) {
	h := newHTTPEnv(t, nil)
	token := h.token("email:read")
	status, _ := h.post(listBody, withToken(token))
	require.Equal(t, http.StatusOK, status)

	list, err := h.tokens.List(context.Background(), h.tenant)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NoError(t, h.tokens.Revoke(context.Background(), h.tenant, list[0].ID))

	status, _ = h.post(listBody, withToken(token))
	require.Equal(t, http.StatusUnauthorized, status, "a revoked token fails at once")
}

// A failing database gives a 500 with a fixed text, never the token or the
// error of the database.
func TestHTTPTokenCheckFailure(t *testing.T) {
	h := newHTTPEnv(t, nil)
	token := h.token("email:read")
	require.NoError(t, h.store.Close())

	status, body := h.post(listBody, withToken(token))

	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, "internal error\n", body)
	require.Zero(t, h.servers.Load())
	require.Contains(t, h.logs.String(), "code=token_check_failed")
	require.NotContains(t, h.logs.String(), token)
	require.NotContains(t, h.logs.String(), "database is closed", "the log has the types of the error, not its text")
}

// ---------------------------------------------------------------- host and origin

func TestHTTPRejectsForeignOrigin(t *testing.T) {
	h := newHTTPEnv(t, nil)
	token := h.token("email:read")

	for _, origin := range []string{"https://evil.test", "null", "", "http://127.0.0.1:" + h.port()} {
		t.Run(origin, func(t *testing.T) {
			status, body := h.post(listBody, both(withToken(token), func(r *http.Request) {
				r.Header.Set("Origin", origin)
			}))
			require.Equal(t, http.StatusForbidden, status, "with no allow-list every Origin is refused")
			require.NotContains(t, body, "evil.test", "the answer does not repeat the origin")
		})
	}
	require.Zero(t, h.servers.Load())
}

func TestHTTPAllowsListedOrigin(t *testing.T) {
	// The entry is written in another case: it is compared after it is made
	// lower case, as a browser sends an origin.
	h := newHTTPEnv(t, []string{"HTTP://LocalHost:5173"})
	token := h.token("email:read")
	origin := func(value string) func(r *http.Request) {
		return func(r *http.Request) { r.Header.Set("Origin", value) }
	}

	status, _ := h.post(listBody, both(withToken(token), origin("http://localhost:5173")))
	require.Equal(t, http.StatusOK, status)

	status, _ = h.post(listBody, origin("http://localhost:5173"))
	require.Equal(t, http.StatusUnauthorized, status, "an allowed origin still needs a token")

	status, _ = h.post(listBody, both(withToken(token), origin("http://localhost:5174")))
	require.Equal(t, http.StatusForbidden, status, "origins are compared exactly")

	status, _ = h.post(listBody, both(withToken(token), func(r *http.Request) {
		r.Header.Add("Origin", "http://localhost:5173")
		r.Header.Add("Origin", "https://evil.test")
	}))
	require.Equal(t, http.StatusForbidden, status, "two Origin headers are refused")
}

func TestNormalizeOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:5173":   "http://localhost:5173",
		"HTTP://LocalHost:5173":   "http://localhost:5173",
		"https://app.example":     "https://app.example",
		"http://[::1]:8080":       "http://[::1]:8080",
		"vscode-webview://abc123": "vscode-webview://abc123",
	} {
		got, err := NormalizeOrigin(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{
		"", "null", "NULL", "*", "localhost:5173", "//localhost:5173", "http://", "http:///path",
		"http://localhost:5173/", "http://localhost:5173/app", "http://localhost:5173?x=1",
		"http://localhost:5173?", "http://localhost:5173#top", "http://localhost:5173#",
		"http://user@localhost:5173", "http://localhost:port", " http://localhost:5173",
		"http://localhost:5173 ", "mailto:me@example.com",
	} {
		_, err := NormalizeOrigin(in)
		require.Error(t, err, "%q", in)
	}
}

func TestHandlerRefusesABadAllowedOrigin(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	tm := tokens.New(st, nil)
	newServer := func() *mcp.Server { return nil }

	for _, origin := range []string{"null", "", "http://localhost:5173/", "localhost"} {
		require.Panics(t, func() { Handler(newServer, tm, []string{"http://ok.example", origin}) }, "%q", origin)
	}
	require.NotPanics(t, func() { Handler(newServer, tm, nil) })
}

func TestHTTPRejectsBadHost(t *testing.T) {
	h := newHTTPEnv(t, nil)
	token := h.token("email:read")
	port := h.port()
	otherPort := strconv.Itoa(mustAtoi(t, port) + 1)

	// The requests go over raw TCP: the HTTP client of Go would rewrite some of
	// these Host values (for example "[localhost]:1" becomes "localhost:1")
	// before it sends them.
	headers := append([]string{"Authorization: Bearer " + token, fmt.Sprintf("Content-Length: %d", len(listBody))}, jsonHeaders...)
	writeList := func(w io.Writer) { _, _ = io.WriteString(w, listBody) }
	for _, host := range []string{
		"evil.test",
		"evil.test:" + port,
		"localhost",
		"localhost:" + otherPort,
		"127.0.0.1:" + otherPort,
		"127.0.0.2:" + port,
		"LOCALHOST:" + port,
		"localhost.:" + port,
		"127.0.0.1.:" + port,
		"2130706433:" + port,
		"0.0.0.0:" + port,
		"[::ffff:127.0.0.1]:" + port,
		"localhost:0" + port,
		"[localhost]:" + port,
		"[127.0.0.1]:" + port,
		"[::1%25lo0]:" + port,
		"[::1%lo0]:" + port,
		"[0:0:0:0:0:0:0:1]:" + port,
	} {
		t.Run(host, func(t *testing.T) {
			require.Equal(t, http.StatusForbidden, h.rawPost(host, headers, writeList))
		})
	}
	require.Zero(t, h.servers.Load())

	for _, host := range []string{"127.0.0.1:" + port, "localhost:" + port, "[::1]:" + port} {
		t.Run(host, func(t *testing.T) {
			require.Equal(t, http.StatusOK, h.rawPost(host, headers, writeList))
		})
	}
}

// The answer to a refused Host repeats nothing of the request.
func TestHTTPBadHostAnswerIsFixed(t *testing.T) {
	h := newHTTPEnv(t, nil)
	status, body := h.post(listBody, both(withToken(h.token("email:read")), func(r *http.Request) { r.Host = "evil.test" }))
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "Forbidden\n", body)
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	require.NoError(t, err)
	return n
}

// The checks run in this order: Host, Origin, body size, token. A page that
// uses DNS rebinding gets 403 before any token handling.
func TestHTTPCheckOrder(t *testing.T) {
	h := newHTTPEnv(t, nil)
	token := h.token("email:read")
	badHost := func(r *http.Request) { r.Host = "evil.test" }
	badOrigin := func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }

	status, _ := h.post(listBody, both(withToken(token), badHost))
	require.Equal(t, http.StatusForbidden, status, "bad host with a valid token")
	status, _ = h.post(listBody, badHost)
	require.Equal(t, http.StatusForbidden, status, "bad host with no token: 403, not 401")
	status, _ = h.post(listBody, both(badHost, withToken("emcp_bad")))
	require.Equal(t, http.StatusForbidden, status, "bad host with a bad token: 403, not 401")
	status, _ = h.post(listBody, badOrigin)
	require.Equal(t, http.StatusForbidden, status, "bad origin with no token: 403, not 401")

	tooLarge := fmt.Sprintf("Content-Length: %d", MaxBodyBytes+1)
	require.Equal(t, http.StatusForbidden,
		h.rawPost("evil.test", append([]string{tooLarge}, jsonHeaders...), nil),
		"the host is checked before the body size")
	require.Equal(t, http.StatusRequestEntityTooLarge,
		h.rawPost("", append([]string{tooLarge}, jsonHeaders...), nil),
		"the body size is checked before the token")
	require.Zero(t, h.servers.Load())
	require.Zero(t, h.tools.Load())
}

// ---------------------------------------------------------------- body size

func TestHTTPRejectsLargeBody(t *testing.T) {
	h := newHTTPEnv(t, nil)
	token := h.token("email:read")
	authHeader := "Authorization: Bearer " + token

	t.Run("declared length", func(t *testing.T) {
		headers := append([]string{authHeader, fmt.Sprintf("Content-Length: %d", MaxBodyBytes+1)}, jsonHeaders...)
		require.Equal(t, http.StatusRequestEntityTooLarge, h.rawPost("", headers, nil))
	})
	t.Run("chunked", func(t *testing.T) {
		headers := append([]string{authHeader, "Transfer-Encoding: chunked"}, jsonHeaders...)
		status := h.rawPost("", headers, func(w io.Writer) {
			chunk := bytes.Repeat([]byte("a"), 1<<20)
			_, _ = io.WriteString(w, fmt.Sprintf("%x\r\n", len(listBody)-1)+listBody[:len(listBody)-1]+"\r\n")
			for written := 0; written <= MaxBodyBytes; written += len(chunk) {
				if _, err := fmt.Fprintf(w, "%x\r\n%s\r\n", len(chunk), chunk); err != nil {
					return
				}
			}
			_, _ = io.WriteString(w, "0\r\n\r\n")
		})
		require.Equal(t, http.StatusRequestEntityTooLarge, status)
	})
	require.Zero(t, h.servers.Load(), "nothing reached the MCP handler")
	require.Zero(t, h.tools.Load())
}

// A body of exactly 15 MB is allowed. The SDK alone would refuse a body over
// 4 MB, which is too small for a message with 10 MB of attachments.
func TestHTTPAcceptsBodyAtTheLimit(t *testing.T) {
	h := newHTTPEnv(t, nil)
	token := h.token("email:read")
	head := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"pad":"`
	tail := `"}}}`
	body := head + strings.Repeat("a", MaxBodyBytes-len(head)-len(tail)) + tail
	require.Len(t, body, MaxBodyBytes)

	status, answer := h.post(body, withToken(token))

	require.Equal(t, http.StatusOK, status)
	require.Contains(t, answer, "list_accounts")
}

// ---------------------------------------------------------------- MCP over HTTP

func TestHTTPStatelessClientCallsListAccounts(t *testing.T) {
	h := newHTTPEnv(t, nil)
	cs := h.connect(h.token("email:read"), nil, nil)

	res := call(t, cs, &mcp.CallToolParams{Name: "list_accounts"})

	require.False(t, res.IsError, textOf(t, res))
	list := structured(t, res)["accounts"].([]any)
	require.Len(t, list, 1)
	require.Equal(t, string(h.acct.ID), list[0].(map[string]any)["id"])

	// A second call works too: the server keeps no session between requests.
	res = call(t, cs, &mcp.CallToolParams{Name: "list_accounts"})
	require.False(t, res.IsError, textOf(t, res))
	require.Positive(t, h.servers.Load())
}

func TestHTTPTokenScopesLimitTools(t *testing.T) {
	h := newHTTPEnv(t, nil)
	cs := h.connect(h.token("email:read"), &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			t.Error("a refused send must not ask the user")
			return &mcp.ElicitResult{Action: "decline"}, nil
		},
	}, nil)

	res := call(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: h.sendArgs()})

	require.True(t, res.IsError)
	require.Contains(t, textOf(t, res), "The access token does not allow this action.",
		"the refusal is the PermissionError with TokenLimited")
	require.Empty(t, h.smtp.Messages())

	rows, err := h.store.ListAudit(context.Background(), h.tenant, store.AuditFilter{Tool: "send_message", Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "refused", rows[0].Outcome)
	require.Equal(t, "token_scope", rows[0].Detail)
	require.Equal(t, clientName, rows[0].Client, "the client name comes from the request")

	res = call(t, cs, &mcp.CallToolParams{Name: "list_accounts"})
	require.False(t, res.IsError, "the token can still read")
}

// The confirmation of a send works over stateless HTTP: the first call returns
// the input request and the request state, and the second call with an accepted
// answer sends. The server keeps nothing between the two requests.
func TestHTTPSendConfirmationInBand(t *testing.T) {
	h := newHTTPEnv(t, nil)
	token := h.token("email:read", "email:send")

	t.Run("by hand", func(t *testing.T) {
		cs := h.connect(token, &mcp.ClientOptions{
			ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				t.Error("the client answers the input request by hand")
				return &mcp.ElicitResult{Action: "decline"}, nil
			},
			MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
		}, nil)

		first := call(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: h.sendArgs()})
		require.False(t, first.IsError)
		require.True(t, first.NeedsInput(), "the first call asks for the confirmation")
		form, ok := first.InputRequests["confirm"].(*mcp.ElicitParams)
		require.True(t, ok, "the input request is an elicitation, got %T", first.InputRequests["confirm"])
		require.Contains(t, form.Message, "Send this email from me@example.com?")
		require.NotEmpty(t, first.RequestState)
		require.Empty(t, h.smtp.Messages(), "nothing is sent before the answer")

		second := call(t, cs, &mcp.CallToolParams{
			Name:      "send_message",
			Arguments: h.sendArgs(),
			InputResponses: mcp.InputResponseMap{"confirm": &mcp.ElicitResult{
				Action: "accept", Content: map[string]any{"confirm": true},
			}},
			RequestState: first.RequestState,
		})
		require.False(t, second.IsError, textOf(t, second))
		require.Equal(t, "sent", structured(t, second)["status"])
		require.Len(t, h.smtp.Messages(), 1)
	})

	t.Run("by the SDK", func(t *testing.T) {
		var asked atomic.Int32
		cs := h.connect(token, &mcp.ClientOptions{
			ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked.Add(1)
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
			},
		}, nil)

		res := call(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: h.sendArgs()})

		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, "sent", structured(t, res)["status"])
		require.Equal(t, int32(1), asked.Load(), "the user was asked once")
		require.Len(t, h.smtp.Messages(), 2)
	})

	rows, err := h.store.ListAudit(context.Background(), h.tenant, store.AuditFilter{Tool: "send_message", Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		require.Equal(t, clientName, row.Client)
	}
}

// A client of an older protocol version cannot be asked in band over stateless
// HTTP: the server cannot send it a request. It gets the fallback.
func TestHTTPSendFromAnOlderClientFallsBack(t *testing.T) {
	h := newHTTPEnv(t, nil)
	var asked atomic.Int32
	cs := h.connect(h.token("email:send"), &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			asked.Add(1)
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
		},
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})

	res := call(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: h.sendArgs()})

	require.False(t, res.IsError, textOf(t, res))
	require.Equal(t, "pending_approval", structured(t, res)["status"])
	require.Zero(t, asked.Load())
	require.Empty(t, h.smtp.Messages())
}

// ---------------------------------------------------------------- logs

func TestHTTPLogsHoldNoSecrets(t *testing.T) {
	h := newHTTPEnv(t, nil)
	readToken := h.token("email:read")
	sendToken := h.token("email:send")
	unknown := "emcp_" + strings.Repeat("b", 51) + "a"

	cs := h.connect(sendToken, &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
		},
	}, nil)
	res := call(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: h.sendArgs()})
	require.Equal(t, "sent", structured(t, res)["status"])
	refused := call(t, h.connect(readToken, nil, nil), &mcp.CallToolParams{Name: "send_message", Arguments: h.sendArgs()})
	require.True(t, refused.IsError)

	h.post(listBody, withToken(unknown))
	h.post(listBody, withToken("emcp_malformed-secret-value"))
	h.post(listBody, both(withToken(readToken), func(r *http.Request) { r.Host = "evil.test" }))
	h.post(listBody, both(withToken(readToken), func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }))
	h.rawPost("", append([]string{"Authorization: Bearer " + readToken, fmt.Sprintf("Content-Length: %d", MaxBodyBytes+1)}, jsonHeaders...), nil)
	h.post(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"secret":"body-secret-value"}}}`, withToken(readToken))

	logs := h.logs.String()
	for _, code := range []string{"code=bad_host", "code=bad_origin", "code=body_too_large", "code=invalid_token"} {
		require.Contains(t, logs, code, "the refusals are logged with a code")
	}
	for _, secret := range []string{
		readToken, sendToken, unknown,
		strings.TrimPrefix(readToken, "emcp_"), strings.TrimPrefix(sendToken, "emcp_"),
		"malformed-secret-value", "body-secret-value",
		"Bearer", "Authorization", "authorization",
		"evil.test", "you@example.com", "me@example.com", "Lunch", "noon",
	} {
		require.NotContains(t, logs, secret)
	}
}

// ---------------------------------------------------------------- Principal

func TestPrincipal(t *testing.T) {
	withInfo := func(info *auth.TokenInfo, meta mcp.Meta) *mcp.CallToolRequest {
		return &mcp.CallToolRequest{
			Params: &mcp.CallToolParamsRaw{Name: "list_accounts", Meta: meta},
			Extra:  &mcp.RequestExtra{TokenInfo: info},
		}
	}

	t.Run("refused", func(t *testing.T) {
		for name, req := range map[string]*mcp.CallToolRequest{
			"nil request":   nil,
			"no extra":      {Params: &mcp.CallToolParamsRaw{Name: "list_accounts"}},
			"no token info": withInfo(nil, nil),
			"no user id":    withInfo(&auth.TokenInfo{Scopes: []string{"email:read"}}, nil),
		} {
			t.Run(name, func(t *testing.T) {
				_, err := Principal(context.Background(), req)
				require.ErrorIs(t, err, ErrNoToken)
			})
		}
	})

	t.Run("tenant, scopes and client", func(t *testing.T) {
		info := &auth.TokenInfo{UserID: "local", Scopes: []string{"email:read"}}
		meta := mcp.Meta{mcp.MetaKeyClientInfo: map[string]any{"name": "some client", "version": "2"}}

		p, err := Principal(context.Background(), withInfo(info, meta))

		require.NoError(t, err)
		require.Equal(t, service.Principal{Tenant: "local", Scopes: policy.Scopes{"email:read"}, Client: "some client"}, p)
		p.Scopes[0] = "email:delete"
		require.Equal(t, []string{"email:read"}, info.Scopes, "the principal has its own copy of the scopes")
	})

	t.Run("no scopes is nothing, never no limit", func(t *testing.T) {
		p, err := Principal(context.Background(), withInfo(&auth.TokenInfo{UserID: "local"}, nil))
		require.NoError(t, err)
		require.NotNil(t, p.Scopes)
		require.Empty(t, p.Scopes)
		require.Empty(t, p.Client)
	})
}

// ---------------------------------------------------------------- listen address

func TestCheckLoopbackAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:1", "127.0.0.1:0", "127.1.2.3:8080", "[::1]:8080", "localhost:8080", "127.0.0.1:65535"} {
		require.NoError(t, CheckLoopbackAddr(addr), addr)
	}
	for _, addr := range []string{
		"0.0.0.0:1", "[::]:1", ":8080", "192.168.1.10:8080", "example.com:8080", "localhost", "127.0.0.1",
		"127.0.0.1:", "127.0.0.1:http", "127.0.0.1:65536", "127.0.0.1:-1", "[::ffff:127.0.0.1]:8080",
		"[::1%lo0]:8080", "LOCALHOST:8080", "localhost.:8080", "127.1:8080", "", "[::1]",
		"[127.0.0.1]:8080", "[localhost]:8080", "[0:0:0:0:0:0:0:1]:8080", "127.0.0.1.:8080", "2130706433:8080",
	} {
		require.Error(t, CheckLoopbackAddr(addr), addr)
	}
}
