package web

import (
	"bytes"
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/discovery"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
)

// marker is the password of the tests with a fake probe. It must never come
// back from the server.
const marker = "Pw-marker-9f3k-NEVER-SHOWN"

// The fixed texts that the connect pages show.
const (
	textTestFailed    = "The connection test failed. Check the user name, the password and the server settings. Some providers need an app password."
	textAuthFailed    = "The server refused the user name or the password. Some providers need an app password, or IMAP access turned on in their settings."
	textCannotConnect = "Could not connect to the server. Check the host names and the ports."
	textHostNotFound  = "A server name was not found. Check the host names."
	textTLSFailed     = "A secure connection could not be made. Check the ports and the security settings."
	textExists        = "An account with this address is already connected."
	textUnderstand    = "To trust the certificate of a server that is not on this computer, also tick “I understand the risk”."
	textBadTrust      = "The certificate to trust is not valid. Test the connection again without trusting a certificate."
	textWrongHost     = "The certificate is not for this host name. Check the host name. This certificate cannot be trusted here."
	textExpired       = "The certificate has expired or is not valid yet. This certificate cannot be trusted here."
	textPinChanged    = "The certificate is not the one you chose to trust. Someone on the network may have changed it. This certificate cannot be trusted here."
	textCertificate   = "A server showed a certificate that this computer does not trust. Nothing was saved."
)

// fakeProbe is the Probe of the service in these tests. It records each config,
// and answers with fn, or with the real imapsmtp.Probe when fn is nil.
type fakeProbe struct {
	mu    sync.Mutex
	calls []imapsmtp.Config
	fn    func(ctx context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error)
}

func (p *fakeProbe) probe(ctx context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, cfg)
	fn := p.fn
	p.mu.Unlock()
	if fn == nil {
		return imapsmtp.Probe(ctx, cfg)
	}
	return fn(ctx, cfg)
}

func (p *fakeProbe) set(fn func(ctx context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fn = fn
}

func (p *fakeProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *fakeProbe) last() imapsmtp.Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[len(p.calls)-1]
}

// probeOK accepts every setting.
func probeOK(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
	return &imapsmtp.ProbeResult{IMAPOK: true, SMTPOK: true}, nil
}

// fakeDNS answers the lookups of discovery for example.org: SRV records for
// imap.example.org and smtp.example.org, and an MX record at a filter
// provider.
type fakeDNS struct{}

