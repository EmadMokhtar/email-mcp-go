package service

// This file holds the use case that connects a new account: the web interface
// and the command line tool both use it.

import (
	"context"
	"errors"
	"fmt"
	"net"
	netmail "net/mail"
	"strings"
	"unicode"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// The audit entry of a new account.
const (
	toolConnectAccount     = "connect_account"
	detailAccountConnected = "account_connected"
)

// The limits of the text of a new account.
const (
	// maxAddressBytes is the longest accepted email address (RFC 5321).
	maxAddressBytes = 254
	// maxHostBytes is the longest accepted host name.
	maxHostBytes = 253
	// maxLabelBytes is the longest part of a host name between two dots.
	maxLabelBytes = 63
)

// The errors of CreateIMAPAccount. Callers check them with errors.Is. Their
// texts never hold the address, a host name or the password.
var (
	// ErrInvalidAddress means that the email address is not a plain address
	// such as "alice@example.com".
	ErrInvalidAddress = errors.New("service: the email address is not valid")
	// ErrInvalidSettings means that the server settings are not valid: a host,
	// a port, a security mode, the user name or a pin.
	ErrInvalidSettings = errors.New("service: the server settings are not valid")
	// ErrNoPassword means that the password is empty.
	ErrNoPassword = errors.New("service: the password is empty")
	// ErrAccountExists means that the tenant already has an account with this
	// address (in any case).
	ErrAccountExists = errors.New("service: an account with this address is already connected")
)

// ConnectionTestError means that the test of the settings failed: a server
// could not be reached, the connection could not be secured, or a server
// refused the login. Nothing was saved. Err is the error of the probe, which
// holds no text of the server and never the password.
type ConnectionTestError struct {
	Err error
}

// Error says that the test failed, and why.
func (e *ConnectionTestError) Error() string {
	return "service: the connection test failed: " + e.Err.Error()
}

// Unwrap returns the error of the probe.
func (e *ConnectionTestError) Unwrap() error { return e.Err }

// CertificateError means that the IMAP server, the SMTP server or both showed a
// certificate that is not accepted. Nothing was saved, and the password was not
// sent to a server whose certificate is here.
//
// A field is nil when that server's certificate was accepted. See
// imapsmtp.CertificateUnknownError for the one case in which the user may be
// offered to trust (pin) the certificate.
type CertificateError struct {
	IMAP *imapsmtp.CertificateUnknownError
	SMTP *imapsmtp.CertificateUnknownError
}

// Error names the servers with a certificate that is not accepted.
func (e *CertificateError) Error() string {
	parts := make([]string, 0, 2)
	for _, c := range e.certs() {
		parts = append(parts, c.Error())
	}
	return "service: a mail server showed a certificate that is not accepted: " + strings.Join(parts, "; ")
}

// Unwrap returns the certificate errors that are set.
func (e *CertificateError) Unwrap() []error {
	var errs []error
	for _, c := range e.certs() {
		errs = append(errs, c)
	}
	return errs
}

func (e *CertificateError) certs() []*imapsmtp.CertificateUnknownError {
	var certs []*imapsmtp.CertificateUnknownError
	for _, c := range []*imapsmtp.CertificateUnknownError{e.IMAP, e.SMTP} {
		if c != nil {
			certs = append(certs, c)
		}
	}
	return certs
}

// CreateIMAPAccount connects a new IMAP and SMTP account for the tenant of the
// caller. In this order it:
//  1. cleans the address and the settings (outer spaces go; host names and pins
//     become lower case; an empty user name becomes the address) and checks them
//     with MailServer.Validate;
//  2. refuses an address that the tenant has already connected;
//  3. tests the settings and the password with the probe, and refuses unless
//     both the IMAP and the SMTP login work;
//  4. makes the account with account.NewIMAPAccount (level Send, both
//     confirmations on, the default limits);
//  5. seals the password with the key of the tenant, and stores the account and
//     the sealed password in one transaction, so a failure leaves nothing;
//  6. writes the audit entry "account_connected" (it holds the account id and
//     no address).
//
// It returns ErrNoPrincipal, ErrInvalidAddress, ErrNoPassword,
// ErrInvalidSettings or ErrAccountExists before any network use. A failed test
// gives a *ConnectionTestError, and a certificate that is not accepted gives a
// *CertificateError. A pin in the settings (PinnedIMAPCert, PinnedSMTPCert) is
// the user's choice to trust that certificate: the caller sets it only after
// the user agreed.
//
// The password is never logged, never put in an error and never stored
// without encryption.
func (s *Service) CreateIMAPAccount(ctx context.Context, address string, server account.MailServer, password secrets.Secret) (*account.Account, error) {
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.d.Vault == nil || s.d.Audit == nil {
		return nil, errors.New("service: the vault and the audit log must be set up to connect an account")
	}
	address, err = cleanAddress(address)
	if err != nil {
		return nil, err
	}
	if password == "" {
		return nil, ErrNoPassword
	}
	server = cleanServer(server, address)
	if err := checkServer(server, s.d.Local); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSettings, err)
	}
	// This check saves a probe that sends the password for nothing. The store
	// checks again when it saves, because another process may connect the same
	// address while the probe runs.
	taken, err := s.addressTaken(ctx, p.Tenant, address)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrAccountExists
	}

	if err := s.probe(ctx, server, password); err != nil {
		return nil, err
	}

	a := account.NewIMAPAccount(p.Tenant, address, server, s.d.Now())
	plain := []byte(password.Reveal())
	version, sealed, err := s.d.Vault.Encrypt(ctx, p.Tenant, secrets.AAD(string(p.Tenant), string(a.ID), passwordSecret), plain)
	clear(plain)
	if err != nil {
		return nil, fmt.Errorf("service: seal the password: %w", err)
	}
	err = s.d.Store.CreateAccountWithSecret(ctx, a, passwordSecret, version, sealed)
	if errors.Is(err, store.ErrExists) {
		return nil, ErrAccountExists
	}
	if err != nil {
		return nil, fmt.Errorf("service: save the account: %w", err)
	}

	s.record(ctx, p, entry{tool: toolConnectAccount, account: a.ID, outcome: audit.OutcomeOK, detail: detailAccountConnected})
	return a, nil
}

