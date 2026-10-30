package service

// This file holds the use case that reconnects an account: it gives a
// connected account a new password, or trusts a new certificate of one of its
// servers, and keeps the account. The account keeps its id, its server
// settings, its levels and switches, its queued messages and its activity. The
// user, never the agent, calls it: from the web interface or with "email-mcp
// account reconnect". No MCP tool reaches it.
//
// A change of the server settings (hosts, ports, security modes) is not a
// reconnect: the user removes the account and adds it again for that.

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// The audit entry of a reconnect. The detail says whether the password and a
// pin changed, and nothing else.
const (
	toolReconnectAccount = "reconnect_account"

	detailReconnected               = "account_reconnected"
	detailReconnectedPassword       = "account_reconnected_password_changed"
	detailReconnectedPin            = "account_reconnected_pin_changed"
	detailReconnectedPasswordAndPin = "account_reconnected_password_and_pin_changed"
)

// The errors of ReconnectIMAPAccount. Callers check them with errors.Is.
var (
	// ErrCertificateDecisionStale means that a decision of the user about a
	// certificate is not about the certificate that the server shows now: the
	// server shows its trusted certificate again, or its certificate changed
	// again during the test. Nothing was saved. The user starts the reconnect
	// again and sees the certificate of now.
	ErrCertificateDecisionStale = errors.New("service: a certificate decision is not about the certificate " +
		"that the server shows now")
	// ErrAccountChanged means that the server settings of the account changed
	// while the reconnect tested them (another program changed them). Nothing
	// was saved.
	ErrAccountChanged = errors.New("service: the account changed during the reconnect")
)

// Reconnect is what the user gives to reconnect an account.
type Reconnect struct {
	// Password is the new password. Empty keeps the stored password.
	Password secrets.Secret
	// PinnedIMAPCert and PinnedSMTPCert are the decisions of the user about
	// certificates, in the same form as the pins that CreateIMAPAccount takes:
	// the SHA-256 fingerprint (hex) of a certificate that the user agreed to
	// trust after they saw it. Empty means no decision.
	//
	// A decision counts only for the server that shows a certificate with this
	// fingerprint in this test, and only when ReconnectPinChange allows a
	// change for that certificate. For PinChangeRemove the pin is removed. The
	// caller sets a decision only after the user confirmed it.
	PinnedIMAPCert, PinnedSMTPCert string
}

// ReconnectResult says what a reconnect changed.
type ReconnectResult struct {
	// PasswordChanged is true when a new password was saved.
	PasswordChanged bool
	// PinChanged is true when a pin was added, replaced or removed.
	PinChanged bool
}

// PinChange is what a reconnect may do with the pin of a server whose
// certificate the stored settings did not accept.
type PinChange int

// The changes of a pin.
const (
	// PinChangeRefused means that the certificate cannot be trusted. Nothing
	// may change.
	PinChangeRefused PinChange = iota
	// PinChangeAdd means that no pin was set, and that the only problem is that
	// no known authority signed the certificate: the rule of a first pin, as
	// for a new account (connect.CheckCertificate). The user may pin it.
	PinChangeAdd
	// PinChangeReplace means that a pin was set, and the server shows another
	// certificate. Its host name and its time are right, and its only other
	// problem is that no known authority signed it. The user may replace the
	// pin with its fingerprint.
	PinChangeReplace
	// PinChangeRemove means that a pin was set, and the server shows another
	// certificate that a known authority signs for the host. The user may remove
	// the pin: the server is then checked as a server without a pin.
	PinChangeRemove
)

