package mime

import (
	"fmt"
	stdhtml "html"
	"io"
	stdmime "mime"
	"net/http"
	"strings"
	"time"

	gomail "github.com/emersion/go-message/mail"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// maxAttachmentBytes is the most attachment data that one message can have.
const maxAttachmentBytes = 10 * 1024 * 1024

// bodies are the text forms of a message body. html is empty when the message
// has no HTML form.
type bodies struct {
	text, html string
}

// composeBodies makes the bodies of a message from Compose. A message with
// HTML and no text gets the text form of its HTML, so that no mail program
// shows an empty text part. A forward adds the original text.
//
// The HTML of Compose comes from the caller. If it is too complex to make
// text of (see ErrHTMLTooComplex), the error wraps that error. The HTML of the
// original does not stop a forward: see newForwarded.
func composeBodies(c mail.Compose, original *mail.Message) (bodies, error) {
	b := bodies{text: c.Body, html: c.HTMLBody}
	if b.text == "" && b.html != "" {
		text, err := HTMLToText(b.html)
		if err != nil {
			return bodies{}, fmt.Errorf("mime: html body: %w", err)
		}
		b.text = text
	}
	if c.ForwardOf != nil {
		f := newForwarded(original)
		b.text += f.plain()
		if b.html != "" {
			b.html += f.html()
		}
	}
	return b, nil
}

// writeAlternative writes the text part and then the HTML part. Mail programs
// use the last part that they can show, so the HTML comes last.
func (b bodies) writeAlternative(iw *gomail.InlineWriter) error {
	if err := writeTextPart(iw.CreatePart, "text/plain", b.text); err != nil {
		return err
	}
	if err := writeTextPart(iw.CreatePart, "text/html", b.html); err != nil {
		return err
	}
	return iw.Close()
}

// writeTextPart writes one UTF-8 text part. The writer encodes it as
// quoted-printable and ends each line with CRLF.
func writeTextPart(create func(gomail.InlineHeader) (io.WriteCloser, error), mediaType, body string) error {
	var h gomail.InlineHeader
	h.Set("Content-Type", mediaType+"; charset=utf-8")
	w, err := create(h)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, body); err != nil {
		return err
	}
	return w.Close()
}

// forwarded is the original message as a forward shows it. Its values come
// from a third party, so each one is a single line (see cleanOriginal).
type forwarded struct {
	from, date, subject, to string
	text                    string
}

func newForwarded(o *mail.Message) forwarded {
	text := o.Text
	if text == "" && o.HTML != "" {
		// Never forward the raw HTML. The text form leaves out what a reader
		// of the message could not see. The original comes from a third party,
		// and HTML that is too complex to convert must not stop the forward:
		// the forward then has no text of the original.
		text, _ = HTMLToText(o.HTML)
	}
	to := make([]string, len(o.To))
	for i, a := range o.To {
		to[i] = displayAddress(a)
	}
	f := forwarded{
		from:    cleanOriginal(displayAddress(o.From)),
		subject: cleanOriginal(o.Subject),
		to:      cleanOriginal(strings.Join(to, ", ")),
		text:    text,
	}
	if !o.Date.IsZero() {
		f.date = o.Date.Format(time.RFC1123Z)
	}
	return f
}

// displayAddress writes an address for people to read: "Name <address>".
func displayAddress(a mail.Address) string {
	if a.Name == "" {
		return a.Email
	}
	return a.Name + " <" + a.Email + ">"
}

const forwardedMarker = "---------- Forwarded message ----------"

// plain is the text that follows the note of the sender in the plain text form.
func (f forwarded) plain() string {
	return "\r\n\r\n" + forwardedMarker + "\r\n" +
		"From: " + f.from + "\r\n" +
		"Date: " + f.date + "\r\n" +
		"Subject: " + f.subject + "\r\n" +
		"To: " + f.to + "\r\n\r\n" +
		f.text
}

// html is the same in HTML. Every value of the original is escaped, and the
// original text is in a quoted block.
func (f forwarded) html() string {
	return "<br><br><div>" + forwardedMarker + "<br>" +
		"From: " + stdhtml.EscapeString(f.from) + "<br>" +
		"Date: " + stdhtml.EscapeString(f.date) + "<br>" +
		"Subject: " + stdhtml.EscapeString(f.subject) + "<br>" +
		"To: " + stdhtml.EscapeString(f.to) + "</div>" +
		"<blockquote><pre>" + stdhtml.EscapeString(f.text) + "</pre></blockquote>"
}

// attachment is a checked attachment, ready to be written.
type attachment struct {
	filename  string
	mediaType string
	params    map[string]string
	data      []byte
}

// collectAttachments returns the attachments of the message: the original
// ones for a forward that asks for them, then the new ones. It returns
// mail.ErrTooLarge when the data is over maxAttachmentBytes in all. The file
// name of an original attachment comes from a third party, so it is cleaned
// (see cleanOriginal). The file name of a new attachment is checked.
func collectAttachments(c mail.Compose, bc BuildContext) ([]attachment, error) {
	var in []mail.OutgoingAttachment
	if c.ForwardOf != nil && c.IncludeOriginalAttachments {
		for _, a := range bc.OriginalAttachments {
			in = append(in, mail.OutgoingAttachment{Filename: cleanOriginal(a.Filename), ContentType: a.ContentType, Data: a.Data})
		}
	}
	in = append(in, c.Attachments...)

	total := 0
	for _, a := range in {
		total += len(a.Data)
	}
	if total > maxAttachmentBytes {
		return nil, fmt.Errorf("%w: attachments are over %d bytes", mail.ErrTooLarge, maxAttachmentBytes)
	}

	out := make([]attachment, 0, len(in))
	for _, a := range in {
		checked, err := newAttachment(a)
		if err != nil {
			return nil, err
		}
		out = append(out, checked)
	}
	return out, nil
}

// newAttachment checks the file name and the content type. An empty content
// type is found from the data. An empty file name becomes "attachment".
//
// The content type must be one type with a slash and parameters that are
// valid, it must not be multipart (the writer would treat the data as parts),
// and its charset, if any, must be UTF-8 or US-ASCII, because the writer
// refuses any other.
func newAttachment(a mail.OutgoingAttachment) (attachment, error) {
	if err := checkValue("attachment file name", a.Filename); err != nil {
		return attachment{}, err
	}
	filename := a.Filename
	if filename == "" {
		filename = "attachment"
	}

	contentType := a.ContentType
	if contentType == "" {
		contentType = http.DetectContentType(a.Data)
	}
	if err := checkValue("attachment content type", contentType); err != nil {
		return attachment{}, err
	}
	mediaType, params, err := stdmime.ParseMediaType(contentType)
	if err != nil || !strings.Contains(mediaType, "/") || strings.HasPrefix(mediaType, "multipart/") {
		return attachment{}, ErrInvalidContentType
	}
	switch strings.ToLower(params["charset"]) {
	case "", "us-ascii", "utf-8":
	default:
		return attachment{}, ErrInvalidContentType
	}
	return attachment{filename: filename, mediaType: mediaType, params: params, data: a.Data}, nil
}

// write writes the attachment as a part of a multipart/mixed message. The
// writer encodes the data as base64.
func (a attachment) write(mw *gomail.Writer) error {
	var h gomail.AttachmentHeader
	h.SetContentType(a.mediaType, a.params)
	if h.Get("Content-Type") == "" {
		return ErrInvalidContentType
	}
	h.SetFilename(a.filename)
	w, err := mw.CreateAttachment(h)
	if err != nil {
		return err
	}
	if _, err := w.Write(a.data); err != nil {
		return err
	}
	return w.Close()
}
