package imapsmtp

import (
	"context"
	"errors"
	"slices"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-smtp"
)

// ProbeResult is what Probe found out about an account.
type ProbeResult struct {
	// IMAPOK is true when the IMAP login worked.
	IMAPOK bool
	// SMTPOK is true when the SMTP login (AUTH) worked.
	SMTPOK bool
	// IMAPCert is set when the IMAP server showed a certificate that is not
	// accepted. The login was not tried then. See CertificateUnknownError for
	// the cases in which the UI may offer to pin the certificate.
	IMAPCert *CertificateUnknownError
	// SMTPCert is the same for the SMTP server.
	SMTPCert *CertificateUnknownError
	// Capabilities are the capabilities that the IMAP server announced after
	// the login, sorted. It is empty when the IMAP login did not work.
	Capabilities []string
}

// Probe tests the settings of an account. It logs in to the IMAP server and
// authenticates to the SMTP server (AUTH PLAIN). It sends no message and opens
// no mailbox.
//
// The connections are protected as the config says, with the same rules and the
// same certificate check as the rest of the backend. A certificate that is not
// accepted does not fail the probe: it is reported in IMAPCert or SMTPCert, and
// the other server is still tested. So the UI can offer to pin both
// certificates after one probe.
//
// Any other failure is an error. This includes a wrong password, a server that
// cannot be reached, and settings that are not valid. The login errors do not
// hold text of the server (see loginError and smtpLoginError). The IMAP server
// is tested first, and an error there ends the probe. Probe never logs the
// password and never returns it.
func Probe(ctx context.Context, cfg Config) (*ProbeResult, error) {
	res := &ProbeResult{}

	client, raw, err := dialWithConn(ctx, cfg)
	var certErr *CertificateUnknownError
	switch {
	case err == nil:
		res.IMAPOK = true
		res.Capabilities = capabilityNames(client.Caps())
		// Nothing is open on this connection, so closing it at once is safe.
		_ = raw.Close()
	case errors.As(err, &certErr):
		res.IMAPCert = certErr
	default:
		return nil, err
	}

	err = withSMTP(ctx, cfg, 0, func(*smtp.Client) error { return nil })
	switch {
	case err == nil:
		res.SMTPOK = true
	case errors.As(err, &certErr):
		res.SMTPCert = certErr
	default:
		return nil, err
	}
	return res, nil
}

// capabilityNames returns the names of the capabilities, sorted.
func capabilityNames(caps imap.CapSet) []string {
	names := make([]string, 0, len(caps))
	for c := range caps {
		names = append(names, string(c))
	}
	slices.Sort(names)
	return names
}
