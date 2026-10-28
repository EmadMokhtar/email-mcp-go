package imapsmtp

import (
	"bytes"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
)

const (
	// snippetFetchBytes is how much of a text part the backend fetches to make
	// the snippet. It is the encoded size, before the transfer encoding is
	// removed.
	snippetFetchBytes = 2048
	// snippetRunes is the longest snippet, in characters.
	snippetRunes = 200
)

// textPart is the part of a message that the snippet comes from.
type textPart struct {
	path     []int // the IMAP part number, for example [1 2] for "1.2"
	html     bool  // text/html. Otherwise text/plain.
	charset  string
	encoding string // Content-Transfer-Encoding
}

// section returns the part number as the text used in a FETCH, "1.2".
func (p *textPart) section() string {
	parts := make([]string, len(p.path))
	for i, n := range p.path {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// structure is what a summary needs from the BODYSTRUCTURE of a message.
type structure struct {
	hasAttachments bool
	hasInvite      bool
	text           *textPart // nil when the message has no usable text part
}

// analyzeStructure reads the BODYSTRUCTURE of a message.
//
// A part is an attachment when its Content-Disposition is "attachment", or
// when it is not multipart and not text (an image, a PDF). A part is an invite
// when it is text/calendar. The snippet comes from the first text/plain part
// that is not an attachment, or, when there is none, from the first text/html
// part that is not an attachment. A part without content is not used.
// Messages inside the message (message/rfc822) are not searched.
func analyzeStructure(bs imap.BodyStructure) structure {
	var s structure
	if bs == nil {
		return s
	}
	var plain, html *textPart
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		leaf, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true // multipart: look at its children
		}
		media := leaf.MediaType()
		isText := strings.EqualFold(leaf.Type, "text")
		disposition := leaf.Disposition()
		attachment := disposition != nil && strings.EqualFold(disposition.Value, "attachment")

		if media == "text/calendar" {
			s.hasInvite = true
		}
		if attachment || !isText {
			s.hasAttachments = true
		}
		if attachment || leaf.Size == 0 {
			return true
		}
		candidate := &textPart{
			path:     slices.Clone(path),
			charset:  leaf.Params["charset"],
			encoding: leaf.Encoding,
		}
		switch media {
		case "text/plain":
			if plain == nil {
				plain = candidate
			}
		case "text/html":
			if html == nil {
				candidate.html = true
				html = candidate
			}
		}
		return true
	})
	if plain != nil {
		s.text = plain
	} else {
		s.text = html
	}
	return s
}

// fetchSummaries returns the summaries of the messages with the given UIDs, in
// the same order as uids. A message that is gone (deleted since the search) is
// left out.
//
// It asks for the envelope, flags, size, date and BODYSTRUCTURE of all
// messages in one FETCH. Then it fetches the start of the text part of each
// message for the snippet, with one FETCH for all messages that have the same
// part number. Everything is read with PEEK, so no message is marked as read.
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
	// The UIDs of the messages for each part number.
	sections := map[string][]imap.UID{}
	paths := map[string][]int{}
	for _, m := range msgs {
		if m.UID == 0 {
			continue
		}
		byUID[m.UID] = m
		st := analyzeStructure(m.BodyStructure)
		structures[m.UID] = st
		if st.text != nil {
			key := st.text.section()
			sections[key] = append(sections[key], m.UID)
			paths[key] = st.text.path
		}
	}

	keys := make([]string, 0, len(sections))
	for key := range sections {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	snippets := map[imap.UID]string{}
	for _, key := range keys {
		bodies, err := fetchTextStart(c, sections[key], paths[key])
		if err != nil {
			return nil, err
		}
		for uid, raw := range bodies {
			snippets[uid] = makeSnippet(structures[uid].text, raw)
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

// fetchTextStart fetches the first bytes of one part from each message, with
// BODY.PEEK so that the messages stay unread. The map has an entry for each
// message the server answered for.
//
// A refusal of the server (NO or BAD, for example for a part that does not
// exist) is not an error: the messages without an entry get an empty snippet.
// A failed connection is an error.
func fetchTextStart(c *imapclient.Client, uids []imap.UID, path []int) (map[imap.UID][]byte, error) {
	cmd := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID: true,
		BodySection: []*imap.FetchItemBodySection{{
			Part:    path,
			Peek:    true,
			Partial: &imap.SectionPartial{Offset: 0, Size: snippetFetchBytes},
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
				data, err := io.ReadAll(io.LimitReader(item.Literal, snippetFetchBytes))
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

// makeSnippet turns the first bytes of a text part into a snippet: decoded
// text, cleaned, with white space collapsed, at most 200 characters. It
// returns "" when the text cannot be decoded.
func makeSnippet(p *textPart, raw []byte) string {
	if p == nil {
		return ""
	}
	truncated := len(raw) >= snippetFetchBytes
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
		text = mime.HTMLToText(text)
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
		for _, a := range env.To {
			if addr, ok := convertAddress(a); ok {
				s.To = append(s.To, addr)
			}
		}
	}
	for _, f := range m.Flags {
		switch {
		case strings.EqualFold(string(f), string(imap.FlagSeen)):
			s.Seen = true
		case strings.EqualFold(string(f), string(imap.FlagFlagged)):
			s.Flagged = true
		case !strings.HasPrefix(string(f), `\`):
			s.Tags = append(s.Tags, mime.CleanUntrusted(string(f)))
		}
	}
	slices.Sort(s.Tags)
	return s
}

// cleanHeaderText decodes the encoded words in a header text and cleans it.
func cleanHeaderText(s string) string {
	return mime.CleanUntrusted(mime.DecodeHeaderValue(s))
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
