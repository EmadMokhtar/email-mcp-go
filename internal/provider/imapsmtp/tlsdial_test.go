package imapsmtp

import (
	"context"
	"crypto/tls"
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
		"server refuses STARTTLS": func(tag, cmd string) (string, bool) {
			return tag + " BAD STARTTLS not supported\r\n", false
		},
		"server says OK but stays in plain text": func(tag, cmd string) (string, bool) {
			return tag + " OK Begin TLS negotiation now\r\n* OK still plain text\r\n", false
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
	fake := newFakeIMAP(t, "", func(string, string) (string, bool) { return "", false })
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
	fake := newFakeIMAP(t, "* OK ready\r\n", func(tag, cmd string) (string, bool) {
		return tag + " NO login failed for " + password + "\r\n", true
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
