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
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// CertificateUnknownError means the server showed a certificate that is not
// accepted. The user interface can offer to pin the certificate with
// FingerprintSHA256, but only in one case.
//
// Callers must offer "trust this certificate" only when Pinned is false and
// the error chain holds an x509.UnknownAuthorityError (use errors.As). Never
// offer it for a wrong pin (the certificate changed, which can be an attack),
// for a host name that does not match (x509.HostnameError), or for a
// certificate that has expired or is not valid yet (x509.CertificateInvalidError
// with the reason x509.Expired).
//
// The name and the time are checked before the chain, on every platform. So
// an unknown authority means that the name matches and the time is valid.
type CertificateUnknownError struct {
	// Protocol says which server showed the certificate: "imap" or "smtp". An
	// empty value means "imap".
	Protocol string
	// Host is the server name the client connected to.
	Host string
	// FingerprintSHA256 is the lowercase hex SHA-256 of the DER bytes of the
	// leaf certificate that the server showed. When Pinned is true, this is
	// the certificate of the server now, not the pin.
	FingerprintSHA256 string
	// Pinned is true when a pin was set and the certificate did not match it.
	Pinned bool
	// Reason is the error from the certificate check when no pin was set. It
	// is nil when Pinned is true.
	Reason error
}

// Unwrap returns Reason, so errors.As can find the x509 error.
func (e *CertificateUnknownError) Unwrap() error { return e.Reason }

// Error describes the problem. It holds the host name, the fingerprint and
// the reason from the certificate check only.
func (e *CertificateUnknownError) Error() string {
	protocol := e.Protocol
	if protocol == "" {
		protocol = protocolIMAP
	}
	switch {
	case e.Pinned:
		return fmt.Sprintf("%s: the certificate of %s does not match the pin (sha256 %s)", protocol, e.Host, e.FingerprintSHA256)
	case e.Reason != nil:
		return fmt.Sprintf("%s: the certificate of %s is not trusted (sha256 %s): %v", protocol, e.Host, e.FingerprintSHA256, e.Reason)
	default:
		return fmt.Sprintf("%s: the certificate of %s is not trusted (sha256 %s)", protocol, e.Host, e.FingerprintSHA256)
	}
}

// The protocols that CertificateUnknownError names.
const (
	protocolIMAP = "imap"
	protocolSMTP = "smtp"
)

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
	client, _, err := dialWithConn(ctx, cfg)
	return client, err
}

// dialWithConn is dialIMAP, and it also returns the network connection under
// the client. A caller that must close the connection at once, whatever the
// client is doing, closes that connection and not the client: Client.Close
// waits for the reader of the client, and the reader can wait for the caller.
func dialWithConn(ctx context.Context, cfg Config) (*imapclient.Client, net.Conn, error) {
	cfg.withDefaults()
	if err := cfg.Server.Validate(cfg.Local); err != nil {
		return nil, nil, fmt.Errorf("imap: server settings: %w", err)
	}
	host := cfg.Server.IMAPHost
	addr := net.JoinHostPort(host, strconv.Itoa(cfg.Server.IMAPPort))

	ctx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()

	dialer := net.Dialer{Timeout: cfg.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("imap: connect: %w", err)
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
			return nil, nil, fmt.Errorf("imap: dial: %w", ctxErr)
		}
		return nil, nil, err
	}
	if !stop() {
		// ctx ended after the login and closed the connection.
		_ = conn.Close()
		return nil, nil, fmt.Errorf("imap: dial: %w", ctx.Err())
	}
	return client, conn, nil
}

// secureAndLogin protects an open connection as the config says and logs in.
// On an error it does not close conn. The caller does.
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
		return nil, fmt.Errorf("imap: greeting: %w", err)
	}
	if err := client.Login(cfg.Server.Username, cfg.Password.Reveal()).Wait(); err != nil {
		// The caller closes the connection at once. A LOGOUT is not needed
		// after a failed login, and no error from closing can hide this one.
		return nil, loginError(err)
	}
	return client, nil
}

// loginError turns the error of a failed LOGIN into the error that callers
// see. The text is "imap: login failed", with the response code of the server
// in brackets when it sent one (for example "[AUTHENTICATIONFAILED]").
//
// It never repeats the free text of the server. Servers often put the user
// name or the mail address in that text, and logs must not hold addresses.
// A network failure keeps its error, because that text has no user data.
func loginError(err error) error {
	var serverErr *imap.Error
	switch {
	case errors.As(err, &serverErr):
		if serverErr.Code != "" {
			return fmt.Errorf("imap: login failed [%s]", serverErr.Code)
		}
		return errors.New("imap: login failed")
	case isNetworkError(err):
		return fmt.Errorf("imap: login failed: %w", err)
	default:
		return errors.New("imap: login failed")
	}
}

