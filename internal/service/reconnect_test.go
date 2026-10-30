package service

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
)

// oldPin is a pin that no test server shows: the certificate that the user
// trusted before the server changed it.
var oldPin = strings.Repeat("0a", 32)

// testServers are a test IMAP server and a test SMTP server with the same login.
type testServers struct {
	imap *imaptest.Server
	smtp *smtptest.Server
}

func startServers(t *testing.T) testServers {
	t.Helper()
	return testServers{imap: imaptest.New(t), smtp: smtptest.New(t)}
}

// pinned returns the settings of the servers with both certificates pinned, as
// a connected account has them.
func (s testServers) pinned() account.MailServer {
	return account.MailServer{
		IMAPHost: s.imap.Host, IMAPPort: s.imap.Port, IMAPSecurity: account.SecurityTLS,
		SMTPHost: s.smtp.Host, SMTPPort: s.smtp.Port, SMTPSecurity: account.SecurityTLS,
		Username:       s.imap.User,
		PinnedIMAPCert: s.imap.CertSHA256,
		PinnedSMTPCert: s.smtp.CertSHA256,
	}
}

// snapshot returns the stored account and its sealed password, to show that a
// call changed nothing.
func (e *connectEnv) snapshot(t *testing.T, a *account.Account) (*account.Account, []byte) {
	t.Helper()
	_, sealed, err := e.store.GetAccountSecret(context.Background(), a.TenantID, a.ID, passwordSecret)
	require.NoError(t, err)
	return e.reload(a), sealed
}

// storedPassword returns the stored password of the account.
func (e *connectEnv) storedPassword(t *testing.T, a *account.Account) string {
	t.Helper()
	ctx := context.Background()
	version, sealed, err := e.store.GetAccountSecret(ctx, a.TenantID, a.ID, passwordSecret)
	require.NoError(t, err)
	plain, err := e.vault.Decrypt(ctx, a.TenantID, version, secrets.AAD(string(a.TenantID), string(a.ID), passwordSecret), sealed)
	require.NoError(t, err)
	return string(plain)
}

// entries returns the audit entries of the tenant.
func (e *connectEnv) entries(t *testing.T) []audit.Entry {
	t.Helper()
	entries, err := e.audit.List(context.Background(), e.tenant, audit.Filter{})
	require.NoError(t, err)
	return entries
}

// requireUnchanged checks that the account and its password are as before, and
// that nothing was audited after the entries in before.
func (e *connectEnv) requireUnchanged(t *testing.T, a, before *account.Account, sealedBefore []byte, entriesBefore int) {
	t.Helper()
	after, sealedAfter := e.snapshot(t, a)
	require.Equal(t, before, after, "the account did not change")
	require.Equal(t, sealedBefore, sealedAfter, "the password did not change")
	require.Len(t, e.entries(t), entriesBefore, "nothing was audited")
}

// builtConfigs records the configs of the backends that a cache builds.
type builtConfigs struct {
	mu   sync.Mutex
	cfgs []imapsmtp.Config
}

func (b *builtConfigs) last(t *testing.T) imapsmtp.Config {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	require.NotEmpty(t, b.cfgs)
	return b.cfgs[len(b.cfgs)-1]
}

// recordBuilds makes the backend cache of the env record the config of each
// backend that it builds.
func (e *connectEnv) recordBuilds(t *testing.T) (*BackendCache, *builtConfigs) {
	t.Helper()
	cache, ok := e.svc.d.Backends.(*BackendCache)
	require.True(t, ok)
	built := &builtConfigs{}
	realNew := cache.newBackend
	cache.newBackend = func(cfg imapsmtp.Config, signer *signing.Signer) *imapsmtp.Backend {
		built.mu.Lock()
		built.cfgs = append(built.cfgs, cfg)
		built.mu.Unlock()
		return realNew(cfg, signer)
	}
	return cache, built
}

