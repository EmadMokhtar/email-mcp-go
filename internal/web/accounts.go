package web

// This file holds the accounts page and the pages that connect an IMAP and SMTP
// account:
//
//  1. GET /connect asks for the email address.
//  2. POST /connect/discover shows the server settings that discovery
//     suggests. It tests nothing and asks for no password: the user first
//     chooses the servers that get the password (an MX guess can name the host
//     of a third party, such as a spam filter).
//  3. POST /connect/settings shows the settings form, filled with the chosen
//     suggestion (or empty), with the password field.
//  4. POST /connect/save tests the settings and saves the account when the
//     test passes (Service.CreateIMAPAccount). When a server shows a
//     certificate that is not trusted, the page shows its fingerprint and may
//     offer to trust it.
//
// An offer to trust a certificate is a signed token (purpose "cert_offer", 10
// minutes) bound to the session, the protocol, the host, the port and the
// fingerprint. The save takes a pin only from such an offer whose protocol, host
// and port are the ones posted now, so a pin that the page never offered, or an
// offer moved to another host, is refused before any test. A plain
// (unencrypted) connection needs its own "I understand" box, which the server
// checks before any test.
//
// The password is never kept between requests, never put in a page, a log or a
// redirect: after every refusal the user types it again. A message about a
// failed test is a fixed text chosen by the type of the error, never the text
// of the server. The only value from a server that a page shows is the
// fingerprint of a certificate.

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/discovery"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/errtype"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// The names of the pages of this file.
const (
	pageAccounts = "accounts"
	pageConnect  = "connect"
)

// The steps of the connect page.
const (
	stepEmail    = "email"
	stepChoose   = "choose"
	stepSettings = "settings"
)

// Fixed codes for the log.
const (
	codeNoService      = "no_service"
	codeAccountsFailed = "accounts_failed"
	codeConnectFailed  = "connect_failed"
	codeConnectRefused = "connect_refused"
)

// webClient is the client name of the audit entries that the user makes in the
// web interface.
const webClient = "web"

// purposeCertOffer is the signing purpose of an offer to trust a certificate.
const purposeCertOffer = "cert_offer"

// certOfferTTL is how long an offer to trust a certificate can be used.
const certOfferTTL = 10 * time.Minute

// certOffer is what an offer token holds: the server offered this session to
// trust the certificate with this fingerprint for this protocol, host and
// port. The token is signed, so the browser cannot change it, and it is
// readable, so it holds nothing secret (the session hash is not the session
// id). The keys are short to keep the form small.
type certOffer struct {
	Session     []byte `json:"s"`
	Protocol    string `json:"p"`
	Host        string `json:"h"`
	Port        int    `json:"n"`
	Fingerprint string `json:"f"`
}

// The defaults of the settings form when the user types the settings.
const (
	defaultIMAPPort = "993"
	defaultSMTPPort = "465"
)

// The fixed texts of the connect pages.
const (
	msgAddress        = "This email address is not valid."
	msgNoPassword     = "Type the password."
	msgExists         = "An account with this address is already connected."
	msgSettings       = "The server settings are not valid. Check the host names (without a port or a scheme), the ports (1 to 65535) and the security settings. A connection without encryption is allowed only to a server on this computer."
	msgTestFailed     = "The connection test failed. Check the user name, the password and the server settings. Some providers need an app password."
	msgAuthFailed     = "The server refused the user name or the password. Some providers need an app password, or IMAP access turned on in their settings."
	msgCannotConnect  = "Could not connect to the server. Check the host names and the ports."
	msgHostNotFound   = "A server name was not found. Check the host names."
	msgTimeout        = "A server did not answer in time. Check the host names and the ports, then try again."
	msgTLSFailed      = "A secure connection could not be made. Check the ports and the security settings."
	msgCertificate    = "A server showed a certificate that this computer does not trust. Nothing was saved."
	msgUnderstand     = "To trust the certificate of a server that is not on this computer, also tick “I understand the risk”."
	msgBadTrust       = "The choice to trust a certificate is not valid: the server settings changed after the offer, or the offer is older than 10 minutes. Test the connection again to see the certificate."
	msgPlain          = "You chose a connection without encryption. To use it, also tick “I understand that this connection is not encrypted”."
	msgCertUnknown    = "No authority that this computer trusts signed this certificate. A local bridge (such as Proton Mail Bridge) uses such a certificate. Trust it only if you expect it."
	msgCertWrongHost  = "The certificate is not for this host name. Check the host name. This certificate cannot be trusted here."
	msgCertExpired    = "The certificate has expired or is not valid yet. This certificate cannot be trusted here."
	msgCertPinChanged = "The certificate is not the one you chose to trust. Someone on the network may have changed it. This certificate cannot be trusted here."
	msgCertInvalid    = "The certificate is not valid. This certificate cannot be trusted here."
	msgTrusted        = "You chose to trust this certificate."
)

