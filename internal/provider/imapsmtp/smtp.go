package imapsmtp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

const (
	// smtpDataRate is the slowest speed, in bytes per second, that the time
	// limit of an SMTP conversation expects for the message data.
	smtpDataRate = 100 * 1024
	// smtpHelloName is the name that the client gives in EHLO. It is the
	// default of go-smtp.
	smtpHelloName = "localhost"
	// maxEnvelopeAddress is the longest address that Send accepts. RFC 5321
	// limits a path to 256 bytes, with the two angle brackets.
	maxEnvelopeAddress = 254
)

// ErrDeliveryUnknown means that Send cannot tell whether the message was
// delivered. The data was sent, and then the server did not confirm it: the
// connection ended, time ran out, or the final reply was garbled or was a code
// that is neither 250 nor a refusal (4xx or 5xx). The server may have taken the
// message, so the caller must not send it again without asking a person: a
// retry could send it twice. Check it with errors.Is.
var ErrDeliveryUnknown = errors.New("smtp: the server did not confirm the message, so it may have been delivered")

// RecipientRejectedError means the SMTP server refused one recipient, so the
// message was not sent to anyone. The error names the recipient by its place in
// RawMessage.Recipients. It never holds the address or the text of the server:
// servers repeat the address in that text, and logs must not hold addresses.
type RecipientRejectedError struct {
	// Index is the place of the recipient in RawMessage.Recipients, counting
	// from 0.
	Index int
	// Total is the number of recipients of the message.
	Total int
	// Code is the SMTP reply code of the server. A code that starts with 4 is a
	// temporary refusal, and one that starts with 5 is a permanent one.
	Code int
}

func (e *RecipientRejectedError) Error() string {
	return fmt.Sprintf("smtp: the server rejected recipient %d of %d (reply code %d), so the message was not sent to anyone",
		e.Index+1, e.Total, e.Code)
}

// Send sends a message with SMTP and, when SaveSentCopy is set, saves a copy in
// the Sent folder.
//
// The connection is protected as cfg.Server.SMTPSecurity says. With
// SecurityTLS, TLS starts at the first byte (usually port 465). With
// SecuritySTARTTLS, the server must offer STARTTLS and the upgrade must work,
// or Send fails before it sends the password. There is no fall back to plain
// text. Plain text is possible only with SecurityPlain, only on a local
// install, and only to a loopback address. The certificate is checked by the
// same code as for IMAP, against PinnedSMTPCert (see newSMTPTLSConfig).
//
// Send logs in with AUTH PLAIN, and then sends MAIL FROM, one RCPT TO for each
// recipient, and the data. It is all or nothing for the recipients. When the
// server refuses one recipient, Send resets the transaction, sends no data, and
// returns a *RecipientRejectedError.
//
// A message must never go out twice. Send returns nil as soon as the server has
// accepted the message. If saving the copy then fails, Send writes the error
// code to the log and still returns nil: an error would make the caller try
// again, and the message would be sent a second time.
//
// Send returns an error only when the message was not accepted, with one
// exception. After the data was sent, only a reply code from 400 to 599 is a
// refusal. When the connection fails (or ctx ends) before the server answers,
// or the answer is garbled or has another code than 250, nobody can tell
// whether the server took the message. The error then wraps ErrDeliveryUnknown.
// The error never holds the text of the server.
//
// A new SMTP connection is made for every call. The time limit of the whole
// conversation is CommandTimeout plus the time that the data needs at
// 100 KiB per second (see smtpDeadline).
func (b *Backend) Send(ctx context.Context, raw *mail.RawMessage) error {
	if err := checkOutgoing(raw); err != nil {
		return err
	}
	err := withSMTP(ctx, b.cfg, len(raw.Data), func(c *smtp.Client) error {
		return sendOn(c, raw)
	})
	if err != nil {
		return err
	}

	// The server has the message. Nothing below may return an error.
	if b.cfg.Server.SaveSentCopy {
		if err := b.saveSentCopy(ctx, raw.Data); err != nil {
			b.logger().WarnContext(ctx, "imapsmtp: the sent copy was not saved", "code", appendErrorCode(err))
		}
	}
	return nil
}