// A new password that the server refuses changes nothing: the old password
// stays, and so does the status.
func TestReconnectWithAWrongPasswordChangesNothing(t *testing.T) {
	const wrong = "pw-wrong-9c1e"
	srv := startServers(t)
	e := newConnectEnv(t, true, nil)
	a := e.addAccount(e.tenant, "alice@example.com", srv.pinned(), srv.imap.Pass,
		func(a *account.Account) { a.Status = account.StatusNeedsReauth })
	before, sealedBefore := e.snapshot(t, a)
	ctx := e.userCtx(e.tenant)

	_, err := e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{Password: wrong})

	var testErr *ConnectionTestError
	require.ErrorAs(t, err, &testErr)
	require.ErrorIs(t, err, imapsmtp.ErrAuthFailed)
	require.NotContains(t, fmt.Sprintf("%v %+v", err, err), wrong)
	e.requireUnchanged(t, a, before, sealedBefore, 0)
	require.Equal(t, srv.imap.Pass, e.storedPassword(t, a))
	require.False(t, bytes.Contains(e.dataFiles(t), []byte(wrong)))
	require.NoError(t, e.svc.ProbeAccount(ctx, a.ID), "the old password still works")
}

// A new password that works is saved, and the account is connected again. The
// account keeps its id, its levels, its queued messages and its activity. The
// next call of the backend cache builds a backend with the new password.
func TestReconnectSavesTheNewPassword(t *testing.T) {
	srv := startServers(t)
	e := newConnectEnv(t, true, nil)
	a := e.addAccount(e.tenant, "alice@example.com", srv.pinned(), "pw-old-revoked", func(a *account.Account) {
		a.Status = account.StatusNeedsReauth
		a.Levels[account.AreaMail] = account.MailOrganize
	})
	bg := context.Background()
	require.NoError(t, e.store.CreateOutbox(bg, &store.OutboxRow{
		ID: "queued1", Tenant: e.tenant, Account: a.ID,
		SendAfter: testStart.Add(time.Hour), NextAttemptAt: testStart.Add(time.Hour), CreatedAt: testStart,
	}, []byte("sealed message"), 1))
	require.NoError(t, e.audit.Record(bg, audit.Entry{
		Tenant: e.tenant, Account: a.ID, Area: account.AreaMail, Client: "agent", Tool: "send_message", Outcome: audit.OutcomeOK,
	}))
	cache, built := e.recordBuilds(t)
	oldBackend, err := cache.Mail(bg, a)
	require.NoError(t, err)
	require.Equal(t, "pw-old-revoked", built.last(t).Password.Reveal())

	res, err := e.svc.ReconnectIMAPAccount(e.userCtx(e.tenant), a.ID, Reconnect{Password: secrets.Secret(srv.imap.Pass)})

	require.NoError(t, err)
	require.Equal(t, &ReconnectResult{PasswordChanged: true}, res)
	got := e.reload(a)
	want := *a
	want.Status = account.StatusOK
	want.CredentialsVersion = a.CredentialsVersion + 1
	require.Equal(t, &want, got, "the id, the levels and the settings stay, and the status is ok")
	require.Equal(t, srv.imap.Pass, e.storedPassword(t, a))
	row, err := e.store.GetOutbox(bg, e.tenant, "queued1")
	require.NoError(t, err)
	require.Equal(t, "queued", row.Status, "the queued message stays")

	entries := e.entries(t)
	require.Len(t, entries, 2, "the old entry stays, and the reconnect adds one")
	var reconnect audit.Entry
	for _, entry := range entries {
		if entry.Tool == "reconnect_account" {
			reconnect = entry
		}
	}
	require.Equal(t, a.ID, reconnect.Account)
	require.Equal(t, audit.OutcomeOK, reconnect.Outcome)
	require.Equal(t, "account_reconnected_password_changed", reconnect.Detail)
	require.Equal(t, "web", reconnect.Client)
	require.Empty(t, reconnect.Recipients)
	require.NotContains(t, fmt.Sprintf("%+v", reconnect), "alice")
	require.Contains(t, AuditTools(), "reconnect_account")

	newBackend, err := cache.Mail(bg, got)
	require.NoError(t, err)
	require.NotSame(t, oldBackend, newBackend, "the backend with the old password is not used again")
	require.Equal(t, srv.imap.Pass, built.last(t).Password.Reveal(), "the next call uses the new password")
	folders, err := newBackend.ListFolders(bg)
	require.NoError(t, err)
	require.NotEmpty(t, folders)
	require.False(t, bytes.Contains(e.dataFiles(t), []byte(srv.imap.Pass)), "the password is stored only sealed")
}

