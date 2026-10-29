package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/app"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// ---------------------------------------------------------------- helpers

// syncBuffer is a bytes.Buffer that many goroutines may write to.
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

// prepareDataDir makes a data directory with a key file and one account of the
// local tenant, as "email-mcp account add" would. The account is at Mail 4 and
// confirms sends. Its servers do not exist: the tests never connect to them.
func prepareDataDir(t *testing.T) (dir, keyPath string, acct *account.Account) {
	t.Helper()
	ctx := context.Background()
	dir = t.TempDir()
	keyPath = filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, secrets.GenerateKeyFile(keyPath))
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	tenant, err := st.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	kek, err := secrets.NewFileKEK(keyPath)
	require.NoError(t, err)
	vault := secrets.NewVault(st, kek)

	acct = account.NewIMAPAccount(tenant, "me@example.com", account.MailServer{
		IMAPHost: "imap.example.com", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.example.com", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
		Username: "me@example.com",
	}, time.Now())
	require.NoError(t, st.CreateAccount(ctx, acct))
	version, sealed, err := vault.Encrypt(ctx, tenant, secrets.AAD(string(tenant), string(acct.ID), "password"), []byte("pw-6c1d"))
	require.NoError(t, err)
	require.NoError(t, st.PutAccountSecret(ctx, tenant, acct.ID, "password", version, sealed))
	return dir, keyPath, acct
}

// sendArgs are the arguments of a simple send from the account.
func sendArgs(acct *account.Account) map[string]any {
	return map[string]any{
		"account": string(acct.ID),
		"to":      []any{"you@example.com"},
		"subject": "Lunch",
		"body":    "Shall we meet at noon?",
	}
}

// structured returns the structured content of a result as a JSON object.
func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	m, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "structured content must be an object, got %T", res.StructuredContent)
	return m
}

// textOf returns the text of the first content block of a result.
func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok, "the first content block must be text, got %T", res.Content[0])
	return tc.Text
}

// stdioServer is "email-mcp serve" running in a goroutine, with pipes for its
// standard input and output.
type stdioServer struct {
	stdout *syncBuffer // a copy of everything the server wrote to standard output
	stderr *syncBuffer
	code   chan int
	cancel context.CancelFunc
	// toServer is the write end of the standard input of the server.
	toServer *io.PipeWriter
	// fromServer is the read end of the standard output of the server.
	fromServer *io.PipeReader
}

// startServe runs "email-mcp serve" with the arguments. The key source comes
// from EMAIL_MCP_KEK, which the test sets.
func startServe(t *testing.T, args ...string) *stdioServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &stdioServer{stdout: &syncBuffer{}, stderr: &syncBuffer{}, code: make(chan int, 1), cancel: cancel,
		toServer: inW, fromServer: outR}
	go func() {
		code := run(ctx, append([]string{"serve"}, args...), inR, io.MultiWriter(outW, s.stdout), s.stderr)
		_ = outW.Close()
		s.code <- code
	}()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		_ = outR.Close()
	})
	return s
}

// connect connects a go-sdk client over the pipes of the server.
func (s *stdioServer) connect(t *testing.T, opts *mcp.ClientOptions, session *mcp.ClientSessionOptions) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "stdio-test-client", Version: "1"}, opts)
	cs, err := c.Connect(context.Background(), &mcp.IOTransport{Reader: s.fromServer, Writer: s.toServer}, session)
	require.NoError(t, err)
	return cs
}

// wait returns the exit code of run.
func (s *stdioServer) wait(t *testing.T) int {
	t.Helper()
	select {
	case code := <-s.code:
		return code
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not stop")
		return -1
	}
}

// requireOnlyProtocolOnStdout checks that every line on standard output is a
// JSON-RPC message: nothing else may be written there.
func requireOnlyProtocolOnStdout(t *testing.T, stdout string) {
	t.Helper()
	require.NotEmpty(t, stdout)
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var msg map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &msg), "standard output has a line that is not JSON: %q", sc.Text())
		require.Equal(t, "2.0", msg["jsonrpc"], "standard output has a line that is not JSON-RPC: %q", sc.Text())
	}
	require.NoError(t, sc.Err())
}

// ---------------------------------------------------------------- stdio

func TestServeStdioRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "master.key")
	t.Setenv("EMAIL_MCP_KEK", "file:"+keyPath)
	s := startServe(t, "--data-dir", dir)
	cs := s.connect(t, nil, nil)

	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 20)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "connect_account"})
	require.NoError(t, err)
	require.False(t, res.IsError, textOf(t, res))
	link, _ := structured(t, res)["link"].(string)
	require.Regexp(t, regexp.MustCompile(`^http://127\.0\.0\.1:\d+/connect$`), link)
	require.NotContains(t, link, "token=")

	// The web server of the link runs while the server runs.
	resp, err := http.Get(link) //nolint:gosec // G107: the URL is the local link of the test server
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a browser without a session sees only the instruction")

	// The client leaves: standard input ends, and the server stops cleanly.
	require.NoError(t, cs.Close())
	require.Equal(t, 0, s.wait(t))

	requireOnlyProtocolOnStdout(t, s.stdout.String())
	require.NotContains(t, s.stderr.String(), link, "the log never has a link")
	_, err = os.Stat(keyPath)
	require.NoError(t, err, "the first start makes the master key file")
}