func (fakeDNS) LookupSRV(_ context.Context, service, _, name string) (string, []*net.SRV, error) {
	if name == "example.org." {
		switch service {
		case "imaps":
			return "", []*net.SRV{{Target: "imap.example.org.", Port: 993}}, nil
		case "submissions":
			return "", []*net.SRV{{Target: "smtp.example.org.", Port: 465}}, nil
		}
	}
	return "", nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if name == "example.org." {
		return []*net.MX{{Host: "mx1.filter-provider.net.", Pref: 10}}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// noHTTP fails every request at once, so discovery uses only the DNS.
type noHTTP struct{}

func (noHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network in tests")
}

// connectFixture is a started server with a real service and a signed-in
// browser.
type connectFixture struct {
	*fixture
	s       *Server
	base    string
	browser *http.Client
	csrf    string
	vault   *secrets.Vault
	audit   *audit.Log
	probe   *fakeProbe
}

func newConnectFixture(t *testing.T) *connectFixture {
	t.Helper()
	f := newFixture(t)
	keyPath := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, secrets.GenerateKeyFile(keyPath))
	kek, err := secrets.NewFileKEK(keyPath)
	require.NoError(t, err)
	vault := secrets.NewVault(f.store, kek)
	log := audit.New(f.store, vault, nil)
	logger := slog.New(slog.NewTextHandler(f.logs, nil))

	s := New(Deps{
		Store:     f.store,
		Signer:    f.signer,
		Discovery: &discovery.Finder{DNS: fakeDNS{}, HTTP: &http.Client{Transport: noHTTP{}}},
		Audit:     log,
		Now:       f.clock.Now,
		Logger:    logger,
	})
	t.Cleanup(func() { require.NoError(t, s.Shutdown(context.Background())) })
	cache := service.NewBackendFactory(f.store, vault, f.signer, true, logger)
	t.Cleanup(cache.Close)
	p := &fakeProbe{}
	s.Attach(service.New(service.Deps{
		Store:    f.store,
		Vault:    vault,
		Signer:   f.signer,
		Backends: cache,
		Audit:    log,
		Links:    s.Links(),
		Local:    true,
		Probe:    p.probe,
		Now:      f.clock.Now,
		Logger:   logger,
	}))

	base, err := s.EnsureStarted(context.Background())
	require.NoError(t, err)
	browser := newBrowser(t)
	f.signIn(s, browser)
	c := &connectFixture{fixture: f, s: s, base: base, browser: browser, vault: vault, audit: log, probe: p}
	resp, body := do(t, browser, newRequest(t, http.MethodGet, base+"/connect", nil))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	m := csrfPattern.FindStringSubmatch(body)
	require.NotNil(t, m, "the connect page has a CSRF field")
	c.csrf = m[1]
	return c
}

// post sends a form of the signed-in browser, with its CSRF token, from the
// same origin.
func (c *connectFixture) post(path string, form url.Values) (*http.Response, string) {
	c.t.Helper()
	values := url.Values{"csrf_token": {c.csrf}}
	for k, v := range form {
		values[k] = v
	}
	return do(c.t, c.browser, sameOrigin(newRequest(c.t, http.MethodPost, c.base+path, values), c.base))
}

// accounts returns the accounts of the local tenant.
func (c *connectFixture) accounts() []*account.Account {
	c.t.Helper()
	tenant, err := c.store.EnsureLocalTenant(context.Background())
	require.NoError(c.t, err)
	list, err := c.store.ListAccounts(context.Background(), tenant)
	require.NoError(c.t, err)
	return list
}

// requireNoLeak checks that the password is not in the answer, its headers,
// the log or a redirect.
func (c *connectFixture) requireNoLeak(resp *http.Response, body, password string) {
	c.t.Helper()
	require.NotContains(c.t, body, password)
	for name, values := range resp.Header {
		for _, v := range values {
			require.NotContains(c.t, v, password, name)
		}
	}
	require.NotContains(c.t, c.logs.String(), password)
}

// saveForm is the settings form for the test servers on 127.0.0.1.
func saveForm(imapSrv *imaptest.Server, smtpSrv *smtptest.Server, password string) url.Values {
	return url.Values{
		"email":         {"alice@example.org"},
		"imap_host":     {imapSrv.Host},
		"imap_port":     {strconv.Itoa(imapSrv.Port)},
		"imap_security": {"tls"},
		"smtp_host":     {smtpSrv.Host},
		"smtp_port":     {strconv.Itoa(smtpSrv.Port)},
		"smtp_security": {"tls"},
		"username":      {imapSrv.User},
		"password":      {password},
	}
}

// remoteForm is a settings form for servers that nobody runs. The tests that
// use it have a fake probe.
func remoteForm(password string) url.Values {
	return url.Values{
		"email":         {"alice@example.org"},
		"imap_host":     {"imap.example.org"},
		"imap_port":     {"993"},
		"imap_security": {"tls"},
		"smtp_host":     {"smtp.example.org"},
		"smtp_port":     {"465"},
		"smtp_security": {"tls"},
		"username":      {"alice@example.org"},
		"password":      {password},
	}
}

// with returns a copy of form with more fields.
func with(form url.Values, kv ...string) url.Values {
	out := url.Values{}
	for k, v := range form {
		out[k] = append([]string(nil), v...)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out.Set(kv[i], kv[i+1])
	}
	return out
}

// grouped is a fingerprint as the page shows it: upper-case pairs of hex
// digits with a colon between them.
func grouped(hexText string) string {
	upper := strings.ToUpper(hexText)
	pairs := make([]string, 0, len(upper)/2)
	for i := 0; i+1 < len(upper); i += 2 {
		pairs = append(pairs, upper[i:i+2])
	}
	return strings.Join(pairs, ":")
}

// input is one <input> element of a page.
type input struct {
	attrs map[string]string
	has   map[string]bool
}

// inputs returns the <input> elements of a page.
func inputs(t *testing.T, page string) []input {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	var out []input
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.DataAtom != atom.Input {
			continue
		}
		in := input{attrs: map[string]string{}, has: map[string]bool{}}
		for _, a := range n.Attr {
			in.attrs[a.Key] = a.Val
			in.has[a.Key] = true
		}
		out = append(out, in)
	}
	return out
}

