// Package account holds the plain types for tenants, accounts, areas,
// permission levels, server settings and limits.
//
// The types here have no storage, network or encryption code. The store, the
// policy engine, the services and the web UI all use them.
package account

import (
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"time"
)

// TenantID identifies a tenant. A tenant owns accounts.
//
// Ids are used in encryption associated data in the form
// "tenant:<id>|account:<id>". An id must never contain "|".
type TenantID string

// AccountID identifies one account. See TenantID for the rule about "|".
type AccountID string

// NewAccountID returns a new random account id: 128 random bits as lowercase
// base32 without padding. The result is 26 characters from "a-z" and "2-7", so
// it never contains "|".
func NewAccountID() AccountID {
	// rand.Text uses the standard base32 alphabet with 128 random bits and no
	// padding. It never fails.
	return AccountID(strings.ToLower(rand.Text()))
}

// Area is a part of an account that can have its own permission level.
type Area string

// The areas of an account.
const (
	AreaMail     Area = "mail"
	AreaCalendar Area = "calendar"
	AreaContacts Area = "contacts"
)

// Level is a permission level. Higher levels include the lower ones.
//
// The numbers are stored and compared, so they must not change. Only the mail
// levels are defined so far. A level of 0 means no access.
type Level int

// The mail levels.
const (
	MailOff Level = iota
	MailRead
	MailOrganize
	MailDraft
	MailSend
	MailDelete
)

// MailName returns the display name of a mail level. It returns "Unknown" for a
// value outside 0 to 5.
func (l Level) MailName() string {
	switch l {
	case MailOff:
		return "Off"
	case MailRead:
		return "Read"
	case MailOrganize:
		return "Organize"
	case MailDraft:
		return "Draft"
	case MailSend:
		return "Send"
	case MailDelete:
		return "Delete"
	default:
		return "Unknown"
	}
}

// Provider is the kind of service behind an account.
type Provider string

// ProviderIMAP is a generic IMAP and SMTP mail server.
const ProviderIMAP Provider = "imap"

// Security is how the client protects a connection to a mail server.
type Security string

// The connection security modes.
const (
	// SecurityTLS means TLS from the first byte (implicit TLS).
	SecurityTLS Security = "tls"
	// SecuritySTARTTLS means a plain start, then an upgrade to TLS.
	SecuritySTARTTLS Security = "starttls"
	// SecurityPlain means no encryption. It is allowed only for a loopback host
	// on a local install.
	SecurityPlain Security = "plain"
)

// MailServer holds the IMAP and SMTP settings of an account.
type MailServer struct {
	IMAPHost     string
	IMAPPort     int
	IMAPSecurity Security
	SMTPHost     string
	SMTPPort     int
	SMTPSecurity Security
	Username     string

	// PinnedIMAPCert and PinnedSMTPCert are the hex SHA-256 of the server's
	// leaf certificate. They are empty when the certificate is not pinned.
	PinnedIMAPCert string
	PinnedSMTPCert string

	// SaveSentCopy tells the mail service to save a copy of each sent message
	// in the Sent folder after it sends.
	SaveSentCopy bool
}

// Validate checks the server settings.
//
// It rejects a port outside 1 to 65535, an empty host and a security value that
// is not one of the three constants. It rejects SecurityPlain to a host that is
// not loopback. It also rejects SecurityPlain everywhere when local is false (a
// hosted install). The checks apply to both IMAP and SMTP.
//
// Validate does not resolve DNS. A host is loopback if it is "localhost" or an
// IP address that net.IP.IsLoopback accepts.
func (m MailServer) Validate(local bool) error {
	if err := validateEndpoint("imap", m.IMAPHost, m.IMAPPort, m.IMAPSecurity, local); err != nil {
		return err
	}
	return validateEndpoint("smtp", m.SMTPHost, m.SMTPPort, m.SMTPSecurity, local)
}

// validateEndpoint checks one host, port and security setting. The name is
// "imap" or "smtp" and is used in error messages. Error messages never contain
// the host or any other user data.
func validateEndpoint(name, host string, port int, security Security, local bool) error {
	if host == "" {
		return fmt.Errorf("account: %s host is empty", name)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("account: %s port must be between 1 and 65535", name)
	}
	switch security {
	case SecurityTLS, SecuritySTARTTLS:
		return nil
	case SecurityPlain:
		if !local {
			return fmt.Errorf("account: %s plain connections are not allowed on a hosted server", name)
		}
		if !isLoopbackHost(host) {
			return fmt.Errorf("account: %s plain connections are allowed only to a loopback host", name)
		}
		return nil
	default:
		return fmt.Errorf("account: %s security must be tls, starttls or plain", name)
	}
}