// accountsPage is the data of the accounts page. It holds no secret, no server
// setting and no user name.
type accountsPage struct {
	Accounts []accountRow
}

// accountRow is one account on the accounts page.
type accountRow struct {
	Address   string
	Provider  string
	MailLevel string
	Status    string
}

// connectPage is the data of the connect page. It has no field for the
// password, so no step can show it.
type connectPage struct {
	Step  string
	Error string
	Email string
	// Suggestions are the settings that discovery found (step "choose").
	Suggestions []serverFields
	// NotFound is true when discovery found nothing (step "choose").
	NotFound bool
	// Server holds the values of the settings form (step "settings").
	Server serverFields
	// Certs are the certificates to show: the ones the test did not accept,
	// and the ones the user chose to trust.
	Certs []certPrompt
	// Understand shows the "I understand the risk" box. It is needed to trust
	// the certificate of a server that is not on this computer.
	Understand        bool
	UnderstandChecked bool
	// Plain shows the warning and the box that a plain (unencrypted)
	// connection needs. It is shown while a server on this computer is set to
	// plain. A plain connection to another host is refused in any case.
	Plain        bool
	PlainChecked bool
}

// serverFields are the values of the settings form, as text, so that a value
// that is not valid can be shown again.
type serverFields struct {
	IMAPHost     string
	IMAPPort     string
	IMAPSecurity string
	SMTPHost     string
	SMTPPort     string
	SMTPSecurity string
	Username     string
}

// SecurityLabel returns the text of a security mode for the page.
func (serverFields) SecurityLabel(mode string) string {
	switch account.Security(mode) {
	case account.SecurityTLS:
		return "TLS"
	case account.SecuritySTARTTLS:
		return "STARTTLS"
	case account.SecurityPlain:
		return "none (not encrypted)"
	default:
		return "unknown"
	}
}

// certPrompt is the certificate of one server on the connect page.
type certPrompt struct {
	// Name is "IMAP" or "SMTP".
	Name string
	// Field is "imap" or "smtp": the form names are "trust_<field>" and
	// "<field>_offer".
	Field string
	// Host is the host that the user typed (cleaned), not a value from the
	// server.
	Host string
	// Fingerprint is the SHA-256 of the certificate in groups, such as
	// "AB:CD:...". It is empty when the server showed no certificate.
	Fingerprint string
	// Problem is a fixed text about the certificate.
	Problem string
	// CanTrust shows the "Trust this certificate" box, with Offer (the signed
	// offer token) in a hidden field.
	CanTrust bool
	Offer    string
	Checked  bool
}

// protocol is one of the two servers of an account, with the parts of the form
// and of the settings that belong to it.
type protocol struct {
	name     string // "IMAP" or "SMTP"
	field    string // "imap" or "smtp"
	host     func(*account.MailServer) string
	port     func(*account.MailServer) int
	security func(*account.MailServer) account.Security
	pin      func(*account.MailServer) *string
	cert     func(*service.CertificateError) *imapsmtp.CertificateUnknownError
}