// inputNamed returns the inputs with the name.
func inputNamed(t *testing.T, page, name string) []input {
	t.Helper()
	var out []input
	for _, in := range inputs(t, page) {
		if in.attrs["name"] == name {
			out = append(out, in)
		}
	}
	return out
}

// requireSafePasswordFields checks every password field of a page: type
// password, no autofill of a saved password, and never a value.
func requireSafePasswordFields(t *testing.T, page string) {
	t.Helper()
	for _, in := range inputs(t, page) {
		if in.attrs["name"] != "password" && in.attrs["type"] != "password" {
			continue
		}
		require.Equal(t, "password", in.attrs["type"])
		require.Equal(t, "password", in.attrs["name"])
		require.Contains(t, []string{"off", "new-password"}, in.attrs["autocomplete"])
		require.False(t, in.has["value"], "a password field never has a value")
	}
}

// requireTrustOffered checks that the page offers to trust the certificate of
// one protocol ("imap" or "smtp") with this fingerprint.
func requireTrustOffered(t *testing.T, page, protocol, fingerprint string) {
	t.Helper()
	boxes := inputNamed(t, page, "trust_"+protocol)
	require.Len(t, boxes, 1, "a %s trust box", protocol)
	require.Equal(t, "checkbox", boxes[0].attrs["type"])
	pins := inputNamed(t, page, protocol+"_pin")
	require.Len(t, pins, 1)
	require.Equal(t, "hidden", pins[0].attrs["type"])
	require.Equal(t, fingerprint, pins[0].attrs["value"])
	require.Contains(t, visibleText(t, page), grouped(fingerprint))
}

// requireTrustNotOffered checks that the page has no way to trust the
// certificate of the protocol.
func requireTrustNotOffered(t *testing.T, page, protocol string) {
	t.Helper()
	require.Empty(t, inputNamed(t, page, "trust_"+protocol))
	require.Empty(t, inputNamed(t, page, protocol+"_pin"))
}

func TestAccountsPageListsAccounts(t *testing.T) {
	c := newConnectFixture(t)

	// Without a session: the no-session page.
	resp, body := do(t, newBrowser(t), newRequest(t, http.MethodGet, c.base+"/", nil))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, instruction, visibleText(t, body))

	// No account yet.
	resp, body = do(t, c.browser, newRequest(t, http.MethodGet, c.base+"/", nil))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "No account is connected yet.")
	require.Contains(t, body, `href="/connect"`)

	// An account of the tenant, with a sealed password and pins, and an account
	// of another tenant.
	ctx := context.Background()
	tenant, err := c.store.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	server := account.MailServer{
		IMAPHost: "imap.example.org", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.example.org", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
		Username: "alice-login", PinnedIMAPCert: strings.Repeat("ab", 32),
	}
	a := account.NewIMAPAccount(tenant, "alice@example.org", server, t0)
	aad := secrets.AAD(string(tenant), string(a.ID), "password")
	version, sealed, err := c.vault.Encrypt(ctx, tenant, aad, []byte(marker))
	require.NoError(t, err)
	require.NoError(t, c.store.CreateAccountWithSecret(ctx, a, "password", version, sealed))
	c.addTenant("other")
	b := account.NewIMAPAccount("other", "bob@other.example", server, t0)
	require.NoError(t, c.store.CreateAccount(ctx, b))

	resp, body = do(t, c.browser, newRequest(t, http.MethodGet, c.base+"/", nil))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	text := visibleText(t, body)
	require.Contains(t, text, "alice@example.org")
	require.Contains(t, text, "Send")
	require.Contains(t, text, "Connected")
	require.NotContains(t, text, "bob@other.example", "an account of another tenant is not shown")
	require.NotContains(t, body, marker)
	require.NotContains(t, body, strings.Repeat("ab", 32), "no pin")
	require.NotContains(t, body, "alice-login", "no user name")
	require.NotContains(t, body, string(sealed))
}

