package main

// This file holds the connect flow that "account add" and "import-env" share.
// Service.CreateIMAPAccount tests the settings and saves the account, but it
// does not decide about certificates or plain connections. Like the web
// interface, the command line does that itself, with the same rules:
//
//   - A plain (unencrypted) connection is allowed only to a server on this
//     computer, and only with --plaintext-i-understand or a typed "yes" on a
//     terminal. To another host it is refused, also with the flag.
//   - The user may trust (pin) a certificate only when
//     connect.CheckCertificate allows it: no pin was set, and the only problem
//     is that no known authority signed it. On a terminal the user sees the
//     host and port that they gave and the SHA-256 fingerprint, and answers
//     "y". A server that is not on this computer also needs a typed "yes".
//     Without a terminal nothing is trusted on its own: a --trust-fingerprint
//     must be the fingerprint that the server showed in this run.
//   - A pin is saved only for the server (protocol, host and port) that showed
//     the certificate.
//   - A failed test prints a fixed text (connect.ErrorText), never the text of
//     the server.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/internal/connect"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// maxConnectTests is the most connection tests that one connect makes: the first
// one, then one after each server whose certificate the user trusted.
const maxConnectTests = 3

// createFunc tests the settings and saves a new account.
// service.Service.CreateIMAPAccount is the real one. Tests use a fake.
type createFunc func(ctx context.Context, address string, server account.MailServer, password secrets.Secret) (*account.Account, error)

// connectOptions are the choices that the user made with flags.
type connectOptions struct {
	// trust holds the fingerprints of --trust-fingerprint, as lower-case hex.
	// They are used only when standard input is not a terminal.
	trust []string
	// plaintext is --plaintext-i-understand.
	plaintext bool
}

// fingerprintList is the value of the repeatable flag --trust-fingerprint. It
// takes a SHA-256 in hex, with or without colons, in any case.
type fingerprintList []string

func (l *fingerprintList) String() string { return strings.Join(*l, ",") }

func (l *fingerprintList) Set(v string) error {
	clean := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(v), ":", ""))
	if !account.IsFingerprint(clean) {
		return errors.New("a fingerprint is a SHA-256 in hex: 64 hex digits, with or without colons")
	}
	*l = append(*l, clean)
	return nil
}

// side is one of the two servers of an account.
type side struct {
	name     string // "IMAP" or "SMTP"
	host     string
	port     int
	security account.Security
	pin      *string
	cert     *imapsmtp.CertificateUnknownError
}

// sidesOf returns the IMAP and the SMTP server of the settings. The pins point
// into server. certErr may be nil.
func sidesOf(server *account.MailServer, certErr *service.CertificateError) []side {
	sides := []side{
		{name: "IMAP", host: server.IMAPHost, port: server.IMAPPort, security: server.IMAPSecurity, pin: &server.PinnedIMAPCert},
		{name: "SMTP", host: server.SMTPHost, port: server.SMTPPort, security: server.SMTPSecurity, pin: &server.PinnedSMTPCert},
	}
	if certErr != nil {
		sides[0].cert, sides[1].cert = certErr.IMAP, certErr.SMTP
	}
	return sides
}

// checkPlain applies the rules of a plain connection, before the password is
// read or sent. A plain connection to a host that is not on this computer is
// refused. One to this computer needs the flag, or a typed "yes" on a terminal.
// The hosts must be cleaned (see connect.CleanHost).
func (c *cli) checkPlain(cmd string, server account.MailServer, opts connectOptions) bool {
	var plain []side
	for _, s := range sidesOf(&server, nil) {
		if s.security != account.SecurityPlain {
			continue
		}
		if !account.IsLoopbackHost(s.host) {
			c.errorf("%s: a connection without encryption is allowed only to a server on this computer "+
				"(localhost or a loopback address). The %s server is not on this computer. Nothing was saved.\n", cmd, s.name)
			return false
		}
		plain = append(plain, s)
	}
	if len(plain) == 0 || opts.plaintext {
		return true
	}
	if !c.sys.isTerminal() {
		c.errorf("%s: the %s connection to %s:%d is not encrypted. To use it, run again with "+
			"--plaintext-i-understand. Nothing was saved.\n", cmd, plain[0].name, plain[0].host, plain[0].port)
		return false
	}
	for _, s := range plain {
		c.printf("The %s connection to %s:%d is not encrypted. The password and the mail cross it as plain text.\n",
			s.name, s.host, s.port)
	}
	c.printf("Use a connection without encryption only for a local bridge that you run on this computer.\n")
	if !c.confirmTyped("Connect without encryption?") {
		c.errorf("%s: stopped. Nothing was saved.\n", cmd)
		return false
	}
	return true
}

