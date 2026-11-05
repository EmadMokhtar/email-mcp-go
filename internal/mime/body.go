package mime

import (
	"fmt"
	stdhtml "html"
	"io"
	stdmime "mime"
	"net/http"
	"strings"
	"time"
	"unicode"

	gomail "github.com/emersion/go-message/mail"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// MaxAttachmentBytes is the most attachment data that one message can have:
// 10 MB. It is the one place of this limit (spec section 10.2): the service uses
// it for GetAttachment and for the attachments of a forward, and the MCP layer
// for the attachments of a tool call.
const MaxAttachmentBytes = 10 << 20

// bodies are the text forms of a message body. html is empty when the message
// has no HTML form.
type bodies struct {
	text, html string
}

// composeBodies makes the bodies of a message from Compose. The plain text body
// is checked (ErrHiddenText). A message with HTML sends the HTML of outgoingHTML
// and the text form of that HTML, so the text that a preview shows is the text
// of what goes out. A forward adds the original.
//
// The HTML of Compose comes from the caller. If it is too complex to clean or
// to make text of (see ErrHTMLTooComplex), the error wraps that error. The HTML
// of the original does not stop a forward: see newForwarded. When nothing is
// left of HTML that had content, composeBodies returns ErrEmptyHTML.
func composeBodies(c mail.Compose, original *mail.Message) (bodies, error) {
	if err := checkVisible("body", c.Body); err != nil {
		return bodies{}, err
	}
	b := bodies{text: c.Body}
	if c.HTMLBody != "" {
		html, err := outgoingHTML(c.HTMLBody)
		if err != nil {
			return bodies{}, fmt.Errorf("mime: html body: %w", err)
		}
		if html == "" && strings.TrimSpace(c.HTMLBody) != "" {
			return bodies{}, ErrEmptyHTML
		}
		b = bodies{html: html}
	}
	if c.ForwardOf != nil {
		// The block of the forward is added after the HTML of the caller was
		// cleaned, so an element that is not closed cannot swallow it.
		f := newForwarded(original)
		if b.html == "" {
			b.text += f.plain()
		} else {
			b.html += f.html()
		}
	}
	if b.html != "" {
		// Made last, so it also has the block of a forward.
		text, err := HTMLToText(b.html)
		if err != nil {
			return bodies{}, fmt.Errorf("mime: html body: %w", err)
		}
		b.text = text
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
	text := CleanUntrusted(o.Text)
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

// displayAddress writes an address for people to read: "Name <address>". The
// address is the real one of the original, so a character that a reader cannot
// see is marked (see MarkInvisible).
func displayAddress(a mail.Address) string {
	address := MarkInvisible(a.Email)
	if a.Name == "" {
		return address
	}
	return a.Name + " <" + address + ">"
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

// MaxMediaTypeBytes is the longest media type of an attachment that Build writes,
// such as "application/pdf". A preview cuts a media type at the same length, so a
// type that Build accepts is always shown whole. The types of Office documents
// are long: the one for .pptx is 73 bytes.
const MaxMediaTypeBytes = 100

// OutgoingAttachments returns the attachments that Build writes for a message,
// with the file names and the content types that go out: the original ones for a
// forward that asks for them, then the new ones. The file name and the content
// type of an original attachment come from a third party, so they are cleaned
// (see cleanOriginalName and originalContentType). A file name that is empty,
// also after the cleaning, is "attachment". An empty content type is found from
// the data.
//
// Build uses this list, and a preview must use it too, so it shows what is sent.
// It does not check the values: see newAttachment.
func OutgoingAttachments(c mail.Compose, bc BuildContext) []mail.OutgoingAttachment {
	var out []mail.OutgoingAttachment
	if c.ForwardOf != nil && c.IncludeOriginalAttachments {
		for _, a := range bc.OriginalAttachments {
			out = append(out, mail.OutgoingAttachment{
				Filename: cleanOriginalName(a.Filename), ContentType: originalContentType(a.ContentType), Data: a.Data,
			})
		}
	}
	out = append(out, c.Attachments...)
	for i := range out {
		if out[i].Filename == "" {
			out[i].Filename = "attachment"
		}
		if out[i].ContentType == "" {
			out[i].ContentType = http.DetectContentType(out[i].Data)
		}
	}
	return out
}

// AttachmentType returns the media type that Build writes for an attachment of
// OutgoingAttachments, or "" for one that Build refuses.
func AttachmentType(a mail.OutgoingAttachment) string {
	mediaType, _, err := parseContentType(a.ContentType)
	if err != nil {
		return ""
	}
	return mediaType
}

// originalContentType cleans the content type of an attachment of the original
// message, which a third party wrote. It must not stop a forward, so a media type
// that is over the limit is not refused. The attachment goes out as
// application/octet-stream, and its data stays the same.
func originalContentType(contentType string) string {
	mediaType, _, err := stdmime.ParseMediaType(contentType)
	if err == nil && len(mediaType) > MaxMediaTypeBytes {
		return "application/octet-stream"
	}
	return contentType
}

// collectAttachments returns the checked attachments of the message (see
// OutgoingAttachments). It returns mail.ErrTooLarge when the data is over
// MaxAttachmentBytes in all.
func collectAttachments(c mail.Compose, bc BuildContext) ([]attachment, error) {
	in := OutgoingAttachments(c, bc)

	total := 0
	for _, a := range in {
		total += len(a.Data)
	}
	if total > MaxAttachmentBytes {
		return nil, fmt.Errorf("%w: attachments are over %d bytes", mail.ErrTooLarge, MaxAttachmentBytes)
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

// isLineSeparator reports whether r is a line or paragraph separator (Zl, Zp).
func isLineSeparator(r rune) bool {
	return unicode.In(r, unicode.Zl, unicode.Zp)
}

// cleanOriginalName cleans the file name of an attachment of the original
// message, which a third party wrote. It turns a line or paragraph separator
// into a space, and then does what cleanOriginal does. The result passes the
// checks of newAttachment.
func cleanOriginalName(s string) string {
	return cleanOriginal(strings.Map(func(r rune) rune {
		if isLineSeparator(r) {
			return ' '
		}
		return r
	}, s))
}

// newAttachment checks the file name and the content type of an attachment of
// OutgoingAttachments. A file name with a line or paragraph separator, or with a
// character that CleanUntrusted removes, is refused with ErrInvalidFilename: a
// character that hides or reorders text could make a name look like another one.
func newAttachment(a mail.OutgoingAttachment) (attachment, error) {
	if err := checkValue("attachment file name", a.Filename); err != nil {
		return attachment{}, err
	}
	if strings.ContainsFunc(a.Filename, isLineSeparator) || HiddenRunes(a.Filename) > 0 {
		return attachment{}, ErrInvalidFilename
	}

	if err := checkValue("attachment content type", a.ContentType); err != nil {
		return attachment{}, err
	}
	mediaType, charset, err := parseContentType(a.ContentType)
	if err != nil {
		return attachment{}, err
	}
	kept := map[string]string{}
	if charset != "" {
		kept["charset"] = charset
	}
	return attachment{filename: a.Filename, mediaType: mediaType, params: kept, data: a.Data}, nil
}

// parseContentType returns the media type of a content type, and its charset in
// lower case when it has one. These are all of the content type that goes out: a
// preview shows the media type, and no preview shows other parameters, so a
// value in them would leave unseen.
//
// The content type must be one type with a slash and parameters that are valid,
// and the media type must be at most MaxMediaTypeBytes long. It must not be
// multipart (the writer would treat the data as parts), and its charset, if any,
// must be UTF-8 or US-ASCII, because the writer refuses any other. Any other
// content type gives ErrInvalidContentType.
func parseContentType(contentType string) (mediaType, charset string, err error) {
	mediaType, params, err := stdmime.ParseMediaType(contentType)
	if err != nil || !strings.Contains(mediaType, "/") || strings.HasPrefix(mediaType, "multipart/") ||
		len(mediaType) > MaxMediaTypeBytes {
		return "", "", ErrInvalidContentType
	}
	charset = strings.ToLower(params["charset"])
	switch charset {
	case "", "us-ascii", "utf-8":
		return mediaType, charset, nil
	}
	return "", "", ErrInvalidContentType
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
