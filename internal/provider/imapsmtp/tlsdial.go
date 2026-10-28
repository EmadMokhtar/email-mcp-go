package imapsmtp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// CertificateUnknownError means the server showed a certificate that is not
// trusted. The user interface can offer to pin the certificate with
// FingerprintSHA256.
//
// A wrong pin gives the same error. Its fingerprint is the certificate the
// server showed now, not the pin.
type CertificateUnknownError struct {
	// Host is the server name the client connected to.
	Host string
	// FingerprintSHA256 is the lowercase hex SHA-256 of the DER bytes of the
	// leaf certificate that the server showed.
	FingerprintSHA256 string
}

// Error describes the problem. It holds the host name and the fingerprint
// only.
func (e *CertificateUnknownError) Error() string {
	return fmt.Sprintf("imap: the certificate of %s is not trusted (sha256 %s)", e.Host, e.FingerprintSHA256)
}

// dialIMAP connects to the IMAP server, secures the connection as
// cfg.Server.IMAPSecurity says, and logs in.
//
// The rules:
//   - cfg.Server must pass MailServer.Validate before anything else happens.
//     So plain text is possible only on a local install and only to a loopback
//     host. For plain text, the address that the connection really reached
//     must be a loopback address too.
//   - TLS needs version 1.2 or newer.
//   - With STARTTLS the connection must be upgraded before LOGIN. If the
//     server does not support STARTTLS, or the upgrade fails, the dial fails.
//     There is no fall back to plain text.
//   - With cfg.Server.PinnedIMAPCert set, the certificate is accepted only if
//     its fingerprint matches. Without a pin, the certificate must be valid
//     for the host and chain to cfg.RootCAs (or the system roots).
//   - A certificate that is not accepted gives *CertificateUnknownError,
//     which errors.As can find in the returned error.
//
// cfg.DialTimeout and ctx limit the whole call: connect, TLS, greeting and
// login. The caller must close the returned client.
func dialIMAP(ctx context.Context, cfg Config) (*imapclient.Client, error) {
	cfg.withDefaults()
	if err := cfg.Server.Validate(cfg.Local); err != nil {
		return nil, fmt.Errorf("imap: server settings: %w", err)
	}
	host := cfg.Server.IMAPHost
	addr := net.JoinHostPort(host, strconv.Itoa(cfg.Server.IMAPPort))

	ctx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()

	dialer := net.Dialer{Timeout: cfg.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("imap: connect: %w", err)
	}
	// From here, the end of ctx closes the connection. That also stops any
	// step that waits for the server. stop runs before cancel (defers run in
	// reverse order), so a successful dial is not closed by its own cancel.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	client, err := secureAndLogin(ctx, cfg, conn)
	if err != nil {
		_ = conn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			// The step failed because the connection was closed. The timeout
			// or the cancel is the real cause.
			return nil, fmt.Errorf("imap: dial: %w", ctxErr)
		}
		return nil, err
	}
	if !stop() {
		// ctx ended after the login and closed the connection.
		_ = client.Close()
		return nil, fmt.Errorf("imap: dial: %w", ctx.Err())
	}
	return client, nil
}

// secureAndLogin protects an open connection as the config says and logs in.
// On an error it leaves closing conn to the caller.
func secureAndLogin(ctx context.Context, cfg Config, conn net.Conn) (*imapclient.Client, error) {
	host := cfg.Server.IMAPHost
	var client *imapclient.Client

	switch cfg.Server.IMAPSecurity {
	case account.SecurityTLS:
		tlsConfig := newTLSConfig(cfg, host)
		// The same protocol name that imapclient.DialTLS announces.
		tlsConfig.NextProtos = []string{"imap"}
		tlsConn := tls.Client(conn, tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("imap: tls handshake: %w", err)
		}
		client = imapclient.New(tlsConn, nil)

	case account.SecuritySTARTTLS:
		// NewStartTLS sends STARTTLS first, before any other command. It
		// fails if the server refuses or if the TLS handshake fails, and it
		// closes the connection then.
		var err error
		client, err = imapclient.NewStartTLS(conn, &imapclient.Options{TLSConfig: newTLSConfig(cfg, host)})
		if err != nil {
			return nil, fmt.Errorf("imap: starttls: %w", err)
		}

	case account.SecurityPlain:
		// Validate looked at the text of the host name. Check the address that
		// the connection really reached, because a name such as "localhost"
		// can be changed to point at another machine.
		if !isLoopbackAddr(conn.RemoteAddr()) {
			return nil, errors.New("imap: a plain connection is allowed only to a loopback address")
		}
		client = imapclient.New(conn, nil)

	default:
		// Validate accepts only the three modes above.
		return nil, errors.New("imap: unknown security mode")
	}

	if err := client.WaitGreeting(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("imap: greeting: %w", err)
	}
	if err := client.Login(cfg.Server.Username, cfg.Password.Reveal()).Wait(); err != nil {
		// Close the connection at once. A LOGOUT is not needed after a failed
		// login, and an error from closing must never hide the login error.
		_ = client.Close()
		return nil, loginError(err, cfg.Password.Reveal())
	}
	return client, nil
}

// loginError wraps the error of a failed LOGIN. The text says "login". It
// never contains the password, even when the server repeats it.
func loginError(err error, password string) error {
	if password != "" && strings.Contains(err.Error(), password) {
		return errors.New("imap: login failed")
	}
	return fmt.Errorf("imap: login failed: %w", err)
}

// newTLSConfig returns the TLS settings for a connection to host.
//
// The standard certificate check is turned off, and verifyCertificate does
// the check instead. TLS calls it for every connection, also for a resumed
// session (VerifyPeerCertificate would be skipped then). It always gets the
// leaf certificate, which is needed for the pin and for
// CertificateUnknownError.
func newTLSConfig(cfg Config, host string) *tls.Config {
	return &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // G402: VerifyConnection checks the certificate
		VerifyConnection:   verifyCertificate(cfg, host),
	}
}

// verifyCertificate returns the function that decides whether to trust the
// certificates of the server.
//
// With a pin, the leaf must have exactly that fingerprint, and nothing else
// is checked. Without a pin, the leaf must be valid for host and chain to the
// root certificates.
func verifyCertificate(cfg Config, host string) func(tls.ConnectionState) error {
	pin := strings.ToLower(strings.TrimSpace(cfg.Server.PinnedIMAPCert))
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return &CertificateUnknownError{Host: host}
		}
		leaf := state.PeerCertificates[0]
		sum := sha256.Sum256(leaf.Raw)
		unknown := &CertificateUnknownError{Host: host, FingerprintSHA256: hex.EncodeToString(sum[:])}

		if pin != "" {
			if subtle.ConstantTimeCompare([]byte(unknown.FingerprintSHA256), []byte(pin)) != 1 {
				return unknown
			}
			return nil
		}

		intermediates := x509.NewCertPool()
		for _, cert := range state.PeerCertificates[1:] {
			intermediates.AddCert(cert)
		}
		_, err := leaf.Verify(x509.VerifyOptions{
			DNSName:       host,
			Roots:         cfg.RootCAs, // nil means the system roots
			Intermediates: intermediates,
		})
		if err != nil {
			return unknown
		}
		return nil
	}
}

// isLoopbackAddr reports whether a is a TCP address on the loopback network.
func isLoopbackAddr(a net.Addr) bool {
	tcp, ok := a.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}
