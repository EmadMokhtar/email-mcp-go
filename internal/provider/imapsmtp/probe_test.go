package imapsmtp

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
)

func TestProbeLogsInToBothServers(t *testing.T) {
	imapSrv := imaptest.New(t)
	smtpSrv := smtptest.New(t)
	cfg := smtpConfig(imapSrv, smtpSrv, account.SecurityTLS)
	logs := captureLog(&cfg)

	res, err := Probe(t.Context(), cfg)

	require.NoError(t, err)
	require.True(t, res.IMAPOK)
	require.True(t, res.SMTPOK)
	require.Nil(t, res.IMAPCert)
	require.Nil(t, res.SMTPCert)
	require.Contains(t, res.Capabilities, "IMAP4rev1")
	require.True(t, slices.IsSorted(res.Capabilities), "the capabilities come in a fixed order")

	// A probe sends nothing and opens no mailbox.
	require.Empty(t, smtpSrv.Messages())
	require.Equal(t, 1, smtpSrv.AuthAttempts())
	rw, ro := imapSrv.SelectCounts()
	require.Zero(t, rw+ro, "no mailbox was selected")

	// The password is nowhere in what a probe gives back or logs.
	requireNoneOf(t, fmt.Sprintf("%+v %#v", res, res), smtpSrv.Pass)
	requireNoneOf(t, logs.String(), smtpSrv.Pass)
}

func TestProbeWorksWithEachSecurityMode(t *testing.T) {
	for name, tc := range map[string]struct {
		imapMode imaptest.Mode
		imapSec  account.Security
		smtpMode smtptest.Mode
		smtpSec  account.Security
	}{
		"STARTTLS":      {imaptest.ModeSTARTTLS, account.SecuritySTARTTLS, smtptest.ModeSTARTTLS, account.SecuritySTARTTLS},
		"plain":         {imaptest.ModePlain, account.SecurityPlain, smtptest.ModePlain, account.SecurityPlain},
		"mixed TLS/STL": {imaptest.ModeTLS, account.SecurityTLS, smtptest.ModeSTARTTLS, account.SecuritySTARTTLS},
	} {
		t.Run(name, func(t *testing.T) {
			imapSrv := imaptest.New(t, imaptest.WithMode(tc.imapMode))
			smtpSrv := smtptest.New(t, smtptest.WithMode(tc.smtpMode))
			cfg := smtpConfig(imapSrv, smtpSrv, tc.smtpSec)
			cfg.Server.IMAPSecurity = tc.imapSec

			res, err := Probe(t.Context(), cfg)

			require.NoError(t, err)
			require.True(t, res.IMAPOK)
			require.True(t, res.SMTPOK)
		})
	}
}

func TestProbeReportsUnknownCerts(t *testing.T) {
	// The certificates are not trusted and not pinned. The probe still finishes,
	// so that the UI can offer to pin both.
	for name, tc := range map[string]struct {
		smtpMode smtptest.Mode
		smtpSec  account.Security
	}{
		"implicit TLS": {smtptest.ModeTLS, account.SecurityTLS},
		"STARTTLS":     {smtptest.ModeSTARTTLS, account.SecuritySTARTTLS},
	} {
		t.Run(name, func(t *testing.T) {
			imapSrv := imaptest.New(t)
			smtpSrv := smtptest.New(t, smtptest.WithMode(tc.smtpMode))
			cfg := smtpConfig(imapSrv, smtpSrv, tc.smtpSec)
			cfg.Server.PinnedIMAPCert = ""
			cfg.Server.PinnedSMTPCert = ""

			res, err := Probe(t.Context(), cfg)

			require.NoError(t, err)
			require.False(t, res.IMAPOK)
			require.False(t, res.SMTPOK)
			require.NotNil(t, res.IMAPCert)
			require.NotNil(t, res.SMTPCert)
			// Each server shows its own certificate.
			require.Equal(t, imapSrv.CertSHA256, res.IMAPCert.FingerprintSHA256)
			require.Equal(t, smtpSrv.CertSHA256, res.SMTPCert.FingerprintSHA256)
			require.NotEqual(t, res.IMAPCert.FingerprintSHA256, res.SMTPCert.FingerprintSHA256)
			require.Equal(t, "imap", res.IMAPCert.Protocol)
			require.Equal(t, "smtp", res.SMTPCert.Protocol)
			require.False(t, res.IMAPCert.Pinned)
			require.False(t, res.SMTPCert.Pinned)
			require.Empty(t, res.Capabilities, "no login, so no capabilities")
			// The password went to neither server.
			require.Zero(t, smtpSrv.AuthAttempts())
		})
	}
}

