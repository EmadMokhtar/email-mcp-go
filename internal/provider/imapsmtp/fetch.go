package imapsmtp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
)

const (
	// defaultBodyChars is the length of a page of the body when the caller
	// gives none. maxBodyChars is the longest page.
	defaultBodyChars = 20000
	maxBodyChars     = 100000

	// maxHTMLBytes is the largest HTML part that the backend converts, in the
	// size that BODYSTRUCTURE gives. The parser of HTML builds a tree of the
	// whole document, which takes about 40 times the size of the document in
	// memory, and it does this for every page of the body. A larger HTML part is
	// not fetched, and not converted. HTML is never converted from its start
	// alone (see textFetchBytes).
	maxHTMLBytes = 4 << 20

	// referencesFetchBytes is the most bytes of the References header that the
	// backend reads. A longer header is cut, and the IDs that are complete are
	// used.
	referencesFetchBytes = 64 << 10

	// tailRunes is how many characters at the end of a cut text are not
	// trusted. A cut can fall inside a character, a quoted-printable escape or
	// an HTML tag, and then the end of the decoded text is wrong. The text
	// before it is the same as in the whole part.
	tailRunes = 8

	// plainBytesPerRune is the most bytes that one character of a text part
	// takes after the transfer encoding is removed (UTF-8).
	plainBytesPerRune = 4

	// fetchSlack is extra bytes, for the header of the part and the line
	// breaks that the estimates do not count.
	fetchSlack = 1024
)

// maxTextFetchBytes is the most bytes of one plain text part that the backend
// fetches, also when it needs the whole part. A longer part is cut, and the
// message says that the body is truncated. It is a variable only so that a
// test can lower it.
var maxTextFetchBytes int64 = 25 << 20

// textSections returns the part numbers of the body of a message: the first
// text/plain part and the first text/html part that are not attachments (see
// isAttachment). Each is nil when the message has no such part. A part without
// content is not used. It reads the structure only: no message data is
// needed, and attachments are never in the result.
//
// A message that is not multipart is part 1.
func textSections(bs imap.BodyStructure) (plain, html []int) {
	for _, l := range leaves(bs) {
		if isAttachment(l.part) || l.part.Size == 0 {
			continue
		}
		switch l.part.MediaType() {
		case "text/plain":
			if plain == nil {
				plain = l.path
			}
		case "text/html":
			if html == nil {
				html = l.path
			}
		}
	}
	return plain, html
}

// bodyPartAt returns what a fetch of the text at path needs. It returns nil
// when the structure has no part at path.
func bodyPartAt(bs imap.BodyStructure, path []int) *textPart {
	for _, l := range leaves(bs) {
		if slices.Equal(l.path, path) {
			return &textPart{
				path:     l.path,
				html:     l.part.MediaType() == "text/html",
				charset:  l.part.Params["charset"],
				encoding: l.part.Encoding,
				size:     l.part.Size,
			}
		}
	}
	return nil
}

// attachmentInfos lists the attachments of a message (see isAttachment), in
// the order of the message, from the structure only. The ID of an attachment
// is its part number, "2" or "1.3", the text that GetAttachment takes.
func attachmentInfos(bs imap.BodyStructure) []mail.AttachmentInfo {
	var out []mail.AttachmentInfo
	for _, l := range leaves(bs) {
		if isAttachment(l.part) {
			out = append(out, attachmentInfo(l))
		}
	}
	return out
}

func attachmentInfo(l leaf) mail.AttachmentInfo {
	return mail.AttachmentInfo{
		ID:          partPath(l.path),
		Filename:    partFilename(l.part),
		ContentType: mime.CleanUntrusted(l.part.MediaType()),
		Size:        decodedSize(l.part.Encoding, l.part.Size),
	}
}

