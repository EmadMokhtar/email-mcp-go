package main

// This file holds "account reconnect". It gives a connected account a new
// password, or trusts a new certificate of one of its servers, and keeps the
// account: its id, its server settings, its levels, its queued messages and its
// activity (Service.ReconnectIMAPAccount). A change of the server settings is
// not a reconnect: remove the account and add it again for that.
//
// The rules about certificates:
//   - A server that had no pin follows the rules of "account add" (see
//     connect.go): a "y" on a terminal, plus a typed "yes" for a server that is
//     not on this computer; without a terminal, --trust-fingerprint, plus
//     --accept-remote-certificate for a server that is not on this computer.
//   - A server whose pinned certificate changed may get a new pin, or lose its
//     pin, only when connect.CheckReconnect allows it. The user sees the host
//     and port, the old and the new fingerprint, and the warning
//     connect.TextCertChangedWarning. On a terminal the user types "yes", for
//     any host. Without a terminal, --trust-fingerprint must be the new
//     fingerprint that the server showed in this run, and
//     --replace-pinned-certificate is the second explicit confirmation.
//   - A pin changes only for the server (protocol, host and port) that showed
//     the certificate.
//   - A failed test prints a fixed text (connect.ErrorText), never the text of
//     the server. The password is never printed.

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/EmadMokhtar/email-mcp-go/internal/connect"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// reconnectOptions are the choices that the user made with flags.
type reconnectOptions struct {
	connectOptions
	// replacePinned is --replace-pinned-certificate: without a terminal, the
	// second confirmation that a changed pinned certificate may replace (or
	// remove) the pin.
	replacePinned bool
}

func runAccountReconnect(ctx context.Context, c *cli, args []string) int {
	const cmd = "account reconnect"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	var trust fingerprintList
	fs.Var(&trust, "trust-fingerprint", "when standard input is not a terminal: accept the certificate with this SHA-256 "+
		"fingerprint, if a server shows it in this run (repeat the flag for the second server)")
	acceptRemote := fs.Bool("accept-remote-certificate", false, "when standard input is not a terminal: also needed "+
		"to trust, with --trust-fingerprint, the certificate of a server that is not on this computer and had no "+
		"trusted certificate")
	replacePinned := fs.Bool("replace-pinned-certificate", false, "when standard input is not a terminal: also needed "+
		"to replace, with --trust-fingerprint, the trusted certificate of a server whose certificate changed")
	rest, code, ok := c.parseFlags(cmd, fs, args)
	if !ok {
		return code
	}
	id, code, ok := c.oneID(cmd, rest)
	if !ok {
		return code
	}
	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	view, ok := c.findAccount(ctx, cmd, a.Service, id)
	if !ok {
		return exitError
	}

	password, err := c.readPassword(ctx, "New password (not shown; press Enter to keep the stored password): ")
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		return exitError
	}
	opts := reconnectOptions{
		connectOptions: connectOptions{trust: trust, acceptRemote: *acceptRemote},
		replacePinned:  *replacePinned,
	}
	r := service.Reconnect{Password: password}
	for range maxConnectTests {
		res, err := a.Service.ReconnectIMAPAccount(userContext(ctx), view.ID, r)
		if err == nil {
			c.printReconnected(view, res)
			return exitOK
		}
		var certErr *service.ReconnectCertificateError
		if !errors.As(err, &certErr) {
			c.failReconnect(cmd, err)
			return exitError
		}
		if !c.decideReconnect(cmd, &r, certErr, opts) {
			return exitError
		}
	}
	c.errorf("%s: the servers kept showing certificates that are not accepted. Nothing was saved.\n", cmd)
	return exitError
}

// printReconnected tells the user what the reconnect changed.
func (c *cli) printReconnected(view service.AccountView, res *service.ReconnectResult) {
	c.printf("Reconnected %s (%s). The IMAP and the SMTP login work.\n", view.Address, view.ID)
	if res.PasswordChanged {
		c.printf("The new password is saved.\n")
	} else {
		c.printf("The stored password is kept.\n")
	}
	if res.PinChanged {
		c.printf("The trusted certificate is updated.\n")
	}
}

// failReconnect prints the fixed text of an error of ReconnectIMAPAccount. An
// error that is not about the input or the test (such as a failure of the
// database) has no fixed text. Its own text is printed: it never holds the
// password, the address or a text of a server.
func (c *cli) failReconnect(cmd string, err error) {
	if errors.Is(err, service.ErrAccountNotFound) {
		c.errorf("%s: No account has this id. Nothing was saved.\n", cmd)
		return
	}
	if text, known := connect.ErrorText(err); known {
		c.errorf("%s: %s Nothing was saved.\n", cmd, text)
		return
	}
	c.errorf("%s: the account could not be reconnected: %v\n", cmd, err)
}

