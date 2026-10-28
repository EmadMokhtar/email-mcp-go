package mime

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	stdmime "mime"
	netmail "net/mail"
	"strings"
	"time"

	gomail "github.com/emersion/go-message/mail"
	"golang.org/x/net/idna"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// Errors that Build and FromDraft return. Callers check them with errors.Is.
// The messages name the field that is wrong. They never hold an address, a
// subject or any other text from the message, so they are safe to log.
var (
	// ErrInvalidAddress means an address is missing or cannot be parsed.
	ErrInvalidAddress = errors.New("mime: invalid address")
	// ErrNoRecipients means the message would go to nobody.
	ErrNoRecipients = errors.New("mime: message has no recipients")
	// ErrHeaderInjection means a value that goes into a header has a line
	// break (CR or LF) or a NUL. A line break would let the value add headers
	// of its own, such as a Bcc.
	ErrHeaderInjection = errors.New("mime: header value has a line break or NUL")
	// ErrNoOriginal means a reply or a forward was asked for, but
	// BuildContext.Original is nil.
	ErrNoOriginal = errors.New("mime: reply or forward needs the original message")
	// ErrReplyAndForward means one Compose asks for a reply and a forward.
	ErrReplyAndForward = errors.New("mime: a message cannot be a reply and a forward")
	// ErrInvalidContentType means the content type of an attachment is not
	// one that Build can write.
	ErrInvalidContentType = errors.New("mime: invalid attachment content type")
)

// BuildContext is what Build needs besides the Compose request.
type BuildContext struct {
	// From is the sender. Its Email is the envelope sender too.
	From mail.Address
	// Self lists the addresses of the user. A reply to all leaves them out.
	Self []string
	// Original is the message that a reply answers or a forward sends on. It
	// must be set for a reply or a forward, and is not used otherwise.
	Original *mail.Message
	// OriginalAttachments are the attachments of Original, with their data.
	// A forward uses them only when Compose.IncludeOriginalAttachments is set.
	OriginalAttachments []mail.Attachment
	// Now is the time for the Date header. The zero time means the clock.
	Now time.Time
	// MessageIDDomain is the part after the "@" in the new Message-ID. Empty
	// means the domain of From.
	MessageIDDomain string
	// ForDraft keeps the Bcc header in the message. Use it when the message
	// is saved as a draft. FromDraft removes the header again before sending.
	ForDraft bool
}

// Build writes a new message, a reply, a reply to all, or a forward, as an
// RFC 5322 message (the internet message format).
//
// Every address goes through net/mail.ParseAddress. Every value that reaches
// a header is checked for a line break, and Build returns ErrHeaderInjection
// for one. This includes values taken from the original message, because a
// third party wrote them.
//
// The recipients are the To, Cc and Bcc addresses, with each address once. The
// first one is kept, and the order is To, Cc, Bcc. They are all in
// RawMessage.Recipients. The message has no Bcc header unless
// BuildContext.ForDraft is set, so nobody learns who else got a copy.
//
// A reply goes to the Reply-To addresses of the original, or else to its
// sender. A reply to all also copies the To and Cc addresses of the original
// to Cc. Addresses in BuildContext.Self are then removed from To and Cc.
// Compose.To, Cc and Bcc are added to those. The reply gets In-Reply-To and
// References headers. Build does not quote the original text.
//
// A forward puts the text of the original under a short "Forwarded message"
// header. If Compose has HTML, the HTML form is the same, with every value
// escaped, and the original text in a quoted block. The HTML of the original
// is never copied.
//
// Build returns mail.ErrTooLarge when the attachments, new and forwarded
// together, are over 10 MB. Build does not change its arguments.
func Build(c mail.Compose, bc BuildContext) (*mail.RawMessage, error) {
	if c.ReplyTo != nil && c.ForwardOf != nil {
		return nil, ErrReplyAndForward
	}
	if (c.ReplyTo != nil || c.ForwardOf != nil) && bc.Original == nil {
		return nil, ErrNoOriginal
	}
	from, err := plainAddress("from", bc.From)
	if err != nil {
		return nil, err
	}
	rcpt, err := resolveRecipients(c, bc)
	if err != nil {
		return nil, err
	}
	subject, err := resolveSubject(c, bc.Original)
	if err != nil {
		return nil, err
	}
	thread, err := resolveThreading(c, bc.Original)
	if err != nil {
		return nil, err
	}
	atts, err := collectAttachments(c, bc)
	if err != nil {
		return nil, err
	}
	id, err := newMessageID(bc.MessageIDDomain, from)
	if err != nil {
		return nil, err
	}
	now := bc.Now
	if now.IsZero() {
		now = time.Now()
	}

	m := &outgoing{
		from:     from,
		rcpt:     rcpt,
		subject:  subject,
		thread:   thread,
		id:       id,
		date:     now,
		forDraft: bc.ForDraft,
		body:     composeBodies(c, bc.Original),
		atts:     atts,
	}
	var buf bytes.Buffer
	if err := m.write(&buf); err != nil {
		return nil, fmt.Errorf("mime: write message: %w", err)
	}
	return &mail.RawMessage{From: from.Address, Recipients: rcpt.envelope(), Data: buf.Bytes()}, nil
}