// ReconnectPinChange decides what a reconnect may do about a certificate that a
// server showed and the stored settings did not accept. A certificate for
// another host name, a certificate out of its time (expired or not valid yet),
// a certificate with another problem, and a value that is not a SHA-256 are
// always refused.
func ReconnectPinChange(c *imapsmtp.CertificateUnknownError) PinChange {
	if c == nil || !account.IsFingerprint(c.FingerprintSHA256) {
		return PinChangeRefused
	}
	var (
		unknownAuthority x509.UnknownAuthorityError
		hostname         x509.HostnameError
		invalid          x509.CertificateInvalidError
	)
	switch {
	case errors.As(c.Reason, &hostname), errors.As(c.Reason, &invalid):
		return PinChangeRefused
	case c.Pinned && c.Reason == nil:
		return PinChangeRemove
	case !errors.As(c.Reason, &unknownAuthority):
		return PinChangeRefused
	case c.Pinned:
		return PinChangeReplace
	default:
		return PinChangeAdd
	}
}

// ReconnectCertificate is the certificate of one server in a reconnect, with
// what the user needs to see to decide about it.
type ReconnectCertificate struct {
	// Host and Port are the stored settings of the server, not values of the
	// server.
	Host string
	Port int
	// OldPin is the stored pin of the server: the certificate that the user
	// trusted before. It is empty when the server had no pin.
	OldPin string
	// Cert is the certificate that the server showed now. Its fingerprint is
	// the only value of the server that a caller may show.
	Cert *imapsmtp.CertificateUnknownError
	// Change is what the user may do about it. PinChangeRefused means nothing.
	Change PinChange
}

// ReconnectCertificateError means that the IMAP server, the SMTP server or both
// showed a certificate that the stored settings do not accept, and the call
// has no decision for it that the rules allow. Nothing was saved, and the
// password was not sent to a server named here. A field is nil when that
// server's certificate was accepted.
type ReconnectCertificateError struct {
	IMAP, SMTP *ReconnectCertificate
}

// Error names the servers with a certificate that is not accepted. It holds
// the host names and the fingerprints only.
func (e *ReconnectCertificateError) Error() string {
	parts := make([]string, 0, 2)
	for _, c := range []*ReconnectCertificate{e.IMAP, e.SMTP} {
		if c != nil && c.Cert != nil {
			parts = append(parts, c.Cert.Error())
		}
	}
	return "service: a mail server showed a certificate that the account does not trust: " + strings.Join(parts, "; ")
}

// Unwrap returns the certificate errors that are set.
func (e *ReconnectCertificateError) Unwrap() []error {
	var errs []error
	for _, c := range []*ReconnectCertificate{e.IMAP, e.SMTP} {
		if c != nil && c.Cert != nil {
			errs = append(errs, c.Cert)
		}
	}
	return errs
}

// reconnectSide is one of the two servers of an account in a reconnect.
type reconnectSide struct {
	host     func(*account.MailServer) string
	port     func(*account.MailServer) int
	pin      func(*account.MailServer) *string
	cert     func(*imapsmtp.ProbeResult) *imapsmtp.CertificateUnknownError
	decision string
	report   **ReconnectCertificate
}