// partFilename returns the file name of a part: the "filename" parameter of the
// Content-Disposition, or else the "name" parameter of the Content-Type. It
// reads RFC 2231 and RFC 2047 values like mime.ParseMessage does, and it is
// cleaned because it comes from a third party. It is empty when the part has
// no name.
func partFilename(p *imap.BodyStructureSinglePart) string {
	if d := p.Disposition(); d != nil {
		if name := mime.CleanUntrusted(mime.ParamText(d.Params, "filename")); name != "" {
			return name
		}
	}
	return mime.CleanUntrusted(mime.ParamText(p.Params, "name"))
}

// isBase64 reports whether encoding is the base64 transfer encoding.
func isBase64(encoding string) bool {
	return strings.EqualFold(strings.TrimSpace(encoding), "base64")
}

// isQuotedPrintable reports whether encoding is the quoted-printable transfer
// encoding.
func isQuotedPrintable(encoding string) bool {
	return strings.EqualFold(strings.TrimSpace(encoding), "quoted-printable")
}

// decodedSize estimates the size of a part after the transfer encoding is
// removed, from its size in BODYSTRUCTURE. Base64 text has lines of 76
// characters and a line break, so 78 bytes hold 57 bytes of data. For the
// other encodings the size is used as it is. For quoted-printable it is an
// upper bound.
func decodedSize(encoding string, size uint32) int64 {
	if isBase64(encoding) {
		return int64(size) * 57 / 78
	}
	return int64(size)
}

// minDecodedSize returns a size that the data of a part is not below, from its
// size in BODYSTRUCTURE. Base64 with lines of 76 characters has 57 bytes of
// data in 78 bytes. Shorter lines make the data a little smaller, so 56 is used.
// Quoted-printable can make a byte into 3 characters, and it breaks long lines
// with 3 more bytes, so 5 bytes of data are the least for 16 bytes. The other
// encodings do not change the size.
func minDecodedSize(encoding string, size uint32) int64 {
	switch {
	case isBase64(encoding):
		return int64(size) * 56 / 78
	case isQuotedPrintable(encoding):
		return int64(size) * 5 / 16
	}
	return int64(size)
}

// maxEncodedSize returns the most bytes that data of dataSize bytes takes in
// the given transfer encoding, with the slack for the estimates. Base64 is a
// third larger than its data and has line breaks, so it gets 50% more.
// Quoted-printable gets 3.5 times (3 for the worst case, and the line breaks).
func maxEncodedSize(encoding string, dataSize int64) int64 {
	n := addSat(dataSize, fetchSlack)
	switch {
	case isBase64(encoding):
		n = addSat(n, dataSize/2)
	case isQuotedPrintable(encoding):
		n = addSat(n, dataSize/2*5) // 3.5 times with the 1 time above
	}
	return n
}

