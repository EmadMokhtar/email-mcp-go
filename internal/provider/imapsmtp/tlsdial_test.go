package imapsmtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
)

func TestDialAcceptsCertFromRootCAs(t *testing.T) {
	srv := imaptest.New(t)
	cfg := testConfig(srv, account.SecurityTLS)
	cfg.RootCAs = srv.CertPool

	c := mustDial(t, cfg)

	require.NoError(t, noop(c))
}

func TestDialRejectsCertForOtherHostName(t *testing.T) {
	srv := imaptest.New(t)
	cfg := testConfig(srv, account.SecurityTLS)
	cfg.RootCAs = srv.CertPool
	// The certificate is valid for the address 127.0.0.1 only.
	cfg.Server.IMAPHost = "localhost"

	_, err := dialIMAP(t.Context(), cfg)

	var certErr *CertificateUnknownError
	require.ErrorAs(t, err, &certErr)
	require.Equal(t, "localhost", certErr.Host)
	require.Equal(t, srv.CertSHA256, certErr.FingerprintSHA256)
}

func TestDialSTARTTLSChecksCertificate(t *testing.T) {
	srv := imaptest.New(t, imaptest.WithMode(imaptest.ModeSTARTTLS))

	t.Run("pinned", func(t *testing.T) {
		require.NoError(t, noop(mustDial(t, pinnedConfig(srv, account.SecuritySTARTTLS))))
	})

	t.Run("unknown", func(t *testing.T) {
		_, err := dialIMAP(t.Context(), testConfig(srv, account.SecuritySTARTTLS))

		var certErr *CertificateUnknownError
		require.ErrorAs(t, err, &certErr)
		require.Equal(t, srv.CertSHA256, certErr.FingerprintSHA256)
	})
}

func TestDialSTARTTLSNeverFallsBackToPlainText(t *testing.T) {
	const greeting = "* OK [CAPABILITY IMAP4rev1] ready\r\n"
	for name, reply := range map[string]fakeReply{
		"server refuses STARTTLS": func(tag, cmd string) (string, fakeThen) {
			return tag + " BAD STARTTLS not supported\r\n", fakeStay
		},
		"server says OK but stays in plain text": func(tag, cmd string) (string, fakeThen) {
			return tag + " OK Begin TLS negotiation now\r\n* OK still plain text\r\n", fakeStay
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeIMAP(t, greeting, reply)
			cfg := fake.plainConfig()
			cfg.Server.IMAPSecurity = account.SecuritySTARTTLS

			_, err := dialIMAP(t.Context(), cfg)

			require.Error(t, err)
			// The login must never be sent: it would show the password.
			received := fake.Received()
			require.NotContains(t, strings.ToUpper(received), "LOGIN")
			require.NotContains(t, received, cfg.Password.Reveal())
		})
	}
}

func TestDialPlainWorksOnlyForLoopbackOnLocalInstall(t *testing.T) {
	srv := imaptest.New(t, imaptest.WithMode(imaptest.ModePlain))

	t.Run("local install", func(t *testing.T) {
		require.NoError(t, noop(mustDial(t, testConfig(srv, account.SecurityPlain))))
	})

	t.Run("hosted install", func(t *testing.T) {
		before := srv.TotalConns()
		cfg := testConfig(srv, account.SecurityPlain)
		cfg.Local = false

		_, err := dialIMAP(t.Context(), cfg)

		require.ErrorContains(t, err, "hosted")
		require.Equal(t, before, srv.TotalConns(), "no connection may be made")
	})
}