// addTenant adds a tenant. The store has no public call for it, so the test
// writes the row on its own connection.
func (c *connectFixture) addTenant(id account.TenantID) {
	c.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(c.dir, "email-mcp.db"))+"?_pragma=busy_timeout(5000)")
	require.NoError(c.t, err)
	defer func() { require.NoError(c.t, db.Close()) }()
	_, err = db.ExecContext(context.Background(), `INSERT INTO tenants (id, created_at) VALUES (?, 0)`, string(id))
	require.NoError(c.t, err)
}

func TestConnectSavesEncryptedPassword(t *testing.T) {
	c := newConnectFixture(t)
	imapSrv := imaptest.New(t)
	smtpSrv := smtptest.New(t)

	// Step 1: the email address.
	resp, body := do(t, c.browser, newRequest(t, http.MethodGet, c.base+"/connect", nil))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, inputNamed(t, body, "email"), 1)
	require.Contains(t, body, `action="/connect/discover"`)
	require.Empty(t, inputNamed(t, body, "password"), "no password before the user chose the servers")

	// Step 2: discovery shows each host. Nothing is tested yet.
	resp, body = c.post("/connect/discover", url.Values{"email": {"alice@example.org"}})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	text := visibleText(t, body)
	for _, host := range []string{"imap.example.org", "smtp.example.org", "imap.filter-provider.net", "smtp.filter-provider.net"} {
		require.Contains(t, text, host)
	}
	require.Empty(t, inputNamed(t, body, "password"), "the user confirms the hosts before typing the password")
	require.Zero(t, c.probe.count(), "discovery does not test a connection")

	// Step 3: the user picks a suggestion. The settings form shows its hosts in
	// fields that the user can change, and asks for the password.
	resp, body = c.post("/connect/settings", url.Values{
		"email": {"alice@example.org"}, "imap_host": {"imap.example.org"}, "imap_port": {"993"},
		"imap_security": {"tls"}, "smtp_host": {"smtp.example.org"}, "smtp_port": {"465"},
		"smtp_security": {"tls"}, "username": {"alice@example.org"},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "imap.example.org", inputNamed(t, body, "imap_host")[0].attrs["value"])
	require.Equal(t, "smtp.example.org", inputNamed(t, body, "smtp_host")[0].attrs["value"])
	require.Len(t, inputNamed(t, body, "password"), 1)
	requireSafePasswordFields(t, body)
	require.Contains(t, body, `action="/connect/save"`)
	require.Zero(t, c.probe.count())

	// Step 4: the user types the local servers and the password. The servers
	// show self-signed certificates, so the page asks about them and saves
	// nothing. The password went to neither server.
	form := saveForm(imapSrv, smtpSrv, imapSrv.Pass)
	resp, body = c.post("/connect/save", form)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textCertificate)
	requireTrustOffered(t, body, "imap", imapSrv.CertSHA256)
	requireTrustOffered(t, body, "smtp", smtpSrv.CertSHA256)
	require.Empty(t, c.accounts())
	require.Zero(t, smtpSrv.AuthAttempts())
	c.requireNoLeak(resp, body, imapSrv.Pass)

	// Step 5: the user trusts both certificates and types the password again.
	resp, body = c.post("/connect/save", with(form,
		"trust_imap", "1", "imap_pin", imapSrv.CertSHA256,
		"trust_smtp", "1", "smtp_pin", smtpSrv.CertSHA256))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	require.Equal(t, "/", resp.Header.Get("Location"))
	c.requireNoLeak(resp, body, imapSrv.Pass)

	accounts := c.accounts()
	require.Len(t, accounts, 1)
	a := accounts[0]
	require.Equal(t, "alice@example.org", a.Address)
	require.Equal(t, imapSrv.CertSHA256, a.Mail.PinnedIMAPCert)
	require.Equal(t, smtpSrv.CertSHA256, a.Mail.PinnedSMTPCert)
	require.Equal(t, account.MailSend, a.Level(account.AreaMail))

	// The password is stored only sealed: not in the database file, not in its
	// write-ahead log.
	require.False(t, bytes.Contains(c.dataFiles(), []byte(imapSrv.Pass)))
	_, _, err := c.store.GetAccountSecret(context.Background(), a.TenantID, a.ID, "password")
	require.NoError(t, err)

	// The connect is audited, without the address.
	entries, err := c.audit.List(context.Background(), a.TenantID, audit.Filter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "account_connected", entries[0].Detail)
	require.Equal(t, a.ID, entries[0].Account)

	// The list shows the new account.
	resp, body = do(t, c.browser, newRequest(t, http.MethodGet, c.base+"/", nil))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "alice@example.org")
	require.NotContains(t, c.logs.String(), "alice@example.org", "the log holds no address")
}

