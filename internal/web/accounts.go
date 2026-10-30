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
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/connect"
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

// userCall returns the service, the session, and a context whose principal is
// the user of the session: the tenant of the session, no scope limit (nil
// Scopes, as on a local install) and the client "web". Every page that calls
// the service uses it. ok is false when the server has no service or the
// request has no session. The caller then answers with fail.
func (s *Server) userCall(r *http.Request) (svc *service.Service, sess session, ctx context.Context, ok bool) {
	svc = s.svc.Load()
	sess, hasSession := sessionFrom(r.Context())
	if svc == nil || !hasSession {
		return nil, session{}, nil, false
	}
	ctx = service.WithPrincipal(r.Context(), service.Principal{Tenant: sess.tenant, Client: webClient})
	return svc, sess, ctx, true
}

// errNoService means that the server has no service yet (see Attach), or that
// the request has no session.
var errNoService = errors.New("web: no service or no session")

// purposeCertOffer is the signing purpose of an offer to trust a certificate.
const purposeCertOffer = "cert_offer"

// certOfferTTL is how long an offer to trust a certificate can be used.
const certOfferTTL = 10 * time.Minute

// certOffer is what an offer token holds: the server offered this session to
// trust the certificate with this fingerprint for this protocol, host and
// port. The token is signed, so the browser cannot change it, and it is
// readable, so it holds nothing secret (the session hash is not the session
// id). The keys are short to keep the form small.
//
// An offer of the reconnect page (reconnect.go) also names the account and the
// pin that the user saw as the old one (empty when the server had none). The
// connect flow takes only offers without an account, and the reconnect page
// only offers for its account and its stored pin.
type certOffer struct {
	Session     []byte `json:"s"`
	Protocol    string `json:"p"`
	Host        string `json:"h"`
	Port        int    `json:"n"`
	Fingerprint string `json:"f"`
	Account     string `json:"a,omitempty"`
	Old         string `json:"o,omitempty"`
}

// The defaults of the settings form when the user types the settings.
const (
	defaultIMAPPort = "993"
	defaultSMTPPort = "465"
)

// The fixed texts of the connect pages that only the web interface uses. The
// texts that the command line tool shares are in the connect package.
const (
	msgUnderstand = "To trust the certificate of a server that is not on this computer, also tick “I understand the risk”."
	msgBadTrust   = "The choice to trust a certificate is not valid: the server settings changed after the offer, or the offer is older than 10 minutes. Test the connection again to see the certificate."
	msgPlain      = "You chose a connection without encryption. To use it, also tick “I understand that this connection is not encrypted”."
	msgTrusted    = "You chose to trust this certificate."
)

// accountsPage is the data of the accounts page. It holds no secret, no server
// setting and no user name.
type accountsPage struct {
	Accounts []accountRow
}