// On stdio every call runs as the local principal (no scope limit), and a client
// of an older protocol version is asked in band with elicitation/create.
func TestServeStdioUsesTheLocalPrincipalAndAsksOlderClients(t *testing.T) {
	dir, keyPath, acct := prepareDataDir(t)
	t.Setenv("EMAIL_MCP_KEK", "file:"+keyPath)
	s := startServe(t, "--data-dir", dir)
	var asked atomic.Int32
	cs := s.connect(t, &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			asked.Add(1)
			return &mcp.ElicitResult{Action: "decline"}, nil
		},
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_accounts"})
	require.NoError(t, err)
	require.False(t, res.IsError, textOf(t, res))
	require.Contains(t, textOf(t, res), string(acct.ID))

	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "send_message", Arguments: sendArgs(acct)})
	require.NoError(t, err)
	require.False(t, res.IsError, textOf(t, res))
	require.Equal(t, "declined", structured(t, res)["status"])
	require.Equal(t, int32(1), asked.Load(), "the user was asked in band")

	require.NoError(t, cs.Close())
	require.Equal(t, 0, s.wait(t))
	requireOnlyProtocolOnStdout(t, s.stdout.String())
	require.NotContains(t, s.stderr.String(), "you@example.com", "the log never has an address")
}

// A signal (here: the end of the context of run) stops the server with exit
// code 0, also while the client is still connected.
func TestServeStdioStopsOnSignal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EMAIL_MCP_KEK", "file:"+filepath.Join(t.TempDir(), "master.key"))
	s := startServe(t, "--data-dir", dir)
	cs := s.connect(t, nil, nil)
	t.Cleanup(func() { _ = cs.Close() })
	_, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	s.cancel()

	require.Equal(t, 0, s.wait(t))
}

// ---------------------------------------------------------------- HTTP

func TestServeHTTPRefusesPublicBind(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8765", "[::]:8765", ":8765", "192.168.1.10:8765", "example.com:80", "127.0.0.1"} {
		t.Run(addr, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			var out, errOut syncBuffer

			code := run(context.Background(), []string{"serve", "--http", addr, "--data-dir", dir}, strings.NewReader(""), &out, &errOut)

			require.Equal(t, exitUsage, code)
			require.Contains(t, errOut.String(), addr)
			require.Empty(t, out.String())
			_, err := os.Stat(dir)
			require.ErrorIs(t, err, os.ErrNotExist, "the address is checked before anything is opened")
		})
	}
}

func TestServeRefusesBadOrigins(t *testing.T) {
	for name, args := range map[string][]string{
		"origin with a path":    {"--http", "127.0.0.1:0", "--allow-origin", "http://localhost:5173/app"},
		"null origin":           {"--http", "127.0.0.1:0", "--allow-origin", "null"},
		"origin without --http": {"--allow-origin", "http://localhost:5173"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			var out, errOut syncBuffer

			code := run(context.Background(), append([]string{"serve", "--data-dir", dir}, args...), strings.NewReader(""), &out, &errOut)

			require.Equal(t, exitUsage, code)
			require.Contains(t, errOut.String(), "origin")
			_, err := os.Stat(dir)
			require.ErrorIs(t, err, os.ErrNotExist, "the origins are checked before anything is opened")
		})
	}
}

// httpServe is serveHTTP running in a goroutine on a loopback listener.
type httpServe struct {
	app    *app.App
	url    string // the address of the MCP endpoint
	base   string
	cancel context.CancelFunc
	done   chan error
}

func startHTTP(t *testing.T, dir, keyPath string) *httpServe {
	t.Helper()
	a, err := app.Open(context.Background(), app.Config{DataDir: dir, KEKSource: "file:" + keyPath, Local: true, LogOutput: &syncBuffer{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	h := &httpServe{app: a, base: "http://" + ln.Addr().String(), cancel: cancel, done: make(chan error, 1)}
	h.url = h.base + mcpPath
	go func() { h.done <- serveHTTP(ctx, a, ln, nil) }()
	t.Cleanup(cancel)
	return h
}

// bearer adds a token to every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (h *httpServe) connect(t *testing.T, token string, opts *mcp.ClientOptions, session *mcp.ClientSessionOptions) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "http-test-client", Version: "1"}, opts)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   h.url,
		HTTPClient: &http.Client{Transport: bearer{token: token}},
	}, session)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (h *httpServe) token(t *testing.T, scopes ...string) string {
	t.Helper()
	token, err := h.app.Tokens.Create(context.Background(), store.LocalTenantID, "test", policy.Scopes(scopes))
	require.NoError(t, err)
	return token
}

// Over HTTP the caller is the token: its scopes limit what it can do. The stdio
// principal (no scope limit) must never be used here.
func TestServeHTTPUsesTheTokenOfTheRequest(t *testing.T) {
	dir, keyPath, acct := prepareDataDir(t)
	h := startHTTP(t, dir, keyPath)
	cs := h.connect(t, h.token(t, "email:read"), nil, nil)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "send_message", Arguments: sendArgs(acct)})
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, textOf(t, res), "The access token does not allow this action.")

	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_accounts"})
	require.NoError(t, err)
	require.False(t, res.IsError, "the token can still read")

	// Without a token, nothing is served.
	resp, err := http.Post(h.url, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	// The MCP endpoint is only at its path.
	resp, err = http.Get(h.base + "/")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	h.cancel()
	require.NoError(t, <-h.done)
}