func TestProbeReportsEachCertOnItsOwn(t *testing.T) {
	imapSrv := imaptest.New(t)
	smtpSrv := smtptest.New(t)

	t.Run("only the SMTP certificate is unknown", func(t *testing.T) {
		cfg := smtpConfig(imapSrv, smtpSrv, account.SecurityTLS)
		cfg.Server.PinnedSMTPCert = ""

		res, err := Probe(t.Context(), cfg)

		require.NoError(t, err)
		require.True(t, res.IMAPOK)
		require.Nil(t, res.IMAPCert)
		require.False(t, res.SMTPOK)
		require.NotNil(t, res.SMTPCert)
		require.NotEmpty(t, res.Capabilities, "the IMAP login worked")
	})

	t.Run("only the IMAP certificate is unknown", func(t *testing.T) {
		cfg := smtpConfig(imapSrv, smtpSrv, account.SecurityTLS)
		cfg.Server.PinnedIMAPCert = ""

		res, err := Probe(t.Context(), cfg)

		require.NoError(t, err)
		require.False(t, res.IMAPOK)
		require.NotNil(t, res.IMAPCert)
		require.True(t, res.SMTPOK)
		require.Nil(t, res.SMTPCert)
	})

	t.Run("a pin that does not match is reported too", func(t *testing.T) {
		cfg := smtpConfig(imapSrv, smtpSrv, account.SecurityTLS)
		cfg.Server.PinnedIMAPCert = strings.Repeat("0", 64)
		cfg.Server.PinnedSMTPCert = strings.Repeat("0", 64)

		res, err := Probe(t.Context(), cfg)

		require.NoError(t, err)
		require.True(t, res.IMAPCert.Pinned)
		require.True(t, res.SMTPCert.Pinned)
	})
}

func TestProbeBadPasswordFails(t *testing.T) {
	const wrong = "wrong-password-123"

	t.Run("IMAP", func(t *testing.T) {
		imapSrv := imaptest.New(t)
		smtpSrv := smtptest.New(t)
		cfg := smtpConfig(imapSrv, smtpSrv, account.SecurityTLS)
		cfg.Password = secrets.Secret(wrong)

		res, err := Probe(t.Context(), cfg)

		require.Error(t, err)
		require.Nil(t, res)
		require.ErrorContains(t, err, "imap: login failed")
		require.ErrorIs(t, err, ErrAuthFailed)
		requireNoneOf(t, err.Error(), wrong, imapSrv.Pass)
		require.Zero(t, smtpSrv.AuthAttempts(), "the SMTP server is not tried after the IMAP login failed")
	})

	t.Run("SMTP", func(t *testing.T) {
		imapSrv := imaptest.New(t)
		// The IMAP login works. The SMTP server wants another password.
		smtpSrv := smtptest.New(t, smtptest.WithLogin(imapSrv.User, "another-password"))
		cfg := smtpConfig(imapSrv, smtpSrv, account.SecurityTLS)
		cfg.Password = secrets.Secret(imapSrv.Pass)

		res, err := Probe(t.Context(), cfg)

		require.Error(t, err)
		require.Nil(t, res)
		require.ErrorContains(t, err, "smtp: login failed")
		require.ErrorContains(t, err, "535")
		require.ErrorIs(t, err, ErrAuthFailed)
		// The server repeats the user name in its text. That text is not in the error.
		requireNoneOf(t, err.Error(), "Authentication failed for", imapSrv.User, imapSrv.Pass, "another-password")
	})
}

func TestProbeConnectionRefusedFails(t *testing.T) {
	t.Run("IMAP", func(t *testing.T) {
		imapSrv := imaptest.New(t)
		smtpSrv := smtptest.New(t)
		cfg := smtpConfig(imapSrv, smtpSrv, account.SecurityTLS)
		imapSrv.Close()

		res, err := Probe(t.Context(), cfg)

		require.ErrorContains(t, err, "imap: connect")
		require.Nil(t, res)
	})

	t.Run("SMTP", func(t *testing.T) {
		imapSrv := imaptest.New(t)
		smtpSrv := smtptest.New(t)
		cfg := smtpConfig(imapSrv, smtpSrv, account.SecurityTLS)
		smtpSrv.Close()

		res, err := Probe(t.Context(), cfg)

		require.ErrorContains(t, err, "smtp: connect")
		require.Nil(t, res)
		requireNoneOf(t, err.Error(), imapSrv.Pass)
	})
}

func TestProbeChecksTheServerSettings(t *testing.T) {
	smtpSrv := smtptest.New(t, smtptest.WithMode(smtptest.ModePlain))
	cfg := smtpConfig(nil, smtpSrv, account.SecurityPlain)
	cfg.Local = false

	res, err := Probe(t.Context(), cfg)

	require.Error(t, err)
	require.Nil(t, res)
	require.Zero(t, smtpSrv.AuthAttempts())
}
