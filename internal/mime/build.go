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
	// ErrInvalidAddress means an address is missing, cannot be parsed, or
	// cannot be sent as it is. The last case is a quoted local part such as
	// "a b"@example.com, and a domain literal such as bob@[1.2.3.4].
	ErrInvalidAddress = errors.New("mime: invalid address")
	// ErrNoRecipients means the message would go to nobody. A draft built with
	// BuildContext.ForDraft is the one message that may have none.
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
	// ErrInvalidFilename means the file name of an attachment has a character
	// that hides or reorders text: a Unicode format character (category Cf,
	// such as U+202E, which makes "invoice" U+202E "gpj.exe" show as
	// "invoiceexe.jpg") or a line or paragraph separator (Zl, Zp).
	ErrInvalidFilename = errors.New("mime: attachment file name has a character that hides or reorders text")
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
	// A draft may have no recipients yet, so Build does not return
	// ErrNoRecipients for it. FromDraft does, so such a draft cannot be sent.
	ForDraft bool
}

// Build writes a new message, a reply, a reply to all, or a forward, as an
// RFC 5322 message (the internet message format).
//
// Every address goes through net/mail.ParseAddress. An address that needs
// quotes or brackets is refused with ErrInvalidAddress, because net/mail
// returns it without the quotes, and the SMTP envelope would not match the
// header (see isEnvelopeSafe). Every value that the
// caller gives and that reaches a header is checked, and Build returns
// ErrHeaderInjection when it has a CR, an LF or a NUL.
//
// Values that come from the original message are not checked but cleaned,
// because a third party wrote them and they must not stop a reply. Each run
// of CR, LF and NUL in the subject and in the display names is replaced with
// one space. A Message-ID or a References entry with such a character is not
// used. An address of the original with such a character cannot be cleaned,
// because that would change the address, so Build refuses it.
//
// The recipients are the To, Cc and Bcc addresses, with each address once. The
// first one is kept, and the order is To, Cc, Bcc. They are all in
// RawMessage.Recipients. The message has no Bcc header unless
// BuildContext.ForDraft is set, so nobody learns who else got a copy. Build
// returns ErrNoRecipients when there are none, except for a draft (see
// BuildContext.ForDraft).
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
// together, are over 10 MB. It returns an error that wraps ErrHTMLTooComplex
// when Compose has HTML and no text, and the HTML is too complex to make the
// text form of it. (The HTML of a forwarded original is not an error: the
// forward then has no text of the original.) Build does not change its
// arguments.
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
	thread := resolveThreading(c, bc.Original)
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

	body, err := composeBodies(c, bc.Original)
	if err != nil {
		return nil, err
	}

	m := &outgoing{
		from:     from,
		rcpt:     rcpt,
		subject:  subject,
		thread:   thread,
		id:       id,
		date:     now,
		forDraft: bc.ForDraft,
		body:     body,
		atts:     atts,
	}
	var buf bytes.Buffer
	if err := m.write(&buf); err != nil {
		return nil, fmt.Errorf("mime: write message: %w", err)
	}
	return &mail.RawMessage{From: from.Address, Recipients: rcpt.envelope(), Data: buf.Bytes()}, nil
}

// lineBreaks are the characters that a header value must not have: CR, LF
// and NUL.
const lineBreaks = "\r\n\x00"

// checkValue returns ErrHeaderInjection when v has a CR, an LF or a NUL.
// field names the value in the error. Use it for values that the caller of
// Build gives. For values from the original message, use cleanOriginal.
func checkValue(field, v string) error {
	if strings.ContainsAny(v, lineBreaks) {
		return fmt.Errorf("%w: %s", ErrHeaderInjection, field)
	}
	return nil
}

// cleanOriginal replaces each run of CR, LF and NUL in s with one space. Use
// it for a value that comes from the original message. A third party wrote
// that value, so it must not stop a reply, but it also must not add a header.
func cleanOriginal(s string) string {
	if !strings.ContainsAny(s, lineBreaks) {
		return s
	}
	var b strings.Builder
	inRun := false
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(lineBreaks, s[i]) >= 0 {
			if !inRun {
				b.WriteByte(' ')
			}
			inRun = true
			continue
		}
		b.WriteByte(s[i])
		inRun = false
	}
	return b.String()
}

// resolveSubject returns the subject: the one in Compose, or else one made
// from the subject of the original. The subject in Compose is checked. The
// subject of the original is cleaned.
func resolveSubject(c mail.Compose, original *mail.Message) (string, error) {
	if c.Subject != "" {
		if err := checkValue("subject", c.Subject); err != nil {
			return "", err
		}
		return c.Subject, nil
	}
	switch {
	case c.ReplyTo != nil:
		return replySubject(cleanOriginal(original.Subject)), nil
	case c.ForwardOf != nil:
		return "Fwd: " + cleanOriginal(original.Subject), nil
	}
	return "", nil
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
// An ID of the original that has a line break or a NUL is not used: it comes
// from a third party, and a changed ID would point at another message.
func resolveThreading(c mail.Compose, original *mail.Message) threading {
	var t threading
	if c.ReplyTo == nil {
		return t
	}
	for _, ref := range original.References {
		if id := usableID(ref); id != "" {
			t.references = append(t.references, id)
		}
	}
	if id := usableID(original.MessageID); id != "" {
		t.inReplyTo = id
		t.references = append(t.references, id)
	}
	return t
}

// usableID returns the ID of a message ID header value of the original, or an
// empty string when the value has a CR, an LF or a NUL anywhere, even at the
// end, where bareID would cut it off.
func usableID(value string) string {
	if strings.ContainsAny(value, lineBreaks) {
		return ""
	}
	return bareID(value)
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
