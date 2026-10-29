package service

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
)

// ProbeAccount tests the stored settings of an account with its stored
// password and its stored pins, and changes nothing.
func TestProbeAccountUsesTheStoredSettingsAndPins(t *testing.T) {
	e := newConnectEnv(t, true, probeOK)
	server := exampleServer()
	server.PinnedIMAPCert = strings.Repeat("a1", 32)
	server.PinnedSMTPCert = strings.Repeat("b2", 32)
	a := e.addAccount(e.tenant, "alice@example.com", server, connectPassword)

	require.NoError(t, e.svc.ProbeAccount(e.userCtx(e.tenant), a.ID))

	calls := e.probe.configs()
	require.Len(t, calls, 1)
	require.Equal(t, server, calls[0].Server, "the stored settings, pins included, are tested")
	require.Equal(t, connectPassword, calls[0].Password.Reveal())
	require.True(t, calls[0].Local)

	// Nothing changed: the account, its status, and the audit log.
	require.Equal(t, a.Status, e.reload(a).Status)
	entries, err := e.audit.List(context.Background(), e.tenant, audit.Filter{})
	require.NoError(t, err)
	require.Empty(t, entries)
}

// An account of another tenant, or an id that does not exist, is not found, and
// nothing is tested.
func TestProbeAccountOfAnotherTenantIsNotFound(t *testing.T) {
	e := newConnectEnv(t, true, probeOK)
	e.addTenant("other")
	theirs := e.addAccount("other", "bob@example.com", exampleServer(), connectPassword)

	err := e.svc.ProbeAccount(e.userCtx(e.tenant), theirs.ID)
	require.ErrorIs(t, err, ErrAccountNotFound)
	err = e.svc.ProbeAccount(e.userCtx(e.tenant), "no-such-account")
	require.ErrorIs(t, err, ErrAccountNotFound)
	err = e.svc.ProbeAccount(e.userCtx(e.tenant), "bad id with spaces")
	require.ErrorIs(t, err, ErrAccountNotFound)
	require.Empty(t, e.probe.configs())

	err = e.svc.ProbeAccount(context.Background(), theirs.ID)
	require.ErrorIs(t, err, ErrNoPrincipal)
}

// A failed test and a certificate that is not accepted come back as the same
// typed errors as from CreateIMAPAccount, without the password.
func TestProbeAccountReportsTypedErrors(t *testing.T) {
	cert := &imapsmtp.CertificateUnknownError{
		Protocol: "imap", Host: "imap.example.com", FingerprintSHA256: strings.Repeat("cd", 32), Pinned: true,
	}
	for name, tc := range map[string]struct {
		probe func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error)
		check func(t *testing.T, err error)
	}{
		"refused login": {
			probe: func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
				return nil, fmt.Errorf("imap login: %w", imapsmtp.ErrAuthFailed)
			},
			check: func(t *testing.T, err error) {
				var testErr *ConnectionTestError
				require.ErrorAs(t, err, &testErr)
				require.ErrorIs(t, err, imapsmtp.ErrAuthFailed)
			},
		},
		"a pin that does not match": {
			probe: func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
				return &imapsmtp.ProbeResult{SMTPOK: true, IMAPCert: cert}, nil
			},
			check: func(t *testing.T, err error) {
				var certErr *CertificateError
				require.ErrorAs(t, err, &certErr)
				require.Same(t, cert, certErr.IMAP)
				require.Nil(t, certErr.SMTP)
			},
		},
		"an unknown authority": {
			probe: func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
				return &imapsmtp.ProbeResult{IMAPOK: true, SMTPCert: &imapsmtp.CertificateUnknownError{
					Protocol: "smtp", FingerprintSHA256: strings.Repeat("ef", 32), Reason: x509.UnknownAuthorityError{},
				}}, nil
			},
			check: func(t *testing.T, err error) {
				var certErr *CertificateError
				require.ErrorAs(t, err, &certErr)
				require.NotNil(t, certErr.SMTP)
			},
		},
		"a login that did not work": {
			probe: func(context.Context, imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
				return &imapsmtp.ProbeResult{IMAPOK: true}, nil
			},
			check: func(t *testing.T, err error) {
				var testErr *ConnectionTestError
				require.ErrorAs(t, err, &testErr)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newConnectEnv(t, true, tc.probe)
			a := e.addAccount(e.tenant, "alice@example.com", exampleServer(), connectPassword)

			err := e.svc.ProbeAccount(e.userCtx(e.tenant), a.ID)

			require.Error(t, err)
			tc.check(t, err)
			require.NotContains(t, err.Error(), connectPassword)
		})
	}
}

// An account without a stored password cannot be tested, and the probe is not
// called.
func TestProbeAccountWithoutAPassword(t *testing.T) {
	e := newConnectEnv(t, true, probeOK)
	a := account.NewIMAPAccount(e.tenant, "alice@example.com", exampleServer(), testStart)
	require.NoError(t, e.store.CreateAccount(context.Background(), a))

	err := e.svc.ProbeAccount(e.userCtx(e.tenant), a.ID)

	require.Error(t, err)
	require.False(t, errors.Is(err, ErrAccountNotFound))
	require.Empty(t, e.probe.configs())
}

// ListStoredAccounts lists the accounts of the tenant as stored. It never asks a
// backend, so it opens no connection to a mail server.
func TestListStoredAccountsAsksNoBackend(t *testing.T) {
	f := newFixture(t)
	f.addTenant("other")
	mine := f.addAccount(f.tenant, "one@example.com", offlineSettings(), "very-secret-password")
	theirs := f.addAccount("other", "them@example.com", offlineSettings(), "pw")
	factory := &fakeFactory{backend: &fakeBackend{}}
	svc := f.newService(factory, &fakeLinks{})

	views, err := svc.ListStoredAccounts(f.ctx())

	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, mine.ID, views[0].ID)
	require.Equal(t, "one@example.com", views[0].Address)
	require.Equal(t, map[account.Area]string{account.AreaMail: "Send"}, views[0].Areas)
	require.Equal(t, account.StatusOK, views[0].Status)
	require.NotEqual(t, theirs.ID, views[0].ID)
	factory.mu.Lock()
	require.Zero(t, factory.mailCalls, "no backend was asked")
	factory.mu.Unlock()

	other := newFixture(t)
	views, err = other.newService(factory, &fakeLinks{}).ListStoredAccounts(other.ctx())
	require.NoError(t, err)
	require.NotNil(t, views)
	require.Empty(t, views)

	_, err = svc.ListStoredAccounts(context.Background())
	require.ErrorIs(t, err, ErrNoPrincipal)
}
