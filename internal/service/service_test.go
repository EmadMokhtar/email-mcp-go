package service

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // the driver that the store uses, for the second tenant

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
)

// testStart is the time at which the test accounts are created.
var testStart = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

// fixture is a store and a vault on disk, with the local tenant. Every test
// that needs accounts builds one.
type fixture struct {
	t      *testing.T
	dir    string
	store  *store.Store
	vault  *secrets.Vault
	signer *signing.Signer
	tenant account.TenantID
}

func newFixture(t *testing.T) *fixture {
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

	return &fixture{
		t:      t,
		dir:    dir,
		store:  st,
		vault:  secrets.NewVault(st, kek),
		signer: signing.New(bytes.Repeat([]byte{7}, 32)),
		tenant: tenant,
	}
}

// addTenant adds a second tenant. The store has no public call for it, so the
// test writes the row on its own connection.
func (f *fixture) addTenant(id account.TenantID) {
	f.t.Helper()
	uriPath := filepath.ToSlash(filepath.Join(f.dir, "email-mcp.db"))
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open("sqlite", "file:"+uriPath+"?_pragma=busy_timeout(5000)")
	require.NoError(f.t, err)
	defer func() { require.NoError(f.t, db.Close()) }()
	_, err = db.ExecContext(context.Background(), `INSERT OR IGNORE INTO tenants (id, created_at) VALUES (?, 0)`, string(id))
	require.NoError(f.t, err)
}

// serverSettings returns settings that point at the test IMAP server. The
// certificate is pinned, so no root certificates are needed. The SMTP half only
// has to be valid, because the read use cases never use it.
func serverSettings(srv *imaptest.Server) account.MailServer {
	return account.MailServer{
		IMAPHost:       srv.Host,
		IMAPPort:       srv.Port,
		IMAPSecurity:   account.SecurityTLS,
		SMTPHost:       "smtp.example.com",
		SMTPPort:       465,
		SMTPSecurity:   account.SecurityTLS,
		Username:       srv.User,
		PinnedIMAPCert: srv.CertSHA256,
	}
}

// offlineSettings returns settings for a server that nobody runs. Backends are
// built without connecting, so tests that never call the server can use them.
func offlineSettings() account.MailServer {
	return account.MailServer{
		IMAPHost:     "imap.example.com",
		IMAPPort:     993,
		IMAPSecurity: account.SecurityTLS,
		SMTPHost:     "smtp.example.com",
		SMTPPort:     465,
		SMTPSecurity: account.SecurityTLS,
		Username:     "user@example.com",
	}
}

// addAccount stores an account of the tenant and its sealed password. The
// optional functions change the account before it is stored.
func (f *fixture) addAccount(tenant account.TenantID, address string, server account.MailServer, password string, change ...func(*account.Account)) *account.Account {
	f.t.Helper()
	ctx := context.Background()
	a := account.NewIMAPAccount(tenant, address, server, testStart)
	for _, fn := range change {
		fn(a)
	}
	require.NoError(f.t, f.store.CreateAccount(ctx, a))
	f.setPassword(a, password)
	return a
}

// setPassword seals a password for the account and stores it. Sealing twice
// gives different bytes, because each seal uses a new random nonce.
func (f *fixture) setPassword(a *account.Account, password string) {
	f.t.Helper()
	ctx := context.Background()
	aad := secrets.AAD(string(a.TenantID), string(a.ID), passwordSecret)
	version, sealed, err := f.vault.Encrypt(ctx, a.TenantID, aad, []byte(password))
	require.NoError(f.t, err)
	require.NoError(f.t, f.store.PutAccountSecret(ctx, a.TenantID, a.ID, passwordSecret, version, sealed))
}

// reload reads the account again from the store.
func (f *fixture) reload(a *account.Account) *account.Account {
	f.t.Helper()
	got, err := f.store.GetAccount(context.Background(), a.TenantID, a.ID)
	require.NoError(f.t, err)
	return got
}

// newService returns a service on the fixture store.
func (f *fixture) newService(backends BackendFactory, links Links) *Service {
	return New(Deps{
		Store:    f.store,
		Vault:    f.vault,
		Signer:   f.signer,
		Backends: backends,
		Links:    links,
		Now:      func() time.Time { return testStart },
	})
}

// newCache returns the default backend factory on the fixture. It is closed when
// the test ends.
func (f *fixture) newCache() *BackendCache {
	c := NewBackendFactory(f.store, f.vault, f.signer, true, nil)
	f.t.Cleanup(c.Close)
	return c
}

// newCacheWithTimers is newCache with a timer that the test fires by hand.
func (f *fixture) newCacheWithTimers(timers *fakeTimers) *BackendCache {
	c := f.newCache()
	c.afterFunc = timers.AfterFunc
	return c
}

// ctx returns a context with a principal of the fixture tenant and no scope
// limit, as on a local install.
func (f *fixture) ctx() context.Context {
	return WithPrincipal(context.Background(), Principal{Tenant: f.tenant, Client: "test-client"})
}

// settingsCall is one call to fakeLinks.Settings.
type settingsCall struct {
	ID    account.AccountID
	Area  account.Area
	Level account.Level
}

// fakeLinks is a Links for tests. Its addresses carry no token.
type fakeLinks struct {
	mu    sync.Mutex
	calls []settingsCall
	err   error // returned by Settings when it is set
}

func (l *fakeLinks) Connect(context.Context) (string, error) { return "https://mcp.test/connect", nil }