// probe tests the settings and the password. It returns nil only when both
// logins worked.
func (s *Service) probe(ctx context.Context, server account.MailServer, password secrets.Secret) error {
	res, err := s.d.Probe(ctx, imapsmtp.Config{
		Server:   server,
		Password: password,
		Local:    s.d.Local,
		Logger:   s.d.Logger,
	})
	switch {
	case err != nil:
		return &ConnectionTestError{Err: err}
	case res == nil:
		return &ConnectionTestError{Err: errors.New("the test gave no result")}
	case res.IMAPCert != nil || res.SMTPCert != nil:
		return &CertificateError{IMAP: res.IMAPCert, SMTP: res.SMTPCert}
	case !res.IMAPOK || !res.SMTPOK:
		return &ConnectionTestError{Err: errors.New("the IMAP or the SMTP login did not work")}
	}
	return nil
}

// addressTaken reports whether the tenant has an account with this address, in
// any case.
func (s *Service) addressTaken(ctx context.Context, tenant account.TenantID, address string) (bool, error) {
	accounts, err := s.d.Store.ListAccounts(ctx, tenant)
	if err != nil {
		return false, fmt.Errorf("service: list accounts: %w", err)
	}
	for _, a := range accounts {
		if strings.EqualFold(a.Address, address) {
			return true, nil
		}
	}
	return false, nil
}

// cleanAddress removes the outer spaces of the address and checks that it is a
// plain address, such as "alice@example.com": no display name, no space, no
// control character, at most 254 bytes.
func cleanAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" || len(address) > maxAddressBytes ||
		strings.ContainsFunc(address, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", ErrInvalidAddress
	}
	parsed, err := netmail.ParseAddress(address)
	if err != nil || parsed.Name != "" || parsed.Address != address {
		return "", ErrInvalidAddress
	}
	return address, nil
}

// cleanServer removes the outer spaces of the hosts, the user name and the pins,
// and makes the hosts and the pins lower case. A white-space host becomes empty,
// so Validate refuses it. An empty user name becomes the address.
func cleanServer(server account.MailServer, address string) account.MailServer {
	server.IMAPHost = strings.ToLower(strings.TrimSpace(server.IMAPHost))
	server.SMTPHost = strings.ToLower(strings.TrimSpace(server.SMTPHost))
	server.Username = strings.TrimSpace(server.Username)
	if server.Username == "" {
		server.Username = address
	}
	server.PinnedIMAPCert = strings.ToLower(strings.TrimSpace(server.PinnedIMAPCert))
	server.PinnedSMTPCert = strings.ToLower(strings.TrimSpace(server.PinnedSMTPCert))
	return server
}

// checkServer checks cleaned settings: Validate, and then the form of the host
// names, of the user name and of the pins. The error texts hold no value.
func checkServer(server account.MailServer, local bool) error {
	if err := server.Validate(local); err != nil {
		return err
	}
	if !validHost(server.IMAPHost) {
		return errors.New("the imap host is not a host name or an IP address")
	}
	if !validHost(server.SMTPHost) {
		return errors.New("the smtp host is not a host name or an IP address")
	}
	if strings.ContainsFunc(server.Username, unicode.IsControl) {
		return errors.New("the user name has a control character")
	}
	if !validPin(server.PinnedIMAPCert) {
		return errors.New("the imap pin is not a SHA-256 in hex")
	}
	if !validPin(server.PinnedSMTPCert) {
		return errors.New("the smtp pin is not a SHA-256 in hex")
	}
	return nil
}

// validHost reports whether host is an IP address, or a host name in lower case
// ASCII: labels of letters, digits, "-" and "_", separated by dots. A port, a
// scheme, a path or a space is refused. A name in another script must be given
// in its ASCII (punycode) form.
func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || len(host) > maxHostBytes {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > maxLabelBytes {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return false
			}
		}
	}
	return true
}

// validPin reports whether pin is empty (no pin) or a fingerprint.
func validPin(pin string) bool {
	return pin == "" || account.IsFingerprint(pin)
}