// Stateless HTTP cannot send a request to the client, so a client of an older
// protocol version is never asked in band. It gets the approval link instead.
func TestServeHTTPNeverAsksOlderClientsInBand(t *testing.T) {
	dir, keyPath, acct := prepareDataDir(t)
	h := startHTTP(t, dir, keyPath)
	var asked atomic.Int32
	cs := h.connect(t, h.token(t, "email:send"), &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			asked.Add(1)
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
		},
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "send_message", Arguments: sendArgs(acct)})

	require.NoError(t, err)
	require.False(t, res.IsError, textOf(t, res))
	out := structured(t, res)
	require.Equal(t, "pending_approval", out["status"])
	require.Regexp(t, `^http://127\.0\.0\.1:\d+/approvals/`, out["approval_link"])
	require.Zero(t, asked.Load())
}

// "serve --http" serves on the address and stops with exit code 0 on a signal.
func TestServeHTTPCommand(t *testing.T) {
	dir, keyPath, _ := prepareDataDir(t)
	t.Setenv("EMAIL_MCP_KEK", "file:"+keyPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stderr := &syncBuffer{}
	code := make(chan int, 1)
	go func() {
		code <- run(ctx, []string{"serve", "--http", "127.0.0.1:0", "--data-dir", dir}, strings.NewReader(""), io.Discard, stderr)
	}()

	addrPattern := regexp.MustCompile(`address=(127\.0\.0\.1:\d+)`)
	var addr string
	require.Eventually(t, func() bool {
		m := addrPattern.FindStringSubmatch(stderr.String())
		if m != nil {
			addr = m[1]
		}
		return m != nil
	}, 10*time.Second, 10*time.Millisecond, "the log names the address")

	resp, err := http.Post("http://"+addr+mcpPath, "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the endpoint answers, and needs a token")

	cancel()
	select {
	case c := <-code:
		require.Equal(t, 0, c)
	case <-time.After(30 * time.Second):
		t.Fatal("serve --http did not stop")
	}
}

func TestHTTPServerHasLimits(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler(), slogDiscard())

	require.Positive(t, srv.ReadHeaderTimeout)
	require.Positive(t, srv.ReadTimeout)
	require.Positive(t, srv.IdleTimeout)
	require.Positive(t, srv.MaxHeaderBytes)
	// A send without an undo window runs inside the request for up to 13
	// minutes, and its answer must still go out.
	require.GreaterOrEqual(t, srv.WriteTimeout, 14*time.Minute)
	require.NotNil(t, srv.ErrorLog)
}

// At most n requests run at once. Each one can hold its 15 MB body twice. A
// request over the limit waits for a free place, and gets 503 when none comes.
func TestLimitRequests(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	h := limitRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			started <- struct{}{}
			<-release
		}
		w.WriteHeader(http.StatusOK)
	}), 1, 100*time.Millisecond, slogDiscard())

	slow := make(chan int, 1)
	go func() {
		rec := newRecorder()
		h.ServeHTTP(rec, mustRequest(t, "/slow"))
		slow <- rec.code
	}()
	<-started

	rec := newRecorder()
	h.ServeHTTP(rec, mustRequest(t, "/fast"))
	require.Equal(t, http.StatusServiceUnavailable, rec.code)
	require.NotEmpty(t, rec.header.Get("Retry-After"))

	close(release)
	require.Equal(t, http.StatusOK, <-slow)
	rec = newRecorder()
	h.ServeHTTP(rec, mustRequest(t, "/fast"))
	require.Equal(t, http.StatusOK, rec.code, "the place is free again")
}

// slogDiscard returns a logger that drops every line.
func slogDiscard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// recorder is a small http.ResponseWriter that is safe to read after the
// handler returned in another goroutine.
type recorder struct {
	header http.Header
	code   int
}

func newRecorder() *recorder { return &recorder{header: http.Header{}, code: http.StatusOK} }

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(p []byte) (int, error) { return len(p), nil }
func (r *recorder) WriteHeader(code int)        { r.code = code }

func mustRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, "http://127.0.0.1"+path, nil)
	require.NoError(t, err)
	return r
}
