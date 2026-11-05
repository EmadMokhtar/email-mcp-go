package mime

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"io"
	"math"
	stdmime "mime"
	"mime/quotedprintable"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// The decoders in this file read mail written by other people. Real mail is
// often damaged: it names a wrong or unknown charset, it breaks base64, or it
// leaves out the closing boundary of a multipart body. The rule is to decode
// as much as possible and to return it. A damaged part never makes the whole
// message fail.
//
// The text these functions return is valid UTF-8. It is not cleaned. Callers
// pass it to CleanUntrusted or HTMLToText.

const (
	// maxParts is the most parts ParseMessage reads. A message with more
	// parts is cut off, and the parts read so far are returned.
	maxParts = 10000
	// maxDepth is the deepest multipart nesting that ParseMessage follows.
	maxDepth = 32
	// maxHeaderBytes is the size limit of one header block, of the message or
	// of a part.
	maxHeaderBytes = 1 << 20
	// maxHeaderFields is the most fields in one header block.
	maxHeaderFields = 10000
	// maxBoundary is the longest multipart boundary that is used. RFC 2046
	// allows 70 characters.
	maxBoundary = 200
	// qpBufSize is the longest line of quoted-printable text that is decoded.
	// The text is cut at a longer line.
	qpBufSize = 256 << 10
)

// DecodeText decodes the body of one text part into a string.
//
// transferEncoding is the Content-Transfer-Encoding of the part. It is
// matched without regard to case: "base64", "quoted-printable", "7bit",
// "8bit" and "binary" are known. An empty or unknown value means that the
// body is not encoded.
//
// params holds the parameters of the Content-Type header. The "charset"
// parameter says how the bytes map to characters. Charset names are matched
// without regard to case. The charset is used only when contentType is empty
// or a text type. If the charset is missing or unknown, the bytes are used as
// they are, and each byte that is not valid UTF-8 becomes U+FFFD.
//
// A part that is damaged half way (for example bad base64) gives the text
// decoded up to that point, and no error. The error is not nil only when
// reading r fails. The text read before the failure is returned with it.
//
// DecodeText reads r to its end and keeps the whole text in memory. The caller
// must pass a bounded reader.
func DecodeText(contentType string, params map[string]string, transferEncoding string, r io.Reader) (string, error) {
	return decodeText(contentType, params, transferEncoding, r, 0)
}

// decodeText is DecodeText with a limit. When maxRunes is above zero, the
// result has at most maxRunes characters, and not much more than that is
// read.
func decodeText(contentType string, params map[string]string, transferEncoding string, r io.Reader, maxRunes int) (string, error) {
	src := &errTracker{r: r}
	body := transferDecoder(transferEncoding, src)
	if isTextOrEmpty(contentType) {
		body = charsetDecoder(params["charset"], body)
	}
	if n := byteLimit(maxRunes); n > 0 {
		body = io.LimitReader(body, n)
	}

	// An error here comes from damaged data. Keep what was decoded.
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(body)

	text := strings.ToValidUTF8(buf.String(), string(utf8.RuneError))
	return truncateRunes(text, maxRunes), src.err
}

// errTracker remembers the first read error of a reader that is a real
// failure. End of input, and input that ends early (io.ErrUnexpectedEOF, which
// the multipart reader reports for a part without a closing boundary), are
// damaged mail, not failures.
type errTracker struct {
	r   io.Reader
	err error
}

func (t *errTracker) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if err != nil && t.err == nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		t.err = err
	}
	return n, err
}

// DecodeTransfer returns a reader that removes the Content-Transfer-Encoding
// from r. It is for data that is not text, such as an attachment: unlike
// DecodeText, it does not change the bytes in any other way.
//
// The encoding is matched without regard to case. "base64" and
// "quoted-printable" are decoded. Anything else ("7bit", "8bit", "binary",
// empty or unknown) is not encoded, so r is read as it is.
//
// Base64 is read like DecodeText reads it: white space and "=" padding are
// ignored, so data with missing or wrong padding still decodes. Data that is
// not valid for the encoding (for example a character that is not in the
// base64 alphabet) gives an error when it is read, after the bytes that were
// decoded before it. DecodeText hides this error, and a caller that wants a
// complete file must not.
//
// The reader reads its input in pieces, but it does not limit it. The caller
// limits the output (for example with io.LimitReader).
func DecodeTransfer(encoding string, r io.Reader) io.Reader {
	return transferDecoder(encoding, r)
}