func TestConnectRefusesWhenProbeFails(t *testing.T) {
	c := newConnectFixture(t)
	imapSrv := imaptest.New(t)
	smtpSrv := smtptest.New(t)
	trusted := func(form url.Values) url.Values {
		return with(form, "trust_imap", "1", "imap_pin", imapSrv.CertSHA256,
			"trust_smtp", "1", "smtp_pin", smtpSrv.CertSHA256)
	}

	// A wrong password: the text is fixed, never the text of the server.
	resp, body := c.post("/connect/save", trusted(saveForm(imapSrv, smtpSrv, marker)))
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	text := visibleText(t, body)
	require.Contains(t, text, textAuthFailed)
	require.NotContains(t, strings.ToLower(text), "login failed")
	require.NotContains(t, text, "AUTHENTICATIONFAILED")
	c.requireNoLeak(resp, body, marker)
	requireSafePasswordFields(t, body)
	// The trust that the user chose stays ticked, so only the password must be
	// typed again.
	for _, protocol := range []string{"imap", "smtp"} {
		boxes := inputNamed(t, body, "trust_"+protocol)
		require.Len(t, boxes, 1)
		require.True(t, boxes[0].has["checked"])
	}

	// A port where nothing listens.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closedPort := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	require.NoError(t, ln.Close())
	resp, body = c.post("/connect/save", with(trusted(saveForm(imapSrv, smtpSrv, imapSrv.Pass)), "imap_port", closedPort))
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textCannotConnect)

	// TLS to a server that speaks plain text.
	plainSrv := imaptest.New(t, imaptest.WithMode(imaptest.ModePlain))
	resp, body = c.post("/connect/save", with(trusted(saveForm(imapSrv, smtpSrv, imapSrv.Pass)), "imap_port", strconv.Itoa(plainSrv.Port)))
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textTLSFailed)

	// A host name that does not exist.
	c.probe.set(func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "imap.example.org", IsNotFound: true}}
	})
	resp, body = c.post("/connect/save", remoteForm(marker))
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textHostNotFound)

	// Any other failure gets the general text.
	c.probe.set(func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
		return nil, errors.New("imap: starttls: the server does not support it")
	})
	resp, body = c.post("/connect/save", remoteForm(marker))
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textTestFailed)

	require.Empty(t, c.accounts(), "nothing was saved")
	require.False(t, bytes.Contains(c.dataFiles(), []byte(marker)))
	require.NotContains(t, c.logs.String(), marker)
}

func TestConnectOffersPinForLoopbackSelfSigned(t *testing.T) {
	c := newConnectFixture(t)
	imapSrv := imaptest.New(t)
	smtpSrv := smtptest.New(t)
	form := saveForm(imapSrv, smtpSrv, imapSrv.Pass)

	resp, body := c.post("/connect/save", form)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	requireTrustOffered(t, body, "imap", imapSrv.CertSHA256)
	requireTrustOffered(t, body, "smtp", smtpSrv.CertSHA256)
	require.Empty(t, inputNamed(t, body, "understand"), "a server on this computer needs no extra warning")
	require.Contains(t, visibleText(t, body), "127.0.0.1", "the page names the host that the user typed")
	requireSafePasswordFields(t, body)
	require.Empty(t, c.accounts())

	// Trusting only the IMAP certificate: the IMAP login now works, and the page
	// asks about the SMTP certificate again. The IMAP trust stays ticked.
	resp, body = c.post("/connect/save", with(form, "trust_imap", "1", "imap_pin", imapSrv.CertSHA256))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	requireTrustOffered(t, body, "smtp", smtpSrv.CertSHA256)
	require.False(t, inputNamed(t, body, "trust_smtp")[0].has["checked"])
	imapBox := inputNamed(t, body, "trust_imap")
	require.Len(t, imapBox, 1)
	require.True(t, imapBox[0].has["checked"])
	require.Empty(t, c.accounts())

	// Trusting both saves the account with both pins.
	resp, _ = c.post("/connect/save", with(form, "trust_imap", "1", "imap_pin", imapSrv.CertSHA256,
		"trust_smtp", "1", "smtp_pin", smtpSrv.CertSHA256))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Len(t, c.accounts(), 1)
}

