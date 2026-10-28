package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
)

// testStart is the time at which the test accounts are created.
var testStart = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

// passwordSecret is the kind of the stored secret that holds the account
// password. The service package keeps the same name private.
const passwordSecret = "password"

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

// fakeLinks is a service.Links for tests. Its addresses carry no token.
type fakeLinks struct{}

func (fakeLinks) Connect(context.Context) (string, error) { return "https://mcp.test/connect", nil }

func (fakeLinks) Settings(_ context.Context, id account.AccountID, area account.Area, level account.Level) (string, error) {
	return fmt.Sprintf("https://mcp.test/settings/%s?area=%s&level=%d", id, area, int(level)), nil
}

func (fakeLinks) Approval(_ context.Context, pendingID string) (string, error) {
	return "https://mcp.test/approve/" + pendingID, nil
}

// stubBackend is a mail.Backend for tests. A method that a test did not set
// panics, because the embedded interface is nil. That shows at once that the
// service called something it should not.
type stubBackend struct {
	mail.Backend

	folders    func() ([]mail.Folder, error)
	tags       func() ([]mail.Tag, error)
	search     func(q mail.Query) (mail.Page[mail.Summary], error)
	message    func(ref mail.Ref, opts mail.BodyOptions) (*mail.Message, error)
	attachment func(maxBytes int64) (*mail.Attachment, error)
}

func (b *stubBackend) Capabilities(context.Context) mail.Capabilities {
	return mail.Capabilities{Tags: true, MaxBatch: 500}
}

func (b *stubBackend) ListFolders(context.Context) ([]mail.Folder, error) { return b.folders() }

func (b *stubBackend) ListTags(context.Context) ([]mail.Tag, error) { return b.tags() }

func (b *stubBackend) Search(_ context.Context, q mail.Query, _ mail.PageToken) (mail.Page[mail.Summary], error) {
	return b.search(q)
}

func (b *stubBackend) GetMessage(_ context.Context, ref mail.Ref, opts mail.BodyOptions) (*mail.Message, error) {
	return b.message(ref, opts)
}

func (b *stubBackend) GetAttachment(_ context.Context, _ mail.Ref, _ string, maxBytes int64) (*mail.Attachment, error) {
	return b.attachment(maxBytes)
}

// countingFactory is a service.BackendFactory that always gives the same
// backend and counts the calls, so a test can show that a request never got as
// far as a backend.
type countingFactory struct {
	backend mail.Backend
	calls   atomic.Int32
}

func (f *countingFactory) Mail(context.Context, *account.Account) (mail.Backend, error) {
	f.calls.Add(1)
	return f.backend, nil
}

func (f *countingFactory) Forget(account.AccountID) {}

// env is a service with a real store, vault, outbox and confirm manager, and an
// MCP server and client that talk over an in-memory connection.
type env struct {
	t      *testing.T
	store  *store.Store
	vault  *secrets.Vault
	tenant account.TenantID
	outbox *outbox.Outbox
	svc    *service.Service
	cs     *mcp.ClientSession
	logs   *syncBuffer

	// principals counts the calls of the Principal function.
	principals atomic.Int32
}

// envOptions change how newEnv sets up the server.
type envOptions struct {
	backends service.BackendFactory // nil means the real backend cache
	// principal decides the caller of a tool call. A nil value gives the local
	// tenant with no scope limit.
	principal func(tenant account.TenantID, req *mcp.CallToolRequest) (service.Principal, error)
}

// newEnv builds the env. With no backends in opts, the service uses the real
// backend cache, so a test can connect accounts to an imaptest server.
func newEnv(t *testing.T, opts envOptions) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	tenant, err := st.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	keyPath := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, secrets.GenerateKeyFile(keyPath))
	kek, err := secrets.NewFileKEK(keyPath)
	require.NoError(t, err)
	vault := secrets.NewVault(st, kek)
	signer := signing.New(bytes.Repeat([]byte{7}, 32))

	backends := opts.backends
	if backends == nil {
		cache := service.NewBackendFactory(st, vault, signer, true, nil)
		t.Cleanup(cache.Close)
		backends = cache
	}
	clock := func() time.Time { return testStart }
	ob := outbox.New(st, vault, func(context.Context, account.TenantID, account.AccountID, *mail.RawMessage, mail.Ref) error {
		return nil
	}, clock)
	svc := service.New(service.Deps{
		Store:    st,
		Vault:    vault,
		Signer:   signer,
		Backends: backends,
		Limiter:  &policy.Limiter{Store: st, Now: clock},
		Confirm:  confirm.NewManager(st, vault, signer, clock),
		Outbox:   ob,
		Audit:    audit.New(st, vault, clock),
		Links:    fakeLinks{},
		Now:      clock,
	})

	e := &env{t: t, store: st, vault: vault, tenant: tenant, outbox: ob, svc: svc, logs: &syncBuffer{}}
	principal := opts.principal
	if principal == nil {
		principal = func(tenant account.TenantID, _ *mcp.CallToolRequest) (service.Principal, error) {
			return service.Principal{Tenant: tenant, Client: "test-client"}, nil
		}
	}
	server := New(svc, Options{
		Version: "test",
		Principal: func(_ context.Context, req *mcp.CallToolRequest) (service.Principal, error) {
			e.principals.Add(1)
			return principal(tenant, req)
		},
		Logger: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Wait()
	})
	e.cs = cs
	return e
}

