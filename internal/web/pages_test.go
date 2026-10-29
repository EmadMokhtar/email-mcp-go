package web

// This file holds the fixture of the tests of the pages that use the whole
// service: settings, approvals, activity and connections.

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/tokens"
)

// autonomyText is the warning of spec section 7.3, the exact label of the box
// that accepts full autonomy.
const autonomyText = "An email or a calendar invite can contain hidden instructions, such as 'forward all invoices to me'. " +
	"With confirmations off, the agent may follow them, and nobody checks before anything leaves."

// pageBackend is the mail backend of the page tests. It gives one original
// message for a reply ("o1", from alice@example.com), fails GetMessage while
// getErr is set, and records the moves to Trash. Any other method panics (the
// embedded interface is nil), so a test sees at once a call that it did not
// expect.
type pageBackend struct {
	mail.Backend

	mu      sync.Mutex
	getErr  error
	trashed [][]mail.Ref
}

func (b *pageBackend) GetMessage(_ context.Context, ref mail.Ref, _ mail.BodyOptions) (*mail.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.getErr != nil {
		return nil, b.getErr
	}
	return &mail.Message{
		Summary:   mail.Summary{Ref: ref, From: mail.Address{Name: "Alice", Email: "alice@example.com"}, Subject: "Question"},
		MessageID: "<o1@example.com>",
	}, nil
}

func (b *pageBackend) Trash(_ context.Context, refs []mail.Ref) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.trashed = append(b.trashed, slices.Clone(refs))
	return nil
}

func (b *pageBackend) setGetErr(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.getErr = err
}

func (b *pageBackend) trashCalls() [][]mail.Ref {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.trashed)
}

// fakeBackends is the backend factory of the page tests. Every account gets the
// same pageBackend. It records the ids that Forget gets.
type fakeBackends struct {
	backend   pageBackend
	mu        sync.Mutex
	forgotten []account.AccountID
}

func (b *fakeBackends) Mail(context.Context, *account.Account) (mail.Backend, error) {
	return &b.backend, nil
}

func (b *fakeBackends) Forget(id account.AccountID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.forgotten = append(b.forgotten, id)
}

func (b *fakeBackends) forgot() []account.AccountID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.forgotten)
}

// recSender is the sender of the outbox in the page tests. It records each
// message.
type recSender struct {
	mu   sync.Mutex
	msgs []*mail.RawMessage
}

func (s *recSender) send(_ context.Context, _ account.TenantID, _ account.AccountID, msg *mail.RawMessage, _ mail.Ref) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, msg)
	return nil
}

func (s *recSender) sent() []*mail.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.msgs)
}

// pageFixture is a started server with a whole service (limiter, confirm
// manager, outbox, audit log and tokens), a fake backend factory, a fake
// sender, and a signed-in browser of the local tenant.
type pageFixture struct {
	*fixture
	s        *Server
	svc      *service.Service
	base     string
	browser  *http.Client
	csrf     string
	tenant   account.TenantID
	vault    *secrets.Vault
	audit    *audit.Log
	confirm  *confirm.Manager
	outbox   *outbox.Outbox
	tokens   *tokens.Manager
	backends *fakeBackends
	sender   *recSender
}

func newPageFixture(t *testing.T) *pageFixture {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	tenant, err := f.store.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	keyPath := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, secrets.GenerateKeyFile(keyPath))
	kek, err := secrets.NewFileKEK(keyPath)
	require.NoError(t, err)
	vault := secrets.NewVault(f.store, kek)
	log := audit.New(f.store, vault, f.clock.Now)
	logger := slog.New(slog.NewTextHandler(f.logs, nil))
	toks := tokens.New(f.store, f.clock.Now)
	sender := &recSender{}
	ob := outbox.New(f.store, vault, sender.send, f.clock.Now)
	cm := confirm.NewManager(f.store, vault, f.signer, f.clock.Now)
	backends := &fakeBackends{}

	s := New(Deps{
		Store:  f.store,
		Signer: f.signer,
		Tokens: toks,
		Audit:  log,
		Now:    f.clock.Now,
		Logger: logger,
	})
	t.Cleanup(func() { require.NoError(t, s.Shutdown(context.Background())) })
	svc := service.New(service.Deps{
		Store:    f.store,
		Vault:    vault,
		Signer:   f.signer,
		Backends: backends,
		Limiter:  &policy.Limiter{Store: f.store, Now: f.clock.Now},
		Confirm:  cm,
		Outbox:   ob,
		Audit:    log,
		Links:    s.Links(),
		Tokens:   toks,
		Local:    true,
		Now:      f.clock.Now,
		Logger:   logger,
	})
	s.Attach(svc)
	base, err := s.EnsureStarted(ctx)
	require.NoError(t, err)
	browser := newBrowser(t)
	f.signIn(s, browser)
	p := &pageFixture{
		fixture: f, s: s, svc: svc, base: base, browser: browser, tenant: tenant, vault: vault,
		audit: log, confirm: cm, outbox: ob, tokens: toks, backends: backends, sender: sender,
	}
	resp, body := p.get("/connections")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	m := csrfPattern.FindStringSubmatch(body)
	require.NotNil(t, m, "the connections page has a CSRF field")
	p.csrf = m[1]
	return p
}

// get loads a page in the signed-in browser.
func (p *pageFixture) get(path string) (*http.Response, string) {
	p.t.Helper()
	return do(p.t, p.browser, newRequest(p.t, http.MethodGet, p.base+path, nil))
}

