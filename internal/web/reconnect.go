package web

// This file holds the reconnect page of an account (Service.ReconnectIMAPAccount).
// The accounts page and the settings page of every account link to it.
// It gives a connected account a new password, or accepts a new certificate of
// one of its servers, and keeps the account: its id, its server settings, its
// levels, its queued messages and its activity. Only the user does it, here or
// with "email-mcp account reconnect <id>". No MCP tool can.
//
//  1. GET /accounts/{id}/reconnect shows the password field.
//  2. POST /accounts/{id}/reconnect tests the stored settings with the posted
//     password (empty keeps the stored one), and saves when the test passes.
//     When a server shows a certificate that the account does not trust, the
//     page shows it as a second step: the host and the port of the stored
//     settings, the old and the new fingerprint, and what the user may do. The
//     user posts the form again with the box ticked.
//
// The decision about a certificate is a signed offer token, the same one as the
// connect flow uses (purpose "cert_offer", 10 minutes), bound to the session,
// the account, the protocol, the host, the port, the old pin and the new
// fingerprint. To replace or remove a pin, the box's label states the warning
// that a changed certificate can mean that someone is in the middle of the
// connection. A first pin of a server that is not on this computer also needs
// the "I understand the risk" box, as in the connect flow.
//
// The password is never kept between requests, never put in a page, a log, a
// URL or a redirect. A message about a failed test is a fixed text chosen by
// the type of the error, never the text of the server.

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"

	"github.com/EmadMokhtar/email-mcp-go/internal/connect"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/errtype"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// The name of the page of this file.
const pageReconnect = "reconnect"

// Fixed codes for the log.
const (
	codeReconnectFailed  = "reconnect_failed"
	codeReconnectRefused = "reconnect_refused"
)

// The fixed texts of the reconnect page.
const (
	// msgReplaceLabel is the label of the box that accepts a certificate in
	// place of a pinned one. It states the warning.
	msgReplaceLabel = "Accept the new certificate of this server instead of the one I trusted. I understand that a " +
		"changed certificate can mean that someone is in the middle of the connection and can read my password and my mail."
	// msgTrustLabel is the label of the box that pins the certificate of a
	// server that had no pin.
	msgTrustLabel = "Trust this certificate"
	// msgAccepted is the problem text of a certificate that the user accepted
	// in this form.
	msgAccepted = "You chose to accept this certificate."
	// msgNothingSaved follows a failed test.
	msgNothingSaved = "Nothing was saved."
)

// reconnectPage is the data of the reconnect page. It has no field for the
// password, so the page can never show it.
type reconnectPage struct {
	ID      string
	Address string
	Status  string
	Error   string
	// Command is the command for a computer without a browser.
	Command string
	// Certs are the certificates to show: the ones that the test did not
	// accept, and the ones that the user chose to accept in this form.
	Certs []reconnectPrompt
	// Understand shows the "I understand the risk" box. A first pin of a
	// server that is not on this computer needs it.
	Understand        bool
	UnderstandChecked bool
}

// reconnectPrompt is the certificate of one server on the reconnect page.
type reconnectPrompt struct {
	// Name is "IMAP" or "SMTP".
	Name string
	// Field is "imap" or "smtp": the form names are "trust_<field>" and
	// "<field>_offer".
	Field string
	// Host and Port are the stored settings, not values of the server.
	Host string
	Port int
	// OldFingerprint is the certificate that the user trusted before, in
	// groups, or empty. Fingerprint is the certificate of now, in groups.
	OldFingerprint string
	Fingerprint    string
	// Problem is a fixed text about the certificate.
	Problem string
	// CanChange shows the box, with Offer (the signed offer token) in a hidden
	// field and Label as its label.
	CanChange bool
	Label     string
	Offer     string
	Checked   bool
	// Warning is the warning about a changed certificate. It is set when the
	// change replaces or removes a pin.
	Warning string
}

// reconnectOf returns the page of an account, without certificates.
func reconnectOf(a *account.Account) reconnectPage {
	return reconnectPage{
		ID:      string(a.ID),
		Address: a.Address,
		Status:  statusLabel(a.Status),
		Command: "email-mcp account reconnect " + string(a.ID),
	}
}