// checkOutgoing checks the parts of a message that the SMTP commands are made
// of. The mime package checks the addresses when it builds a message. This
// check is the last one before the network, and it keeps anything out of a
// command line that could end the line or add a parameter. Its errors never
// hold an address.
func checkOutgoing(raw *mail.RawMessage) error {
	switch {
	case raw == nil || len(raw.Data) == 0:
		return errors.New("smtp: there is no message to send")
	case len(raw.Recipients) == 0:
		return errors.New("smtp: the message has no recipients")
	case !validEnvelopeAddress(raw.From):
		return errors.New("smtp: the sender address is not valid")
	}
	for i, to := range raw.Recipients {
		if !validEnvelopeAddress(to) {
			return fmt.Errorf("smtp: recipient %d of %d is not a valid address", i+1, len(raw.Recipients))
		}
	}
	return nil
}

// validEnvelopeAddress reports whether addr can be written into an SMTP path,
// "MAIL FROM:<addr>". It is not empty, not too long, valid UTF-8, and it has no
// space, no control character (CR, LF and NUL included) and no angle bracket.
func validEnvelopeAddress(addr string) bool {
	if addr == "" || len(addr) > maxEnvelopeAddress || !utf8.ValidString(addr) {
		return false
	}
	return !strings.ContainsFunc(addr, func(r rune) bool {
		return r == '<' || r == '>' || unicode.IsSpace(r) || unicode.IsControl(r)
	})
}

// isASCII reports whether all the strings use ASCII characters only.
func isASCII(list ...string) bool {
	for _, s := range list {
		for i := 0; i < len(s); i++ {
			if s[i] > unicode.MaxASCII {
				return false
			}
		}
	}
	return true
}

// sendOn sends the message on a logged-in connection.
func sendOn(c *smtp.Client, raw *mail.RawMessage) error {
	opts := &smtp.MailOptions{
		Size: int64(len(raw.Data)),
		// An address with non-ASCII characters needs SMTPUTF8. go-smtp fails
		// before it sends the command when the server does not offer it.
		UTF8: !isASCII(append([]string{raw.From}, raw.Recipients...)...),
	}
	if err := c.Mail(raw.From, opts); err != nil {
		return smtpStepError("MAIL FROM", err)
	}
	for i, to := range raw.Recipients {
		err := c.Rcpt(to, nil)
		if err == nil {
			continue
		}
		var reply *smtp.SMTPError
		if errors.As(err, &reply) {
			// All or nothing: the recipients that the server took before must
			// not get the message. RSET ends the transaction. Its result does
			// not matter, because the connection ends now.
			_ = c.Reset()
			return &RecipientRejectedError{Index: i, Total: len(raw.Recipients), Code: reply.Code}
		}
		return smtpStepError("RCPT TO", err)
	}

	w, err := c.Data()
	if err != nil {
		var reply *smtp.SMTPError
		if errors.As(err, &reply) {
			_ = c.Reset()
		}
		return smtpStepError("DATA", err)
	}
	if _, err := w.Write(raw.Data); err != nil {
		// w is not closed on purpose. Closing it would end the message with the
		// final dot, and the server could take a message that is cut short.
		return smtpStepError("DATA", err)
	}
	if _, err := w.CloseWithResponse(); err != nil {
		// Only a reply code from 400 to 599 says for sure that the server did not
		// take the message. A code such as 251 is a success code that is not the
		// one that SMTP defines for the end of data, so the server may have
		// taken the message. The cause goes through smtpStepError, which leaves
		// out the line that the server sent: it can hold an address.
		var reply *smtp.SMTPError
		if errors.As(err, &reply) && reply.Code >= 400 && reply.Code <= 599 {
			return fmt.Errorf("smtp: the server did not accept the message (reply code %d)", reply.Code)
		}
		return fmt.Errorf("%w: %w", ErrDeliveryUnknown, smtpStepError("end of data", err))
	}
	return nil
}