func TestConnectTrustRules(t *testing.T) {
	imapSrv := imaptest.New(t)
	smtpSrv := smtptest.New(t)

	t.Run("a host name that does not match", func(t *testing.T) {
		c := newConnectFixture(t)
		// The certificate is for 127.0.0.1 only.
		resp, body := c.post("/connect/save", with(saveForm(imapSrv, smtpSrv, imapSrv.Pass), "imap_host", "LOCALHOST"))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		requireTrustNotOffered(t, body, "imap")
		requireTrustOffered(t, body, "smtp", smtpSrv.CertSHA256)
		require.Contains(t, visibleText(t, body), textWrongHost)
		require.Empty(t, c.accounts())
	})

	t.Run("an expired certificate", func(t *testing.T) {
		c := newConnectFixture(t)
		c.probe.set(func(_ context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
			return &imapsmtp.ProbeResult{SMTPOK: true, IMAPCert: &imapsmtp.CertificateUnknownError{
				Protocol: "imap", Host: cfg.Server.IMAPHost, FingerprintSHA256: strings.Repeat("cd", 32),
				Reason: x509.CertificateInvalidError{Reason: x509.Expired},
			}}, nil
		})
		resp, body := c.post("/connect/save", remoteForm(marker))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		requireTrustNotOffered(t, body, "imap")
		require.Contains(t, visibleText(t, body), textExpired)
		require.Empty(t, inputNamed(t, body, "understand"))
		c.requireNoLeak(resp, body, marker)
	})

	t.Run("a pin that does not match", func(t *testing.T) {
		c := newConnectFixture(t)
		wrong := strings.Repeat("0", 64)
		resp, body := c.post("/connect/save", with(saveForm(imapSrv, smtpSrv, imapSrv.Pass),
			"trust_imap", "1", "imap_pin", wrong, "trust_smtp", "1", "smtp_pin", smtpSrv.CertSHA256))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		requireTrustNotOffered(t, body, "imap")
		text := visibleText(t, body)
		require.Contains(t, text, textPinChanged)
		require.Contains(t, text, grouped(imapSrv.CertSHA256), "the page shows the certificate that the server shows now")
		require.Empty(t, c.accounts())
	})

	t.Run("a server that is not on this computer", func(t *testing.T) {
		c := newConnectFixture(t)
		fingerprint := strings.Repeat("e1", 32)
		c.probe.set(func(_ context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
			if cfg.Server.PinnedIMAPCert == fingerprint {
				return &imapsmtp.ProbeResult{IMAPOK: true, SMTPOK: true}, nil
			}
			return &imapsmtp.ProbeResult{SMTPOK: true, IMAPCert: &imapsmtp.CertificateUnknownError{
				Protocol: "imap", Host: cfg.Server.IMAPHost, FingerprintSHA256: fingerprint,
				Reason: x509.UnknownAuthorityError{},
			}}, nil
		})
		form := remoteForm(marker)

		resp, body := c.post("/connect/save", form)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		requireTrustOffered(t, body, "imap", fingerprint)
		understand := inputNamed(t, body, "understand")
		require.Len(t, understand, 1, "a remote server needs the extra box")
		require.Equal(t, "checkbox", understand[0].attrs["type"])
		require.False(t, understand[0].has["checked"])
		require.Equal(t, 1, c.probe.count())

		// The trust box alone is refused before any test.
		resp, body = c.post("/connect/save", with(form, "trust_imap", "1", "imap_pin", fingerprint))
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
		require.Contains(t, visibleText(t, body), textUnderstand)
		requireTrustOffered(t, body, "imap", fingerprint)
		require.Len(t, inputNamed(t, body, "understand"), 1)
		require.Equal(t, 1, c.probe.count(), "nothing was tested")
		c.requireNoLeak(resp, body, marker)
		require.Empty(t, c.accounts())

		// With "I understand", the certificate is pinned and the account saved.
		resp, _ = c.post("/connect/save", with(form, "trust_imap", "1", "imap_pin", fingerprint, "understand", "1"))
		require.Equal(t, http.StatusSeeOther, resp.StatusCode)
		require.Equal(t, fingerprint, c.probe.last().Server.PinnedIMAPCert)
		require.Empty(t, c.probe.last().Server.PinnedSMTPCert)
		require.Len(t, c.accounts(), 1)
		require.Equal(t, fingerprint, c.accounts()[0].Mail.PinnedIMAPCert)
	})

	t.Run("the risk box is not kept for a new certificate", func(t *testing.T) {
		c := newConnectFixture(t)
		imapPin, smtpPin := strings.Repeat("a1", 32), strings.Repeat("b2", 32)
		c.probe.set(func(_ context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
			if cfg.Server.PinnedIMAPCert != imapPin {
				return &imapsmtp.ProbeResult{SMTPOK: true, IMAPCert: &imapsmtp.CertificateUnknownError{
					Protocol: "imap", Host: cfg.Server.IMAPHost, FingerprintSHA256: imapPin, Reason: x509.UnknownAuthorityError{},
				}}, nil
			}
			return &imapsmtp.ProbeResult{IMAPOK: true, SMTPCert: &imapsmtp.CertificateUnknownError{
				Protocol: "smtp", Host: cfg.Server.SMTPHost, FingerprintSHA256: smtpPin, Reason: x509.UnknownAuthorityError{},
			}}, nil
		})

		resp, body := c.post("/connect/save", with(remoteForm(marker), "trust_imap", "1", "imap_pin", imapPin, "understand", "1"))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		requireTrustOffered(t, body, "smtp", smtpPin)
		require.False(t, inputNamed(t, body, "trust_smtp")[0].has["checked"])
		require.True(t, inputNamed(t, body, "trust_imap")[0].has["checked"], "the earlier trust stays")
		understand := inputNamed(t, body, "understand")
		require.Len(t, understand, 1)
		require.False(t, understand[0].has["checked"], "a new certificate needs a new decision")
		require.Empty(t, c.accounts())
	})

	t.Run("a pin that is not a fingerprint", func(t *testing.T) {
		c := newConnectFixture(t)
		c.probe.set(probeOK)
		for _, pin := range []string{"", "abc", strings.Repeat("zz", 32), strings.Repeat("ab", 33)} {
			resp, body := c.post("/connect/save", with(remoteForm(marker), "trust_imap", "1", "imap_pin", pin, "understand", "1"))
			require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, pin)
			require.Contains(t, visibleText(t, body), textBadTrust, pin)
		}
		require.Zero(t, c.probe.count())
		require.Empty(t, c.accounts())
	})

	t.Run("a pin without its trust box is ignored", func(t *testing.T) {
		c := newConnectFixture(t)
		c.probe.set(probeOK)
		resp, _ := c.post("/connect/save", with(remoteForm(marker), "imap_pin", strings.Repeat("ab", 32)))
		require.Equal(t, http.StatusSeeOther, resp.StatusCode)
		require.Empty(t, c.probe.last().Server.PinnedIMAPCert)
	})
}

