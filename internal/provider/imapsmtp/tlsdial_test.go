package imapsmtp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strconv"
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
	require.ErrorIs(t, err, ErrAuthFailed)
}

func TestLoginErrorWithoutResponseCode(t *testing.T) {
	fake := newFakeIMAP(t, "* OK ready\r\n", func(tag, cmd string) (string, fakeThen) {
		return tag + " NO sorry bob@example.com\r\n", fakeStay
	})

	_, err := dialIMAP(t.Context(), fake.plainConfig())

	require.EqualError(t, err, "imap: login failed")
	require.ErrorIs(t, err, ErrAuthFailed)
}

func TestLoginErrorMarksOnlyARefusedLogin(t *testing.T) {
	// A NO answer to LOGIN is a refused login, unless the server says that it is
	// not working (UNAVAILABLE, SERVERBUG). A BAD answer is a broken command, not
	// a refusal.
	for answer, refused := range map[string]bool{
		"NO [AUTHENTICATIONFAILED] wrong":   true,
		"NO [AUTHORIZATIONFAILED] no":       true,
		"NO [CONTACTADMIN] blocked":         true,
		"NO sorry":                          true,
		"NO [UNAVAILABLE] try again later":  false,
		"NO [SERVERBUG] something broke":    false,
		"NO [INUSE] another session":        false,
		"NO [LIMIT] too many connections":   false,
		"BAD [AUTHENTICATIONFAILED] syntax": false,
		"BAD unknown command":               false,
	} {
		fake := newFakeIMAP(t, "* OK ready\r\n", func(tag, cmd string) (string, fakeThen) {
			return tag + " " + answer + "\r\n", fakeStay
		})

		_, err := dialIMAP(t.Context(), fake.plainConfig())

		require.ErrorContains(t, err, "imap: login failed", answer)
		require.Equal(t, refused, errors.Is(err, ErrAuthFailed), answer)
	}
}

// INUSE and LIMIT say that the server is busy or has too many connections for
// the user (RFC 5530). The password was not refused, so the account must not be
// marked for a reconnect. The error says that the server is busy.
func TestLoginErrorTellsABusyServer(t *testing.T) {
	for answer, busy := range map[string]bool{
		"NO [INUSE] server-text-41 for bob@example.com": true,
		"NO [LIMIT] server-text-41 for bob@example.com": true,
		"NO [AUTHENTICATIONFAILED] wrong":               false,
		"NO [UNAVAILABLE] try again":                    false,
	} {
		fake := newFakeIMAP(t, "* OK ready\r\n", func(tag, cmd string) (string, fakeThen) {
			return tag + " " + answer + "\r\n", fakeStay
		})

		_, err := dialIMAP(t.Context(), fake.plainConfig())

		require.Equal(t, busy, errors.Is(err, ErrServerBusy), answer)
		require.False(t, busy && errors.Is(err, ErrAuthFailed), "a busy server did not refuse the password")
		require.NotContains(t, err.Error(), "server-text-41", "no free text of the server")
		require.NotContains(t, err.Error(), "bob@example.com", "no free text of the server")
	}
}

// startHandshakeServer starts a TLS server that shows a self-signed
// certificate valid between notBefore and notAfter for 127.0.0.1, and only
// does the TLS handshake. A dial fails at the handshake, so no IMAP is
// needed. It returns the server address.
func startHandshakeServer(t *testing.T, notBefore, notAfter time.Time) (host string, port int) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				// The client stops the handshake, so an error is expected.
				_ = conn.(*tls.Conn).Handshake()
				_ = conn.Close()
			}()
		}
	}()

	host, portText, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	port, err = strconv.Atoi(portText)
	require.NoError(t, err)
	return host, port
}

// With no RootCAs, the system verifier runs. On macOS and Windows it reports
// "unknown authority" for any self-signed certificate, also when the name is
// wrong or the certificate is expired. The reason must not depend on the
// platform: only a certificate with the right name and a valid time may look
// like an unknown authority, because only then may the UI offer to trust it.
func TestDialCertificateReasonDoesNotDependOnPlatform(t *testing.T) {
	now := time.Now()

	t.Run("right name, valid time", func(t *testing.T) {
		host, port := startHandshakeServer(t, now.Add(-time.Hour), now.Add(time.Hour))
		cfg := baseConfig()
		cfg.Server.IMAPHost, cfg.Server.IMAPPort, cfg.Server.IMAPSecurity = host, port, account.SecurityTLS

		_, err := dialIMAP(t.Context(), cfg)

		var certErr *CertificateUnknownError
		require.ErrorAs(t, err, &certErr)
		require.False(t, certErr.Pinned)
		var unknownAuthority x509.UnknownAuthorityError
		require.ErrorAs(t, err, &unknownAuthority, "the one case where the UI may offer trust")
	})

	t.Run("wrong name", func(t *testing.T) {
		srv := imaptest.New(t)
		cfg := testConfig(srv, account.SecurityTLS)
		cfg.Server.IMAPHost = "localhost" // the certificate is for 127.0.0.1 only

		_, err := dialIMAP(t.Context(), cfg)

		var hostnameErr x509.HostnameError
		require.ErrorAs(t, err, &hostnameErr)
		var unknownAuthority x509.UnknownAuthorityError
		require.False(t, errors.As(err, &unknownAuthority), "a wrong name must not look like an unknown authority")
	})

	for name, window := range map[string][2]time.Time{
		"expired":       {now.Add(-48 * time.Hour), now.Add(-time.Hour)},
		"not valid yet": {now.Add(time.Hour), now.Add(48 * time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			host, port := startHandshakeServer(t, window[0], window[1])
			cfg := baseConfig()
			cfg.Server.IMAPHost, cfg.Server.IMAPPort, cfg.Server.IMAPSecurity = host, port, account.SecurityTLS

			_, err := dialIMAP(t.Context(), cfg)

			var certErr *CertificateUnknownError
			require.ErrorAs(t, err, &certErr)
			require.False(t, certErr.Pinned)
			var invalid x509.CertificateInvalidError
			require.ErrorAs(t, err, &invalid)
			require.Equal(t, x509.Expired, invalid.Reason)
			var unknownAuthority x509.UnknownAuthorityError
			require.False(t, errors.As(err, &unknownAuthority), "an invalid time must not look like an unknown authority")
		})
	}
}

func TestCertificateUnknownErrorNamesTheProtocol(t *testing.T) {
	// The IMAP errors keep their text. An error that has no protocol is an
	// IMAP error.
	for protocol, want := range map[string]string{"": "imap: ", "imap": "imap: ", "smtp": "smtp: "} {
		for _, certErr := range []*CertificateUnknownError{
			{Protocol: protocol, Host: "mail.example.com", FingerprintSHA256: "ab"},
			{Protocol: protocol, Host: "mail.example.com", FingerprintSHA256: "ab", Pinned: true},
			{Protocol: protocol, Host: "mail.example.com", FingerprintSHA256: "ab", Reason: errors.New("why")},
		} {
			require.True(t, strings.HasPrefix(certErr.Error(), want), "%q: %s", protocol, certErr.Error())
			require.Contains(t, certErr.Error(), "mail.example.com")
		}
	}
}