// handleReconnect shows the reconnect page of an account of the tenant of the
// session. It changes nothing.
func (s *Server) handleReconnect(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r.Context())
	if !ok {
		s.fail(w, r, codeReconnectFailed, errNoSession)
		return
	}
	a, found := s.loadAccount(w, r, sess)
	if !found {
		return
	}
	s.render(w, r, http.StatusOK, pageReconnect, reconnectOf(a))
}

// handleSaveReconnect tests the stored settings with the posted password and
// saves the reconnect when the test passes. On success it redirects to the
// accounts page. Otherwise it shows the page again, with a fixed text and
// without the password.
//
// Before any test, each certificate that the user chose to accept must come
// with a valid offer of this session for this account and for the stored
// protocol, host, port and pin (its fingerprint becomes the decision), and a
// first pin of a server that is not on this computer also needs the "I
// understand the risk" box.
func (s *Server) handleSaveReconnect(w http.ResponseWriter, r *http.Request) {
	svc, sess, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeReconnectFailed, errNoService)
		return
	}
	a, found := s.loadAccount(w, r, sess)
	if !found {
		return
	}
	if a.Mail == nil {
		s.fail(w, r, codeReconnectFailed, errors.New("web: the account has no mail server settings"))
		return
	}
	page := reconnectOf(a)
	understand := r.PostForm.Get("understand") == "1"
	page.UnderstandChecked = understand

	var req service.Reconnect
	needUnderstand := false
	for _, p := range protocols {
		if r.PostForm.Get("trust_"+p.field) != "1" {
			continue
		}
		token := r.PostForm.Get(p.field + "_offer")
		offer, ok := s.openReconnectOffer(sess, p, a, token)
		if !ok {
			page.Error = msgBadTrust
			continue
		}
		*p.decision(&req) = offer.Fingerprint
		prompt := reconnectPrompt{
			Name: p.name, Field: p.field, Host: offer.Host, Port: offer.Port,
			Fingerprint: connect.GroupFingerprint(offer.Fingerprint), Problem: msgAccepted,
			CanChange: true, Label: msgTrustLabel, Offer: token, Checked: true,
		}
		if offer.Old != "" {
			prompt.OldFingerprint = connect.GroupFingerprint(offer.Old)
			prompt.Label, prompt.Warning = msgReplaceLabel, connect.TextCertChangedWarning
		} else if !account.IsLoopbackHost(offer.Host) {
			needUnderstand = true
		}
		page.Certs = append(page.Certs, prompt)
	}
	page.Understand = needUnderstand
	switch {
	case page.Error != "":
		s.render(w, r, http.StatusUnprocessableEntity, pageReconnect, page)
		return
	case needUnderstand && !understand:
		page.Error = msgUnderstand
		s.render(w, r, http.StatusUnprocessableEntity, pageReconnect, page)
		return
	}
	req.Password = secrets.Secret(r.PostForm.Get("password"))

	_, err := svc.ReconnectIMAPAccount(ctx, a.ID, req)
	if err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	var certErr *service.ReconnectCertificateError
	if errors.As(err, &certErr) {
		s.log.Info("reconnect refused", "code", codeReconnectRefused, "reason", "certificate")
		page.Error = connect.TextCertificate
		page.Certs, page.Understand, err = s.reconnectPrompts(sess, a, page.Certs, certErr)
		if err != nil {
			s.fail(w, r, codeReconnectFailed, err)
			return
		}
		// The risk box was ticked for the certificates shown before. A new
		// certificate needs a new decision, so the box starts empty.
		page.UnderstandChecked = false
		s.render(w, r, http.StatusOK, pageReconnect, page)
		return
	}
	if errors.Is(err, service.ErrAccountNotFound) {
		s.message(w, r, http.StatusNotFound, titleAccountAbsent, msgNoAccount)
		return
	}
	text, known := connect.ErrorText(err)
	if !known {
		s.fail(w, r, codeReconnectFailed, err)
		return
	}
	s.log.Info("reconnect refused", "code", codeReconnectRefused, "error_types", errtype.Chain(err))
	page.Error = text + " " + msgNothingSaved
	s.render(w, r, http.StatusUnprocessableEntity, pageReconnect, page)
}