// addSat adds two numbers that are not negative. It returns the largest int64
// when the sum does not fit.
func addSat(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// plusOne returns n+1, or n when n is the largest int64. It is the limit for
// reading "one byte more than allowed", to see that the data is too large.
func plusOne(n int64) int64 { return addSat(n, 1) }

// countingReader counts the bytes that it reads and keeps the first error
// that is not the end of the data. The decoders hide errors of their source, so
// this is how readAttachment tells a failed connection from damaged data.
type countingReader struct {
	r   io.Reader
	n   int64
	err error
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if err != nil && err != io.EOF && c.err == nil {
		c.err = err
	}
	return n, err
}

// errDamaged means that the data of an attachment is not valid for its
// transfer encoding.
var errDamaged = errors.New("imap: the attachment data is damaged")

// readAttachment reads the data of an attachment from r, the part as the
// server sends it, and removes the transfer encoding. It reads at most
// maxEncodedSize bytes from r and returns at most maxBytes of data. When there
// is more of either, the error wraps mail.ErrTooLarge. A negative maxBytes
// counts as zero.
//
// An error of r is returned as it is. Data that is not valid for its encoding
// gives an error that wraps errDamaged: a PDF with a missing piece is worse
// than no PDF.
func readAttachment(r io.Reader, encoding string, maxBytes int64) ([]byte, error) {
	maxBytes = max(maxBytes, 0)
	limit := maxEncodedSize(encoding, maxBytes)
	src := &countingReader{r: io.LimitReader(r, plusOne(limit))}
	data, err := io.ReadAll(io.LimitReader(mime.DecodeTransfer(encoding, src), plusOne(maxBytes)))

	switch {
	case src.err != nil:
		return nil, src.err
	case int64(len(data)) > maxBytes:
		return nil, fmt.Errorf("%w: the attachment is larger than %d bytes", mail.ErrTooLarge, maxBytes)
	case src.n > limit:
		return nil, fmt.Errorf("%w: the attachment is larger than %d bytes after decoding", mail.ErrTooLarge, maxBytes)
	case err != nil:
		return nil, fmt.Errorf("%w (%s)", errDamaged, err)
	}
	return data, nil
}

// referenceID matches one message ID in angle brackets.
var referenceID = regexp.MustCompile(`<[^<>\s]+>`)

// parseReferences reads the message IDs of a References header, as the server
// sends it with BODY[HEADER.FIELDS (References)]. The header can be folded
// over several lines, and it can be cut at the end. Only IDs in angle
// brackets are used, with the brackets. An ID that is cut is dropped. The IDs
// come from a third party, so they are cleaned.
func parseReferences(header []byte) []string {
	var out []string
	for _, id := range referenceID.FindAll(header, -1) {
		cleaned := mime.CleanUntrusted(string(id))
		if cleaned == "<>" || strings.ContainsFunc(cleaned, unicode.IsSpace) {
			continue
		}
		out = append(out, cleaned)
	}
	return out
}

// messageID writes a message ID of an envelope in angle brackets, like the
// IDs of the References header. go-imap gives it without the brackets. It
// returns an empty string for an empty ID or one that is not a single word.
func messageID(id string) string {
	id = mime.CleanUntrusted(strings.TrimSpace(id))
	id = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(id, "<"), ">"))
	if id == "" || strings.ContainsFunc(id, unicode.IsSpace) {
		return ""
	}
	return "<" + id + ">"
}

// pageRunes returns n characters of text from the character at offset, and
// whether more text follows. The offset and n count characters (runes), not
// bytes. An offset at or after the end gives an empty page.
func pageRunes(text string, offset, n int) (page string, more bool) {
	start := runeIndex(text, offset)
	if start >= len(text) {
		return "", false
	}
	end := start + runeIndex(text[start:], n)
	return text[start:end], end < len(text)
}