// protocols are the two servers, IMAP first.
var protocols = []protocol{
	{
		name: "IMAP", field: "imap",
		host:     func(m *account.MailServer) string { return m.IMAPHost },
		port:     func(m *account.MailServer) int { return m.IMAPPort },
		security: func(m *account.MailServer) account.Security { return m.IMAPSecurity },
		pin:      func(m *account.MailServer) *string { return &m.PinnedIMAPCert },
		cert:     func(e *service.CertificateError) *imapsmtp.CertificateUnknownError { return e.IMAP },
	},
	{
		name: "SMTP", field: "smtp",
		host:     func(m *account.MailServer) string { return m.SMTPHost },
		port:     func(m *account.MailServer) int { return m.SMTPPort },
		security: func(m *account.MailServer) account.Security { return m.SMTPSecurity },
		pin:      func(m *account.MailServer) *string { return &m.PinnedSMTPCert },
		cert:     func(e *service.CertificateError) *imapsmtp.CertificateUnknownError { return e.SMTP },
	},
}

// handleAccounts shows the accounts of the tenant of the session.
func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r.Context())
	if !ok {
		s.fail(w, r, codeAccountsFailed, errNoSession)
		return
	}
	accounts, err := s.d.Store.ListAccounts(r.Context(), sess.tenant)
	if err != nil {
		s.fail(w, r, codeAccountsFailed, err)
		return
	}
	page := accountsPage{Accounts: make([]accountRow, 0, len(accounts))}
	for _, a := range accounts {
		page.Accounts = append(page.Accounts, accountRow{
			Address:   a.Address,
			Provider:  providerLabel(a.Provider),
			MailLevel: a.Level(account.AreaMail).MailName(),
			Status:    statusLabel(a.Status),
		})
	}
	s.render(w, r, http.StatusOK, pageAccounts, page)
}

// providerLabel returns the text of a provider for the page.
func providerLabel(p account.Provider) string {
	if p == account.ProviderIMAP {
		return "IMAP and SMTP"
	}
	return "Unknown"
}

// statusLabel returns the text of an account status for the page.
func statusLabel(st account.Status) string {
	switch st {
	case account.StatusOK:
		return "Connected"
	case account.StatusNeedsReauth:
		return "Needs to be reconnected"
	default:
		return "Unknown"
	}
}

// handleConnect shows the first step: the email address.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, pageConnect, connectPage{Step: stepEmail})
}

// handleDiscover shows the settings that discovery suggests for the address.
// It sends only the domain of the address to the network, and tests nothing.
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	page := connectPage{Step: stepChoose, Email: strings.TrimSpace(r.PostForm.Get("email"))}
	if s.d.Discovery == nil {
		page.NotFound = true
		s.render(w, r, http.StatusOK, pageConnect, page)
		return
	}
	servers, err := s.d.Discovery.Mail(r.Context(), page.Email)
	switch {
	case errors.Is(err, discovery.ErrInvalidAddress):
		page.Step, page.Error = stepEmail, msgAddress
		s.render(w, r, http.StatusUnprocessableEntity, pageConnect, page)
		return
	case err != nil:
		// Nothing found, or the time ran out: the user types the settings.
		page.NotFound = true
	default:
		for _, m := range servers {
			page.Suggestions = append(page.Suggestions, fieldsOf(m))
		}
	}
	s.render(w, r, http.StatusOK, pageConnect, page)
}

// handleSettings shows the settings form, filled with the posted settings: a
// suggestion that the user chose, or nothing. Empty ports and security modes
// get the usual values, and an empty user name gets the address. It tests
// nothing.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	page := connectPage{Step: stepSettings, Email: strings.TrimSpace(r.PostForm.Get("email"))}
	page.Server = readFields(r)
	f := &page.Server
	if f.IMAPPort == "" {
		f.IMAPPort = defaultIMAPPort
	}
	if f.IMAPSecurity == "" {
		f.IMAPSecurity = string(account.SecurityTLS)
	}
	if f.SMTPPort == "" {
		f.SMTPPort = defaultSMTPPort
	}
	if f.SMTPSecurity == "" {
		f.SMTPSecurity = string(account.SecurityTLS)
	}
	if f.Username == "" {
		f.Username = page.Email
	}
	page.Plain = plainOnThisComputer(mailServerOf(*f))
	s.render(w, r, http.StatusOK, pageConnect, page)
}

