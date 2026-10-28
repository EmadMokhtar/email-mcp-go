package mime

import (
	"bytes"
	"fmt"
	netmail "net/mail"
	"strings"

	gomail "github.com/emersion/go-message/mail"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// FromDraft turns a saved draft into a message that is ready to send.
//
// It reads the sender from the first address in From. It reads the recipients
// from the To, Cc and Bcc headers, each address once. It then removes every
// Bcc header, with its continuation lines, from the message. It leaves every
// other byte as it is, so the message that goes out is the one that was
// saved, minus the Bcc header.
//
// FromDraft returns ErrInvalidAddress when the sender is missing, or when one
// of the headers has an address that it cannot read: a recipient that is
// skipped silently would be mail that is never delivered. It returns
// ErrNoRecipients when the draft has no recipient at all.
//
// It returns ErrHeaderInjection when the header has a CR that is not followed
// by an LF. A program that reads such a CR as a line break would find a header
// that this function did not see, a Bcc that it did not remove.
func FromDraft(raw []byte) (*mail.RawMessage, error) {
	head := scanDraftHeader(raw)
	if head.bareCR {
		return nil, fmt.Errorf("%w: draft header", ErrHeaderInjection)
	}

	var from *netmail.Address
	var rcpt recipients
	for _, f := range head.fields {
		list, err := parseDraftAddresses(f)
		if err != nil {
			return nil, err
		}
		switch f.name {
		case "from":
			if from == nil && len(list) > 0 {
				from = list[0]
			}
		case "to":
			rcpt.to = append(rcpt.to, list...)
		case "cc":
			rcpt.cc = append(rcpt.cc, list...)
		case "bcc":
			rcpt.bcc = append(rcpt.bcc, list...)
		}
	}
	if from == nil {
		return nil, fmt.Errorf("%w: draft has no sender", ErrInvalidAddress)
	}
	rcpt.dedupe()
	if rcpt.empty() {
		return nil, ErrNoRecipients
	}
	return &mail.RawMessage{From: from.Address, Recipients: rcpt.envelope(), Data: head.withoutBcc(raw)}, nil
}

// draftField is one header field of a draft, as it is in the bytes.
type draftField struct {
	// name is the field name in lower case, without the spaces before the
	// colon. It is empty for a line that is not a field.
	name string
	// value is the text after the colon, unfolded: the line breaks of the
	// continuation lines are removed.
	value string
	// start and end are the range of the field in the draft. It includes the
	// continuation lines and the last line break.
	start, end int
}

// draftHeader is the header of a draft. The scan does not parse the values.
type draftHeader struct {
	fields []draftField
	// bareCR is true when a line of the header has a CR in it that is not
	// part of the line break.
	bareCR bool
}

// scanDraftHeader finds the header fields of a draft, up to the first empty
// line. A line that starts with a space or a tab belongs to the field before
// it. A line that is not a field and not a continuation is kept as a field
// with no name. The scan is not strict on purpose: the aim is to find every
// line that could be a Bcc header.
func scanDraftHeader(raw []byte) draftHeader {
	var head draftHeader
	for pos := 0; pos < len(raw); {
		end := len(raw)
		if i := bytes.IndexByte(raw[pos:], '\n'); i >= 0 {
			end = pos + i + 1
		}
		line := bytes.TrimSuffix(raw[pos:end], []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			break
		}
		if bytes.IndexByte(line, '\r') >= 0 {
			head.bareCR = true
		}

		if last := len(head.fields) - 1; last >= 0 && (line[0] == ' ' || line[0] == '\t') {
			head.fields[last].value += string(line)
			head.fields[last].end = end
		} else {
			head.fields = append(head.fields, newDraftField(line, pos, end))
		}
		pos = end
	}
	return head
}

func newDraftField(line []byte, start, end int) draftField {
	f := draftField{start: start, end: end}
	if name, value, found := bytes.Cut(line, []byte(":")); found {
		f.name = strings.ToLower(strings.TrimSpace(string(name)))
		f.value = string(value)
	}
	return f
}

// withoutBcc returns a copy of raw without the Bcc fields.
func (h draftHeader) withoutBcc(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	kept := 0
	for _, f := range h.fields {
		if f.name == "bcc" {
			out = append(out, raw[kept:f.start]...)
			kept = f.end
		}
	}
	return append(out, raw[kept:]...)
}

// parseDraftAddresses reads the addresses of a From, To, Cc or Bcc field. The
// other fields have none. An empty value has none either.
func parseDraftAddresses(f draftField) ([]*netmail.Address, error) {
	switch f.name {
	case "from", "to", "cc", "bcc":
	default:
		return nil, nil
	}
	if strings.TrimSpace(f.value) == "" {
		return nil, nil
	}
	list, err := gomail.ParseAddressList(f.value)
	if err != nil {
		// The error of the parser holds the text of the address.
		return nil, fmt.Errorf("%w: %s header of the draft", ErrInvalidAddress, f.name)
	}
	for _, a := range list {
		if err := checkAddress(f.name, a); err != nil {
			return nil, err
		}
	}
	return list, nil
}