// reconnectCert is one certificate of a *service.ReconnectCertificateError, with
// the decision field of the request that belongs to its server.
type reconnectCert struct {
	side
	check    connect.ReconnectCheck
	decision *string
}

// decideReconnect shows each certificate that the test did not accept and
// decides with the user whether to accept it. When every one is accepted, it
// sets the decisions in r and returns true.
func (c *cli) decideReconnect(cmd string, r *service.Reconnect, certErr *service.ReconnectCertificateError, opts reconnectOptions) bool {
	var certs []reconnectCert
	for _, s := range []struct {
		name     string
		rc       *service.ReconnectCertificate
		decision *string
	}{
		{"IMAP", certErr.IMAP, &r.PinnedIMAPCert},
		{"SMTP", certErr.SMTP, &r.PinnedSMTPCert},
	} {
		if s.rc != nil {
			certs = append(certs, reconnectCert{
				side:  side{name: s.name, host: s.rc.Host, port: s.rc.Port},
				check: connect.CheckReconnect(s.rc), decision: s.decision,
			})
		}
	}
	// While one certificate cannot be accepted, the reconnect cannot work, so
	// nothing is offered.
	for _, s := range certs {
		if !s.check.CanChange() {
			for _, d := range certs {
				c.describeReconnect(d)
			}
			c.errorf("%s: the certificate of the %s server cannot be trusted. Nothing was saved.\n", cmd, s.name)
			return false
		}
	}
	for _, s := range certs {
		c.describeReconnect(s)
		accepted := false
		if s.check.ReplacesPin() {
			accepted = c.decideReplacePin(cmd, s, opts)
		} else {
			accepted = c.decideTrust(cmd, s.side, s.check.Fingerprint, opts.connectOptions)
		}
		if !accepted {
			return false
		}
	}
	for _, s := range certs {
		*s.decision = s.check.Fingerprint
	}
	return true
}

// describeReconnect shows a certificate that the test did not accept: the host
// and port of the stored settings, the old and the new fingerprint, and the
// fixed text of the problem. It shows no other value of the server.
func (c *cli) describeReconnect(s reconnectCert) {
	c.printf("The %s server %s:%d showed a certificate that this account does not trust.\n", s.name, s.host, s.port)
	if s.check.OldFingerprint != "" {
		c.printf("  Trusted until now, SHA-256: %s\n", connect.GroupFingerprint(s.check.OldFingerprint))
	}
	if s.check.Fingerprint != "" {
		c.printf("  Shown now, SHA-256:         %s\n", connect.GroupFingerprint(s.check.Fingerprint))
	}
	c.printf("  %s\n", s.check.Problem)
}

// decideReplacePin asks the user whether to replace (or remove) the pin of a
// server whose pinned certificate changed. It always shows the warning. On a
// terminal the user must type "yes". Without a terminal, --trust-fingerprint
// must name the new fingerprint, and --replace-pinned-certificate must be
// given too.
func (c *cli) decideReplacePin(cmd string, s reconnectCert, opts reconnectOptions) bool {
	c.printf("%s\n", connect.TextCertChangedWarning)
	question := fmt.Sprintf("Trust the new certificate of the %s server %s:%d instead of the old one?", s.name, s.host, s.port)
	if s.check.Change == service.PinChangeRemove {
		question = fmt.Sprintf("Stop pinning a certificate for the %s server %s:%d, and check it with the authorities "+
			"that this computer trusts?", s.name, s.host, s.port)
	}
	if !c.sys.isTerminal() {
		fingerprint := s.check.Fingerprint
		if !slices.Contains(opts.trust, fingerprint) {
			c.errorf("%s: the certificate of the %s server %s:%d changed. Nothing was saved.\n", cmd, s.name, s.host, s.port)
			c.errorf("%s: %s Then, to accept it without a terminal, run again with --trust-fingerprint %s "+
				"--replace-pinned-certificate.\n", cmd, msgCheckFingerprint, fingerprint)
			return false
		}
		if !opts.replacePinned {
			c.errorf("%s: to replace the trusted certificate of the %s server without a terminal, also give "+
				"--replace-pinned-certificate. Nothing was saved.\n", cmd, s.name)
			return false
		}
		c.printf("Accepted the new certificate of the %s server: it matches --trust-fingerprint, and "+
			"--replace-pinned-certificate is given.\n", s.name)
		return true
	}
	c.printf("%s\n", msgCheckFingerprint)
	if !c.confirmTyped(question) {
		c.errorf("%s: the certificate of the %s server was not accepted. Nothing was saved.\n", cmd, s.name)
		return false
	}
	return true
}