// handleSave tests the settings with the password and saves the account when
// the test passes. On success it redirects to the accounts page. Otherwise it
// shows the form again, with a fixed text and without the password.
//
// Before any test it checks, in this order: each certificate that the user
// chose to trust must come with a valid offer of this session for the posted
// protocol, host and port (its fingerprint becomes the pin); a server that is
// not on this computer also needs the "I understand the risk" box; a plain
// connection needs the "I understand that this connection is not encrypted"
// box; and the password must not be empty.
func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	svc := s.svc.Load()
	sess, ok := sessionFrom(r.Context())
	if svc == nil || !ok {
		s.fail(w, r, codeNoService, errors.New("web: no service or no session"))
		return
	}

	page := connectPage{Step: stepSettings, Email: strings.TrimSpace(r.PostForm.Get("email"))}
	page.Server = readFields(r)
	server := mailServerOf(page.Server)
	understand := r.PostForm.Get("understand") == "1"
	page.UnderstandChecked = understand

	page.Plain = plainOnThisComputer(server)
	page.PlainChecked = page.Plain && r.PostForm.Get("plain_understand") == "1"

	// The certificates that the user chose to trust become pins, but only from
	// an offer that this server made for these settings.
	needUnderstand := false
	for _, p := range protocols {
		if r.PostForm.Get("trust_"+p.field) != "1" {
			continue
		}
		token := r.PostForm.Get(p.field + "_offer")
		offer, ok := s.openOffer(sess, p, server, token)
		if !ok {
			page.Error = msgBadTrust
			continue
		}
		host := p.host(&server)
		*p.pin(&server) = offer.Fingerprint
		page.Certs = append(page.Certs, certPrompt{
			Name: p.name, Field: p.field, Host: host, Fingerprint: groupFingerprint(offer.Fingerprint),
			Problem: msgTrusted, CanTrust: true, Offer: token, Checked: true,
		})
		if !account.IsLoopbackHost(host) {
			needUnderstand = true
		}
	}
	page.Understand = needUnderstand
	switch {
	case page.Error != "":
		s.render(w, r, http.StatusUnprocessableEntity, pageConnect, page)
		return
	case needUnderstand && !understand:
		page.Error = msgUnderstand
		s.render(w, r, http.StatusUnprocessableEntity, pageConnect, page)
		return
	case page.Plain && !page.PlainChecked:
		page.Error = msgPlain
		s.render(w, r, http.StatusUnprocessableEntity, pageConnect, page)
		return
	}
	password := r.PostForm.Get("password")
	if password == "" {
		page.Error = msgNoPassword
		s.render(w, r, http.StatusUnprocessableEntity, pageConnect, page)
		return
	}

	ctx := service.WithPrincipal(r.Context(), service.Principal{Tenant: sess.tenant, Client: webClient})
	_, err := svc.CreateIMAPAccount(ctx, page.Email, server, secrets.Secret(password))
	if err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	var certErr *service.CertificateError
	if errors.As(err, &certErr) {
		s.log.Info("connect refused", "code", codeConnectRefused, "reason", "certificate")
		page.Error = msgCertificate
		page.Certs, page.Understand, err = s.certPrompts(sess, page.Certs, certErr, server)
		if err != nil {
			s.fail(w, r, codeConnectFailed, err)
			return
		}
		// The risk box was ticked for the certificates shown before. A new
		// certificate needs a new decision, so the box starts empty.
		page.UnderstandChecked = false
		s.render(w, r, http.StatusOK, pageConnect, page)
		return
	}
	text, known := saveErrorText(err)
	if !known {
		s.fail(w, r, codeConnectFailed, err)
		return
	}
	s.log.Info("connect refused", "code", codeConnectRefused, "error_types", errtype.Chain(err))
	page.Error = text
	s.render(w, r, http.StatusUnprocessableEntity, pageConnect, page)
}