// saveSentCopy appends the message to the Sent folder with the flag \Seen. The
// folder is the one with the \Sent attribute. A missing folder is an error
// that wraps mail.ErrNotFound.
//
// APPEND is not repeated after a dropped connection: the server may have
// stored the copy already.
func (b *Backend) saveSentCopy(ctx context.Context, data []byte) error {
	return b.pool.withNoRetry(ctx, func(c *imapclient.Client) error {
		folder, err := resolveOn(c, mail.AliasSent)
		if err != nil {
			return err
		}
		_, err = appendMessage(c, folder, data, []imap.Flag{imap.FlagSeen})
		return err
	})
}

// logSafeResponseCodes are the response codes of IMAP servers (RFC 5530 and
// RFC 7889) that appendErrorCode may log. The code comes from the server, so it
// is logged only when it is one that the standards define: any other word could
// be a name or an address.
var logSafeResponseCodes = map[imap.ResponseCode]bool{
	imap.ResponseCodeAlert:               true,
	imap.ResponseCodeAuthorizationFailed: true,
	imap.ResponseCodeCannot:              true,
	imap.ResponseCodeClientBug:           true,
	imap.ResponseCodeContactAdmin:        true,
	imap.ResponseCodeCorruption:          true,
	imap.ResponseCodeExpired:             true,
	imap.ResponseCodeInUse:               true,
	imap.ResponseCodeLimit:               true,
	imap.ResponseCodeNoPerm:              true,
	imap.ResponseCodeOverQuota:           true,
	imap.ResponseCodeServerBug:           true,
	imap.ResponseCodeTooBig:              true,
	imap.ResponseCodeTryCreate:           true,
	imap.ResponseCodeUnavailable:         true,
}

// appendErrorCode returns a short code for the failure of an APPEND. The code
// is for logs. It comes from a fixed list of words, and it never holds text
// that a server or a user wrote.
func appendErrorCode(err error) string {
	var serverErr *imap.Error
	switch {
	case errors.Is(err, mail.ErrNotFound):
		return "no_sent_folder"
	case errors.Is(err, errPoolClosed):
		return "closed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &serverErr):
		if logSafeResponseCodes[serverErr.Code] {
			return string(serverErr.Code)
		}
		return "server_" + strings.ToLower(string(serverErr.Type))
	case isNetworkError(err):
		return "network"
	default:
		return "failed"
	}
}

// discardLogger is the logger of a backend that has none.
var discardLogger = slog.New(slog.DiscardHandler)

// logger returns the logger of the backend, or one that logs nothing.
func (b *Backend) logger() *slog.Logger {
	if b.cfg.Logger != nil {
		return b.cfg.Logger
	}
	return discardLogger
}

// ---------------------------------------------------------------- the connection

// smtpDeadline returns the time limit of a whole SMTP conversation that sends
// dataLen bytes: CommandTimeout, plus the time that the data needs at
// smtpDataRate. So a message of 25 MiB gets 256 seconds more than a short one.
func smtpDeadline(commandTimeout time.Duration, dataLen int) time.Duration {
	if dataLen < 0 {
		dataLen = 0
	}
	return commandTimeout + time.Duration(int64(dataLen))*time.Second/smtpDataRate
}

// deadlineConn is a connection that has one time limit for all of its use.
//
// go-smtp sets a deadline before each command and clears it after the
// command. So a deadline that is set once on the connection would last only
// until the first command ends. deadlineConn lets every deadline through, but
// never one that is later than limit, and it turns "no deadline" into limit.
type deadlineConn struct {
	net.Conn
	limit time.Time
}

func (c *deadlineConn) capped(t time.Time) time.Time {
	if t.IsZero() || t.After(c.limit) {
		return c.limit
	}
	return t
}

func (c *deadlineConn) SetDeadline(t time.Time) error { return c.Conn.SetDeadline(c.capped(t)) }

func (c *deadlineConn) SetReadDeadline(t time.Time) error {
	return c.Conn.SetReadDeadline(c.capped(t))
}