// ReconnectIMAPAccount reconnects an account of the tenant of the caller: it
// tests the stored server settings with a new password (or the stored one when
// r.Password is empty), and saves the new password and the pins that the user
// decided, and marks the account as connected. In this order it:
//  1. finds the account in the tenant (ErrAccountNotFound) and checks the form
//     of the decisions (ErrInvalidSettings), before any network use;
//  2. tests the IMAP and the SMTP login with the stored settings and pins (the
//     same test as CreateIMAPAccount). A server whose certificate is not
//     accepted fails at the TLS handshake, so it never gets the password;
//  3. for each server whose certificate was not accepted, takes the decision
//     of r for it only when its fingerprint is the one that the server showed
//     and ReconnectPinChange allows a change. Otherwise it returns a
//     *ReconnectCertificateError, with what the user needs to see to decide;
//  4. when a pin changed, tests again with the new pins, so the login of that
//     server is tested too, under the new trust;
//  5. seals a new password with the key of the tenant, and saves the pins, the
//     sealed password and the status "ok" in one step (store.ReconnectAccount);
//  6. writes the audit entry "reconnect_account". Its detail says whether the
//     password and a pin changed. It holds no address.
//
// A failed test, a refusal and an error change nothing. A failed test gives a
// *ConnectionTestError. A decision for a server that shows no new certificate
// gives ErrCertificateDecisionStale, and so does a certificate that changed
// again between the two tests. Server settings that changed during the test
// give ErrAccountChanged.
//
// The cached backend of the account is not closed here: a call may use it at
// this moment. The backend cache compares a fingerprint of the server settings
// (the pins too) and of the sealed password on every call, so the next call
// builds a backend with the new password and pins, and the old backend closes
// after a grace time (see BackendCache). The same happens in a server that
// runs in another process.
//
// The password is never logged, never put in an error and never stored
// without encryption.
func (s *Service) ReconnectIMAPAccount(ctx context.Context, id account.AccountID, r Reconnect) (*ReconnectResult, error) {
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.d.Vault == nil || s.d.Audit == nil {
		return nil, errors.New("service: the vault and the audit log must be set up to reconnect an account")
	}
	a, err := s.d.Store.GetAccount(ctx, p.Tenant, id)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalidID) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("service: look up account: %w", err)
	}
	if a.Mail == nil {
		return nil, errors.New("service: the account has no mail server settings")
	}
	imapDecision := strings.ToLower(strings.TrimSpace(r.PinnedIMAPCert))
	smtpDecision := strings.ToLower(strings.TrimSpace(r.PinnedSMTPCert))
	if !validPin(imapDecision) || !validPin(smtpDecision) {
		return nil, fmt.Errorf("%w: a certificate decision is not a SHA-256 in hex", ErrInvalidSettings)
	}

	res := &ReconnectResult{PasswordChanged: r.Password != ""}
	password := r.Password
	if password == "" {
		if password, err = s.storedPassword(ctx, a); err != nil {
			return nil, err
		}
	}

	tested := *a.Mail
	next, err := s.reconnectPins(ctx, tested, password, imapDecision, smtpDecision)
	if err != nil {
		return nil, err
	}
	res.PinChanged = next.PinnedIMAPCert != tested.PinnedIMAPCert || next.PinnedSMTPCert != tested.PinnedSMTPCert
	if res.PinChanged {
		if err := s.probe(ctx, next, password); err != nil {
			var certErr *CertificateError
			if errors.As(err, &certErr) {
				// A certificate changed again after the first test. The user
				// decided about another certificate.
				return nil, ErrCertificateDecisionStale
			}
			return nil, err
		}
	}

	save := store.AccountReconnect{
		Tested:         tested,
		PinnedIMAPCert: next.PinnedIMAPCert,
		PinnedSMTPCert: next.PinnedSMTPCert,
		SecretKind:     passwordSecret,
	}
	if res.PasswordChanged {
		plain := []byte(password.Reveal())
		save.KeyVersion, save.Sealed, err = s.d.Vault.Encrypt(ctx, p.Tenant, secrets.AAD(string(p.Tenant), string(a.ID), passwordSecret), plain)
		clear(plain)
		if err != nil {
			return nil, fmt.Errorf("service: seal the password: %w", err)
		}
	}
	err = s.d.Store.ReconnectAccount(ctx, p.Tenant, a.ID, save)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, ErrAccountNotFound
	case errors.Is(err, store.ErrConflict):
		return nil, ErrAccountChanged
	case err != nil:
		return nil, fmt.Errorf("service: save the reconnect: %w", err)
	}

	s.record(ctx, p, entry{tool: toolReconnectAccount, account: a.ID, outcome: audit.OutcomeOK, detail: reconnectDetail(res)})
	return res, nil
}

