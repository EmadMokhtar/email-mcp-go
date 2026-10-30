package service

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
)

// connectPassword is the password of the connect tests that use a fake probe.
const connectPassword = "pw-7Hq2-never-shown" //nolint:gosec // G101: a test password, used to check that it is never shown

// probeRecorder is the Probe of a connect test. It records each config, and
// answers with fn, or with the real imapsmtp.Probe when fn is nil.
type probeRecorder struct {
	mu    sync.Mutex
	calls []imapsmtp.Config
	fn    func(ctx context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error)
}

func (p *probeRecorder) probe(ctx context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, cfg)
	fn := p.fn
	p.mu.Unlock()
	if fn == nil {
		return imapsmtp.Probe(ctx, cfg)
	}
	return fn(ctx, cfg)
}

func (p *probeRecorder) configs() []imapsmtp.Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]imapsmtp.Config(nil), p.calls...)
}

// probeOK is a probe that accepts every setting.
func probeOK(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
	return &imapsmtp.ProbeResult{IMAPOK: true, SMTPOK: true}, nil
}

// connectEnv is a service that can connect accounts, with an audit log, a
// backend cache and a recording probe.
type connectEnv struct {
	*fixture
	svc   *Service
	probe *probeRecorder
	audit *audit.Log
}

func newConnectEnv(t *testing.T, local bool, fn func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error)) *connectEnv {
	t.Helper()
	f := newFixture(t)
	p := &probeRecorder{fn: fn}
	log := audit.New(f.store, f.vault, nil)
	cache := NewBackendFactory(f.store, f.vault, f.signer, local, nil)
	t.Cleanup(cache.Close)
	svc := New(Deps{
		Store:    f.store,
		Vault:    f.vault,
		Signer:   f.signer,
		Backends: cache,
		Audit:    log,
		Local:    local,
		Probe:    p.probe,
		Now:      func() time.Time { return testStart },
	})
	return &connectEnv{fixture: f, svc: svc, probe: p, audit: log}
}

// userCtx is the context of the user of the tenant, as the web interface makes
// it: no scopes, because the user acts and not an agent.
func (e *connectEnv) userCtx(tenant account.TenantID) context.Context {
	return WithPrincipal(context.Background(), Principal{Tenant: tenant, Client: "web"})
}

// exampleServer returns valid settings for servers that nobody runs. The
// connect tests that use a fake probe use them.
func exampleServer() account.MailServer {
	return account.MailServer{
		IMAPHost:     "imap.example.com",
		IMAPPort:     993,
		IMAPSecurity: account.SecurityTLS,
		SMTPHost:     "smtp.example.com",
		SMTPPort:     465,
		SMTPSecurity: account.SecurityTLS,
		Username:     "alice@example.com",
	}
}

// requireNoAccounts checks that the tenant has no account and no audit entry.
func (e *connectEnv) requireNoAccounts(t *testing.T, tenant account.TenantID) {
	t.Helper()
	accounts, err := e.store.ListAccounts(context.Background(), tenant)
	require.NoError(t, err)
	require.Empty(t, accounts, "no account was stored")
	entries, err := e.audit.List(context.Background(), tenant, audit.Filter{})
	require.NoError(t, err)
	require.Empty(t, entries, "nothing was audited")
}

// dataFiles returns the bytes of every file in the data directory: the
// database and its write-ahead log.
func (e *connectEnv) dataFiles(t *testing.T) []byte {
	t.Helper()
	entries, err := os.ReadDir(e.dir)
	require.NoError(t, err)
	var all []byte
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(e.dir, entry.Name()))
		require.NoError(t, err)
		all = append(all, data...)
	}
	return all
}

