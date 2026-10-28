package mime

import (
	"bytes"

	"golang.org/x/net/html"
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
// A link that cannot be seen loses its href. As in HTMLToText, an element
// inside a hidden parent is still shown when it sets its own font size or
// visibility.
//
// The whole document is needed. A style rule at the end can hide an element at
// the start, so the caller must not pass the start of a document alone.
func VisibleHTML(markup string) string {
	doc, err := parseHTML(markup)
	if err != nil {
		// See HTMLToText: return nothing, instead of markup that was not checked.
		return ""
	}
	v := newVisibility(doc)
	v.prune(doc)

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		// A buffer cannot fail to write. Return nothing to be safe.
		return ""
	}
	return SanitizeHTML(buf.String())
}

// prune removes from the tree below parent everything that a reader cannot
// see. It follows the same rules as the walk of HTMLToText (see visibility).
//
// It calls itself for each open element. The parser refuses a document that is
// nested more than 512 elements deep, so the depth is small.
func (v *visibility) prune(parent *html.Node) {
	for c := parent.FirstChild; c != nil; {
		next := c.NextSibling // c may leave the tree
		switch c.Type {
		case html.TextNode:
			if v.state().textHidden() {
				parent.RemoveChild(c)
			}
		case html.ElementNode:
			v.pruneElement(parent, c)
		default:
			// Comments, a doctype and the like are not content.
			parent.RemoveChild(c)
		}
		c = next
	}
}

func (v *visibility) pruneElement(parent, n *html.Node) {
	effect, shown := v.classify(n)
	if !shown {
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
		// html.Render writes the text of these elements as it is, without
		// escaping, because a browser reads them as raw text. Here it would turn
		// escaped text into markup. For a reader they are blocks, so they become
		// a div, or a pre for xmp and plaintext, which keep their line breaks.
		if n.Data == "noscript" {
			n.Data, n.DataAtom = "div", atom.Div
		} else {
			n.Data, n.DataAtom = "pre", atom.Pre
		}
	}
	v.prune(n)
}

// removeAttribute removes the attributes with the given key.
func removeAttribute(n *html.Node, key string) {
	kept := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key != key {
			kept = append(kept, a)
		}
	}
	n.Attr = kept
}
