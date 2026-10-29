// Package connect holds the parts of connecting an IMAP and SMTP account that
// the web interface and the command line tool share, so the two always agree:
//
//   - the fixed texts for the errors of service.Service.CreateIMAPAccount and
//     for a failed connection test (ErrorText, TestFailureText). A text is
//     chosen by the type of the error, never from the text of the error, so the
//     text of a mail server never reaches the user;
//   - the decision whether the user may be offered to trust (pin) a
//     certificate that a server showed (CheckCertificate);
//   - whether a server on this computer is set to a plain (unencrypted)
//     connection (PlainOnThisComputer);
//   - the form of a fingerprint for people (GroupFingerprint).
//
// The only value from a server that a caller may show is the fingerprint of a
// certificate.
package connect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// The fixed texts about the input and the connection test.
const (
	TextAddress       = "This email address is not valid."
	TextNoPassword    = "Type the password."
	TextExists        = "An account with this address is already connected."
	TextSettings      = "The server settings are not valid. Check the host names (without a port or a scheme), the ports (1 to 65535) and the security settings. A connection without encryption is allowed only to a server on this computer."
	TextTestFailed    = "The connection test failed. Check the user name, the password and the server settings. Some providers need an app password."
	TextAuthFailed    = "The server refused the user name or the password. Some providers need an app password, or IMAP access turned on in their settings."
	TextCannotConnect = "Could not connect to the server. Check the host names and the ports."
	TextHostNotFound  = "A server name was not found. Check the host names."
	TextTimeout       = "A server did not answer in time. Check the host names and the ports, then try again."
	TextTLSFailed     = "A secure connection could not be made. Check the ports and the security settings."
)

// The fixed texts about a certificate that the test did not accept.
const (
	// TextCertificate says that the test stopped at a certificate.
	TextCertificate = "A server showed a certificate that this computer does not trust. Nothing was saved."
	// TextCertUnknown is the problem of the one certificate that may be
	// trusted: no known authority signed it.
	TextCertUnknown    = "No authority that this computer trusts signed this certificate. A local bridge (such as Proton Mail Bridge) uses such a certificate. Trust it only if you expect it."
	TextCertWrongHost  = "The certificate is not for this host name. Check the host name. This certificate cannot be trusted here."
	TextCertExpired    = "The certificate has expired or is not valid yet. This certificate cannot be trusted here."
	TextCertPinChanged = "The certificate is not the one you chose to trust. Someone on the network may have changed it. This certificate cannot be trusted here."
	TextCertInvalid    = "The certificate is not valid. This certificate cannot be trusted here."
)

// Certificate is the decision about a certificate that a server showed and the
// connection test did not accept.
type Certificate struct {
	// Fingerprint is the SHA-256 of the certificate in lower-case hex, 64
	// digits. It is empty when the server showed no certificate, or when the
	// value is not a SHA-256.
	Fingerprint string
	// Problem is a fixed text that says what is wrong with the certificate.
	Problem string
	// CanTrust is true when the user may be offered to trust (pin) the
	// certificate.
	CanTrust bool
}

// CheckCertificate decides about a certificate that the test did not accept. It
// allows the offer to trust it in one case only: no pin was set, the only
// problem is that no known authority signed it, and the fingerprint is a
// SHA-256. The name and the time of the certificate are checked before the
// authority, so then they are right. A host name that does not match, a
// certificate out of its time, and a pin that did not match are never offered.
//
// The caller shows the fingerprint and the host and port that the user gave,
// never another value of the server.
func CheckCertificate(c *imapsmtp.CertificateUnknownError) Certificate {
	var out Certificate
	if account.IsFingerprint(c.FingerprintSHA256) {
		out.Fingerprint = c.FingerprintSHA256
	}
	var (
		unknownAuthority x509.UnknownAuthorityError
		hostname         x509.HostnameError
		invalid          x509.CertificateInvalidError
	)
	switch {
	case c.Pinned:
		out.Problem = TextCertPinChanged
	case errors.As(c.Reason, &hostname):
		out.Problem = TextCertWrongHost
	case errors.As(c.Reason, &invalid) && invalid.Reason == x509.Expired:
		out.Problem = TextCertExpired
	case errors.As(c.Reason, &unknownAuthority) && out.Fingerprint != "":
		out.Problem = TextCertUnknown
		out.CanTrust = true
	default:
		out.Problem = TextCertInvalid
	}
	return out
}

// ErrorText returns the fixed text for an error of
// service.Service.CreateIMAPAccount, other than a *service.CertificateError.
// known is false for an error that is not about the input or the test, such as
// a failure of the database.
func ErrorText(err error) (text string, known bool) {
	var testErr *service.ConnectionTestError
	switch {
	case errors.Is(err, service.ErrInvalidAddress):
		return TextAddress, true
	case errors.Is(err, service.ErrNoPassword):
		return TextNoPassword, true
	case errors.Is(err, service.ErrInvalidSettings):
		return TextSettings, true
	case errors.Is(err, service.ErrAccountExists):
		return TextExists, true
	case errors.As(err, &testErr):
		return TestFailureText(testErr.Err), true
	}
	return "", false
}

// TestFailureText returns the fixed text for a failed connection test, by the
// type of the error. It never uses the text of the error.
func TestFailureText(err error) string {
	var (
		dnsErr    *net.DNSError
		netErr    net.Error
		recordErr tls.RecordHeaderError
		alertErr  tls.AlertError
		opErr     *net.OpError
	)
	switch {
	case errors.Is(err, imapsmtp.ErrAuthFailed):
		return TextAuthFailed
	case errors.As(err, &dnsErr):
		return TextHostNotFound
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return TextTimeout
	case errors.As(err, &recordErr), errors.As(err, &alertErr):
		return TextTLSFailed
	case errors.As(err, &opErr):
		return TextCannotConnect
	default:
		return TextTestFailed
	}
}

// PlainOnThisComputer reports whether a server on this computer is set to a
// plain (unencrypted) connection. Such a connection needs the explicit consent
// of the user. A plain connection to another host is refused by the service
// (account.MailServer.Validate), so no consent can allow it. The hosts must be
// cleaned first (no outer spaces, lower case).
func PlainOnThisComputer(m account.MailServer) bool {
	return (m.IMAPSecurity == account.SecurityPlain && account.IsLoopbackHost(m.IMAPHost)) ||
		(m.SMTPSecurity == account.SecurityPlain && account.IsLoopbackHost(m.SMTPHost))
}

// GroupFingerprint writes a fingerprint in upper-case pairs with a colon
// between them, such as "AB:CD:EF", as browsers show one.
func GroupFingerprint(hexText string) string {
	upper := strings.ToUpper(hexText)
	var b strings.Builder
	for i := 0; i+1 < len(upper); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(upper[i : i+2])
	}
	return b.String()
}