func TestCreateIMAPAccountSavesTheAccount(t *testing.T) {
	imapSrv := imaptest.New(t)
	smtpSrv := smtptest.New(t)
	e := newConnectEnv(t, true, nil)
	ctx := e.userCtx(e.tenant)
	server := account.MailServer{
		IMAPHost:       " " + imapSrv.Host + " ",
		IMAPPort:       imapSrv.Port,
		IMAPSecurity:   account.SecurityTLS,
		SMTPHost:       smtpSrv.Host,
		SMTPPort:       smtpSrv.Port,
		SMTPSecurity:   account.SecurityTLS,
		Username:       imapSrv.User,
		PinnedIMAPCert: imapSrv.CertSHA256,
		PinnedSMTPCert: smtpSrv.CertSHA256,
	}

	a, err := e.svc.CreateIMAPAccount(ctx, " alice@example.com ", server, secrets.Secret(imapSrv.Pass))
	require.NoError(t, err)

	// A new account with the defaults of a new account.
	require.Equal(t, e.tenant, a.TenantID)
	require.Equal(t, "alice@example.com", a.Address)
	require.Equal(t, account.ProviderIMAP, a.Provider)
	require.Equal(t, account.MailSend, a.Level(account.AreaMail))
	require.True(t, a.ConfirmSends)
	require.True(t, a.ConfirmDeletes)
	require.Equal(t, account.DefaultLimits(), a.Limits)
	require.Equal(t, account.StatusOK, a.Status)
	require.Equal(t, testStart, a.CreatedAt)
	require.Equal(t, imapSrv.Host, a.Mail.IMAPHost, "the host is trimmed")
	require.Equal(t, imapSrv.CertSHA256, a.Mail.PinnedIMAPCert)
	require.Equal(t, smtpSrv.CertSHA256, a.Mail.PinnedSMTPCert)
	stored, err := e.store.GetAccount(context.Background(), e.tenant, a.ID)
	require.NoError(t, err)
	require.Equal(t, a, stored)

	// The probe ran once, with the cleaned settings, before the save.
	calls := e.probe.configs()
	require.Len(t, calls, 1)
	require.Equal(t, *a.Mail, calls[0].Server)
	require.Equal(t, imapSrv.Pass, calls[0].Password.Reveal())
	require.True(t, calls[0].Local)
	require.Equal(t, 1, smtpSrv.AuthAttempts())

	// The password is sealed with the associated data that the backend factory
	// uses: the factory builds a working backend from it.
	version, sealed, err := e.store.GetAccountSecret(context.Background(), e.tenant, a.ID, "password")
	require.NoError(t, err)
	plain, err := e.vault.Decrypt(context.Background(), e.tenant, version,
		secrets.AAD(string(e.tenant), string(a.ID), "password"), sealed)
	require.NoError(t, err)
	require.Equal(t, imapSrv.Pass, string(plain))
	b, err := e.svc.d.Backends.Mail(context.Background(), stored)
	require.NoError(t, err)
	folders, err := b.ListFolders(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, folders)

	// One audit entry, with no address in it.
	entries, err := e.audit.List(context.Background(), e.tenant, audit.Filter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	got := entries[0]
	require.Equal(t, a.ID, got.Account)
	require.Equal(t, account.AreaMail, got.Area)
	require.Equal(t, "connect_account", got.Tool)
	require.Equal(t, audit.OutcomeOK, got.Outcome)
	require.Equal(t, "account_connected", got.Detail)
	require.Equal(t, "web", got.Client)
	require.Empty(t, got.Recipients)
	require.NotContains(t, fmt.Sprintf("%+v", got), "alice")

	// The password is in no data file, also not in the write-ahead log.
	require.False(t, bytes.Contains(e.dataFiles(t), []byte(imapSrv.Pass)))
}

func TestCreateIMAPAccountChecksBeforeTheProbe(t *testing.T) {
	e := newConnectEnv(t, true, probeOK)
	ctx := e.userCtx(e.tenant)

	invalid := map[string]func(*account.MailServer){
		"empty imap host":           func(s *account.MailServer) { s.IMAPHost = "" },
		"white space imap host":     func(s *account.MailServer) { s.IMAPHost = "  \t " },
		"white space smtp host":     func(s *account.MailServer) { s.SMTPHost = "\n" },
		"host with a port":          func(s *account.MailServer) { s.IMAPHost = "imap.example.com:993" },
		"host with a space":         func(s *account.MailServer) { s.SMTPHost = "smtp example.com" },
		"host with a scheme":        func(s *account.MailServer) { s.IMAPHost = "imaps://imap.example.com" },
		"port zero":                 func(s *account.MailServer) { s.IMAPPort = 0 },
		"port too large":            func(s *account.MailServer) { s.SMTPPort = 70000 },
		"unknown security":          func(s *account.MailServer) { s.IMAPSecurity = "ssl" },
		"plain to a remote host":    func(s *account.MailServer) { s.IMAPSecurity = account.SecurityPlain },
		"pin that is not a hash":    func(s *account.MailServer) { s.PinnedIMAPCert = "abc" },
		"pin that is not hex":       func(s *account.MailServer) { s.PinnedSMTPCert = strings.Repeat("z", 64) },
		"user name with a newline":  func(s *account.MailServer) { s.Username = "alice\r\nA1 LOGOUT" },
		"user name with a NUL byte": func(s *account.MailServer) { s.Username = "alice\x00" },
	}
	for name, change := range invalid {
		server := exampleServer()
		change(&server)
		_, err := e.svc.CreateIMAPAccount(ctx, "alice@example.com", server, connectPassword)
		require.ErrorIs(t, err, ErrInvalidSettings, name)
	}

	for _, address := range []string{"", "   ", "alice", "@example.com", "alice@", "Alice <alice@example.com>", "a b@example.com", "alice@example.com\r\nBcc: x@y.z"} {
		_, err := e.svc.CreateIMAPAccount(ctx, address, exampleServer(), connectPassword)
		require.ErrorIs(t, err, ErrInvalidAddress, "%q", address)
	}

	_, err := e.svc.CreateIMAPAccount(ctx, "alice@example.com", exampleServer(), "")
	require.ErrorIs(t, err, ErrNoPassword)

	_, err = e.svc.CreateIMAPAccount(context.Background(), "alice@example.com", exampleServer(), connectPassword)
	require.ErrorIs(t, err, ErrNoPrincipal)

	require.Empty(t, e.probe.configs(), "nothing was probed")
	e.requireNoAccounts(t, e.tenant)
}

func TestCreateIMAPAccountCleansTheSettings(t *testing.T) {
	e := newConnectEnv(t, true, probeOK)
	ctx := e.userCtx(e.tenant)
	pin := strings.Repeat("Ab", 32)
	server := account.MailServer{
		IMAPHost:       " IMAP.Example.COM ",
		IMAPPort:       993,
		IMAPSecurity:   account.SecurityTLS,
		SMTPHost:       "\tSMTP.example.com\n",
		SMTPPort:       587,
		SMTPSecurity:   account.SecuritySTARTTLS,
		Username:       "  bob  ",
		PinnedIMAPCert: " " + pin + " ",
	}

	a, err := e.svc.CreateIMAPAccount(ctx, "alice@example.com", server, connectPassword)
	require.NoError(t, err)
	require.Equal(t, "imap.example.com", a.Mail.IMAPHost)
	require.Equal(t, "smtp.example.com", a.Mail.SMTPHost)
	require.Equal(t, "bob", a.Mail.Username)
	require.Equal(t, strings.ToLower(pin), a.Mail.PinnedIMAPCert)
	require.Empty(t, a.Mail.PinnedSMTPCert)
	require.Equal(t, *a.Mail, e.probe.configs()[0].Server, "the probe got the cleaned settings")

	// An empty user name means the address.
	server = exampleServer()
	server.Username = "  "
	a, err = e.svc.CreateIMAPAccount(ctx, "carol@example.com", server, connectPassword)
	require.NoError(t, err)
	require.Equal(t, "carol@example.com", a.Mail.Username)
}

func TestCreateIMAPAccountRefusesWhenTheProbeFails(t *testing.T) {
	unknown := &imapsmtp.CertificateUnknownError{
		Protocol: "imap", Host: "imap.example.com",
		FingerprintSHA256: strings.Repeat("ab", 32), Reason: x509.UnknownAuthorityError{},
	}
	probeErr := errors.New("imap: login failed [AUTHENTICATIONFAILED]")
	cases := map[string]struct {
		fn    func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error)
		check func(t *testing.T, err error)
	}{
		"login failed": {
			fn: func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) { return nil, probeErr },
			check: func(t *testing.T, err error) {
				var testErr *ConnectionTestError
				require.ErrorAs(t, err, &testErr)
				require.ErrorIs(t, err, probeErr)
			},
		},
		"unknown certificate": {
			fn: func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
				return &imapsmtp.ProbeResult{IMAPCert: unknown, SMTPOK: true}, nil
			},
			check: func(t *testing.T, err error) {
				var certErr *CertificateError
				require.ErrorAs(t, err, &certErr)
				require.Same(t, unknown, certErr.IMAP)
				require.Nil(t, certErr.SMTP)
			},
		},
		"smtp not ok without a reason": {
			fn: func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
				return &imapsmtp.ProbeResult{IMAPOK: true}, nil
			},
			check: func(t *testing.T, err error) {
				var testErr *ConnectionTestError
				require.ErrorAs(t, err, &testErr)
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newConnectEnv(t, true, tc.fn)
			_, err := e.svc.CreateIMAPAccount(e.userCtx(e.tenant), "alice@example.com", exampleServer(), connectPassword)
			require.Error(t, err)
			tc.check(t, err)
			require.NotContains(t, fmt.Sprintf("%v %+v", err, err), connectPassword)
			e.requireNoAccounts(t, e.tenant)
			require.False(t, bytes.Contains(e.dataFiles(t), []byte(connectPassword)))
		})
	}
}