func (c *deadlineConn) SetWriteDeadline(t time.Time) error {
	return c.Conn.SetWriteDeadline(c.capped(t))
}

// withSMTP connects to the SMTP server, protects the connection, logs in with
// AUTH PLAIN, and runs fn. dataLen is the size of the message that fn sends,
// and it sets the time limit (see smtpDeadline). When fn succeeds, withSMTP
// says QUIT.
//
// The security rules are those of Send. The login is sent only on a connection
// that is encrypted, or on a plain connection that is allowed.
//
// The errors never hold the text of the server, because servers repeat the user
// name or an address in it. ctx ends the conversation at once.
func withSMTP(ctx context.Context, cfg Config, dataLen int, fn func(c *smtp.Client) error) error {
	cfg.withDefaults()
	if err := cfg.Server.Validate(cfg.Local); err != nil {
		return fmt.Errorf("smtp: server settings: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	host := cfg.Server.SMTPHost
	addr := net.JoinHostPort(host, strconv.Itoa(cfg.Server.SMTPPort))

	// DialTimeout limits the connect and the TLS handshake of implicit TLS.
	dialCtx, cancelDial := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancelDial()
	dialer := net.Dialer{Timeout: cfg.DialTimeout}
	raw, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp: connect: %w", err)
	}
	defer func() { _ = raw.Close() }()

	limit := time.Now().Add(smtpDeadline(cfg.CommandTimeout, dataLen))
	if d, ok := ctx.Deadline(); ok && d.Before(limit) {
		limit = d
	}
	conn := &deadlineConn{Conn: raw, limit: limit}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("smtp: set the time limit: %w", err)
	}
	// The end of ctx closes the connection. That also stops any step that waits
	// for the server.
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()

	// fail turns the error of a step into the error that the caller sees. When
	// ctx has ended or the time limit has passed, that is the real cause, and
	// the step only saw a closed connection or a timeout. An error that says
	// the outcome is not known keeps saying it, with the cause added.
	fail := func(err error) error {
		var cause error
		var netErr net.Error
		switch {
		case ctx.Err() != nil:
			cause = ctx.Err()
		case errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) || !time.Now().Before(limit):
			cause = fmt.Errorf("the conversation took longer than the time limit: %w", context.DeadlineExceeded)
		default:
			return err
		}
		if errors.Is(err, ErrDeliveryUnknown) {
			return fmt.Errorf("%w: %w", ErrDeliveryUnknown, cause)
		}
		return fmt.Errorf("smtp: %w", cause)
	}

	client, err := secureSMTP(dialCtx, cfg, conn)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = client.Close() }()
	if err := loginSMTP(client, cfg); err != nil {
		return fail(err)
	}
	if err := fn(client); err != nil {
		return fail(err)
	}
	// The work is done. A failed QUIT changes nothing.
	_ = client.Quit()
	return nil
}

// secureSMTP protects an open connection as the config says and says hello. On
// an error it does not close conn. The caller does.
//
// When it returns a client, the connection is encrypted, or it is a plain
// connection that the config allows.
func secureSMTP(ctx context.Context, cfg Config, conn *deadlineConn) (*smtp.Client, error) {
	host := cfg.Server.SMTPHost
	var client *smtp.Client

	switch cfg.Server.SMTPSecurity {
	case account.SecurityTLS:
		tlsConn := tls.Client(conn, newSMTPTLSConfig(cfg, host))
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, smtpStepError("tls handshake", err)
		}
		client = smtp.NewClient(tlsConn)

	case account.SecuritySTARTTLS:
		// NewClientStartTLS says hello, fails when STARTTLS is not offered,
		// sends STARTTLS, and fails when the server refuses. It closes the
		// connection then. The TLS handshake itself starts with the next
		// write, which is the hello below.
		var err error
		client, err = smtp.NewClientStartTLS(conn, newSMTPTLSConfig(cfg, host))
		if err != nil {
			return nil, smtpStepError("starttls", err)
		}

	case account.SecurityPlain:
		// Validate looked at the text of the host name. Check the address that
		// the connection really reached, because a name such as "localhost"
		// can be changed to point at another machine.
		if !isLoopbackAddr(conn.RemoteAddr()) {
			return nil, errors.New("smtp: a plain connection is allowed only to a loopback address")
		}
		client = smtp.NewClient(conn)

	default:
		// Validate accepts only the three modes above.
		return nil, errors.New("smtp: unknown security mode")
	}

	// With TLS, the hello also reads the greeting. With STARTTLS, it is the
	// hello after the upgrade, and it makes the TLS handshake happen, so a
	// certificate that is not accepted fails here, before the login.
	if err := client.Hello(smtpHelloName); err != nil {
		return nil, smtpStepError("hello", err)
	}
	if cfg.Server.SMTPSecurity != account.SecurityPlain {
		state, ok := client.TLSConnectionState()
		if !ok || !state.HandshakeComplete || state.Version < tls.VersionTLS12 {
			return nil, errors.New("smtp: the connection is not encrypted")
		}
	}
	return client, nil
}

