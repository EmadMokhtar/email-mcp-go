package mime

import (
	"slices"
	"strings"
)

// PartSeparator goes between two text parts when they are joined into one text.
const PartSeparator = "\n\n"

// PartNode describes one part of a message, for LayoutOf. ParseMessage makes
// these from the message, and the IMAP backend makes them from a BODYSTRUCTURE,
// so both read a message by the same rule.
type PartNode struct {
	// ID is the part path, as IMAP numbers parts: "1.2". The only part of a
	// message that is not multipart is "1". LayoutOf does not read it.
	ID string
	// MediaType is the lower case media type, such as "text/plain".
	MediaType string
	// Attachment is true when the Content-Disposition is "attachment".
	Attachment bool
	// Named is true when the part has a file name.
	Named bool
	// Empty is true when the part has no content.
	Empty bool
	// ContentID is the Content-ID of a part that is not multipart. A
	// BODYSTRUCTURE has none for a multipart, so a multipart never has one here.
	ContentID string
	// Start is the start parameter of a multipart/related: the Content-ID of its
	// root part.
	Start string
	// Children are the parts of a multipart, in the order of the message.
	Children []*PartNode

	// These are for ParseMessage.
	text     string
	size     int64
	filename string
}

// PartLayout says what each part of a message is.
type PartLayout struct {
	// Shown holds the text parts with content that a mail client shows, in the
	// order of the message. It is empty when the body is empty.
	Shown []*PartNode
	// Others holds the text parts with content that are other versions of the
	// body, which a client does not show.
	Others []*PartNode
	// Attachments holds the attachments, in the order of the message.
	Attachments []*PartNode
}

// LayoutOf finds the body and the attachments of a message. This is the one rule
// for what a mail client shows. An agent must read what the user reads, so the
// read path and the preview of a draft both use it.
//
//   - Only text/plain and text/html parts can be the body. A part with a
//     Content-Disposition of "attachment" never is.
//   - In a multipart/alternative, a client shows the last part that it can show,
//     so that part is the body, also when it is empty. The parts before it are
//     other versions.
//   - In a multipart/related, a client shows the root part: the part that the
//     start parameter names, or else the first part. A text part that is not the
//     root is another version.
//   - In any other multipart, a client shows every part, in order.
//   - Outside a multipart/alternative, a text part with a file name is a file and
//     not the body. When the message has no other body, the first such part is
//     the body. Inside an alternative the name does not count, because a client
//     shows the part all the same.
//
// A part is an attachment when its Content-Disposition is "attachment", when it
// is not text, or when it has a file name and is not the body. A text part of
// another type without a name, such as text/calendar, is neither.
func LayoutOf(root *PartNode) PartLayout {
	if root == nil {
		return PartLayout{}
	}
	shown, others := pickParts(root, false, false)
	if len(shown) == 0 && len(others) == 0 {
		shown, others = pickParts(root, false, true)
		shown = shown[:min(len(shown), 1)]
	}
	body := map[*PartNode]bool{}
	for _, n := range shown {
		body[n] = true
	}
	for _, n := range others {
		body[n] = true
	}
	return PartLayout{
		Shown:       withContent(shown),
		Others:      withContent(others),
		Attachments: attachmentsIn(root, body, nil),
	}
}

// pickParts returns the text parts below n, itself included, that a client shows
// and the other versions of them. Both can hold empty parts. inAlternative says
// that n is an alternative of a multipart/alternative. named says that a part
// with a file name counts as text of the body.
func pickParts(n *PartNode, inAlternative, named bool) (shown, others []*PartNode) {
	switch {
	case !isMultipart(n):
		if canBeBody(n, inAlternative, named) {
			return []*PartNode{n}, nil
		}
		return nil, nil
	case n.MediaType == "multipart/alternative":
		for _, child := range slices.Backward(n.Children) {
			s, o := pickParts(child, true, named)
			if shown == nil {
				shown, others = s, o
				continue
			}
			others = append(append(others, s...), o...)
		}
		return shown, others
	case n.MediaType == "multipart/related":
		for _, child := range rootFirst(n) {
			s, o := pickParts(child, inAlternative, named)
			if shown == nil {
				shown, others = s, o
				continue
			}
			others = append(append(others, s...), o...)
		}
		return shown, others
	}
	for _, child := range n.Children {
		s, o := pickParts(child, false, named)
		shown = append(shown, s...)
		others = append(others, o...)
	}
	return shown, others
}

// canBeBody reports whether a part that is not multipart can be text of the body.
func canBeBody(n *PartNode, inAlternative, named bool) bool {
	if n.Attachment || !isBodyType(n.MediaType) {
		return false
	}
	return !n.Named || inAlternative || named
}

// isBodyType reports whether a mail client can show this media type as the body.
func isBodyType(mediaType string) bool {
	return mediaType == "text/plain" || mediaType == "text/html"
}

func isMultipart(n *PartNode) bool { return strings.HasPrefix(n.MediaType, "multipart/") }

// rootFirst returns the children of a multipart/related with the root part first.
// A start parameter that names no part is ignored.
func rootFirst(n *PartNode) []*PartNode {
	start := bareContentID(n.Start)
	root := slices.IndexFunc(n.Children, func(c *PartNode) bool {
		return start != "" && bareContentID(c.ContentID) == start
	})
	if root <= 0 {
		return n.Children
	}
	return slices.Insert(slices.Delete(slices.Clone(n.Children), root, root+1), 0, n.Children[root])
}

// bareContentID removes the angle brackets of a Content-ID.
func bareContentID(id string) string {
	return strings.Trim(strings.TrimSpace(id), "<>")
}

func withContent(parts []*PartNode) []*PartNode {
	var out []*PartNode
	for _, n := range parts {
		if !n.Empty {
			out = append(out, n)
		}
	}
	return out
}

// attachmentsIn adds the attachments below n, itself included, to out. body holds
// the parts that are text of the body.
func attachmentsIn(n *PartNode, body map[*PartNode]bool, out []*PartNode) []*PartNode {
	if isMultipart(n) {
		for _, child := range n.Children {
			out = attachmentsIn(child, body, out)
		}
		return out
	}
	if n.Attachment || !strings.HasPrefix(n.MediaType, "text/") || (n.Named && !body[n]) {
		out = append(out, n)
	}
	return out
}