// An empty password keeps the stored one. The reconnect still tests the
// servers, and marks the account as connected again.
func TestReconnectWithAnEmptyPasswordKeepsTheStoredOne(t *testing.T) {
	srv := startServers(t)
	e := newConnectEnv(t, true, nil)
	a := e.addAccount(e.tenant, "alice@example.com", srv.pinned(), srv.imap.Pass,
		func(a *account.Account) { a.Status = account.StatusNeedsReauth })
	_, sealedBefore := e.snapshot(t, a)

	res, err := e.svc.ReconnectIMAPAccount(e.userCtx(e.tenant), a.ID, Reconnect{})

	require.NoError(t, err)
	require.Equal(t, &ReconnectResult{}, res)
	got, sealedAfter := e.snapshot(t, a)
	require.Equal(t, account.StatusOK, got.Status)
	require.Equal(t, sealedBefore, sealedAfter, "the stored password was not sealed again")
	entries := e.entries(t)
	require.Len(t, entries, 1)
	require.Equal(t, "account_reconnected", entries[0].Detail)
	require.Len(t, e.probe.configs(), 1)
	require.Equal(t, srv.imap.Pass, e.probe.configs()[0].Password.Reveal(), "the test used the stored password")
}

// A server whose pinned certificate changed is refused without a decision of
// the user. The error shows the host, the port, the old pin and the new
// certificate. With the decision, the pin is replaced, and only for that server.
// The cached backend with the old pin is not used again.
func TestReconnectReplacesAChangedPinOnlyWithTheConfirmation(t *testing.T) {
	srv := startServers(t)
	e := newConnectEnv(t, true, nil)
	server := srv.pinned()
	server.PinnedIMAPCert = oldPin
	a := e.addAccount(e.tenant, "alice@example.com", server, srv.imap.Pass)
	before, sealedBefore := e.snapshot(t, a)
	ctx := e.userCtx(e.tenant)
	cache, built := e.recordBuilds(t)
	oldBackend, err := cache.Mail(context.Background(), a)
	require.NoError(t, err)

	_, err = e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{Password: secrets.Secret(srv.imap.Pass)})

	var certErr *ReconnectCertificateError
	require.ErrorAs(t, err, &certErr)
	require.Nil(t, certErr.SMTP, "the SMTP certificate is still the pinned one")
	require.NotNil(t, certErr.IMAP)
	require.Equal(t, srv.imap.Host, certErr.IMAP.Host)
	require.Equal(t, srv.imap.Port, certErr.IMAP.Port)
	require.Equal(t, oldPin, certErr.IMAP.OldPin)
	require.Equal(t, srv.imap.CertSHA256, certErr.IMAP.Cert.FingerprintSHA256)
	require.True(t, certErr.IMAP.Cert.Pinned)
	require.Equal(t, PinChangeReplace, certErr.IMAP.Change)
	require.NotContains(t, err.Error(), srv.imap.Pass)
	e.requireUnchanged(t, a, before, sealedBefore, 0)

	// A decision to remove the pin does not replace it.
	_, err = e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{
		Password: secrets.Secret(srv.imap.Pass), PinnedIMAPCert: srv.imap.CertSHA256, RemoveIMAPPin: true,
	})
	require.ErrorAs(t, err, &certErr)
	e.requireUnchanged(t, a, before, sealedBefore, 0)

	// The user confirmed the new certificate. The decision may be in upper case.
	res, err := e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{
		Password: secrets.Secret(srv.imap.Pass), PinnedIMAPCert: " " + strings.ToUpper(srv.imap.CertSHA256) + " ",
	})

	require.NoError(t, err)
	require.Equal(t, &ReconnectResult{PasswordChanged: true, PinChanged: true}, res)
	got := e.reload(a)
	require.Equal(t, srv.imap.CertSHA256, got.Mail.PinnedIMAPCert, "the IMAP pin is the new certificate")
	require.Equal(t, srv.smtp.CertSHA256, got.Mail.PinnedSMTPCert, "the SMTP pin did not change")
	require.Equal(t, account.StatusOK, got.Status)
	entries := e.entries(t)
	require.Len(t, entries, 1)
	require.Equal(t, "account_reconnected_password_and_pin_changed", entries[0].Detail)

	newBackend, err := cache.Mail(context.Background(), got)
	require.NoError(t, err)
	require.NotSame(t, oldBackend, newBackend)
	require.Equal(t, srv.imap.CertSHA256, built.last(t).Server.PinnedIMAPCert, "the next call uses the new pin")
	_, err = newBackend.ListFolders(context.Background())
	require.NoError(t, err)
}