func TestCreateIMAPAccountRefusesADuplicateAddress(t *testing.T) {
	e := newConnectEnv(t, true, probeOK)
	ctx := e.userCtx(e.tenant)
	first, err := e.svc.CreateIMAPAccount(ctx, "alice@example.com", exampleServer(), connectPassword)
	require.NoError(t, err)

	_, err = e.svc.CreateIMAPAccount(ctx, " ALICE@Example.com ", exampleServer(), connectPassword)
	require.ErrorIs(t, err, ErrAccountExists)
	require.Len(t, e.probe.configs(), 1, "a duplicate is refused before the probe")
	accounts, err := e.store.ListAccounts(context.Background(), e.tenant)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, first.ID, accounts[0].ID)

	// Another tenant may connect the same address.
	e.addTenant("t2")
	_, err = e.svc.CreateIMAPAccount(e.userCtx("t2"), "alice@example.com", exampleServer(), connectPassword)
	require.NoError(t, err)
}

func TestCreateIMAPAccountRefusesADuplicateMadeDuringTheProbe(t *testing.T) {
	var e *connectEnv
	e = newConnectEnv(t, true, func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
		// Another process connects the same address while the probe runs.
		other := account.NewIMAPAccount(e.tenant, "Alice@example.com", exampleServer(), testStart)
		if err := e.store.CreateAccountWithSecret(context.Background(), other, "password", 1, []byte("sealed")); err != nil {
			return nil, err
		}
		return &imapsmtp.ProbeResult{IMAPOK: true, SMTPOK: true}, nil
	})

	_, err := e.svc.CreateIMAPAccount(e.userCtx(e.tenant), "alice@example.com", exampleServer(), connectPassword)
	require.ErrorIs(t, err, ErrAccountExists)
	accounts, err := e.store.ListAccounts(context.Background(), e.tenant)
	require.NoError(t, err)
	require.Len(t, accounts, 1, "only the account of the other process is stored")
	require.False(t, bytes.Contains(e.dataFiles(t), []byte(connectPassword)))
}