// checkValue returns ErrHeaderInjection when v has a CR, an LF or a NUL.
// field names the value in the error.
func checkValue(field, v string) error {
	if strings.ContainsAny(v, "\r\n\x00") {
		return fmt.Errorf("%w: %s", ErrHeaderInjection, field)
	}
	return nil
}

// resolveSubject returns the subject: the one in Compose, or else one made
// from the subject of the original.
func resolveSubject(c mail.Compose, original *mail.Message) (string, error) {
	subject := c.Subject
	if subject == "" {
		switch {
		case c.ReplyTo != nil:
			subject = replySubject(original.Subject)
		case c.ForwardOf != nil:
			subject = "Fwd: " + original.Subject
		}
	}
	if err := checkValue("subject", subject); err != nil {
		return "", err
	}
	return subject, nil
}

// replySubject adds "Re: " unless the subject already has it. The check
// ignores case.
func replySubject(original string) string {
	original = strings.TrimSpace(original)
	if strings.HasPrefix(strings.ToLower(original), "re:") {
		return original
	}
	return strings.TrimSpace("Re: " + original)
}

// threading holds the message IDs for the In-Reply-To and References headers.
// The IDs have no angle brackets.
type threading struct {
	inReplyTo  string
	references []string
}

// resolveThreading makes the threading headers of a reply. Other messages
// have none. The References are those of the original, then its Message-ID.
func resolveThreading(c mail.Compose, original *mail.Message) (threading, error) {
	var t threading
	if c.ReplyTo == nil {
		return t, nil
	}
	for _, ref := range original.References {
		id := bareID(ref)
		if id == "" {
			continue
		}
		if err := checkValue("references", id); err != nil {
			return threading{}, err
		}
		t.references = append(t.references, id)
	}
	if id := bareID(original.MessageID); id != "" {
		if err := checkValue("message-id of the original", id); err != nil {
			return threading{}, err
		}
		t.inReplyTo = id
		t.references = append(t.references, id)
	}
	return t, nil
}

// bareID removes the spaces and the angle brackets around a message ID.
func bareID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	return strings.TrimSpace(id)
}

// newMessageID makes a new message ID, without angle brackets: 16 random
// bytes in hex, "@", and a domain.
func newMessageID(domain string, from *netmail.Address) (string, error) {
	if domain == "" {
		if i := strings.LastIndex(from.Address, "@"); i >= 0 {
			domain = from.Address[i+1:]
		}
	}
	if err := checkValue("message id domain", domain); err != nil {
		return "", err
	}
	domain, err := idna.Lookup.ToASCII(domain)
	if err != nil || !isHostName(domain) {
		return "", errors.New("mime: invalid message id domain")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("mime: make message id: %w", err)
	}
	return hex.EncodeToString(random[:]) + "@" + domain, nil
}