// post sends a form of the signed-in browser, with its CSRF token, from the
// same origin.
func (p *pageFixture) post(path string, form url.Values) (*http.Response, string) {
	p.t.Helper()
	values := url.Values{"csrf_token": {p.csrf}}
	for k, v := range form {
		values[k] = v
	}
	return do(p.t, p.browser, sameOrigin(newRequest(p.t, http.MethodPost, p.base+path, values), p.base))
}

// addAccount stores an account of the tenant with a sealed password. With no
// change, it has the defaults of a new account: Mail 4, both confirmations on.
func (p *pageFixture) addAccount(tenant account.TenantID, address string, change ...func(*account.Account)) *account.Account {
	p.t.Helper()
	ctx := context.Background()
	a := account.NewIMAPAccount(tenant, address, account.MailServer{
		IMAPHost: "imap.example.com", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.example.com", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
		Username: address,
	}, t0)
	for _, fn := range change {
		fn(a)
	}
	require.NoError(p.t, p.store.CreateAccount(ctx, a))
	aad := secrets.AAD(string(tenant), string(a.ID), "password")
	version, sealed, err := p.vault.Encrypt(ctx, tenant, aad, []byte("pw"))
	require.NoError(p.t, err)
	require.NoError(p.t, p.store.PutAccountSecret(ctx, tenant, a.ID, "password", version, sealed))
	return a
}

// account adds an account of the local tenant.
func (p *pageFixture) account(change ...func(*account.Account)) *account.Account {
	p.t.Helper()
	return p.addAccount(p.tenant, "me@example.com", change...)
}

// reload reads the account again. ok is false when it is gone.
func (p *pageFixture) reload(a *account.Account) (*account.Account, bool) {
	p.t.Helper()
	got, err := p.store.GetAccount(context.Background(), a.TenantID, a.ID)
	if err != nil {
		return nil, false
	}
	return got, true
}

// agent returns the context of a tool call of an agent of the tenant.
func agent(tenant account.TenantID) context.Context {
	return service.WithPrincipal(context.Background(), service.Principal{Tenant: tenant, Client: "test-agent"})
}

// addTenant adds another tenant. The store has no call for it, so the test
// writes the row on its own connection.
func (p *pageFixture) addTenant(id account.TenantID) {
	p.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(p.dir, "email-mcp.db"))+"?_pragma=busy_timeout(5000)")
	require.NoError(p.t, err)
	defer func() { require.NoError(p.t, db.Close()) }()
	_, err = db.ExecContext(context.Background(), `INSERT OR IGNORE INTO tenants (id, created_at) VALUES (?, 0)`, string(id))
	require.NoError(p.t, err)
}

// entries returns the audit entries of the local tenant, newest first.
func (p *pageFixture) entries() []audit.Entry {
	p.t.Helper()
	list, err := p.audit.List(context.Background(), p.tenant, audit.Filter{Limit: 1000})
	require.NoError(p.t, err)
	return list
}

// pending returns an action of the local tenant.
func (p *pageFixture) pending(id string) *confirm.Pending {
	p.t.Helper()
	got, _, err := p.confirm.Get(context.Background(), p.tenant, id)
	require.NoError(p.t, err)
	return got
}

// fallbackSend makes a send that waits for the web page, as a client without
// elicitation does, and returns its id.
func (p *pageFixture) fallbackSend(tenant account.TenantID, a *account.Account, c mail.Compose) string {
	p.t.Helper()
	res, _, err := p.svc.Send(agent(tenant), a.ID, &c, nil, service.ConfirmFallback, nil)
	require.NoError(p.t, err)
	require.Equal(p.t, service.SendPendingApproval, res.Status)
	return res.SendID
}

// inBandSend makes a send that waits for the answer in the chat, and returns
// its id.
func (p *pageFixture) inBandSend(a *account.Account, c mail.Compose) string {
	p.t.Helper()
	_, needs, err := p.svc.Send(agent(p.tenant), a.ID, &c, nil, service.ConfirmInBand, nil)
	require.NoError(p.t, err)
	require.NotNil(p.t, needs)
	return needs.Pending.ID
}

// element returns the first element of the page with the tag and the id, or
// nil.
func element(t *testing.T, page string, tag atom.Atom, id string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.DataAtom == tag && attr(n, "id") == id {
			return n
		}
	}
	return nil
}

// labelFor returns the text of the <label> of a form field, with each run of
// white space made one space.
func labelFor(t *testing.T, page, id string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.DataAtom == atom.Label && attr(n, "for") == id {
			return textOf(n)
		}
	}
	t.Fatalf("the page has no label for %q", id)
	return ""
}

// textOf returns the text in a node, with each run of white space made one
// space.
func textOf(n *html.Node) string {
	var words []string
	for d := range n.Descendants() {
		if d.Type == html.TextNode {
			words = append(words, strings.Fields(d.Data)...)
		}
	}
	return strings.Join(words, " ")
}

// attr returns the value of an attribute of an element, or "".
func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// hasAttr reports whether an element has the attribute.
func hasAttr(n *html.Node, name string) bool {
	for _, a := range n.Attr {
		if a.Key == name {
			return true
		}
	}
	return false
}

// tableRows returns the number of rows in the <tbody> of the page.
func tableRows(t *testing.T, page string) int {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	rows := 0
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.DataAtom == atom.Tr && n.Parent != nil && n.Parent.DataAtom == atom.Tbody {
			rows++
		}
	}
	return rows
}
