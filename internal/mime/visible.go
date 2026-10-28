package mime

import (
	"bytes"
	"fmt"

	"github.com/EmadMokhtar/email-mcp-go/internal/thirdparty/xhtml"
	"golang.org/x/net/html/atom"
)

// VisibleHTML returns the HTML that a reader of the mail can see, cleaned so
// that it is safe to show in a page.
//
// SanitizeHTML removes style, class and hidden attributes, but it keeps the
// content of the elements that they hide. Without the styles, that content
// would show. So VisibleHTML first removes what HTMLToText would leave out (see
// HTMLToText for the rules): elements that a browser does not show, elements
// with the hidden or aria-hidden attribute, elements that an inline style or a
// rule of a style element hides, and the text that is hidden because of the
// font size or the visibility that it inherits. Then it renders the rest and
// passes it through SanitizeHTML. The result has the properties of SanitizeHTML
// (no img, script or style element, no style attribute, and only http, https
// and mailto links), and nothing that HTMLToText hides.
//
// The only attribute that stays is href. The others (title, lang, id, alt,
// cite and so on) hold text that a reader never sees, and a sender can use them
// to give instructions to an agent. So the table cells lose their colspan and
// rowspan too. An element in the MathML or SVG namespace that is a textarea is
// removed, because its content is read as text by the sanitizer, and its
// attributes would come out as visible text.
//
// A link that cannot be seen loses its href. As in HTMLToText, an element
// inside a hidden parent is still shown when it sets its own font size or
// visibility.
//
// The whole document is needed. A style rule at the end can hide an element at
// the start, so the caller must not pass the start of a document alone. A
// document that would make the parser build too many nodes gives an error that
// wraps ErrHTMLTooComplex (see HTMLToText).
func VisibleHTML(markup string) (string, error) {
	doc, err := parseHTML(markup)
	if err != nil {
		return "", err
	}
	v := newVisibility(doc)
	v.prune(doc)

	var buf bytes.Buffer
	if err := xhtml.Render(&buf, doc); err != nil {
		// A buffer cannot fail to write. Return nothing to be safe.
		return "", fmt.Errorf("mime: render html: %w", err)
	}
	return SanitizeHTML(buf.String()), nil
}

// prune removes from the tree below parent everything that a reader cannot
// see. It follows the same rules as the walk of HTMLToText (see visibility).
//
// It calls itself for each open element. The parser refuses a document that is
// nested more than 512 elements deep, so the depth is small.
func (v *visibility) prune(parent *xhtml.Node) {
	for c := parent.FirstChild; c != nil; {
		next := c.NextSibling // c may leave the tree
		switch c.Type {
		case xhtml.TextNode:
			if v.state().textHidden() {
				parent.RemoveChild(c)
			}
		case xhtml.ElementNode:
			v.pruneElement(parent, c)
		default:
			// Comments, a doctype and the like are not content.
			parent.RemoveChild(c)
		}
		c = next
	}
}

func (v *visibility) pruneElement(parent, n *xhtml.Node) {
	effect, shown := v.classify(n)
	if !shown || (n.Data == "textarea" && n.Namespace != "") {
		parent.RemoveChild(n)
		return
	}
	v.push(n, effect)
	defer v.pop()

	switch n.Data {
	case "a":
		// A link that cannot be seen shows nothing, not even its url. Its
		// children may still be shown, so the element stays.
		if v.state().textHidden() {
			removeAttribute(n, "href")
		}
	case "noscript", "xmp", "plaintext":
		// xhtml.Render writes the text of these elements as it is, without
		// escaping, because a browser reads them as raw text. Here it would turn
		// escaped text into markup. For a reader they are blocks, so they become
		// a div, or a pre for xmp and plaintext, which keep their line breaks.
		if n.Data == "noscript" {
			n.Data, n.DataAtom = "div", atom.Div
		} else {
			n.Data, n.DataAtom = "pre", atom.Pre
		}
	}
	// The styles and the other attributes were read above (here and for the
	// elements below, which read their own). Nothing but the link stays.
	keepOnly(n, "href")
	v.prune(n)
}

// keepOnly removes every attribute of the element except the one with the given
// key.
func keepOnly(n *xhtml.Node, key string) {
	kept := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key == key && a.Namespace == "" {
			kept = append(kept, a)
		}
	}
	n.Attr = kept
}

// removeAttribute removes the attributes with the given key.
func removeAttribute(n *xhtml.Node, key string) {
	kept := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key != key {
			kept = append(kept, a)
		}
	}
	n.Attr = kept
}