// reconnectPins tests the stored settings with the password, and returns the
// settings with the pins that the decisions change. It returns an error when a
// login fails, when a server shows a certificate that no decision allows, or
// when a decision is for a server that shows no new certificate.
func (s *Service) reconnectPins(ctx context.Context, tested account.MailServer, password secrets.Secret, imapDecision, smtpDecision string) (account.MailServer, error) {
	res, err := s.d.Probe(ctx, imapsmtp.Config{Server: tested, Password: password, Local: s.d.Local, Logger: s.d.Logger})
	switch {
	case err != nil:
		return account.MailServer{}, &ConnectionTestError{Err: err}
	case res == nil:
		return account.MailServer{}, &ConnectionTestError{Err: errors.New("the test gave no result")}
	case res.IMAPCert == nil && res.SMTPCert == nil && (!res.IMAPOK || !res.SMTPOK):
		return account.MailServer{}, &ConnectionTestError{Err: errors.New("the IMAP or the SMTP login did not work")}
	}

	next := tested
	certErr := &ReconnectCertificateError{}
	refused, stale := false, false
	sides := []reconnectSide{
		{
			host:     func(m *account.MailServer) string { return m.IMAPHost },
			port:     func(m *account.MailServer) int { return m.IMAPPort },
			pin:      func(m *account.MailServer) *string { return &m.PinnedIMAPCert },
			cert:     func(r *imapsmtp.ProbeResult) *imapsmtp.CertificateUnknownError { return r.IMAPCert },
			decision: imapDecision, report: &certErr.IMAP,
		},
		{
			host:     func(m *account.MailServer) string { return m.SMTPHost },
			port:     func(m *account.MailServer) int { return m.SMTPPort },
			pin:      func(m *account.MailServer) *string { return &m.PinnedSMTPCert },
			cert:     func(r *imapsmtp.ProbeResult) *imapsmtp.CertificateUnknownError { return r.SMTPCert },
			decision: smtpDecision, report: &certErr.SMTP,
		},
	}
	for _, side := range sides {
		oldPin := *side.pin(&tested)
		c := side.cert(res)
		if c == nil {
			// The certificate of this server is accepted. A decision that is not
			// its stored pin is about a certificate that it does not show.
			if side.decision != "" && side.decision != oldPin {
				stale = true
			}
			continue
		}
		change := ReconnectPinChange(c)
		if change != PinChangeRefused && side.decision != "" && side.decision == c.FingerprintSHA256 {
			if change == PinChangeRemove {
				*side.pin(&next) = ""
			} else {
				*side.pin(&next) = c.FingerprintSHA256
			}
			continue
		}
		refused = true
		*side.report = &ReconnectCertificate{
			Host: side.host(&tested), Port: side.port(&tested), OldPin: oldPin, Cert: c, Change: change,
		}
	}
	switch {
	case refused:
		return account.MailServer{}, certErr
	case stale:
		return account.MailServer{}, ErrCertificateDecisionStale
	}
	return next, nil
}

// reconnectDetail returns the detail code of the audit entry of a reconnect.
func reconnectDetail(res *ReconnectResult) string {
	switch {
	case res.PasswordChanged && res.PinChanged:
		return detailReconnectedPasswordAndPin
	case res.PasswordChanged:
		return detailReconnectedPassword
	case res.PinChanged:
		return detailReconnectedPin
	default:
		return detailReconnected
	}
}

// storedPassword decrypts the stored password of an account.
func (s *Service) storedPassword(ctx context.Context, a *account.Account) (secrets.Secret, error) {
	keyVersion, sealed, err := s.d.Store.GetAccountSecret(ctx, a.TenantID, a.ID, passwordSecret)
	if errors.Is(err, store.ErrNotFound) {
		return "", errors.New("service: the account has no stored password")
	}
	if err != nil {
		return "", fmt.Errorf("service: read the account password: %w", err)
	}
	plain, err := s.d.Vault.Decrypt(ctx, a.TenantID, keyVersion, secrets.AAD(string(a.TenantID), string(a.ID), passwordSecret), sealed)
	if err != nil {
		return "", fmt.Errorf("service: decrypt the account password: %w", err)
	}
	password := secrets.Secret(plain)
	// The Secret holds its own copy of the text. Clear the bytes of the
	// plaintext.
	clear(plain)
	return password, nil
}
