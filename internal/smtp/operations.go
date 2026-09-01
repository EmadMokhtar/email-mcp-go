package smtp

import (
	"bytes"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/pkg/models"
	"github.com/go-mail/mail/v2"
)

// forwardDateLayout is how the original send time is shown in a forward.
const forwardDateLayout = "Mon, Jan 2, 2006 at 3:04 PM"

// newDialer builds an SMTP dialer from the configuration. The port is stored
// as a string, so parse it here and report a clear error when it is not a
// number, rather than silently dialing port 0.
func (c *Client) newDialer() (*mail.Dialer, error) {
	port, err := strconv.Atoi(c.config.SMTPPort)
	if err != nil {
		return nil, fmt.Errorf("invalid SMTP port %q: %w", c.config.SMTPPort, err)
	}

	d := mail.NewDialer(c.config.SMTPHost, port, c.config.SMTPUsername, c.config.SMTPPassword)
	if c.config.SMTPTLS {
		// The library default is opportunistic STARTTLS, which silently sends
		// the credentials and the message in the clear if the server does not
		// offer STARTTLS or an attacker strips it. Require it instead.
		// NewDialer already turns on implicit TLS for port 465, where
		// StartTLSPolicy does not apply.
		d.StartTLSPolicy = mail.MandatoryStartTLS
	} else {
		d.StartTLSPolicy = mail.NoStartTLS
	}

	return d, nil
}

func (c *Client) SendEmail(req *models.SendEmailRequest) error {
	m := mail.NewMessage()

	m.SetHeader("From", c.config.SMTPUsername)
	m.SetHeader("To", req.To...)

	if len(req.Cc) > 0 {
		m.SetHeader("Cc", req.Cc...)
	}

	if len(req.Bcc) > 0 {
		m.SetHeader("Bcc", req.Bcc...)
	}

	m.SetHeader("Subject", req.Subject)

	if req.IsHTML {
		m.SetBody("text/html", req.Body)
	} else {
		m.SetBody("text/plain", req.Body)
	}

	// Add attachments
	for _, att := range req.Attachments {
		m.AttachReader(att.Filename, bytes.NewReader(att.Data))
	}

	d, err := c.newDialer()
	if err != nil {
		return err
	}

	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	return nil
}

// replyAllCc returns everyone to copy on a reply-all: the original To and Cc
// recipients in one list. They must be combined, because SetHeader replaces
// the field instead of adding to it, so setting "Cc" twice would keep only
// the recipients from the second call.
func replyAllCc(originalEmail *models.Email) []string {
	cc := make([]string, 0, len(originalEmail.To)+len(originalEmail.Cc))
	cc = append(cc, originalEmail.To...)
	cc = append(cc, originalEmail.Cc...)

	return cc
}

func (c *Client) ReplyToEmail(originalEmail *models.Email, body string, replyAll bool, isHTML bool) error {
	m := mail.NewMessage()

	m.SetHeader("From", c.config.SMTPUsername)

	// Set To (reply to sender)
	if len(originalEmail.From) > 0 {
		m.SetHeader("To", originalEmail.From[0])
	}

	// Reply all - copy everyone the original message reached.
	if replyAll {
		if cc := replyAllCc(originalEmail); len(cc) > 0 {
			m.SetHeader("Cc", cc...)
		}
	}

	// Set subject with Re: prefix
	subject := originalEmail.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	m.SetHeader("Subject", subject)

	// Set In-Reply-To and References headers
	if originalEmail.MessageID != "" {
		m.SetHeader("In-Reply-To", originalEmail.MessageID)
		m.SetHeader("References", originalEmail.MessageID)
	}

	if isHTML {
		m.SetBody("text/html", body)
	} else {
		m.SetBody("text/plain", body)
	}

	d, err := c.newDialer()
	if err != nil {
		return err
	}

	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("failed to send reply: %w", err)
	}

	return nil
}

// forwardPreambleText renders the "forwarded message" header block for a
// plain text forward.
func forwardPreambleText(message string, originalEmail *models.Email) string {
	var b strings.Builder

	if message != "" {
		b.WriteString(message)
		b.WriteString("\n\n")
	}

	b.WriteString("---------- Forwarded message ---------\n")
	b.WriteString(fmt.Sprintf("From: %s\n", strings.Join(originalEmail.From, ", ")))
	b.WriteString(fmt.Sprintf("Date: %s\n", originalEmail.Date.Format(forwardDateLayout)))
	b.WriteString(fmt.Sprintf("Subject: %s\n", originalEmail.Subject))
	b.WriteString(fmt.Sprintf("To: %s\n", strings.Join(originalEmail.To, ", ")))
	b.WriteString("\n\n")

	return b.String()
}

// forwardPreambleHTML renders the same header block for an HTML forward. Every
// value is escaped, and line breaks become <br> so the block still reads as
// separate lines once a mail client renders it.
func forwardPreambleHTML(message string, originalEmail *models.Email) string {
	var b strings.Builder

	if message != "" {
		b.WriteString(htmlLines(message))
		b.WriteString("<br><br>")
	}

	b.WriteString("---------- Forwarded message ---------<br>")
	b.WriteString("From: " + html.EscapeString(strings.Join(originalEmail.From, ", ")) + "<br>")
	b.WriteString("Date: " + html.EscapeString(originalEmail.Date.Format(forwardDateLayout)) + "<br>")
	b.WriteString("Subject: " + html.EscapeString(originalEmail.Subject) + "<br>")
	b.WriteString("To: " + html.EscapeString(strings.Join(originalEmail.To, ", ")) + "<br>")
	b.WriteString("<br><br>")

	return b.String()
}

// htmlLines escapes text and keeps its line structure.
func htmlLines(text string) string {
	return strings.ReplaceAll(html.EscapeString(text), "\n", "<br>")
}

func (c *Client) ForwardEmail(originalEmail *models.Email, to []string, message string) error {
	m := mail.NewMessage()

	m.SetHeader("From", c.config.SMTPUsername)
	m.SetHeader("To", to...)

	// Set subject with Fwd: prefix
	subject := originalEmail.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "fwd:") && !strings.HasPrefix(strings.ToLower(subject), "fw:") {
		subject = "Fwd: " + subject
	}
	m.SetHeader("Subject", subject)

	// Build forwarded message body. The preamble is plain text, so when the
	// original body is HTML it has to be escaped and given real line breaks;
	// pasting it in raw would collapse the lines and let a "<" or "&" in a
	// subject or address be read as markup.
	if originalEmail.HTMLBody != "" {
		m.SetBody("text/html", forwardPreambleHTML(message, originalEmail)+originalEmail.HTMLBody)
	} else {
		m.SetBody("text/plain", forwardPreambleText(message, originalEmail)+originalEmail.TextBody)
	}

	// Forward attachments
	for _, att := range originalEmail.Attachments {
		m.AttachReader(att.Filename, bytes.NewReader(att.Data))
	}

	d, err := c.newDialer()
	if err != nil {
		return err
	}

	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("failed to forward email: %w", err)
	}

	return nil
}