// transferDecoder undoes the Content-Transfer-Encoding of a body.
func transferDecoder(encoding string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.RawStdEncoding, base64Cleaner{r})
	case "quoted-printable":
		// The reader of the standard library fails on a line that is longer
		// than its buffer (4096 bytes), and the text would be cut there.
		// Mail has such lines, so give it a bigger buffer.
		return quotedprintable.NewReader(bufio.NewReaderSize(r, qpBufSize))
	default:
		// 7bit, 8bit, binary, empty and unknown values: not encoded.
		return r
	}
}

// base64Cleaner removes the bytes that mail puts into base64 but that are
// not part of the data: spaces, tabs, line breaks and the "=" padding. The
// decoder that follows does not need padding. This way, data whose padding is
// missing or wrong still decodes.
type base64Cleaner struct{ r io.Reader }

func (c base64Cleaner) Read(p []byte) (int, error) {
	for {
		n, err := c.r.Read(p)
		kept := 0
		for _, b := range p[:n] {
			switch b {
			case ' ', '\t', '\r', '\n', '=':
			default:
				p[kept] = b
				kept++
			}
		}
		if kept > 0 || err != nil || n == 0 {
			return kept, err
		}
	}
}

// charsetDecoder converts a body from the named charset to UTF-8. When the
// charset is unknown, the body is returned as it is.
func charsetDecoder(label string, r io.Reader) io.Reader {
	label = strings.TrimSpace(label)
	switch strings.ToLower(label) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		// No conversion. Mail that says "us-ascii" often holds UTF-8, and
		// this way it is not damaged.
		return r
	}
	converted, err := charset.Reader(label, r)
	if err != nil {
		return r
	}
	return converted
}

func isTextOrEmpty(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	return contentType == "" || strings.HasPrefix(contentType, "text/")
}

// byteLimit returns how many bytes are enough to hold maxRunes characters.
// It returns 0 when there is no limit.
func byteLimit(maxRunes int) int64 {
	if maxRunes <= 0 || int64(maxRunes) > math.MaxInt64/utf8.UTFMax {
		return 0
	}
	return int64(maxRunes) * utf8.UTFMax
}

// truncateRunes cuts s to at most n characters. It does nothing when n is
// zero or negative. s must be valid UTF-8.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return s
	}
	end := 0
	for i := 0; i < n && end < len(s); i++ {
		_, size := utf8.DecodeRuneInString(s[end:])
		end += size
	}
	return s[:end]
}

// headerDecoder decodes RFC 2047 encoded words ("=?UTF-8?B?...?=").
var headerDecoder = &stdmime.WordDecoder{CharsetReader: charset.Reader}

// DecodeHeaderValue decodes the RFC 2047 encoded words in a header value. For
// example, "=?UTF-8?B?w5xiZXI=?=" is the UTF-8 bytes of a word that starts
// with a U with a diaeresis (two dots above). When the value cannot be
// decoded (for example its charset is unknown), the raw value is returned.
// The result is always valid UTF-8. It is not cleaned.
func DecodeHeaderValue(v string) string {
	decoded, err := headerDecoder.DecodeHeader(v)
	if err != nil {
		decoded = v
	}
	return strings.ToValidUTF8(decoded, string(utf8.RuneError))
}

// Parsed is the content of a whole message.
type Parsed struct {
	// Shown holds the text parts that a mail client shows, in the order of the
	// message (see LayoutOf). Each is cut to the maxText argument of ParseMessage.
	// They are decoded, but not cleaned. To show a message to a person, use this
	// or ShownText, and not Text or HTML.
	Shown []ShownPart
	// Text is the first text/plain part and HTML is the first text/html part of
	// the body and its other versions. A message that Build made has one of each.
	// Each is cut to the maxText argument of ParseMessage. They are decoded, but
	// not cleaned.
	Text, HTML string
	// Attachments describes the parts that are attachments, without their
	// data. The ID of each is its MIME part path, as IMAP numbers parts: "2"
	// is the second part, and "1.3" is the third part of the first part. The
	// only part of a message that is not multipart is "1".
	Attachments []mail.AttachmentInfo
	// HasInvite is true when a part is text/calendar, or has a "method"
	// parameter in its Content-Type.
	HasInvite bool
	// Incomplete is true when the walk skipped something: a header over its
	// limit (a part that is skipped, or the rest of the message header that is
	// taken as the body), too many parts, nesting that is too deep, a multipart
	// part without a usable boundary, a read error or a recovered panic. The
	// parts that were skipped may be attachments that are not in Attachments.
	Incomplete bool
}

// ShownPart is one text part that a mail client shows.
type ShownPart struct {
	// ID is the part path, like the ID of an attachment.
	ID string
	// Text is the decoded part: HTML when the HTML field is true, else plain text.
	Text string
	HTML bool
}