func TestConnectCleansHosts(t *testing.T) {
	c := newConnectFixture(t)
	c.probe.set(probeOK)
	resp, _ := c.post("/connect/save", with(remoteForm(marker),
		"imap_host", "  IMAP.Example.ORG ", "smtp_host", "\tSmtp.EXAMPLE.org\n"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	got := c.probe.last().Server
	require.Equal(t, "imap.example.org", got.IMAPHost)
	require.Equal(t, "smtp.example.org", got.SMTPHost)
	require.Equal(t, "imap.example.org", c.accounts()[0].Mail.IMAPHost)

	// A host of white space only is refused before any test.
	resp, body := c.post("/connect/save", with(remoteForm(marker), "email", "bob@example.org", "imap_host", "   "))
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "The server settings are not valid.")
	require.Equal(t, 1, c.probe.count())
}

func TestConnectRefusesADuplicateAddress(t *testing.T) {
	c := newConnectFixture(t)
	c.probe.set(probeOK)
	resp, _ := c.post("/connect/save", remoteForm(marker))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	resp, body := c.post("/connect/save", with(remoteForm(marker), "email", " ALICE@example.org "))
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textExists)
	require.Equal(t, 1, c.probe.count(), "the duplicate was not tested")
	require.Len(t, c.accounts(), 1)
	c.requireNoLeak(resp, body, marker)
}