func TestCreateIMAPAccountPlainNeedsALocalInstall(t *testing.T) {
	plain := exampleServer()
	plain.IMAPHost, plain.IMAPSecurity = "127.0.0.1", account.SecurityPlain
	plain.SMTPHost, plain.SMTPSecurity = "localhost", account.SecurityPlain

	hosted := newConnectEnv(t, false, probeOK)
	_, err := hosted.svc.CreateIMAPAccount(hosted.userCtx(hosted.tenant), "alice@example.com", plain, connectPassword)
	require.ErrorIs(t, err, ErrInvalidSettings)
	require.Empty(t, hosted.probe.configs())

	local := newConnectEnv(t, true, probeOK)
	_, err = local.svc.CreateIMAPAccount(local.userCtx(local.tenant), "alice@example.com", plain, connectPassword)
	require.NoError(t, err)
	require.Equal(t, account.SecurityPlain, local.probe.configs()[0].Server.IMAPSecurity)
}

func TestCreateIMAPAccountNeedsTheVaultAndTheAudit(t *testing.T) {
	f := newFixture(t)
	p := &probeRecorder{fn: probeOK}
	svc := New(Deps{Store: f.store, Backends: f.newCache(), Local: true, Probe: p.probe})
	ctx := WithPrincipal(context.Background(), Principal{Tenant: f.tenant})

	_, err := svc.CreateIMAPAccount(ctx, "alice@example.com", exampleServer(), connectPassword)
	require.Error(t, err)
	require.Empty(t, p.configs(), "nothing is probed when the account could not be saved")
	accounts, err := f.store.ListAccounts(context.Background(), f.tenant)
	require.NoError(t, err)
	require.Empty(t, accounts)
}