func (l *fakeLinks) Settings(_ context.Context, id account.AccountID, area account.Area, level account.Level) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, settingsCall{ID: id, Area: area, Level: level})
	if l.err != nil {
		return "", l.err
	}
	return fmt.Sprintf("https://mcp.test/settings/%s?area=%s&level=%d", id, area, int(level)), nil
}

func (l *fakeLinks) Approval(_ context.Context, pendingID string) (string, error) {
	return "https://mcp.test/approve/" + pendingID, nil
}

func (l *fakeLinks) settingsCalls() []settingsCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]settingsCall(nil), l.calls...)
}

// fakeFactory is a BackendFactory for tests that must not reach a mail server.
// It counts the calls, so a test can show that a refused request never got as
// far as a backend.
type fakeFactory struct {
	mu        sync.Mutex
	backend   mail.Backend                       // used for an account that has no entry in byAccount
	byAccount map[account.AccountID]mail.Backend // a backend for one account
	err       error                              // returned by Mail when it is set
	mailCalls int
}

func (f *fakeFactory) Mail(_ context.Context, a *account.Account) (mail.Backend, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mailCalls++
	if f.err != nil {
		return nil, f.err
	}
	if b, ok := f.byAccount[a.ID]; ok {
		return b, nil
	}
	return f.backend, nil
}

// Forget does nothing. The read use cases never call it.
func (f *fakeFactory) Forget(account.AccountID) {}

func (f *fakeFactory) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mailCalls
}

// fakeBackend is a mail.Backend for tests. A method that a test did not
// override panics, because the embedded interface is nil. That shows at once
// that the service called something it should not.
type fakeBackend struct {
	mail.Backend

	caps     mail.Capabilities
	tags     []mail.Tag
	tagsErr  error
	attErr   error
	attBytes int64 // the maxBytes that GetAttachment received

	// How Capabilities behaves. By default it answers at once.
	capsDelay   time.Duration // wait this long, or until the context ends
	capsBlock   bool          // wait until the context ends, as a backend that honours it
	capsStuck   chan struct{} // wait until this channel is closed and ignore the context
	capsEntered chan struct{} // gets a value each time Capabilities starts (buffered)
	capsRunning atomic.Int32  // calls that run now
	capsPeak    atomic.Int32  // the most calls that ran at the same time
}

func (b *fakeBackend) Capabilities(ctx context.Context) mail.Capabilities {
	n := b.capsRunning.Add(1)
	defer b.capsRunning.Add(-1)
	for {
		peak := b.capsPeak.Load()
		if n <= peak || b.capsPeak.CompareAndSwap(peak, n) {
			break
		}
	}
	if b.capsEntered != nil {
		select {
		case b.capsEntered <- struct{}{}:
		default:
		}
	}
	switch {
	case b.capsStuck != nil:
		<-b.capsStuck
		return mail.Capabilities{}
	case b.capsBlock:
		<-ctx.Done()
		return mail.Capabilities{}
	case b.capsDelay > 0:
		select {
		case <-time.After(b.capsDelay):
		case <-ctx.Done():
			return mail.Capabilities{}
		}
	}
	return b.caps
}

func (b *fakeBackend) ListTags(context.Context) ([]mail.Tag, error) { return b.tags, b.tagsErr }

func (b *fakeBackend) ListFolders(context.Context) ([]mail.Folder, error) {
	return []mail.Folder{{Name: "INBOX", Alias: mail.AliasInbox}}, nil
}

func (b *fakeBackend) GetAttachment(_ context.Context, _ mail.Ref, _ string, maxBytes int64) (*mail.Attachment, error) {
	b.attBytes = maxBytes
	if b.attErr != nil {
		return nil, b.attErr
	}
	return &mail.Attachment{AttachmentInfo: mail.AttachmentInfo{ID: "2", Filename: "a.txt"}, Data: []byte("x")}, nil
}

// crlf turns the line breaks of a test message into CRLF, as mail has them.
func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

// plainMail returns a simple message with a plain text body.
func plainMail(subject, body string) []byte {
	return crlf("From: Sender <sender@example.com>\n" +
		"To: Me <me@example.com>\n" +
		"Subject: " + subject + "\n" +
		"Date: Tue, 10 Mar 2026 10:00:00 +0000\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: text/plain; charset=utf-8\n" +
		"\n" + body + "\n")
}

// fakeTimers replaces time.AfterFunc in the backend cache. A test fires a timer
// by hand, so it does not wait for two minutes.
type fakeTimers struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

type fakeTimer struct {
	delay   time.Duration
	fn      func()
	stopped bool
}

// AfterFunc has the signature of BackendCache.afterFunc.
func (ft *fakeTimers) AfterFunc(d time.Duration, fn func()) (stop func() bool) {
	t := &fakeTimer{delay: d, fn: fn}
	ft.mu.Lock()
	ft.timers = append(ft.timers, t)
	ft.mu.Unlock()
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

// count returns how many timers were started.
func (ft *fakeTimers) count() int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return len(ft.timers)
}

// get returns the i-th timer that was started.
func (ft *fakeTimers) get(i int) *fakeTimer {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.timers[i]
}

// fire runs the i-th timer unless it was stopped, as the clock would.
func (ft *fakeTimers) fire(i int) {
	t := ft.get(i)
	ft.mu.Lock()
	stopped := t.stopped
	t.stopped = true
	ft.mu.Unlock()
	if !stopped {
		t.fn()
	}
}
