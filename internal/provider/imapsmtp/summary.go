package imapsmtp

import (
	"bytes"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
)

const (
	// snippetFetchBytes is how much of a text/plain part the backend fetches to
	// make the snippet. It is the encoded size, before the transfer encoding is
	// removed. An HTML part is fetched whole (see maxSnippetHTMLBytes).
	snippetFetchBytes = 2048
	// snippetRunes is the longest snippet, in characters.
	snippetRunes = 200
	// maxSnippetHTMLBytes is the largest HTML part that a snippet is made from, in
	// the size that BODYSTRUCTURE gives. A message with a larger HTML part has no
	// snippet. The whole part is needed, because a style rule at the end can hide
	// text at the start (see textFetchBytes). A list makes a snippet for every
	// message, so this is smaller than maxHTMLBytes.
	maxSnippetHTMLBytes = 1 << 20
	// snippetHTMLBudget is the most bytes of HTML parts that one list fetches for
	// its snippets. When the parts of the next message do not fit, that message
	// has no snippet. This is the limit on the memory and the traffic of a list,
	// because a sender chooses the size of a part.
	snippetHTMLBudget = 8 << 20
)

// textPart is a text part of a message: what a fetch of its text needs.
type textPart struct {
	path     []int // the IMAP part number, for example [1 2] for "1.2"
	html     bool  // text/html. Otherwise text/plain.
	charset  string
	encoding string // Content-Transfer-Encoding
	size     uint32 // the size in bytes before the transfer encoding is removed
}

// section returns the part number as the text used in a FETCH, "1.2".
func (p *textPart) section() string { return partPath(p.path) }

// partPath writes a part number as the text used in a FETCH, "1.2".
func partPath(path []int) string {
	parts := make([]string, len(path))
	for i, n := range path {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// leaf is a part that is not multipart, with its part number.
type leaf struct {
	path []int
	part *imap.BodyStructureSinglePart
}

// leaves returns the parts of a message that are not multipart, in the order
// of the message. A message inside the message (message/rfc822) is one part:
// its own parts are not searched. A message that is not multipart is part 1.
func leaves(bs imap.BodyStructure) []leaf {
	if bs == nil {
		return nil
	}
	var out []leaf
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		if single, ok := part.(*imap.BodyStructureSinglePart); ok {
			out = append(out, leaf{path: slices.Clone(path), part: single})
		}
		return true // multipart: look at its children
	})
	return out
}

// structure is what the BODYSTRUCTURE of a message says about it.
type structure struct {
	hasAttachments bool
	hasInvite      bool
	// shown holds the text parts that a mail client shows, in the order of the
	// message, and others holds the other versions of the body (see
	// mime.LayoutOf). All have content.
	shown, others []*textPart
	// attachments holds the attachments, in the order of the message.
	attachments []leaf
}

// analyzeStructure reads the BODYSTRUCTURE of a message. mime.LayoutOf says what
// each part is, and this is the one rule for HasAttachments of a summary, for the
// list of attachments of a message, and for the body. A part is an invite when it
// is text/calendar. Messages inside the message (message/rfc822) are not searched.
func analyzeStructure(bs imap.BodyStructure) structure {
	var s structure
	byID := map[string]leaf{}
	for _, l := range leaves(bs) {
		byID[partPath(l.path)] = l
		if l.part.MediaType() == "text/calendar" {
			s.hasInvite = true
		}
	}
	layout := mime.LayoutOf(partTree(bs, nil))
	for _, n := range layout.Shown {
		s.shown = append(s.shown, newTextPart(byID[n.ID]))
	}
	for _, n := range layout.Others {
		s.others = append(s.others, newTextPart(byID[n.ID]))
	}
	for _, n := range layout.Attachments {
		s.attachments = append(s.attachments, byID[n.ID])
	}
	s.hasAttachments = len(s.attachments) > 0
	return s
}

// firstText returns the first text that a mail client shows, which the snippet
// comes from. It is nil when the body is empty.
func (s structure) firstText() *textPart {
	if len(s.shown) == 0 {
		return nil
	}
	return s.shown[0]
}

// partTree describes the part at path for mime.LayoutOf. path is nil for the
// message. A message that is not multipart is part 1.
func partTree(bs imap.BodyStructure, path []int) *mime.PartNode {
	switch p := bs.(type) {
	case *imap.BodyStructureSinglePart:
		if path == nil {
			path = []int{1}
		}
		disposition := p.Disposition()
		return &mime.PartNode{
			ID:         partPath(path),
			MediaType:  p.MediaType(),
			Attachment: disposition != nil && strings.EqualFold(disposition.Value, "attachment"),
			Named:      partFilename(p) != "",
			Empty:      p.Size == 0,
			ContentID:  p.ID,
		}
	case *imap.BodyStructureMultiPart:
		n := &mime.PartNode{ID: partPath(path), MediaType: p.MediaType()}
		if p.Extended != nil {
			n.Start = p.Extended.Params["start"]
		}
		for i, child := range p.Children {
			n.Children = append(n.Children, partTree(child, append(slices.Clone(path), i+1)))
		}
		return n
	}
	return nil
}