// A server that had no pin and now shows a certificate that no known authority
// signed follows the first-pin rules: the user may pin it.
func TestReconnectPinsACertificateForTheFirstTime(t *testing.T) {
	srv := startServers(t)
	e := newConnectEnv(t, true, nil)
	server := srv.pinned()
	server.PinnedSMTPCert = ""
	a := e.addAccount(e.tenant, "alice@example.com", server, srv.imap.Pass)
	ctx := e.userCtx(e.tenant)

	_, err := e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{})
	var certErr *ReconnectCertificateError
	require.ErrorAs(t, err, &certErr)
	require.Nil(t, certErr.IMAP)
	require.Equal(t, PinChangeAdd, certErr.SMTP.Change)
	require.Empty(t, certErr.SMTP.OldPin)

	res, err := e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{PinnedSMTPCert: srv.smtp.CertSHA256})
	require.NoError(t, err)
	require.Equal(t, &ReconnectResult{PinChanged: true}, res)
	require.Equal(t, srv.smtp.CertSHA256, e.reload(a).Mail.PinnedSMTPCert)
	require.Equal(t, srv.imap.CertSHA256, e.reload(a).Mail.PinnedIMAPCert)
}

// A decision counts only for the server that showed the certificate. A
// decision for a server whose certificate is still the trusted one, or a
// decision for the wrong server, changes nothing.
func TestReconnectChangesOnlyThePinOfTheServerThatShowedIt(t *testing.T) {
	srv := startServers(t)
	e := newConnectEnv(t, true, nil)
	server := srv.pinned()
	server.PinnedIMAPCert = oldPin
	a := e.addAccount(e.tenant, "alice@example.com", server, srv.imap.Pass)
	before, sealedBefore := e.snapshot(t, a)
	ctx := e.userCtx(e.tenant)

	// The SMTP server shows its trusted certificate, so a decision for it is
	// not about anything that the user saw.
	_, err := e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{
		PinnedIMAPCert: srv.imap.CertSHA256, PinnedSMTPCert: srv.imap.CertSHA256,
	})
	require.ErrorIs(t, err, ErrCertificateDecisionStale)
	e.requireUnchanged(t, a, before, sealedBefore, 0)

	// The decision for the IMAP certificate was given for the SMTP server.
	_, err = e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{PinnedSMTPCert: srv.imap.CertSHA256})
	var certErr *ReconnectCertificateError
	require.ErrorAs(t, err, &certErr)
	require.NotNil(t, certErr.IMAP)
	e.requireUnchanged(t, a, before, sealedBefore, 0)

	// A decision for another certificate than the one the server shows.
	_, err = e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{PinnedIMAPCert: strings.Repeat("ef", 32)})
	require.ErrorAs(t, err, &certErr)
	require.Equal(t, srv.imap.CertSHA256, certErr.IMAP.Cert.FingerprintSHA256)
	e.requireUnchanged(t, a, before, sealedBefore, 0)
}

// A certificate for another host is refused, also with a decision of the user:
// the only problems that a reconnect accepts are a changed pin and an unknown
// authority.
func TestReconnectRefusesACertificateForAnotherHost(t *testing.T) {
	srv := startServers(t)
	e := newConnectEnv(t, true, nil)
	server := srv.pinned()
	server.IMAPHost = "localhost" // the certificate is for 127.0.0.1 only
	server.PinnedIMAPCert = oldPin
	a := e.addAccount(e.tenant, "alice@example.com", server, srv.imap.Pass)
	before, sealedBefore := e.snapshot(t, a)

	_, err := e.svc.ReconnectIMAPAccount(e.userCtx(e.tenant), a.ID, Reconnect{PinnedIMAPCert: srv.imap.CertSHA256})

	var certErr *ReconnectCertificateError
	require.ErrorAs(t, err, &certErr)
	require.Equal(t, PinChangeRefused, certErr.IMAP.Change)
	var hostnameErr x509.HostnameError
	require.ErrorAs(t, certErr.IMAP.Cert.Reason, &hostnameErr)
	e.requireUnchanged(t, a, before, sealedBefore, 0)
}