// runeIndex returns the byte index of the n-th character of s, or len(s) when
// s has fewer characters.
func runeIndex(s string, n int) int {
	i := 0
	for ; n > 0 && i < len(s); n-- {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return i
}

// dropLastRunes removes the last n characters of s.
func dropLastRunes(s string, n int) string {
	for ; n > 0 && s != ""; n-- {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// textFetchBytes returns how many bytes of the start of a text part to fetch to
// get at least want characters of text, in the worst case for the encoding.
//
// The text takes up to 4 bytes for a character in UTF-8, and no other charset
// takes more. Base64 makes 3 bytes into 4 and adds a line break to every 76
// characters. Quoted-printable makes a byte into 3 characters in the worst
// case, and it breaks long lines. The result is at most the size of the part
// and at most 25 MiB.
//
// An HTML part is fetched whole, because the text of HTML does not depend on
// the start alone. A style rule at the end of the document can hide an element
// at the start, and mime.HTMLToText leaves out hidden text. With only the
// start, text that the sender hid would show. The caller does not ask for an
// HTML part larger than maxHTMLBytes (see chooseBody), so the result for HTML
// is at most 4 MiB.
func textFetchBytes(p *textPart, want int) int64 {
	if p.html {
		return min(int64(p.size), maxHTMLBytes)
	}
	whole := min(int64(p.size), maxTextFetchBytes)
	// A character needs at least one byte, so more characters than the cap
	// are never useful. This also keeps the products below from overflowing.
	decoded := min(int64(max(want, 0)), maxTextFetchBytes) * plainBytesPerRune

	var n int64
	switch {
	case isBase64(p.encoding):
		n = (decoded + 2) / 3 * 4
		n += n / 38 // two bytes of line break for 76
	case isQuotedPrintable(p.encoding):
		n = decoded * 7 / 2
	default:
		n = decoded
	}
	return min(n+fetchSlack, whole)
}

// openRef reads a ref and checks the folder name in it. The ref comes from the
// caller, so it is not trusted. The error wraps mail.ErrNotFound.
func openRef(ref mail.Ref) (imapRef, error) {
	r, err := decodeRef(ref)
	if err != nil {
		return imapRef{}, err
	}
	if err := checkFolderName(r.Mailbox); err != nil {
		return imapRef{}, err
	}
	return r, nil
}

// examineRef opens the folder of a ref read-only and checks that the UIDs of
// the folder still mean what they meant when the ref was made.
//
// A different UIDVALIDITY means that the folder was rebuilt, so the UID of the
// ref can name another message. A folder that is gone is the same case. Both
// give an error that wraps mail.ErrRefStale. The ref is never used with
// another UIDVALIDITY: the wrong message must never be returned.
func examineRef(c *imapclient.Client, r imapRef) error {
	sel, err := examine(c, r.Mailbox)
	if err != nil {
		if errors.Is(err, mail.ErrNotFound) {
			return fmt.Errorf("%w: the folder of the message is gone, search again", mail.ErrRefStale)
		}
		return err
	}
	if sel.UIDValidity != r.UIDValidity {
		return fmt.Errorf("%w: the folder was rebuilt since the reference was made, search again", mail.ErrRefStale)
	}
	return nil
}

// errGone is the error for a message that is not in its folder any more.
func errGone() error {
	return fmt.Errorf("%w: the message is not in the folder any more", mail.ErrNotFound)
}

// fetchOne fetches the items in opts for one message and keeps them in memory.
// It is for small items (the structure, the envelope, a header). The data of
// a body part goes through readSection, which can limit it. It returns an
// error that wraps mail.ErrNotFound when the server has no such message.
func fetchOne(c *imapclient.Client, uid imap.UID, opts *imap.FetchOptions) (*imapclient.FetchMessageBuffer, error) {
	cmd := c.Fetch(imap.UIDSetNum(uid), opts)
	// Collect closes the command. This also covers an early return.
	defer func() { _ = cmd.Close() }()
	msgs, err := cmd.Collect()
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if m.UID == uid {
			return m, nil
		}
	}
	return nil, errGone()
}

// errNoData is the error for a part that the structure says has data but that
// the server answers with NIL.
func errNoData(section *imap.FetchItemBodySection) error {
	return fmt.Errorf("%w: the server sent no data for part %q", mail.ErrNotFound, partPath(section.Part))
}

// readSection sends one FETCH for one body section of one message and gives
// the data to consume as a reader. The reader ends with the data of the
// section. consume must not keep it. It returns false when the server sent no
// section.
//
// wantData is true when the structure of the message says that the section has
// data. A NIL answer (no data) is then an error that wraps mail.ErrNotFound,
// and not an empty result: the server contradicts itself, and an empty file
// must not look like a good one.
//
// The FETCH is closed before readSection returns, also when consume fails.
// The data that consume did not read is read and thrown away then, because
// the connection cannot be used for another command before that.
func readSection(c *imapclient.Client, uid imap.UID, section *imap.FetchItemBodySection, wantData bool, consume func(r io.Reader) error) (bool, error) {
	cmd := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{section},
	})
	// The pool needs the command closed before this function returns, also on
	// an error. The normal path reads the error of Close below.
	defer func() { _ = cmd.Close() }()

	found := false
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		for {
			item := msg.Next()
			if item == nil {
				break
			}
			// The command asks for one section, so any section is the answer.
			// Servers write the section a little differently (for example the
			// <origin> of a partial fetch).
			data, ok := item.(imapclient.FetchItemDataBodySection)
			if !ok {
				continue
			}
			found = true
			if data.Literal == nil { // NIL: no data
				if wantData {
					return found, errNoData(section)
				}
				continue
			}
			if err := consume(data.Literal); err != nil {
				return found, err
			}
		}
	}
	if err := cmd.Close(); err != nil {
		return false, err
	}
	return found, nil
}