// certPrompts returns the certificates to show after a test that did not
// accept a certificate, and whether the "I understand the risk" box is needed.
// A certificate of the test replaces the one that the user chose to trust for
// the same server (its pin did not match). A trust of the user for a server
// whose certificate the test accepted stays.
func (s *Server) certPrompts(sess session, trusted []certPrompt, certErr *service.CertificateError, server account.MailServer) ([]certPrompt, bool, error) {
	var prompts []certPrompt
	understand := false
	for _, p := range protocols {
		prompt, found := certPrompt{}, false
		if c := p.cert(certErr); c != nil {
			var err error
			if prompt, err = s.promptFor(sess, p, server, c); err != nil {
				return nil, false, err
			}
			found = true
		} else {
			for _, t := range trusted {
				if t.Field == p.field {
					prompt, found = t, true
				}
			}
		}
		if !found {
			continue
		}
		if prompt.CanTrust && !account.IsLoopbackHost(prompt.Host) {
			understand = true
		}
		prompts = append(prompts, prompt)
	}
	return prompts, understand, nil
}

// promptFor describes a certificate that the test did not accept. It offers to
// trust it in one case only: no pin was set, the only problem is that no known
// authority signed it, and the fingerprint is a SHA-256. The name and the time
// of the certificate are checked before the authority, so then they are right.
// A host name that does not match, a certificate out of its time, and a pin that
// did not match are never offered.
//
// The offer is a signed token for this session, protocol, host, port and
// fingerprint (see certOffer). The save accepts the trust only with it.
func (s *Server) promptFor(sess session, p protocol, server account.MailServer, c *imapsmtp.CertificateUnknownError) (certPrompt, error) {
	host := p.host(&server)
	prompt := certPrompt{Name: p.name, Field: p.field, Host: host}
	if account.IsFingerprint(c.FingerprintSHA256) {
		prompt.Fingerprint = groupFingerprint(c.FingerprintSHA256)
	}
	var (
		unknownAuthority x509.UnknownAuthorityError
		hostname         x509.HostnameError
		invalid          x509.CertificateInvalidError
	)
	switch {
	case c.Pinned:
		prompt.Problem = msgCertPinChanged
	case errors.As(c.Reason, &hostname):
		prompt.Problem = msgCertWrongHost
	case errors.As(c.Reason, &invalid) && invalid.Reason == x509.Expired:
		prompt.Problem = msgCertExpired
	case errors.As(c.Reason, &unknownAuthority) && prompt.Fingerprint != "":
		token, err := s.d.Signer.Seal(purposeCertOffer, certOffer{
			Session: sess.hash, Protocol: p.field, Host: host, Port: p.port(&server), Fingerprint: c.FingerprintSHA256,
		}, certOfferTTL)
		if err != nil {
			return certPrompt{}, fmt.Errorf("web: seal a certificate offer: %w", err)
		}
		prompt.Problem = msgCertUnknown
		prompt.CanTrust = true
		prompt.Offer = token
	default:
		prompt.Problem = msgCertInvalid
	}
	return prompt, nil
}

// openOffer checks an offer token that came back with a ticked trust box. It
// accepts the offer only when its signature is good, it has not expired, it was
// made for this session, and its protocol, host and port are the ones posted
// now (cleaned as the form is). So a pin that the page never offered, an offer
// of another session, and an offer that the user moved to another host or port
// are refused.
func (s *Server) openOffer(sess session, p protocol, server account.MailServer, token string) (certOffer, bool) {
	var o certOffer
	if token == "" || s.d.Signer.Open(purposeCertOffer, token, &o) != nil {
		return certOffer{}, false
	}
	ok := subtle.ConstantTimeCompare(o.Session, sess.hash) == 1 &&
		o.Protocol == p.field &&
		o.Host == p.host(&server) &&
		o.Port == p.port(&server) &&
		account.IsFingerprint(o.Fingerprint)
	if !ok {
		return certOffer{}, false
	}
	return o, true
}