// reconnectPrompts returns the certificates to show after a test that did not
// accept a certificate, and whether the "I understand the risk" box is needed.
// A certificate of the test replaces the one that the user chose for the same
// server. A choice of the user for a server whose certificate the test
// accepted stays.
func (s *Server) reconnectPrompts(sess session, a *account.Account, chosen []reconnectPrompt, certErr *service.ReconnectCertificateError) ([]reconnectPrompt, bool, error) {
	var prompts []reconnectPrompt
	understand := false
	for _, p := range protocols {
		prompt, found := reconnectPrompt{}, false
		if rc := p.reconnect(certErr); rc != nil {
			var err error
			if prompt, err = s.reconnectPromptFor(sess, a, p, rc); err != nil {
				return nil, false, err
			}
			found = true
		} else {
			for _, c := range chosen {
				if c.Field == p.field {
					prompt, found = c, true
				}
			}
		}
		if !found {
			continue
		}
		if prompt.CanChange && prompt.Warning == "" && !account.IsLoopbackHost(prompt.Host) {
			understand = true
		}
		prompts = append(prompts, prompt)
	}
	return prompts, understand, nil
}

// reconnectPromptFor describes a certificate that the test did not accept. It
// offers a change only when connect.CheckReconnect allows it. The offer is a
// signed token for this session, account, protocol, host, port, old pin and
// fingerprint (see certOffer).
func (s *Server) reconnectPromptFor(sess session, a *account.Account, p protocol, rc *service.ReconnectCertificate) (reconnectPrompt, error) {
	check := connect.CheckReconnect(rc)
	prompt := reconnectPrompt{Name: p.name, Field: p.field, Host: rc.Host, Port: rc.Port, Problem: check.Problem}
	if check.Fingerprint != "" {
		prompt.Fingerprint = connect.GroupFingerprint(check.Fingerprint)
	}
	if check.OldFingerprint != "" {
		prompt.OldFingerprint = connect.GroupFingerprint(check.OldFingerprint)
	}
	if !check.CanChange() {
		return prompt, nil
	}
	token, err := s.d.Signer.Seal(purposeCertOffer, certOffer{
		Session: sess.hash, Protocol: p.field, Host: rc.Host, Port: rc.Port, Fingerprint: check.Fingerprint,
		Account: string(a.ID), Old: check.OldFingerprint,
	}, certOfferTTL)
	if err != nil {
		return reconnectPrompt{}, fmt.Errorf("web: seal a certificate offer: %w", err)
	}
	prompt.CanChange, prompt.Offer, prompt.Label = true, token, msgTrustLabel
	if check.ReplacesPin() {
		prompt.Label, prompt.Warning = msgReplaceLabel, connect.TextCertChangedWarning
	}
	return prompt, nil
}

// openReconnectOffer checks an offer token that came back with a ticked box on
// the reconnect page. It accepts the offer only when its signature is good, it
// has not expired, it was made for this session and this account, and its
// protocol, host, port and old pin are the stored ones of the account now. So
// an offer of the connect flow, an offer of another account, and an offer made
// before the stored pin changed are refused.
func (s *Server) openReconnectOffer(sess session, p protocol, a *account.Account, token string) (certOffer, bool) {
	var o certOffer
	if token == "" || s.d.Signer.Open(purposeCertOffer, token, &o) != nil {
		return certOffer{}, false
	}
	stored := *a.Mail
	ok := subtle.ConstantTimeCompare(o.Session, sess.hash) == 1 &&
		o.Account == string(a.ID) &&
		o.Protocol == p.field &&
		o.Host == p.host(&stored) &&
		o.Port == p.port(&stored) &&
		o.Old == *p.pin(&stored) &&
		account.IsFingerprint(o.Fingerprint)
	if !ok {
		return certOffer{}, false
	}
	return o, true
}