// isLoopbackHost reports whether host is "localhost" or a loopback IP address.
// It looks only at the text of host and never resolves a name.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	// ParseIP returns nil for a name that is not an IP address. A nil IP is
	// never loopback.
	return net.ParseIP(host).IsLoopback()
}

// maxUndoWindow is the longest allowed undo window.
const maxUndoWindow = 300 * time.Second

// Limits are the per-account limits for sending and deleting.
type Limits struct {
	SendsPerHour         int
	SendsPerDay          int
	RecipientsPerMessage int
	TrashPerDay          int

	// UndoWindowAuto is how long a send waits before delivery when the account
	// does not ask for confirmation.
	UndoWindowAuto time.Duration
	// UndoWindowConfirmed is the same wait after the user confirmed the send.
	UndoWindowConfirmed time.Duration

	// NewRecipientCheck asks for confirmation when a message goes to an address
	// that the account has never sent to, even when ConfirmSends is off.
	NewRecipientCheck bool
}

// DefaultLimits returns the limits of a new account.
func DefaultLimits() Limits {
	return Limits{
		SendsPerHour:         20,
		SendsPerDay:          200,
		RecipientsPerMessage: 50,
		TrashPerDay:          500,
		UndoWindowAuto:       30 * time.Second,
		UndoWindowConfirmed:  0,
		NewRecipientCheck:    false,
	}
}

// Validate checks the limits. Counts must not be negative. Each undo window
// must be between 0 and 300 seconds.
func (l Limits) Validate() error {
	counts := []struct {
		name  string
		value int
	}{
		{"sends per hour", l.SendsPerHour},
		{"sends per day", l.SendsPerDay},
		{"recipients per message", l.RecipientsPerMessage},
		{"trash per day", l.TrashPerDay},
	}
	for _, c := range counts {
		if c.value < 0 {
			return fmt.Errorf("account: %s must not be negative", c.name)
		}
	}
	windows := []struct {
		name  string
		value time.Duration
	}{
		{"undo window (automatic)", l.UndoWindowAuto},
		{"undo window (confirmed)", l.UndoWindowConfirmed},
	}
	for _, w := range windows {
		if w.value < 0 || w.value > maxUndoWindow {
			return fmt.Errorf("account: %s must be between 0 and 300 seconds", w.name)
		}
	}
	return nil
}

// Status is the health of an account.
type Status string

// The account statuses.
const (
	StatusOK          Status = "ok"
	StatusNeedsReauth Status = "needs_reauth"
)

// Account is one connected mailbox (and later calendar and contacts) of a
// tenant.
type Account struct {
	ID          AccountID
	TenantID    TenantID
	Provider    Provider
	Address     string
	DisplayName string

	// Mail holds the server settings. It is nil for a provider that has none.
	Mail *MailServer

	// Levels is the permission level for each area. A missing area means 0.
	Levels map[Area]Level

	// ConfirmSends asks the user before a send. ConfirmDeletes asks the user
	// before a delete. Full autonomy means both are off.
	ConfirmSends   bool
	ConfirmDeletes bool

	// AutonomyAcceptedAt is the time the user accepted the risk of full
	// autonomy (both confirmations off). It is nil until then.
	AutonomyAcceptedAt *time.Time

	Limits    Limits
	Status    Status
	CreatedAt time.Time
}

// NewIMAPAccount returns a new IMAP account with the defaults of a new
// account: mail level Send, both confirmations on and the default limits. It
// makes the account id with NewAccountID. It keeps a copy of server.
func NewIMAPAccount(tenant TenantID, address string, server MailServer, now time.Time) *Account {
	return &Account{
		ID:             NewAccountID(),
		TenantID:       tenant,
		Provider:       ProviderIMAP,
		Address:        address,
		Mail:           &server,
		Levels:         map[Area]Level{AreaMail: MailSend},
		ConfirmSends:   true,
		ConfirmDeletes: true,
		Limits:         DefaultLimits(),
		Status:         StatusOK,
		CreatedAt:      now,
	}
}

// Level returns the permission level for an area. It returns 0 when the area is
// missing.
func (a *Account) Level(area Area) Level {
	return a.Levels[area]
}