// GetMessage returns one message: the headers, the attachment list and, as
// opts says, one page of the body.
//
// The ref must be one that Search or Changes returned. A ref of a folder that
// was rebuilt (a different UIDVALIDITY) or that is gone gives mail.ErrRefStale,
// and the message is not read, also when a message with the same UID exists
// now. A message that is not in the folder any more gives mail.ErrNotFound.
//
// The message data is read with BODY.PEEK in a read-only folder, so the
// message is not marked as read. The backend fetches the BODYSTRUCTURE
// first and reads only the text/plain or text/html part that the format needs.
// Of a text/plain part it reads only the start, as much as the page needs
// (BODY.PEEK[n]<0.size>). An HTML part is read whole (see textFetchBytes), and
// only if it is not larger than 4 MiB. It never fetches an attachment part.
//
// The formats:
//   - text: Text is the first text/plain part. A message without one gives the
//     text of the first text/html part (see mime.HTMLToText).
//   - html: HTML is the first text/html part, without what a reader cannot see
//     and cleaned (see mime.VisibleHTML). A message without one gives the
//     text/plain part in Text.
//   - none: only the headers and the attachment list.
//
// An empty format means text.
//
// An HTML part larger than 4 MiB is not converted, because the conversion needs
// memory of about 40 times the size, for every page. With the text format, the
// plain text part is used when there is one. Otherwise, and with the html
// format, the error wraps mail.ErrTooLarge, and the format "none" still works.
//
// The page starts at the character opts.Offset of the body after it is
// decoded and cleaned (for HTML, after the hidden parts are removed and it is
// sanitized) and has at most opts.MaxChars characters (default 20000, at most
// 100000). When more follows, Truncated is true and NextOffset is the offset
// of the next page. A plain text part longer than 25 MiB is read only up to
// that size. The page that reaches the end of what was read is Truncated too,
// and the page after it is empty and not Truncated.
//
// Snippet is not set: the body is in Text or HTML. ReplyTo is what the server
// puts in the ENVELOPE, and that is the From address when the message has no
// Reply-To header.
func (b *Backend) GetMessage(ctx context.Context, ref mail.Ref, opts mail.BodyOptions) (*mail.Message, error) {
	r, err := openRef(ref)
	if err != nil {
		return nil, err
	}
	format := opts.Format
	switch format {
	case "":
		format = mail.BodyText
	case mail.BodyText, mail.BodyHTML, mail.BodyNone:
	default:
		return nil, fmt.Errorf("imap: unknown body format %q", format)
	}
	maxChars := opts.MaxChars
	switch {
	case maxChars <= 0:
		maxChars = defaultBodyChars
	case maxChars > maxBodyChars:
		maxChars = maxBodyChars
	}
	// No part of a message has more characters than this, so a larger offset
	// is past the end of any body.
	offset := int(min(int64(max(opts.Offset, 0)), maxTextFetchBytes))

	var msg *mail.Message
	// The pool may run this function twice, so it starts from nothing.
	err = b.pool.with(ctx, func(c *imapclient.Client) error {
		msg = nil
		if err := examineRef(c, r); err != nil {
			return err
		}
		uid := imap.UID(r.UID)
		m, err := fetchOne(c, uid, &imap.FetchOptions{
			UID:           true,
			Flags:         true,
			Envelope:      true,
			InternalDate:  true,
			RFC822Size:    true,
			BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
			BodySection: []*imap.FetchItemBodySection{{
				Specifier:    imap.PartSpecifierHeader,
				HeaderFields: []string{"References"},
				Peek:         true,
				Partial:      &imap.SectionPartial{Offset: 0, Size: referencesFetchBytes},
			}},
		})
		if err != nil {
			return err
		}
		out := newMessage(r, m)
		if format != mail.BodyNone {
			if err := fillBody(c, uid, m.BodyStructure, format, offset, maxChars, out); err != nil {
				return err
			}
		}
		msg = out
		return nil
	})
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// newMessage makes a message from the fetched data, without the body.
func newMessage(r imapRef, m *imapclient.FetchMessageBuffer) *mail.Message {
	st := analyzeStructure(m.BodyStructure)
	s := newSummary(m)
	s.Ref = encodeRef(r)
	s.HasAttachments = st.hasAttachments
	s.HasInvite = st.hasInvite

	msg := &mail.Message{Summary: s, Attachments: attachmentInfos(m.BodyStructure)}
	if env := m.Envelope; env != nil {
		msg.Cc = convertAddresses(env.Cc)
		msg.ReplyTo = convertAddresses(env.ReplyTo)
		msg.MessageID = messageID(env.MessageID)
		if len(env.InReplyTo) > 0 {
			msg.InReplyTo = messageID(env.InReplyTo[0])
		}
	}
	// One section was asked for: the References header.
	if len(m.BodySection) > 0 {
		msg.References = parseReferences(m.BodySection[0].Bytes)
	}
	return msg
}

// chooseBody picks the part that the body comes from, how to turn its decoded
// text into the text of the page, and whether that text is HTML. It returns a
// nil part when the message has no body to show.
//
// An HTML part larger than maxHTMLBytes is not converted. The text format then
// uses the plain text part, if there is one. Without a plain text part, and for
// the html format, the error wraps mail.ErrTooLarge. The caller can still ask
// for the headers and the attachments with the format "none".
func chooseBody(bs imap.BodyStructure, format mail.BodyFormat) (part *textPart, render func(string) string, isHTML bool, err error) {
	plain, html := textSections(bs)
	var htmlPart *textPart
	if html != nil {
		htmlPart = bodyPartAt(bs, html)
	}
	tooLarge := func(what, advice string) error {
		return fmt.Errorf("%w: the HTML body is too large to %s (%d bytes, the limit is %d bytes); %s",
			mail.ErrTooLarge, what, htmlPart.size, maxHTMLBytes, advice)
	}

	switch {
	case format == mail.BodyHTML && htmlPart != nil:
		if htmlPart.size > maxHTMLBytes {
			advice := `use body_format "none" to read the headers and the attachments only`
			if plain != nil {
				advice = `use body_format "text" to read the plain text part, or "none" for the headers and the attachments only`
			}
			return nil, nil, false, tooLarge("clean", advice)
		}
		return htmlPart, func(s string) string { return mime.CleanUntrusted(mime.VisibleHTML(s)) }, true, nil
	case plain != nil:
		return bodyPartAt(bs, plain), mime.CleanUntrusted, false, nil
	case htmlPart != nil:
		if htmlPart.size > maxHTMLBytes {
			return nil, nil, false, tooLarge("convert", `use body_format "none" to read the headers and the attachments only`)
		}
		return htmlPart, mime.HTMLToText, false, nil
	}
	return nil, nil, false, nil
}

// fillBody fetches the start of the body part and sets the page in msg.
func fillBody(c *imapclient.Client, uid imap.UID, bs imap.BodyStructure, format mail.BodyFormat, offset, maxChars int, msg *mail.Message) error {
	part, render, isHTML, err := chooseBody(bs, format)
	if err != nil || part == nil {
		return err
	}
	// One more character than the page tells that more text follows.
	text, partial, err := fetchBodyText(c, uid, part, offset+maxChars+1, render)
	if err != nil {
		return err
	}
	page, more := pageRunes(text, offset, maxChars)
	// The text ends at the cap on the bytes of a text part, but the body goes on.
	// A page that reaches the end of this text says so, and the next page is
	// empty and ends the paging. (Text that ends before the page is not more.)
	if partial && page != "" {
		more = true
	}
	if isHTML {
		msg.HTML = page
	} else {
		msg.Text = page
	}
	if more {
		msg.Truncated = true
		msg.NextOffset = offset + utf8.RuneCountInString(page)
	}
	return nil
}

// fetchBodyText returns the text of a body part, made by render from the
// decoded part. The text has at least need characters, or it is all the text
// the backend reads of the part. partial is true when that is not the whole
// part, because the part is longer than the cap on the bytes (25 MiB).
//
// It fetches only the start of a plain text part (see textFetchBytes). When
// that has less than need characters of text (for example, because the part
// has many characters that are removed) and the part is longer, it fetches the
// whole part in a second command, up to the cap. It never fetches more than
// twice. An HTML part is fetched whole in the first command.
func fetchBodyText(c *imapclient.Client, uid imap.UID, p *textPart, need int, render func(string) string) (text string, partial bool, err error) {
	first := textFetchBytes(p, need+tailRunes)
	text, partial, err = fetchBodyStart(c, uid, p, first, render)
	if err != nil {
		return "", false, err
	}
	if whole := min(int64(p.size), maxTextFetchBytes); partial && utf8.RuneCountInString(text) < need && whole > first {
		text, partial, err = fetchBodyStart(c, uid, p, whole, render)
		if err != nil {
			return "", false, err
		}
	}
	return text, partial, nil
}

// fetchBodyStart fetches the first n bytes of a text part (BODY.PEEK[part]<0.n>)
// and returns the text that render makes from them. cut is true when the part
// is longer than the bytes fetched. Then the end of the text is not used (see
// tailRunes).
//
// An HTML part is only used whole: n must be its size. If the server sends more
// than that, the structure was wrong, and the error wraps mail.ErrTooLarge.
// The backend never makes text of the start of an HTML part.
func fetchBodyStart(c *imapclient.Client, uid imap.UID, p *textPart, n int64, render func(string) string) (text string, cut bool, err error) {
	var raw []byte
	found, err := readSection(c, uid, &imap.FetchItemBodySection{
		Part:    p.path,
		Peek:    true,
		Partial: &imap.SectionPartial{Offset: 0, Size: n},
	}, true, func(r io.Reader) error {
		var err error
		// The server should send at most n bytes. One that sends more is not
		// allowed to fill the memory. For HTML, one byte more shows that.
		raw, err = io.ReadAll(io.LimitReader(r, plusOne(n)))
		return err
	})
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, errGone()
	}
	if p.html && int64(len(raw)) > n {
		return "", false, fmt.Errorf("%w: the HTML part is larger than the %d bytes that the server said", mail.ErrTooLarge, n)
	}
	raw = raw[:min(int64(len(raw)), n)]

	cut = int64(len(raw)) >= n && n < int64(p.size)
	if cut && p.html {
		// The caller asks for the whole part, so this does not happen. If it
		// ever does, the cut text must not be converted.
		return "", false, fmt.Errorf("%w: only the start of the HTML part was read", mail.ErrTooLarge)
	}
	if cut && isBase64(p.encoding) {
		raw = cutBase64(raw)
	}
	mediaType := "text/plain"
	if p.html {
		mediaType = "text/html"
	}
	// DecodeText does not fail for damaged data. It fails only when reading
	// fails, and a byte slice always reads.
	decoded, _ := mime.DecodeText(mediaType, map[string]string{"charset": p.charset}, p.encoding, bytes.NewReader(raw))
	if cut {
		decoded = dropLastRunes(decoded, tailRunes)
	}
	return render(decoded), cut, nil
}