// isHostName reports whether s is made of letters, digits, dots, hyphens and
// underscores only. That is all a message ID needs, and none of it can end the
// ID or add a header.
func isHostName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// outgoing is a message that is ready to be written. Build checks and
// prepares and checks every part of it first, so that write adds no new check.
type outgoing struct {
	from     *netmail.Address
	rcpt     recipients
	subject  string
	thread   threading
	id       string
	date     time.Time
	forDraft bool
	body     bodies
	atts     []attachment
}

// header returns the header fields of the message, but not the Content-Type
// ones. The writer adds those.
func (m *outgoing) header() gomail.Header {
	fields := [][2]string{{"From", formatAddresses([]*netmail.Address{m.from})}}
	if len(m.rcpt.to) > 0 {
		fields = append(fields, [2]string{"To", formatAddresses(m.rcpt.to)})
	}
	if len(m.rcpt.cc) > 0 {
		fields = append(fields, [2]string{"Cc", formatAddresses(m.rcpt.cc)})
	}
	if m.forDraft && len(m.rcpt.bcc) > 0 {
		fields = append(fields, [2]string{"Bcc", formatAddresses(m.rcpt.bcc)})
	}
	fields = append(fields,
		[2]string{"Subject", stdmime.QEncoding.Encode("utf-8", m.subject)},
		[2]string{"Date", m.date.Format(time.RFC1123Z)},
		[2]string{"Message-ID", "<" + m.id + ">"},
	)
	if m.thread.inReplyTo != "" {
		fields = append(fields, [2]string{"In-Reply-To", "<" + m.thread.inReplyTo + ">"})
	}
	if len(m.thread.references) > 0 {
		fields = append(fields, [2]string{"References", "<" + strings.Join(m.thread.references, "> <") + ">"})
	}
	fields = append(fields, [2]string{"MIME-Version", "1.0"})

	// The header type writes the last field added first.
	var h gomail.Header
	for i := len(fields) - 1; i >= 0; i-- {
		h.Add(fields[i][0], fields[i][1])
	}
	return h
}

// formatAddresses writes addresses for a header. Names that are not ASCII are
// encoded. An address with no name is written without angle brackets.
func formatAddresses(list []*netmail.Address) string {
	parts := make([]string, len(list))
	for i, a := range list {
		parts[i] = a.String()
		if a.Name == "" {
			parts[i] = strings.TrimSuffix(strings.TrimPrefix(parts[i], "<"), ">")
		}
	}
	return strings.Join(parts, ", ")
}

// write writes the whole message. The shape follows the content: one
// text/plain part, or multipart/alternative when there is HTML, and
// multipart/mixed around it when there are attachments.
func (m *outgoing) write(w io.Writer) error {
	h := m.header()
	if len(m.atts) == 0 {
		if m.body.html == "" {
			h.Set("Content-Type", "text/plain; charset=utf-8")
			tw, err := gomail.CreateSingleInlineWriter(w, h)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(tw, m.body.text); err != nil {
				return err
			}
			return tw.Close()
		}
		iw, err := gomail.CreateInlineWriter(w, h)
		if err != nil {
			return err
		}
		return m.body.writeAlternative(iw)
	}

	mw, err := gomail.CreateWriter(w, h)
	if err != nil {
		return err
	}
	if m.body.html == "" {
		err = writeTextPart(mw.CreateSingleInline, "text/plain", m.body.text)
	} else {
		var iw *gomail.InlineWriter
		if iw, err = mw.CreateInline(); err == nil {
			err = m.body.writeAlternative(iw)
		}
	}
	if err != nil {
		return err
	}
	for _, a := range m.atts {
		if err := a.write(mw); err != nil {
			return err
		}
	}
	return mw.Close()
}