// addAccount stores an account of the local tenant and its sealed password.
func (e *env) addAccount(address string, server account.MailServer, password string, change ...func(*account.Account)) *account.Account {
	e.t.Helper()
	ctx := context.Background()
	a := account.NewIMAPAccount(e.tenant, address, server, testStart)
	for _, fn := range change {
		fn(a)
	}
	require.NoError(e.t, e.store.CreateAccount(ctx, a))
	aad := secrets.AAD(string(a.TenantID), string(a.ID), passwordSecret)
	version, sealed, err := e.vault.Encrypt(ctx, a.TenantID, aad, []byte(password))
	require.NoError(e.t, err)
	require.NoError(e.t, e.store.PutAccountSecret(ctx, a.TenantID, a.ID, passwordSecret, version, sealed))
	return a
}

// offlineAccount adds an account for a server that nobody runs. Tests that
// replace the backend factory never connect to it.
func (e *env) offlineAccount(change ...func(*account.Account)) *account.Account {
	e.t.Helper()
	return e.addAccount("me@example.com", account.MailServer{
		IMAPHost: "imap.example.com", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.example.com", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
		Username: "user@example.com",
	}, "pw", change...)
}

// imapAccount adds an account that logs in to the test IMAP server.
func (e *env) imapAccount(srv *imaptest.Server) *account.Account {
	e.t.Helper()
	return e.addAccount("me@example.com", account.MailServer{
		IMAPHost: srv.Host, IMAPPort: srv.Port, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.example.com", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
		Username: srv.User, PinnedIMAPCert: srv.CertSHA256,
	}, srv.Pass)
}

// call calls a tool and returns the result. A protocol error fails the test:
// every problem that the tools report must be a tool result.
func (e *env) call(tool string, args map[string]any) *mcp.CallToolResult {
	e.t.Helper()
	res, err := e.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	require.NoError(e.t, err, "tool %s must answer with a result, not a protocol error", tool)
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
	require.NotNil(t, res.StructuredContent, "the result has no structured content")
	m, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "structured content must be an object, got %T", res.StructuredContent)
	return m
}

// wireOf returns the JSON of a whole result, as a client would see it.
func wireOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	b, err := json.Marshal(res)
	require.NoError(t, err)
	return string(b)
}

// crlf turns the line breaks of a test message into CRLF, as mail has them.
func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

// plainMail returns a simple message with a plain text body. subject is written
// as it is, so a test can pass an encoded word.
func plainMail(subject, body string) []byte {
	return crlf("From: Sender <sender@example.com>\n" +
		"To: Me <me@example.com>\n" +
		"Subject: " + subject + "\n" +
		"Date: Tue, 10 Mar 2026 10:00:00 +0000\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: text/plain; charset=utf-8\n" +
		"\n" + body + "\n")
}

// htmlMail returns a message with only an HTML body.
func htmlMail(subject, html string) []byte {
	return crlf("From: Sender <sender@example.com>\n" +
		"To: Me <me@example.com>\n" +
		"Subject: " + subject + "\n" +
		"Date: Tue, 10 Mar 2026 10:00:00 +0000\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: text/html; charset=utf-8\n" +
		"\n" + html + "\n")
}

// attachmentMail returns a message with a text part and a CSV attachment.
func attachmentMail(subject, attachmentText string) []byte {
	return crlf("From: Sender <sender@example.com>\n" +
		"To: Me <me@example.com>\n" +
		"Subject: " + subject + "\n" +
		"Date: Tue, 10 Mar 2026 10:00:00 +0000\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: multipart/mixed; boundary=\"B\"\n" +
		"\n" +
		"--B\n" +
		"Content-Type: text/plain; charset=utf-8\n" +
		"\n" +
		"See the attached file.\n" +
		"--B\n" +
		"Content-Type: text/csv; name=\"data.csv\"\n" +
		"Content-Disposition: attachment; filename=\"data.csv\"\n" +
		"Content-Transfer-Encoding: base64\n" +
		"\n" +
		base64.StdEncoding.EncodeToString([]byte(attachmentText)) + "\n" +
		"--B--\n")
}