// newTLSConfig returns the TLS settings for a connection to the IMAP server
// host. The certificate is checked against PinnedIMAPCert.
//
// The standard certificate check is turned off, and verifyCertificate does
// the check instead. TLS calls it for every connection, also for a resumed
// session (VerifyPeerCertificate would be skipped then). It always gets the
// leaf certificate, which is needed for the pin and for
// CertificateUnknownError.
func newTLSConfig(cfg Config, host string) *tls.Config {
	return newPinnedTLSConfig(cfg, host, protocolIMAP, cfg.Server.PinnedIMAPCert)
}

// newSMTPTLSConfig is newTLSConfig for the SMTP server host. The certificate is
// checked against PinnedSMTPCert, with the same code.
func newSMTPTLSConfig(cfg Config, host string) *tls.Config {
	return newPinnedTLSConfig(cfg, host, protocolSMTP, cfg.Server.PinnedSMTPCert)
}

func newPinnedTLSConfig(cfg Config, host, protocol, pin string) *tls.Config {
	return &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // G402: VerifyConnection checks the certificate
		VerifyConnection:   verifyCertificate(cfg, host, protocol, pin),
	}
}

// verifyCertificate returns the function that decides whether to trust the
// certificates of the server. protocol is "imap" or "smtp", and it goes into
// the error.
//
// With a pin, the leaf must have exactly that fingerprint, and nothing else
// is checked. Without a pin, the leaf must be valid for host and chain to the
// root certificates.
func verifyCertificate(cfg Config, host, protocol, pin string) func(tls.ConnectionState) error {
	pin = strings.ToLower(strings.TrimSpace(pin))
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return &CertificateUnknownError{Protocol: protocol, Host: host, Reason: errors.New("the server showed no certificate")}
		}
		leaf := state.PeerCertificates[0]
		sum := sha256.Sum256(leaf.Raw)
		fingerprint := hex.EncodeToString(sum[:])

		if pin != "" {
			if subtle.ConstantTimeCompare([]byte(fingerprint), []byte(pin)) != 1 {
				return &CertificateUnknownError{Protocol: protocol, Host: host, FingerprintSHA256: fingerprint, Pinned: true}
			}
			return nil
		}

		// Check the name and the time before the chain. The system verifier of
		// macOS and Windows reports "unknown authority" for every self-signed
		// certificate, also when the name is wrong or the certificate has
		// expired. These checks give the same reason on every platform.
		if err := leaf.VerifyHostname(host); err != nil {
			return &CertificateUnknownError{Protocol: protocol, Host: host, FingerprintSHA256: fingerprint, Reason: err}
		}
		if err := checkValidTime(leaf, time.Now()); err != nil {
			return &CertificateUnknownError{Protocol: protocol, Host: host, FingerprintSHA256: fingerprint, Reason: err}
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
			return &CertificateUnknownError{Protocol: protocol, Host: host, FingerprintSHA256: fingerprint, Reason: err}
		}
		return nil
	}
}

// checkValidTime returns an x509.CertificateInvalidError with the reason
// x509.Expired when now is outside the validity time of cert. That reason
// covers a certificate that has expired and one that is not valid yet.
func checkValidTime(cert *x509.Certificate, now time.Time) error {
	switch {
	case now.Before(cert.NotBefore):
		return x509.CertificateInvalidError{
			Cert:   cert,
			Reason: x509.Expired,
			Detail: fmt.Sprintf("current time %s is before %s", now.Format(time.RFC3339), cert.NotBefore.Format(time.RFC3339)),
		}
	case now.After(cert.NotAfter):
		return x509.CertificateInvalidError{
			Cert:   cert,
			Reason: x509.Expired,
			Detail: fmt.Sprintf("current time %s is after %s", now.Format(time.RFC3339), cert.NotAfter.Format(time.RFC3339)),
		}
	}
	return nil
}

// isLoopbackAddr reports whether a is a TCP address on the loopback network.
func isLoopbackAddr(a net.Addr) bool {
	tcp, ok := a.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}