// plainOnThisComputer reports whether a server on this computer is set to a
// plain (unencrypted) connection. Such a connection needs the explicit consent
// of the user. A plain connection to another host is refused by the service,
// so no consent can allow it.
func plainOnThisComputer(server account.MailServer) bool {
	for _, p := range protocols {
		if p.security(&server) == account.SecurityPlain && account.IsLoopbackHost(p.host(&server)) {
			return true
		}
	}
	return false
}

// saveErrorText returns the fixed text for an error of CreateIMAPAccount. known
// is false for an error that is not about the input or the test, such as a
// failure of the database.
func saveErrorText(err error) (text string, known bool) {
	var testErr *service.ConnectionTestError
	switch {
	case errors.Is(err, service.ErrInvalidAddress):
		return msgAddress, true
	case errors.Is(err, service.ErrNoPassword):
		return msgNoPassword, true
	case errors.Is(err, service.ErrInvalidSettings):
		return msgSettings, true
	case errors.Is(err, service.ErrAccountExists):
		return msgExists, true
	case errors.As(err, &testErr):
		return testFailureText(testErr.Err), true
	}
	return "", false
}

// testFailureText returns the fixed text for a failed connection test, by the
// type of the error. It never uses the text of the error.
func testFailureText(err error) string {
	var (
		dnsErr    *net.DNSError
		netErr    net.Error
		recordErr tls.RecordHeaderError
		alertErr  tls.AlertError
		opErr     *net.OpError
	)
	switch {
	case errors.Is(err, imapsmtp.ErrAuthFailed):
		return msgAuthFailed
	case errors.As(err, &dnsErr):
		return msgHostNotFound
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return msgTimeout
	case errors.As(err, &recordErr), errors.As(err, &alertErr):
		return msgTLSFailed
	case errors.As(err, &opErr):
		return msgCannotConnect
	default:
		return msgTestFailed
	}
}

// readFields reads the settings of the posted form. Outer spaces go, and host
// names become lower case, so the form shows them as they are tested.
func readFields(r *http.Request) serverFields {
	get := func(name string) string { return strings.TrimSpace(r.PostForm.Get(name)) }
	return serverFields{
		IMAPHost:     strings.ToLower(get("imap_host")),
		IMAPPort:     get("imap_port"),
		IMAPSecurity: get("imap_security"),
		SMTPHost:     strings.ToLower(get("smtp_host")),
		SMTPPort:     get("smtp_port"),
		SMTPSecurity: get("smtp_security"),
		Username:     get("username"),
	}
}

// fieldsOf returns the form values of settings.
func fieldsOf(m account.MailServer) serverFields {
	return serverFields{
		IMAPHost:     m.IMAPHost,
		IMAPPort:     strconv.Itoa(m.IMAPPort),
		IMAPSecurity: string(m.IMAPSecurity),
		SMTPHost:     m.SMTPHost,
		SMTPPort:     strconv.Itoa(m.SMTPPort),
		SMTPSecurity: string(m.SMTPSecurity),
		Username:     m.Username,
	}
}

// mailServerOf returns the settings of the form values. A port that is not a
// number becomes 0, which the service refuses.
func mailServerOf(f serverFields) account.MailServer {
	port := func(text string) int {
		n, err := strconv.Atoi(text)
		if err != nil {
			return 0
		}
		return n
	}
	return account.MailServer{
		IMAPHost:     f.IMAPHost,
		IMAPPort:     port(f.IMAPPort),
		IMAPSecurity: account.Security(f.IMAPSecurity),
		SMTPHost:     f.SMTPHost,
		SMTPPort:     port(f.SMTPPort),
		SMTPSecurity: account.Security(f.SMTPSecurity),
		Username:     f.Username,
	}
}

// groupFingerprint writes a fingerprint in upper-case pairs with a colon
// between them, such as "AB:CD:EF", as browsers show one.
func groupFingerprint(hexText string) string {
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