// newTextPart returns what a fetch of the text of a part needs.
func newTextPart(l leaf) *textPart {
	return &textPart{
		path:     l.path,
		html:     l.part.MediaType() == "text/html",
		charset:  l.part.Params["charset"],
		encoding: l.part.Encoding,
		size:     l.part.Size,
	}
}

// fetchSummaries returns the summaries of the messages with the given UIDs, in
// the same order as uids. A message that is gone (deleted since the search) is
// left out.
//
// It asks for the envelope, flags, size, date and BODYSTRUCTURE of all
// messages in one FETCH. Then it fetches the text of each message for the
// snippet (see planSnippets), with one FETCH for all messages that need the same
// fetch, so the number of commands does not grow with the number of messages.
// Everything is read with PEEK, so no message is marked as read.
//
// A snippet that cannot be fetched or decoded is empty. It does not make the
// call fail, unless the connection failed.
func fetchSummaries(c *imapclient.Client, mailbox string, uidValidity uint32, uids []imap.UID) ([]mail.Summary, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	// Collect reads the whole answer and closes the command.
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID:           true,
		Flags:         true,
		Envelope:      true,
		InternalDate:  true,
		RFC822Size:    true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		return nil, err
	}

	byUID := make(map[imap.UID]*imapclient.FetchMessageBuffer, len(msgs))
	structures := make(map[imap.UID]structure, len(msgs))
	for _, m := range msgs {
		if m.UID == 0 {
			continue
		}
		byUID[m.UID] = m
		structures[m.UID] = analyzeStructure(m.BodyStructure)
	}

	snippets := map[imap.UID]string{}
	for _, f := range planSnippets(uids, structures) {
		bodies, err := fetchTextStart(c, f.uids, f.path, f.size)
		if err != nil {
			return nil, err
		}
		for uid, raw := range bodies {
			snippets[uid] = makeSnippet(structures[uid].firstText(), raw)
		}
	}

	out := make([]mail.Summary, 0, len(uids))
	for _, uid := range uids {
		m, ok := byUID[uid]
		if !ok {
			continue
		}
		st := structures[uid]
		s := newSummary(m)
		s.Ref = encodeRef(imapRef{Mailbox: mailbox, UIDValidity: uidValidity, UID: uint32(uid)})
		s.HasAttachments = st.hasAttachments
		s.HasInvite = st.hasInvite
		s.Snippet = snippets[uid]
		out = append(out, s)
	}
	return out, nil
}

// snippetFetch is one FETCH for the snippets of a list: the first size bytes of
// the part at path, of each message in uids.
type snippetFetch struct {
	path []int
	size int64
	uids []imap.UID
}

// planSnippets decides what to fetch for the snippets of the messages in uids.
// One fetch can only ask for one part number and one size, so the messages are
// grouped by these.
//
// Of a text/plain part, the start is enough. An HTML part is used whole or not at
// all, because HTML is never converted from its start alone. A part larger than
// maxSnippetHTMLBytes is left out, and so is a part that does not fit in
// snippetHTMLBudget. The budget goes to the messages in the order of uids, so the
// first messages of the list get their snippet. A message that is left out does
// not fall back to its text/plain part, which a mail client does not show.
func planSnippets(uids []imap.UID, structures map[imap.UID]structure) []*snippetFetch {
	budget := int64(snippetHTMLBudget)
	var plan []*snippetFetch
	fetches := map[string]*snippetFetch{}
	for _, uid := range uids {
		p := structures[uid].firstText()
		if p == nil {
			continue
		}
		size := int64(snippetFetchBytes)
		if p.html {
			if p.size > maxSnippetHTMLBytes || int64(p.size) > budget {
				continue
			}
			budget -= int64(p.size)
			// One byte more shows a part that is larger than the structure said.
			size = maxSnippetHTMLBytes + 1
		}
		key := p.section() + "/" + strconv.FormatInt(size, 10)
		f := fetches[key]
		if f == nil {
			f = &snippetFetch{path: p.path, size: size}
			fetches[key] = f
			plan = append(plan, f)
		}
		f.uids = append(f.uids, uid)
	}
	return plan
}

// fetchTextStart fetches the first size bytes of one part from each message, with
// BODY.PEEK so that the messages stay unread. The map has an entry for each
// message the server answered for.
//
// A refusal of the server (NO or BAD, for example for a part that does not
// exist) is not an error: the messages without an entry get an empty snippet.
// A failed connection is an error.
func fetchTextStart(c *imapclient.Client, uids []imap.UID, path []int, size int64) (map[imap.UID][]byte, error) {
	cmd := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID: true,
		BodySection: []*imap.FetchItemBodySection{{
			Part:    path,
			Peek:    true,
			Partial: &imap.SectionPartial{Offset: 0, Size: size},
		}},
	})
	// The command must be closed before this function returns, also on an
	// error. The pool relies on it. The normal path reads the error of Close
	// below. The error of a second Close adds nothing.
	defer func() { _ = cmd.Close() }()

	out := make(map[imap.UID][]byte, len(uids))
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		var (
			uid  imap.UID
			body []byte
		)
		for {
			item := msg.Next()
			if item == nil {
				break
			}
			switch item := item.(type) {
			case imapclient.FetchItemDataUID:
				uid = item.UID
			case imapclient.FetchItemDataBodySection:
				if item.Literal == nil {
					continue
				}
				// The server should send at most the bytes asked for. A server
				// that sends more is not allowed to fill the memory.
				data, err := io.ReadAll(io.LimitReader(item.Literal, size))
				if err != nil {
					return nil, err
				}
				body = data
			}
		}
		if uid != 0 {
			out[uid] = body
		}
	}
	if err := cmd.Close(); err != nil && !isServerError(err) {
		return nil, err
	}
	return out, nil
}