// saveAccount tests the settings and saves the account. When a server shows a
// certificate that is not accepted, it decides with the user whether to trust
// it, and tests again with the pin. It prints what went wrong and returns false
// when nothing was saved.
func (c *cli) saveAccount(ctx context.Context, cmd string, create createFunc, address string, server account.MailServer,
	password secrets.Secret, opts connectOptions) (*account.Account, bool) {
	for range maxConnectTests {
		a, err := create(ctx, address, server, password)
		if err == nil {
			return a, true
		}
		var certErr *service.CertificateError
		if !errors.As(err, &certErr) {
			c.failConnect(cmd, err)
			return nil, false
		}
		if !c.trustCertificates(cmd, &server, certErr, opts) {
			return nil, false
		}
	}
	c.errorf("%s: the servers kept showing certificates that are not accepted. Nothing was saved.\n", cmd)
	return nil, false
}

// failConnect prints the fixed text of an error of CreateIMAPAccount. An error
// that is not about the input or the test (such as a failure of the database)
// has no fixed text. Its own text is printed: it never holds the password, the
// address or a text of a server.
func (c *cli) failConnect(cmd string, err error) {
	if text, known := connect.ErrorText(err); known {
		c.errorf("%s: %s Nothing was saved.\n", cmd, text)
		return
	}
	c.errorf("%s: the account could not be saved: %v\n", cmd, err)
}

// trustCertificates shows each certificate that the test did not accept and
// decides with the user whether to trust it. When every one is trusted, it sets
// their pins in server and returns true.
func (c *cli) trustCertificates(cmd string, server *account.MailServer, certErr *service.CertificateError, opts connectOptions) bool {
	type shown struct {
		side
		check connect.Certificate
	}
	var certs []shown
	for _, s := range sidesOf(server, certErr) {
		if s.cert != nil {
			certs = append(certs, shown{side: s, check: connect.CheckCertificate(s.cert)})
		}
	}
	// While one certificate cannot be trusted, the connect cannot work, so
	// nothing is offered.
	for _, s := range certs {
		if !s.check.CanTrust {
			for _, d := range certs {
				c.describeCertificate(d.side, d.check)
			}
			c.errorf("%s: the certificate of the %s server cannot be trusted. Nothing was saved.\n", cmd, s.name)
			return false
		}
	}
	for _, s := range certs {
		c.describeCertificate(s.side, s.check)
		if !c.decideTrust(cmd, s.side, s.check.Fingerprint, opts) {
			return false
		}
	}
	for _, s := range certs {
		*s.pin = s.check.Fingerprint
	}
	return true
}

// describeCertificate shows a certificate that the test did not accept: the
// host and port that the user gave, the fingerprint, and the fixed text of the
// problem. It shows no other value of the server.
func (c *cli) describeCertificate(s side, check connect.Certificate) {
	c.printf("The %s server %s:%d showed a certificate that this computer does not trust.\n", s.name, s.host, s.port)
	if check.Fingerprint != "" {
		c.printf("  SHA-256 fingerprint: %s\n", connect.GroupFingerprint(check.Fingerprint))
	}
	c.printf("  %s\n", check.Problem)
}

// decideTrust asks the user whether to trust one certificate. Without a
// terminal it trusts it only when --trust-fingerprint names its fingerprint.
func (c *cli) decideTrust(cmd string, s side, fingerprint string, opts connectOptions) bool {
	if !c.sys.isTerminal() {
		if slices.Contains(opts.trust, fingerprint) {
			c.printf("Trusted the certificate of the %s server: it matches --trust-fingerprint.\n", s.name)
			return true
		}
		c.errorf("%s: the certificate of the %s server %s:%d is not trusted. Check the fingerprint, and to trust "+
			"it without a terminal, run again with --trust-fingerprint %s. Nothing was saved.\n",
			cmd, s.name, s.host, s.port, fingerprint)
		return false
	}
	if !c.confirm("Trust this certificate?") {
		c.errorf("%s: the certificate of the %s server was not trusted. Nothing was saved.\n", cmd, s.name)
		return false
	}
	if !account.IsLoopbackHost(s.host) {
		c.printf("%s is not on this computer. If this is not the certificate that you expect, someone on the network "+
			"can read your password and your mail.\n", s.host)
		if !c.confirmTyped(fmt.Sprintf("Trust the certificate of %s anyway?", s.host)) {
			c.errorf("%s: the certificate of the %s server was not trusted. Nothing was saved.\n", cmd, s.name)
			return false
		}
	}
	return true
}