// accountRow is one account on the accounts page.
type accountRow struct {
	// ID names the account in the link to its settings page.
	ID        string
	Address   string
	Provider  string
	MailLevel string
	Status    string
	// NeedsReconnect shows the link to the reconnect page.
	NeedsReconnect bool
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
	name      string // "IMAP" or "SMTP"
	field     string // "imap" or "smtp"
	host      func(*account.MailServer) string
	port      func(*account.MailServer) int
	security  func(*account.MailServer) account.Security
	pin       func(*account.MailServer) *string
	cert      func(*service.CertificateError) *imapsmtp.CertificateUnknownError
	reconnect func(*service.ReconnectCertificateError) *service.ReconnectCertificate
	decision  func(*service.Reconnect) *string
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
		reconnect: func(e *service.ReconnectCertificateError) *service.ReconnectCertificate {
			return e.IMAP
		},
		decision: func(r *service.Reconnect) *string { return &r.PinnedIMAPCert },
	},
	{
		name: "SMTP", field: "smtp",
		host:     func(m *account.MailServer) string { return m.SMTPHost },
		port:     func(m *account.MailServer) int { return m.SMTPPort },
		security: func(m *account.MailServer) account.Security { return m.SMTPSecurity },
		pin:      func(m *account.MailServer) *string { return &m.PinnedSMTPCert },
		cert:     func(e *service.CertificateError) *imapsmtp.CertificateUnknownError { return e.SMTP },
		reconnect: func(e *service.ReconnectCertificateError) *service.ReconnectCertificate {
			return e.SMTP
		},
		decision: func(r *service.Reconnect) *string { return &r.PinnedSMTPCert },
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
			ID:        string(a.ID),
			Address:   a.Address,
			Provider:  providerLabel(a.Provider),
			MailLevel: a.Level(account.AreaMail).MailName(),
			Status:    statusLabel(a.Status),

			NeedsReconnect: a.Status == account.StatusNeedsReauth,
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
		page.Step, page.Error = stepEmail, connect.TextAddress
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
	page.Plain = connect.PlainOnThisComputer(mailServerOf(*f))
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
	svc, sess, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeNoService, errNoService)
		return
	}

	page := connectPage{Step: stepSettings, Email: strings.TrimSpace(r.PostForm.Get("email"))}
	page.Server = readFields(r)
	server := mailServerOf(page.Server)
	understand := r.PostForm.Get("understand") == "1"
	page.UnderstandChecked = understand

	page.Plain = connect.PlainOnThisComputer(server)
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
			Name: p.name, Field: p.field, Host: host, Fingerprint: connect.GroupFingerprint(offer.Fingerprint),
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
		page.Error = connect.TextNoPassword
		s.render(w, r, http.StatusUnprocessableEntity, pageConnect, page)
		return
	}

	_, err := svc.CreateIMAPAccount(ctx, page.Email, server, secrets.Secret(password))
	if err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	var certErr *service.CertificateError
	if errors.As(err, &certErr) {
		s.log.Info("connect refused", "code", codeConnectRefused, "reason", "certificate")
		page.Error = connect.TextCertificate
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
	text, known := connect.ErrorText(err)
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
// trust it only when connect.CheckCertificate allows it: no pin was set, the
// only problem is that no known authority signed it, and the fingerprint is a
// SHA-256. A host name that does not match, a certificate out of its time, and a
// pin that did not match are never offered.
//
// The offer is a signed token for this session, protocol, host, port and
// fingerprint (see certOffer). The save accepts the trust only with it.
func (s *Server) promptFor(sess session, p protocol, server account.MailServer, c *imapsmtp.CertificateUnknownError) (certPrompt, error) {
	host := p.host(&server)
	check := connect.CheckCertificate(c)
	prompt := certPrompt{Name: p.name, Field: p.field, Host: host, Problem: check.Problem}
	if check.Fingerprint != "" {
		prompt.Fingerprint = connect.GroupFingerprint(check.Fingerprint)
	}
	if check.CanTrust {
		token, err := s.d.Signer.Seal(purposeCertOffer, certOffer{
			Session: sess.hash, Protocol: p.field, Host: host, Port: p.port(&server), Fingerprint: check.Fingerprint,
		}, certOfferTTL)
		if err != nil {
			return certPrompt{}, fmt.Errorf("web: seal a certificate offer: %w", err)
		}
		prompt.CanTrust = true
		prompt.Offer = token
	}
	return prompt, nil
}

// openOffer checks an offer token that came back with a ticked trust box. It
// accepts the offer only when its signature is good, it has not expired, it was
// made for this session, and its protocol, host and port are the ones posted
// now (cleaned as the form is). So a pin that the page never offered, an offer
// of another session, and an offer that the user moved to another host or port
// are refused. An offer of the reconnect page (it names an account) is refused
// too.
func (s *Server) openOffer(sess session, p protocol, server account.MailServer, token string) (certOffer, bool) {
	var o certOffer
	if token == "" || s.d.Signer.Open(purposeCertOffer, token, &o) != nil {
		return certOffer{}, false
	}
	ok := subtle.ConstantTimeCompare(o.Session, sess.hash) == 1 &&
		o.Account == "" && o.Old == "" &&
		o.Protocol == p.field &&
		o.Host == p.host(&server) &&
		o.Port == p.port(&server) &&
		account.IsFingerprint(o.Fingerprint)
	if !ok {
		return certOffer{}, false
	}
	return o, true
}

// readFields reads the settings of the posted form. Outer spaces go, and host
// names become lower case (connect.CleanHost), so the form shows them as they
// are tested.
func readFields(r *http.Request) serverFields {
	get := func(name string) string { return strings.TrimSpace(r.PostForm.Get(name)) }
	return serverFields{
		IMAPHost:     connect.CleanHost(r.PostForm.Get("imap_host")),
		IMAPPort:     get("imap_port"),
		IMAPSecurity: get("imap_security"),
		SMTPHost:     connect.CleanHost(r.PostForm.Get("smtp_host")),
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