// makeSnippet turns what fetchTextStart read of a text part into a snippet:
// decoded text, cleaned, with white space collapsed, at most 200 characters. raw
// is the first bytes of a text/plain part, or a whole HTML part. It returns ""
// when the text cannot be decoded.
//
// HTML is never converted from its start alone. When raw is longer than the size
// in the structure, the structure is wrong, and raw may not be the whole part. So
// there is no snippet.
func makeSnippet(p *textPart, raw []byte) string {
	if p == nil || (p.html && int64(len(raw)) > int64(p.size)) {
		return ""
	}
	truncated := !p.html && len(raw) >= snippetFetchBytes
	encoding := strings.ToLower(strings.TrimSpace(p.encoding))
	if truncated && encoding == "base64" {
		raw = cutBase64(raw)
	}
	mediaType := "text/plain"
	if p.html {
		mediaType = "text/html"
	}
	// DecodeText does not fail for damaged data. It fails only when reading
	// fails, and a byte slice always reads.
	text, _ := mime.DecodeText(mediaType, map[string]string{"charset": p.charset}, p.encoding, bytes.NewReader(raw))
	if truncated {
		// The cut can fall inside a character. The decoder replaced the rest of
		// that character with U+FFFD.
		text = strings.TrimRight(text, "\U0000fffd")
	}
	if p.html {
		// HTML that is too complex to convert gives no snippet, and no error:
		// the snippet is only a help to read the list.
		text, _ = mime.HTMLToText(text)
	}
	text = mime.CleanUntrusted(text)
	text = strings.Join(strings.Fields(text), " ")
	return cutRunes(text, snippetRunes)
}

// cutBase64 removes white space from the start of a base64 text and cuts it to
// a multiple of 4 characters, because a base64 group of 4 characters is the
// smallest piece that decodes alone.
func cutBase64(raw []byte) []byte {
	clean := make([]byte, 0, len(raw))
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
		default:
			clean = append(clean, b)
		}
	}
	return clean[:len(clean)&^3]
}

// cutRunes cuts s to at most n characters.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	end := 0
	for i := 0; i < n; i++ {
		_, size := utf8.DecodeRuneInString(s[end:])
		end += size
	}
	return strings.TrimSpace(s[:end])
}

// newSummary makes a summary from the fetched data of a message, without the
// ref, the snippet and the attachment flags.
//
// Everything in it comes from a third party, so the text is cleaned (see
// mime.CleanUntrusted) and the encoded words of the headers are decoded.
func newSummary(m *imapclient.FetchMessageBuffer) mail.Summary {
	s := mail.Summary{
		Date: m.InternalDate,
		Size: m.RFC822Size,
	}
	if env := m.Envelope; env != nil {
		s.Subject = cleanHeaderText(env.Subject)
		if !env.Date.IsZero() {
			s.Date = env.Date
		}
		for _, a := range env.From {
			if addr, ok := convertAddress(a); ok {
				s.From = addr
				break
			}
		}
		s.To = convertAddresses(env.To)
	}
	for _, f := range m.Flags {
		switch {
		case strings.EqualFold(string(f), string(imap.FlagSeen)):
			s.Seen = true
		case strings.EqualFold(string(f), string(imap.FlagFlagged)):
			s.Flagged = true
		case strings.EqualFold(string(f), string(imap.FlagDraft)):
			s.Draft = true
		case isTag(string(f)):
			// A tag name has only letters, digits, - and _, so it needs no
			// cleaning. Other keywords (system flags, $-keywords and the
			// spam marks) are not tags (see isTag).
			s.Tags = append(s.Tags, string(f))
		}
	}
	slices.Sort(s.Tags)
	return s
}

// cleanHeaderText decodes the encoded words in a header text and cleans it.
func cleanHeaderText(s string) string {
	return mime.CleanUntrusted(mime.DecodeHeaderValue(s))
}

// convertAddresses converts the addresses of an envelope field. Group markers
// are left out.
func convertAddresses(list []imap.Address) []mail.Address {
	var out []mail.Address
	for _, a := range list {
		if addr, ok := convertAddress(a); ok {
			out = append(out, addr)
		}
	}
	return out
}

// convertAddress converts an address of the envelope. It returns false for a
// group marker, which has no mail address.
func convertAddress(a imap.Address) (mail.Address, bool) {
	email := a.Addr()
	if email == "" {
		return mail.Address{}, false
	}
	return mail.Address{Name: cleanHeaderText(a.Name), Email: mime.CleanUntrusted(email)}, true
}