// A certificate out of its time, or with another problem, is refused, also with
// a decision of the user. No second test runs.
func TestReconnectRefusesAnInvalidCertificateEvenWithTheDecision(t *testing.T) {
	fingerprint := strings.Repeat("ab", 32)
	for name, reason := range map[string]error{
		"expired or not valid yet": x509.CertificateInvalidError{Reason: x509.Expired},
		"wrong host":               x509.HostnameError{Host: "imap.example.com"},
		"another problem":          errors.New("bad"),
	} {
		t.Run(name, func(t *testing.T) {
			e := newConnectEnv(t, true, func(_ context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
				return &imapsmtp.ProbeResult{SMTPOK: true, IMAPCert: &imapsmtp.CertificateUnknownError{
					Protocol: "imap", Host: cfg.Server.IMAPHost, FingerprintSHA256: fingerprint, Pinned: true, Reason: reason,
				}}, nil
			})
			server := exampleServer()
			server.PinnedIMAPCert = oldPin
			a := e.addAccount(e.tenant, "alice@example.com", server, connectPassword)
			before, sealedBefore := e.snapshot(t, a)

			_, err := e.svc.ReconnectIMAPAccount(e.userCtx(e.tenant), a.ID, Reconnect{PinnedIMAPCert: fingerprint})

			var certErr *ReconnectCertificateError
			require.ErrorAs(t, err, &certErr)
			require.Equal(t, PinChangeRefused, certErr.IMAP.Change)
			require.Len(t, e.probe.configs(), 1, "no second test")
			e.requireUnchanged(t, a, before, sealedBefore, 0)
		})
	}
}

// When a known authority now signs the new certificate for the host, the user
// may remove the pin. The second test then runs without the pin, so the full
// check of the certificate decides.
func TestReconnectRemovesThePinForACertificateThatAnAuthoritySigns(t *testing.T) {
	fingerprint := strings.Repeat("ab", 32)
	e := newConnectEnv(t, true, func(_ context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
		if cfg.Server.PinnedIMAPCert == oldPin {
			return &imapsmtp.ProbeResult{SMTPOK: true, IMAPCert: &imapsmtp.CertificateUnknownError{
				Protocol: "imap", Host: cfg.Server.IMAPHost, FingerprintSHA256: fingerprint, Pinned: true,
			}}, nil
		}
		return &imapsmtp.ProbeResult{IMAPOK: true, SMTPOK: true}, nil
	})
	server := exampleServer()
	server.PinnedIMAPCert = oldPin
	server.PinnedSMTPCert = strings.Repeat("cd", 32)
	a := e.addAccount(e.tenant, "alice@example.com", server, connectPassword)
	ctx := e.userCtx(e.tenant)

	_, err := e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{})
	var certErr *ReconnectCertificateError
	require.ErrorAs(t, err, &certErr)
	require.Equal(t, PinChangeRemove, certErr.IMAP.Change)

	// A decision to replace the pin does not remove it: the user agreed to
	// trust one certificate, not every certificate that an authority signs.
	_, err = e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{PinnedIMAPCert: fingerprint})
	require.ErrorAs(t, err, &certErr)
	require.Equal(t, oldPin, e.reload(a).Mail.PinnedIMAPCert)

	res, err := e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{PinnedIMAPCert: fingerprint, RemoveIMAPPin: true})
	require.NoError(t, err)
	require.Equal(t, &ReconnectResult{PinChanged: true}, res)
	got := e.reload(a)
	require.Empty(t, got.Mail.PinnedIMAPCert, "the pin is removed")
	require.Equal(t, server.PinnedSMTPCert, got.Mail.PinnedSMTPCert)
	calls := e.probe.configs()
	require.Len(t, calls, 4)
	require.Empty(t, calls[3].Server.PinnedIMAPCert, "the second test ran without the pin")
	require.Equal(t, server.PinnedSMTPCert, calls[3].Server.PinnedSMTPCert)
}