// loginSMTP logs in with AUTH PLAIN. It does nothing else, and it fails when the
// server does not offer PLAIN: the password goes out with this one mechanism
// only.
func loginSMTP(client *smtp.Client, cfg Config) error {
	if !client.SupportsAuth(sasl.Plain) {
		return errors.New("smtp: the server does not offer AUTH PLAIN")
	}
	auth := sasl.NewPlainClient("", cfg.Server.Username, cfg.Password.Reveal())
	if err := client.Auth(auth); err != nil {
		return smtpLoginError(err)
	}
	return nil
}

// The SMTP replies to AUTH that mean a refused login (they match ErrAuthFailed).
const (
	// smtpAuthFailed is "authentication credentials invalid" (RFC 4954).
	smtpAuthFailed = 535
	// smtpAppPasswordNeeded is "authentication mechanism is too weak". Google
	// sends it when the account needs an app password.
	smtpAppPasswordNeeded = 534
)

// smtpLoginError turns the error of a failed AUTH into the error that callers
// see: "smtp: login failed" with the reply code of the server. It never repeats
// the free text of the server. Servers often put the user name or the mail
// address in that text, and logs must not hold addresses. A network failure
// keeps its error, because that text has no user data. A reply 535 or 534
// matches ErrAuthFailed.
func smtpLoginError(err error) error {
	var reply *smtp.SMTPError
	switch {
	case errors.As(err, &reply):
		text := fmt.Sprintf("smtp: login failed (reply code %d)", reply.Code)
		if reply.Code == smtpAuthFailed || reply.Code == smtpAppPasswordNeeded {
			return &authError{text: text}
		}
		return errors.New(text)
	case isNetworkError(err):
		return fmt.Errorf("smtp: login failed: %w", err)
	default:
		return errors.New("smtp: login failed")
	}
}

// smtpStepError describes the failure of one step of the SMTP conversation. It
// keeps what helps a person: the step, the reply code of the server, the
// certificate error, the network error. It leaves out the text of the server,
// which can repeat the user name or an address.
func smtpStepError(step string, err error) error {
	var (
		certErr  *CertificateUnknownError
		reply    *smtp.SMTPError
		protoErr textproto.ProtocolError
	)
	switch {
	case errors.As(err, &certErr):
		// The text of the error says what is wrong and starts with "smtp:".
		return err
	case errors.As(err, &reply):
		return fmt.Errorf("smtp: %s failed (reply code %d)", step, reply.Code)
	case errors.As(err, &protoErr):
		// The text of a ProtocolError is the line that the server sent.
		return fmt.Errorf("smtp: %s failed: the server sent a reply that is not valid", step)
	case strings.HasPrefix(err.Error(), "smtp: "):
		// go-smtp refused before it sent anything (for example "server doesn't
		// support STARTTLS"). The text has no data of the server.
		return err
	default:
		return fmt.Errorf("smtp: %s: %w", step, err)
	}
}