func TestPasswordNeverRendered(t *testing.T) {
	c := newConnectFixture(t)
	fingerprint := strings.Repeat("e1", 32)
	unknownCert := func(_ context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
		return &imapsmtp.ProbeResult{IMAPCert: &imapsmtp.CertificateUnknownError{
			Protocol: "imap", Host: cfg.Server.IMAPHost, FingerprintSHA256: fingerprint, Reason: x509.UnknownAuthorityError{},
		}, SMTPCert: &imapsmtp.CertificateUnknownError{
			Protocol: "smtp", Host: cfg.Server.SMTPHost, FingerprintSHA256: fingerprint, Pinned: true,
		}}, nil
	}
	// The probe error holds the password on purpose, to show that the page
	// never shows the text of an error.
	leakyError := func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
		return nil, errors.New("imap: login failed for " + marker)
	}

	cases := []struct {
		name  string
		probe func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error)
		path  string
		form  url.Values
	}{
		{"discover", probeOK, "/connect/discover", url.Values{"email": {"alice@example.org"}, "password": {marker}}},
		{"discover, bad address", probeOK, "/connect/discover", url.Values{"email": {"alice"}, "password": {marker}}},
		{"discover, nothing found", probeOK, "/connect/discover", url.Values{"email": {"alice@nothing-here.org"}, "password": {marker}}},
		{"settings", probeOK, "/connect/settings", with(remoteForm(marker))},
		{"save, certificate", unknownCert, "/connect/save", remoteForm(marker)},
		{"save, test failed", leakyError, "/connect/save", remoteForm(marker)},
		{"save, bad address", probeOK, "/connect/save", with(remoteForm(marker), "email", "not an address")},
		{"save, bad settings", probeOK, "/connect/save", with(remoteForm(marker), "imap_port", "99999")},
		{"save, plain to a remote host", probeOK, "/connect/save", with(remoteForm(marker), "imap_security", "plain")},
		{"save, needs I understand", probeOK, "/connect/save", with(remoteForm(marker), "trust_imap", "1", "imap_pin", fingerprint)},
		{"save, bad pin", probeOK, "/connect/save", with(remoteForm(marker), "trust_imap", "1", "imap_pin", "x")},
		{"save, control character in the user name", probeOK, "/connect/save", with(remoteForm(marker), "username", "\x00")},
		{"save, success", probeOK, "/connect/save", remoteForm(marker)},
		{"save, duplicate", probeOK, "/connect/save", remoteForm(marker)},
	}
	for _, tc := range cases {
		c.probe.set(tc.probe)
		resp, body := c.post(tc.path, tc.form)
		require.NotEqual(t, http.StatusInternalServerError, resp.StatusCode, tc.name)
		c.requireNoLeak(resp, body, marker)
		requireSafePasswordFields(t, body)
		if resp.StatusCode == http.StatusSeeOther {
			require.Equal(t, "/", resp.Header.Get("Location"), tc.name)
		}
	}
	// The pages without a form post.
	for _, path := range []string{"/", "/connect"} {
		resp, body := do(t, c.browser, newRequest(t, http.MethodGet, c.base+path, nil))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		c.requireNoLeak(resp, body, marker)
		requireSafePasswordFields(t, body)
	}
	require.False(t, bytes.Contains(c.dataFiles(), []byte(marker)))
}

func TestConnectRoutesNeedTheirMethod(t *testing.T) {
	c := newConnectFixture(t)
	c.probe.set(probeOK)

	for _, path := range []string{"/connect/discover", "/connect/settings", "/connect/save"} {
		resp, _ := do(t, c.browser, newRequest(t, http.MethodGet, c.base+path, nil))
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, path)
	}
	for _, path := range []string{"/", "/connect"} {
		resp, _ := c.post(path, url.Values{})
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, path)
	}
	// "/" is only the accounts page, not every path.
	resp, _ := do(t, c.browser, newRequest(t, http.MethodGet, c.base+"/nothing", nil))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// A save without the form token saves nothing.
	form := remoteForm(marker)
	resp, _ = do(t, c.browser, sameOrigin(newRequest(t, http.MethodPost, c.base+"/connect/save", form), c.base))
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Zero(t, c.probe.count())
	require.Empty(t, c.accounts())
}