// The input is checked before any test, and an account of another tenant is
// not found.
func TestReconnectChecksBeforeTheProbe(t *testing.T) {
	e := newConnectEnv(t, true, probeOK)
	a := e.addAccount(e.tenant, "alice@example.com", exampleServer(), connectPassword)
	e.addTenant("t2")
	ctx := e.userCtx(e.tenant)

	_, err := e.svc.ReconnectIMAPAccount(context.Background(), a.ID, Reconnect{})
	require.ErrorIs(t, err, ErrNoPrincipal)
	for _, id := range []account.AccountID{"missing", "a|b", ""} {
		_, err = e.svc.ReconnectIMAPAccount(ctx, id, Reconnect{})
		require.ErrorIs(t, err, ErrAccountNotFound, "%q", id)
	}
	_, err = e.svc.ReconnectIMAPAccount(e.userCtx("t2"), a.ID, Reconnect{})
	require.ErrorIs(t, err, ErrAccountNotFound, "another tenant")
	for _, pin := range []string{"abc", strings.Repeat("z", 64)} {
		_, err = e.svc.ReconnectIMAPAccount(ctx, a.ID, Reconnect{PinnedSMTPCert: pin})
		require.ErrorIs(t, err, ErrInvalidSettings, pin)
	}
	require.Empty(t, e.probe.configs(), "nothing was tested")
	require.Empty(t, e.entries(t))
}

// When another program changes the server settings while the test runs, the
// test was made with other settings, so nothing is saved.
func TestReconnectRefusesSettingsThatChangedDuringTheTest(t *testing.T) {
	var e *connectEnv
	var a *account.Account
	e = newConnectEnv(t, true, func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
		changed := e.reload(a)
		changed.Mail.IMAPHost = "imap.other.example.com"
		if err := e.store.UpdateAccount(context.Background(), changed); err != nil {
			return nil, err
		}
		return &imapsmtp.ProbeResult{IMAPOK: true, SMTPOK: true}, nil
	})
	a = e.addAccount(e.tenant, "alice@example.com", exampleServer(), connectPassword,
		func(a *account.Account) { a.Status = account.StatusNeedsReauth })
	_, sealedBefore := e.snapshot(t, a)

	_, err := e.svc.ReconnectIMAPAccount(e.userCtx(e.tenant), a.ID, Reconnect{Password: connectPassword})

	require.ErrorIs(t, err, ErrAccountChanged)
	got, sealedAfter := e.snapshot(t, a)
	require.Equal(t, account.StatusNeedsReauth, got.Status)
	require.Equal(t, sealedBefore, sealedAfter)
	require.Empty(t, e.entries(t))
}

// ReconnectPinChange allows a change only for a changed pin whose new
// certificate has no other problem than an unknown authority (replace), or no
// problem at all (remove), and for a first pin under the rules of a new account.
func TestReconnectPinChange(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	unknown := x509.UnknownAuthorityError{}
	for name, tc := range map[string]struct {
		cert *imapsmtp.CertificateUnknownError
		want PinChange
	}{
		"no certificate":                        {nil, PinChangeRefused},
		"first pin, unknown authority":          {&imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Reason: unknown}, PinChangeAdd},
		"first pin, wrapped unknown authority":  {&imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Reason: fmt.Errorf("x: %w", unknown)}, PinChangeAdd},
		"first pin, no fingerprint":             {&imapsmtp.CertificateUnknownError{Reason: unknown}, PinChangeRefused},
		"first pin, wrong host":                 {&imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Reason: x509.HostnameError{}}, PinChangeRefused},
		"first pin, expired":                    {&imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Reason: x509.CertificateInvalidError{Reason: x509.Expired}}, PinChangeRefused},
		"first pin, no reason":                  {&imapsmtp.CertificateUnknownError{FingerprintSHA256: fp}, PinChangeRefused},
		"changed pin, unknown authority":        {&imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Pinned: true, Reason: unknown}, PinChangeReplace},
		"changed pin, signed for the host":      {&imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Pinned: true}, PinChangeRemove},
		"changed pin, wrong host":               {&imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Pinned: true, Reason: x509.HostnameError{}}, PinChangeRefused},
		"changed pin, expired":                  {&imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Pinned: true, Reason: x509.CertificateInvalidError{Reason: x509.Expired}}, PinChangeRefused},
		"changed pin, another problem":          {&imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Pinned: true, Reason: errors.New("bad")}, PinChangeRefused},
		"changed pin, fingerprint not a sha256": {&imapsmtp.CertificateUnknownError{FingerprintSHA256: "ABCD", Pinned: true, Reason: unknown}, PinChangeRefused},
		"changed pin, no fingerprint":           {&imapsmtp.CertificateUnknownError{Pinned: true}, PinChangeRefused},
	} {
		require.Equal(t, tc.want, ReconnectPinChange(tc.cert), name)
	}
}