// ShownText returns all that a mail client shows of the message, as plain text.
// The error wraps ErrHTMLTooComplex when an HTML part cannot be turned into text.
func (p *Parsed) ShownText() (string, error) {
	texts := make([]string, 0, len(p.Shown))
	for _, part := range p.Shown {
		text := part.Text
		if part.HTML {
			var err error
			if text, err = HTMLToText(text); err != nil {
				return "", err
			}
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, PartSeparator), nil
}

// ParseMessage reads a whole message (header and body) and finds its text,
// its HTML and its attachments. LayoutOf says which part is what, so the result
// is the same as for a backend that reads the structure of a message.
//
// Names of attachments are cleaned with CleanUntrusted. The size of an attachment
// is its size after the transfer encoding is removed.
//
// maxText limits each text part to that many characters. Zero or less means no
// limit.
//
// The work is bounded. ParseMessage reads at most 10000 parts, follows
// multipart nesting to a fixed depth, and reads at most 1 MiB and 10000 fields
// of each header block. A part whose header is over a limit is skipped (it
// keeps its number). A header line that is not valid is skipped. Damaged mail
// does not make ParseMessage fail, and it does not panic: it returns what it
// decoded. When it skipped something, Parsed.Incomplete is true. The error is
// not nil only when reading r fails. The Parsed value is then still returned,
// with what was decoded before the failure.
func ParseMessage(r io.Reader, maxText int) (*Parsed, error) {
	parsed, _, err := parseMessage(r, maxText)
	return parsed, err
}

// parseMessage is ParseMessage. It also returns the value of a panic that it
// recovered from, or nil. Tests use this to see panics that ParseMessage
// hides on purpose.
func parseMessage(r io.Reader, maxText int) (parsed *Parsed, recovered any, err error) {
	src := &errTracker{r: r}
	w := &walker{maxText: maxText, out: &Parsed{}}
	func() {
		defer func() { recovered = recover() }()
		w.run(src)
	}()
	w.finish()
	if recovered != nil || src.err != nil {
		w.out.Incomplete = true
	}
	return w.out, recovered, src.err
}

// walker goes through the parts of one message. The walk builds a tree of the
// parts, and finish fills out from it.
type walker struct {
	maxText int
	out     *Parsed
	// parts counts the parts found, not counting the message itself.
	parts int
	// root is the message. A part joins the tree before it is read, so what was
	// read before a failure is used.
	root *PartNode
}

func (w *walker) run(src io.Reader) {
	br := bufio.NewReader(src)
	// If the header is too big, the fields read so far are used and the rest
	// is taken as the body.
	h, end := readHeader(newLineReader(br), nil)
	if end == headerTooBig {
		w.out.Incomplete = true
	}
	w.part(h, br, "", 0, nil)
}

// finish fills out from the tree of the parts.
func (w *walker) finish() {
	layout := LayoutOf(w.root)
	body := map[*PartNode]bool{}
	for _, n := range layout.Shown {
		w.out.Shown = append(w.out.Shown, ShownPart{ID: n.ID, Text: n.text, HTML: n.MediaType == "text/html"})
		body[n] = true
	}
	for _, n := range layout.Others {
		body[n] = true
	}
	for _, n := range layout.Attachments {
		w.out.Attachments = append(w.out.Attachments, mail.AttachmentInfo{
			ID:          n.ID,
			Filename:    n.filename,
			ContentType: n.MediaType,
			Size:        n.size,
		})
	}
	if w.root != nil {
		w.setFirstOfEachKind(w.root, body)
	}
}

// setFirstOfEachKind sets Text and HTML from the parts of the body below n, in the
// order of the message.
func (w *walker) setFirstOfEachKind(n *PartNode, body map[*PartNode]bool) {
	for _, child := range n.Children {
		w.setFirstOfEachKind(child, body)
	}
	switch {
	case !body[n]:
	case n.MediaType == "text/plain" && w.out.Text == "":
		w.out.Text = n.text
	case n.MediaType == "text/html" && w.out.HTML == "":
		w.out.HTML = n.text
	}
}

// part handles one part. id is its part path, empty for the message itself, and
// parent is nil for the message itself. It returns false when the whole walk must
// stop.
func (w *walker) part(h textproto.Header, body io.Reader, id string, depth int, parent *PartNode) bool {
	mediaType, params := contentTypeOf(h)
	if _, hasMethod := params["method"]; hasMethod || mediaType == "text/calendar" {
		w.out.HasInvite = true
	}

	n := &PartNode{ID: id, MediaType: mediaType}
	if parent == nil {
		w.root = n
	} else {
		parent.Children = append(parent.Children, n)
	}
	if isMultipart(n) {
		n.Start = params["start"]
		return w.multipart(n, params["boundary"], body, depth)
	}
	if n.ID == "" {
		n.ID = "1"
	}
	w.leaf(n, h, params, body)
	return true
}

func (w *walker) multipart(n *PartNode, boundary string, body io.Reader, depth int) bool {
	if depth >= maxDepth || boundary == "" || len(boundary) > maxBoundary {
		w.out.Incomplete = true
		return true
	}
	mr := newMultipartReader(body, boundary)
	for i := 1; ; i++ {
		h, partBody, tooBig, ok := mr.next()
		if !ok {
			return true
		}
		w.parts++
		if w.parts > maxParts {
			w.out.Incomplete = true
			return false
		}
		if tooBig {
			// The part keeps its place in the numbering, but it is not read.
			w.out.Incomplete = true
			continue
		}
		childID := strconv.Itoa(i)
		if n.ID != "" {
			childID = n.ID + "." + childID
		}
		if !w.part(h, partBody, childID, depth+1, n) {
			return false
		}
	}
}

// leaf reads a part that is not multipart. Which parts are used is decided after
// the walk, so a part that can be the body is decoded here.
func (w *walker) leaf(n *PartNode, h textproto.Header, params map[string]string, body io.Reader) {
	encoding := h.Get("Content-Transfer-Encoding")
	disposition, _ := parseMedia(h.Get("Content-Disposition"))
	n.Attachment = disposition == "attachment"
	n.ContentID = h.Get("Content-Id")
	n.filename = CleanUntrusted(filenameOf(h))
	n.Named = n.filename != ""

	raw := &byteCounter{r: body}
	switch {
	case n.Attachment || !isBodyType(n.MediaType):
		// Count the bytes without keeping them.
		n.size, _ = io.Copy(io.Discard, transferDecoder(encoding, raw))
	case n.Named:
		// This part can be the body or an attachment, so it needs its text and
		// its size.
		data, _ := io.ReadAll(raw)
		n.text, _ = decodeText(n.MediaType, params, encoding, bytes.NewReader(data), w.maxText)
		n.size, _ = io.Copy(io.Discard, transferDecoder(encoding, bytes.NewReader(data)))
	default:
		n.text, _ = decodeText(n.MediaType, params, encoding, raw, w.maxText)
	}
	n.Empty = raw.n == 0
}

// byteCounter counts the bytes read from r.
type byteCounter struct {
	r io.Reader
	n int64
}

func (c *byteCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// filenameOf returns the decoded file name of a part, or an empty string.
// The library reads RFC 2231 and RFC 2047 names, and falls back to the "name"
// parameter of the Content-Type. It gives up on some names that mail has: a
// charset other than UTF-8 in RFC 2231, an unquoted name with a space. For
// these, and when its result holds U+FFFD, the parameters are read again
// without strict rules (see lenientFilename).
func filenameOf(h textproto.Header) string {
	ah := gomail.AttachmentHeader{Header: message.Header{Header: h}}
	name, err := ah.Filename()
	name = strings.ToValidUTF8(name, string(utf8.RuneError))
	if err == nil && name != "" && !strings.ContainsRune(name, utf8.RuneError) {
		return name
	}
	if lenient := lenientFilename(h); lenient != "" && (name == "" || !strings.ContainsRune(lenient, utf8.RuneError)) {
		return lenient
	}
	return name
}

// mediaTypePattern is what a media type may look like (RFC 6838). A media
// type goes back to the caller, and it comes from a third party. This keeps
// everything else out.
var mediaTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,126}/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$`)

// contentTypeOf returns the lower case media type of a part and its
// parameters. A part without a Content-Type is text/plain (RFC 2045). A media
// type that is not valid becomes application/octet-stream.
func contentTypeOf(h textproto.Header) (string, map[string]string) {
	value := h.Get("Content-Type")
	if strings.TrimSpace(value) == "" {
		return "text/plain", nil
	}
	mediaType, params := parseMedia(value)
	if !mediaTypePattern.MatchString(mediaType) {
		mediaType = "application/octet-stream"
	}
	return mediaType, params
}

// parseMedia splits a header value such as "text/plain; charset=utf-8" into
// the lower case first word and the parameters. When the value is damaged
// (a missing semicolon, an unquoted boundary with an "=" in it), it still
// returns the first word and the parameters that can be read.
func parseMedia(value string) (string, map[string]string) {
	mediaType, params, err := stdmime.ParseMediaType(value)
	if err == nil {
		return mediaType, params
	}
	first := value
	if i := strings.IndexAny(first, "; \t"); i >= 0 {
		first = first[:i]
	}
	return strings.ToLower(strings.TrimSpace(first)), rawParams(value, true)
}