func TestDialTimesOutWithoutGreeting(t *testing.T) {
	fake := newFakeIMAP(t, "", func(string, string) (string, fakeThen) { return "", fakeStay })
	cfg := fake.plainConfig()
	cfg.DialTimeout = 150 * time.Millisecond

	start := time.Now()
	_, err := dialIMAP(t.Context(), cfg)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestLoginErrorNeverRepeatsThePassword(t *testing.T) {
	const password = "hunter2-hunter2"
	// This server repeats the password in its answer and then drops the
	// connection, so a LOGOUT after the failure would fail as well.
	fake := newFakeIMAP(t, "* OK ready\r\n", func(tag, cmd string) (string, fakeThen) {
		return tag + " NO login failed for " + password + "\r\n", fakeClose
	})
	cfg := fake.plainConfig()
	cfg.Password = secrets.Secret(password)

	_, err := dialIMAP(t.Context(), cfg)

	require.Error(t, err)
	require.ErrorContains(t, err, "login")
	require.NotContains(t, err.Error(), password)
}

func TestPlainRemoteAddressMustBeLoopback(t *testing.T) {
	for addr, want := range map[net.Addr]bool{
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 143}:     true,
		&net.TCPAddr{IP: net.IPv6loopback, Port: 143}:           true,
		&net.TCPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 143}:      false,
		&net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 143}: false,
		&net.UnixAddr{Name: "/tmp/imap.sock", Net: "unix"}:      false,
	} {
		require.Equal(t, want, isLoopbackAddr(addr), addr.String())
	}
}

func TestDialRequiresTLS12OrNewer(t *testing.T) {
	cfg := newTLSConfig(baseConfig(), "mail.example.com")

	require.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
	// The standard check is off, so our own check must be set.
	require.True(t, cfg.InsecureSkipVerify)
	require.NotNil(t, cfg.VerifyConnection)
	require.Equal(t, "mail.example.com", cfg.ServerName)
}

func TestDialCertificateErrorSaysWhyItFailed(t *testing.T) {
	srv := imaptest.New(t)

	t.Run("wrong pin", func(t *testing.T) {
		cfg := testConfig(srv, account.SecurityTLS)
		cfg.Server.PinnedIMAPCert = strings.Repeat("0", 64)

		_, err := dialIMAP(t.Context(), cfg)

		var certErr *CertificateUnknownError
		require.ErrorAs(t, err, &certErr)
		require.True(t, certErr.Pinned)
		require.NoError(t, certErr.Reason)
		require.NoError(t, certErr.Unwrap())
	})

	t.Run("self-signed certificate", func(t *testing.T) {
		_, err := dialIMAP(t.Context(), testConfig(srv, account.SecurityTLS))

		var certErr *CertificateUnknownError
		require.ErrorAs(t, err, &certErr)
		require.False(t, certErr.Pinned)
		// This is the only case where the UI may offer "trust this certificate".
		var unknownAuthority x509.UnknownAuthorityError
		require.ErrorAs(t, err, &unknownAuthority)
	})

	t.Run("trusted certificate for another host", func(t *testing.T) {
		cfg := testConfig(srv, account.SecurityTLS)
		cfg.RootCAs = srv.CertPool
		cfg.Server.IMAPHost = "localhost"

		_, err := dialIMAP(t.Context(), cfg)

		var certErr *CertificateUnknownError
		require.ErrorAs(t, err, &certErr)
		require.False(t, certErr.Pinned)
		var hostnameErr x509.HostnameError
		require.ErrorAs(t, err, &hostnameErr)
		var unknownAuthority x509.UnknownAuthorityError
		require.False(t, errors.As(err, &unknownAuthority), "a wrong host must not look like an unknown authority")
	})
}

func TestLoginErrorHasNoServerText(t *testing.T) {
	// Servers often repeat the user name or the mail address in the answer.
	fake := newFakeIMAP(t, "* OK ready\r\n", func(tag, cmd string) (string, fakeThen) {
		return tag + " NO [AUTHENTICATIONFAILED] no account for alice@example.com\r\n", fakeStay
	})
	cfg := fake.plainConfig()
	cfg.Server.Username = "alice@example.com"

	_, err := dialIMAP(t.Context(), cfg)

	require.EqualError(t, err, "imap: login failed [AUTHENTICATIONFAILED]")
}

func TestLoginErrorWithoutResponseCode(t *testing.T) {
	fake := newFakeIMAP(t, "* OK ready\r\n", func(tag, cmd string) (string, fakeThen) {
		return tag + " NO sorry bob@example.com\r\n", fakeStay
	})

	_, err := dialIMAP(t.Context(), fake.plainConfig())

	require.EqualError(t, err, "imap: login failed")
}