// GetRaw returns the whole message as the server stores it (RFC 5322).
//
// It asks for the size of the message first (RFC822.SIZE). A message larger
// than maxBytes gives mail.ErrTooLarge and is not fetched. The refs follow the
// rules of GetMessage. The message is read with BODY.PEEK, so it is not marked
// as read.
func (b *Backend) GetRaw(ctx context.Context, ref mail.Ref, maxBytes int64) ([]byte, error) {
	r, err := openRef(ref)
	if err != nil {
		return nil, err
	}
	maxBytes = max(maxBytes, 0)

	var raw []byte
	// The pool may run this function twice, so it starts from nothing.
	err = b.pool.with(ctx, func(c *imapclient.Client) error {
		raw = nil
		if err := examineRef(c, r); err != nil {
			return err
		}
		uid := imap.UID(r.UID)
		m, err := fetchOne(c, uid, &imap.FetchOptions{UID: true, RFC822Size: true})
		if err != nil {
			return err
		}
		if m.RFC822Size > maxBytes {
			return fmt.Errorf("%w: the message has %d bytes, the limit is %d", mail.ErrTooLarge, m.RFC822Size, maxBytes)
		}

		found, err := readSection(c, uid, &imap.FetchItemBodySection{Peek: true}, m.RFC822Size > 0, func(r io.Reader) error {
			var err error
			// One byte more than the limit shows that the message is larger
			// than the server said.
			raw, err = io.ReadAll(io.LimitReader(r, plusOne(maxBytes)))
			return err
		})
		if err != nil {
			return err
		}
		if !found {
			return errGone()
		}
		if int64(len(raw)) > maxBytes {
			return fmt.Errorf("%w: the message is larger than %d bytes", mail.ErrTooLarge, maxBytes)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// GetAttachment returns the data of one part of a message. The attachment ID
// is a part number that GetMessage lists, such as "2" or "1.3". It must be a
// part that is not multipart (a multipart part has no data of its own) and it
// must be in the structure of the message, else the error wraps
// mail.ErrNotFound. Nothing is fetched for an ID that does not exist.
//
// The limit is for the data after the transfer encoding is removed. The
// backend refuses an attachment that is clearly larger than maxBytes from the
// size in the structure, without fetching it. Otherwise it fetches the part with
// BODY.PEEK and reads no more than maxBytes of data (plus what the encoding
// adds). Data that is larger gives mail.ErrTooLarge. The refs follow the rules
// of GetMessage.
func (b *Backend) GetAttachment(ctx context.Context, ref mail.Ref, attachmentID string, maxBytes int64) (*mail.Attachment, error) {
	r, err := openRef(ref)
	if err != nil {
		return nil, err
	}
	maxBytes = max(maxBytes, 0)

	var att *mail.Attachment
	// The pool may run this function twice, so it starts from nothing.
	err = b.pool.with(ctx, func(c *imapclient.Client) error {
		att = nil
		if err := examineRef(c, r); err != nil {
			return err
		}
		uid := imap.UID(r.UID)
		m, err := fetchOne(c, uid, &imap.FetchOptions{UID: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true}})
		if err != nil {
			return err
		}
		l, ok := leafByID(m.BodyStructure, attachmentID)
		if !ok {
			return fmt.Errorf("%w: the message has no part %q", mail.ErrNotFound, attachmentID)
		}
		if minDecodedSize(l.part.Encoding, l.part.Size) > maxBytes {
			return fmt.Errorf("%w: the attachment is larger than %d bytes", mail.ErrTooLarge, maxBytes)
		}

		var data []byte
		found, err := readSection(c, uid, &imap.FetchItemBodySection{Part: l.path, Peek: true}, l.part.Size > 0, func(r io.Reader) error {
			var err error
			data, err = readAttachment(r, l.part.Encoding, maxBytes)
			return err
		})
		if err != nil {
			return err
		}
		if !found {
			return errGone()
		}
		info := attachmentInfo(l)
		info.Size = int64(len(data))
		att = &mail.Attachment{AttachmentInfo: info, Data: data}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return att, nil
}

// leafByID finds the part with the given ID (a part number such as "1.2") among
// the parts that are not multipart.
func leafByID(bs imap.BodyStructure, id string) (leaf, bool) {
	for _, l := range leaves(bs) {
		if partPath(l.path) == id {
			return l, true
		}
	}
	return leaf{}, false
}
