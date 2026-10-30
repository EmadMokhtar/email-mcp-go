// Package imapsmtp is the IMAP and SMTP backend of email-mcp.
//
// The package connects to an IMAP server with a secure dialer (TLS 1.2 or
// newer, STARTTLS that never falls back to plain text, and certificate
// pinning) and keeps a small pool of logged-in connections. Backend, made by
// New, implements mail.Backend on top of the pool. It lists folders, searches,
// reports new messages, and reads messages and attachments. The folder aliases
// (@trash and the others) come from the SPECIAL-USE attributes of the folders.
//
// It saves and discards drafts in the IMAP account. It sends mail with SMTP
// over a connection that has the same rules and the same certificate check as
// IMAP (see Backend.Send), and then saves a copy in the Sent folder if the
// account says so. Probe tests the settings of an account before it is saved.
package imapsmtp

import (
	"crypto/x509"
	"log/slog"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
)

// The defaults that withDefaults applies.
const (
	defaultDialTimeout    = 15 * time.Second
	defaultCommandTimeout = 60 * time.Second
	defaultIdleTimeout    = 5 * time.Minute
	defaultPoolSize       = 2
)

// Config holds everything the backend needs to talk to one account's servers.
type Config struct {
	// Server holds the host, port, security mode and pinned certificates.
	Server account.MailServer
	// Password is the account password. It is sent only in the IMAP LOGIN
	// command and the SMTP AUTH command.
	Password secrets.Secret
	// Local is true for a local install. Only then is a plain (unencrypted)
	// connection allowed, and only to a loopback host.
	Local bool

	// DialTimeout limits one attempt to connect, start TLS, read the greeting
	// and log in.
	DialTimeout time.Duration
	// CommandTimeout limits one pooled operation. When it ends, the
	// connection is closed.
	CommandTimeout time.Duration
	// IdleTimeout is how long an unused pooled connection may stay open.
	IdleTimeout time.Duration
	// PoolSize is the largest number of IMAP connections for the account.
	PoolSize int

	// RootCAs replaces the system root certificates when it is set. It is
	// for tests only. It has no effect when the server certificate is pinned.
	RootCAs *x509.CertPool

	// TokenScope binds page tokens and cursors to one account. The backend
	// factory sets it to "<tenant>/<account>".
	TokenScope string

	// Logger receives the few events that the backend logs. It is optional:
	// nil means that nothing is logged. The backend logs error codes only,
	// never a subject, an address, a name or text that a server sent.
	Logger *slog.Logger
}

// withDefaults replaces every zero or negative limit with its default.
func (c *Config) withDefaults() {
	if c.DialTimeout <= 0 {
		c.DialTimeout = defaultDialTimeout
	}
	if c.CommandTimeout <= 0 {
		c.CommandTimeout = defaultCommandTimeout
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = defaultIdleTimeout
	}
	if c.PoolSize <= 0 {
		c.PoolSize = defaultPoolSize
	}
}
