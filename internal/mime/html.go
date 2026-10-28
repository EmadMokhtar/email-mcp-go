package mime

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/EmadMokhtar/email-mcp-go/internal/thirdparty/xhtml"
	"github.com/microcosm-cc/bluemonday"
)

// HTMLToText turns HTML mail into plain text for an agent to read.
//
// The text keeps what a person sees in a mail client and leaves out what a
// person does not see. A sender can use the hidden parts to give instructions
// to an agent without the reader noticing. HTMLToText removes:
//   - elements that browsers do not show or that only hold other documents
//     (script, style, head, title, template, iframe, object, embed, svg and
//     more), and images, including their alt text;
//   - elements with the hidden attribute or aria-hidden="true";
//   - elements, with everything inside, that a style hides as a whole:
//     display:none, an opacity of zero or less, and a height of zero with
//     clipped overflow (see parseStyle);
//   - text that has no readable size, or is not visible, because of the
//     inherited properties font-size and visibility. Children inherit them,
//     but a child can set its own value: text in a child with font-size:13px
//     is shown even if its parent has font-size:0, and a relative size such
//     as 2em is a multiple of the size of the parent. A size of 1pt (about
//     1.33px) or less is not readable;
//   - comments.
//
// Styles come from the style attribute and from the rules in style elements.
// A rule is used only when the selector is one class, one id or one element
// name, alone or in a comma list. Longer selectors (descendant, child,
// attribute or pseudo selectors, and so on) are ignored, and so are @media
// and the other at-rules.
//
// Hiding wins on each element. For font-size and visibility, every
// declaration that applies to an element counts: those in its style attribute
// and those in the rules for its name, its classes and its id. It does not
// matter which one comes first, which source is stronger, or whether it has
// !important (anything after a "!" is cut off). Each declaration is worked out
// against the state of the parent: inherit, unset and revert take the value of
// the parent, and a relative size is a multiple of the size of the parent. If
// any declaration hides, the element is hidden. If none does, the element is
// shown, and the size that its children get is the smallest of its sizes.
// This is stricter than a browser, which picks one declaration. The known cost
// is that text which a sender hides and then shows again on the same element
// (for example font-size:0 in a rule and font-size:14px in the style
// attribute) is dropped. A child is a different element, so it can still show
// its text inside a hidden parent. A display:none, and the other declarations
// that hide a whole element, are applied even if something else would show the
// element again.
//
// It does not find text that is hidden in other ways. Colour tricks, such as
// white text on a white background, are not detected. The same is true for
// small but not tiny fonts, text moved off the page, zero width, and text
// clipped away. The default styles of the browser are not applied, and sizes
// that need a calculation (calc, var) are not worked out.
//
// Links are written as "text (url)". The url is parsed and shown with spaces,
// round brackets and other unsafe characters written as %XX, so that it is one
// word. It is shown only for http, https and mailto, only if it is no longer
// than 2048 characters, and only if the link can be seen. Block elements
// become line breaks. The result is passed through CleanUntrusted, so it is
// always valid UTF-8 with no invisible characters.
//
// A document that would make the parser do too much work, or that is nested
// more than 512 elements deep, gives an empty string and an error that wraps
// ErrHTMLTooComplex. Nothing is returned for it, instead of text that was not
// checked. The size of the markup is not limited here: the caller does that.
func HTMLToText(markup string) (string, error) {
	doc, err := parseHTML(markup)
	if err != nil {
		return "", err
	}
	r := textRenderer{visibility: newVisibility(doc)}
	r.walk(doc)
	return CleanUntrusted(tidyLines(string(r.buf))), nil
}

// ErrHTMLTooComplex means that HTML would make the parser do too much work, so
// it was not parsed. HTMLToText and VisibleHTML return it, also for a document
// that is nested too deep for the parser.
//
// The size of the input does not bound the work of the parser and of the code
// that reads the tree. The tree builder keeps a list of the formatting elements
// (a, b, i, font and so on) that it has not closed. For each piece of text it
// copies every element of that list into the tree again ("reconstruct the
// active formatting elements"). A few tens of kilobytes of input that opens
// hundreds of formatting elements with different attributes, and then has many
// paragraphs, make millions of nodes and gigabytes of memory. A copy of an
// element with a long attribute (a link of a megabyte) shares the attribute
// with the original, but this package reads and writes the attribute again for
// each copy. The parser that is used here (internal/thirdparty/xhtml) counts
// its work and stops at a limit, so the memory and the time are bounded
// whatever the markup is.
var ErrHTMLTooComplex = errors.New("mime: html too complex to convert")

// maxHTMLCost is the most work that the parser may do for one document (see
// xhtml.ParseWithLimit). A node costs xhtml.NodeCost (16) and an attribute
// xhtml.AttrCost (16) plus its bytes, so the limit is one million nodes, or 16
// MB of attributes. A marketing mail of 4 MiB with tables, links and inline
// styles costs about 6.9 million (232,000 nodes, 75,000 attributes, 2 MB of
// attributes).
const maxHTMLCost = 16_000_000

// parseHTML parses HTML mail with a limit on the work of the parser. The error
// wraps ErrHTMLTooComplex, for a document that needs too much work and for one
// that is nested more than 512 elements deep. HTMLToText and VisibleHTML parse
// in the same way, so they see the same document.
//
// Scripting is off because a mail client does not run scripts. With it on, the
// parser would treat the content of noscript as raw text.
func parseHTML(markup string) (*xhtml.Node, error) {
	doc, err := xhtml.ParseWithLimit(strings.NewReader(markup), maxHTMLCost, xhtml.ParseOptionEnableScripting(false))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHTMLTooComplex, err)
	}
	return doc, nil
}

// dropTags are removed together with everything inside them.
var dropTags = tagSet(
	// A browser does not show these (display:none in its default style sheet).
	"head", "title", "script", "style", "template", "noembed", "noframes", "datalist", "rp",
	// Embedded content. The text inside is a fallback or another document.
	"iframe", "object", "embed", "svg",
	// An image has no text. Its alt text is written by the sender, so it is
	// not used.
	"img",
)

// paragraphTags are separated from the text around them by a blank line.
var paragraphTags = tagSet("p", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "pre", "hr", "dl")

// lineTags start on a new line and end the line.
var lineTags = tagSet(
	"div", "li", "ul", "ol", "tr", "table", "section", "article", "header", "footer", "nav",
	"aside", "main", "figure", "figcaption", "address", "form", "fieldset", "center",
	"details", "summary", "dt", "dd",
)

func tagSet(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// visibility decides what a reader cannot see in a parsed document. It is the
// analysis behind HTMLToText and VisibleHTML, so both leave out the same parts.
// A walk over the document calls classify for each element and push when it
// opens an element that classify kept, and it takes the state off the stack
// (pop) when it closes that element.
type visibility struct {
	rules  *styleRules // rules in style elements
	states []inherited // inherited properties of the open elements, innermost last
	root   float64     // font size of the html element, in pixels (for rem)
}

// newVisibility reads the style rules of the document and returns the state
// for the start of a walk.
func newVisibility(doc *xhtml.Node) visibility {
	return visibility{
		rules:  collectStyleRules(doc),
		root:   initialFontSize,
		states: []inherited{{fontSize: initialFontSize}},
	}
}

// textRenderer collects the text of a parsed document.
type textRenderer struct {
	visibility
	buf    []byte
	pre    int        // how many pre elements we are inside
	bullet bool       // a list item started and its "- " is not written yet
	links  []linkMark // links that are open, innermost last
}

// inherited holds the properties that an element gets from its parent.
type inherited struct {
	invisible bool    // visibility is hidden or collapse
	fontSize  float64 // font size in pixels
}

// textHidden reports whether text with these properties cannot be seen.
func (s inherited) textHidden() bool {
	return s.invisible || s.fontSize <= minReadableFontSize
}

// state returns the properties of the element that is open now.
func (v *visibility) state() inherited {
	return v.states[len(v.states)-1]
}

// classify says whether an element is shown at all. When it is not, the
// element is skipped with everything inside it: it is a dropped tag (see
// dropTags), it has a hiding attribute, or a style hides it as a whole. When it
// is shown, the effect of its styles is returned, for push.
func (v *visibility) classify(n *xhtml.Node) (effect styleEffect, shown bool) {
	if dropTags[n.Data] || hasHiddenAttribute(n) {
		return styleEffect{}, false
	}
	effect = v.rules.effectFor(n)
	if style, ok := attributeValue(n, "style"); ok {
		effect = effect.merge(parseStyle(style))
	}
	return effect, !effect.drop
}

// push opens an element with the given effect and works out its properties.
func (v *visibility) push(n *xhtml.Node, e styleEffect) {
	s := v.state()
	s.invisible = e.invisible(s.invisible)
	s.fontSize = e.size(s.fontSize, v.root)
	// rem is relative to the html element. Its own size uses the default.
	if n.Data == "html" && n.Parent != nil && n.Parent.Type == xhtml.DocumentNode {
		v.root = s.fontSize
	}
	v.states = append(v.states, s)
}

// pop closes the element that push opened last.
func (v *visibility) pop() { v.states = v.states[:len(v.states)-1] }

// linkMark remembers where the text of an open link starts.
type linkMark struct {
	node  *xhtml.Node
	start int    // position in buf where the link text starts
	url   string // the url to show after the link text
}

// walk visits every node below root, in document order. It does not call
// itself, so the depth of the document cannot exhaust the stack. (The parser
// also refuses documents that are nested more than 512 elements deep.)
func (r *textRenderer) walk(root *xhtml.Node) {
	n := root
	for {
		entered := r.enter(n)
		if entered && n.FirstChild != nil {
			n = n.FirstChild
			continue
		}
		if entered {
			r.exit(n)
		}
		// Go up until there is a next sibling, leaving each parent on the way.
		for n != root && n.NextSibling == nil {
			n = n.Parent
			r.exit(n)
		}
		if n == root {
			return
		}
		n = n.NextSibling
	}
}

// enter starts a node. It returns false when the node must be skipped with
// everything inside it. Then exit is not called for the node.
func (r *textRenderer) enter(n *xhtml.Node) bool {
	switch n.Type {
	case xhtml.DocumentNode:
		return true
	case xhtml.TextNode:
		if !r.state().textHidden() {
			r.text(CleanUntrusted(n.Data))
		}
		return false
	case xhtml.ElementNode:
		// Handled below.
	default:
		return false // comments, doctype and so on
	}

	tag := n.Data
	effect, shown := r.classify(n)
	if !shown {
		return false
	}
	if tag == "br" {
		r.hardBreak()
		return false
	}

	// From here the element is open, and exit must close it.
	r.push(n, effect)
	switch {
	case paragraphTags[tag]:
		r.newline(2)
	case lineTags[tag]:
		r.newline(1)
	}
	switch tag {
	case "li":
		r.bullet = true
	case "pre":
		r.pre++
	case "a":
		// A link that cannot be seen shows nothing, not even its url.
		if r.state().textHidden() {
			break
		}
		if u := linkURL(n); u != "" {
			// Write a pending "- " first, so it is not counted as link text.
			r.flushBullet()
			r.links = append(r.links, linkMark{node: n, start: len(r.buf), url: u})
		}
	}
	return true
}

// exit ends an element that enter returned true for.
func (r *textRenderer) exit(n *xhtml.Node) {
	if n.Type != xhtml.ElementNode {
		return
	}
	tag := n.Data
	switch tag {
	case "li":
		r.bullet = false
	case "pre":
		r.pre--
	case "td", "th":
		r.space() // keep table cells apart
	case "a":
		r.endLink(n)
	}
	switch {
	case paragraphTags[tag]:
		r.newline(2)
	case lineTags[tag]:
		r.newline(1)
	}
	r.pop()
}

// endLink writes the url after the text of a link.
func (r *textRenderer) endLink(n *xhtml.Node) {
	last := len(r.links) - 1
	if last < 0 || r.links[last].node != n {
		return // this link has no url to show
	}
	m := r.links[last]
	r.links = r.links[:last]

	// bytes.TrimSpace does not copy, and neither does the comparison. A copy of
	// the text of every open link would be slow for links inside links.
	shown := bytes.TrimSpace(r.buf[m.start:])
	switch {
	case len(shown) == 0:
		// No visible text, for example a link around an image. Show the url
		// alone, so the reader still sees where the link goes.
		r.space()
		r.text(m.url)
	case string(shown) == m.url:
		// The text already is the url. Show it once.
	default:
		r.text(" (" + m.url + ")")
	}
}

// space writes one space. It does nothing at the start of a line, or after
// another space, so runs of white space collapse.
func (r *textRenderer) space() {
	if n := len(r.buf); n > 0 && r.buf[n-1] != ' ' && r.buf[n-1] != '\n' {
		r.buf = append(r.buf, ' ')
	}
}

// trimSpace removes the space at the end of the output, if there is one.
// There is never more than one, because space() does not write two.
func (r *textRenderer) trimSpace() {
	if n := len(r.buf); n > 0 && r.buf[n-1] == ' ' {
		r.buf = r.buf[:n-1]
	}
}

// newline makes the output end with at least n line breaks. It adds only the
// ones that are missing, and nothing at the very start.
func (r *textRenderer) newline(n int) {
	if len(r.buf) == 0 {
		return
	}
	r.trimSpace()
	have := 0
	for i := len(r.buf) - 1; i >= 0 && r.buf[i] == '\n' && have < n; i-- {
		have++
	}
	for ; have < n; have++ {
		r.buf = append(r.buf, '\n')
	}
}

// hardBreak writes one line break, even when the line is empty (the br element).
func (r *textRenderer) hardBreak() {
	r.trimSpace()
	r.buf = append(r.buf, '\n')
}

func (r *textRenderer) flushBullet() {
	if r.bullet {
		r.buf = append(r.buf, '-', ' ')
		r.bullet = false
	}
}

// text writes text from the page. In HTML, a line break inside text is only
// white space. Inside pre it is a real line break.
func (r *textRenderer) text(s string) {
	for _, c := range s {
		switch {
		case c == '\n' && r.pre > 0:
			r.hardBreak()
		case isHTMLSpace(c):
			r.space()
		default:
			r.flushBullet()
			r.buf = utf8.AppendRune(r.buf, c)
		}
	}
}

// isHTMLSpace reports white space that HTML collapses, plus the no-break space.
func isHTMLSpace(c rune) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\f', 0xA0:
		return true
	}
	return false
}

// tidyLines trims every line and keeps at most one blank line in a row (so
// three or more line breaks become two). It also trims the whole text.
func tidyLines(s string) string {
	var lines []string
	blank := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// maxShownURL is the longest url that is shown, in characters.
const maxShownURL = 2048

// linkURL returns the url to show for an a element, or "" when none is shown.
//
// The href is parsed. If it does not parse, or its scheme is not http, https
// or mailto, nothing is shown. What is shown is the parsed url written out
// again, with every character that is not safe in one word written as %XX.
// That includes spaces and round brackets. Without this, a sender could write
// an href that ends the url early and goes on with a sentence that looks like
// text of the mail. A url that is longer than maxShownURL is not shown.
func linkURL(n *xhtml.Node) string {
	href, ok := attributeValue(n, "href")
	if !ok {
		return ""
	}
	href = CleanUntrusted(href)
	// Browsers ignore tabs and line breaks inside a url, and white space at its
	// ends. Do the same, so the url we show is the one a browser would open.
	href = strings.Map(func(c rune) rune {
		if c == '\t' || c == '\n' {
			return -1
		}
		return c
	}, href)
	href = strings.TrimSpace(href)

	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	switch u.Scheme { // url.Parse writes the scheme in lower case
	case "http", "https", "mailto":
	default:
		return "" // includes a relative url, which has no meaning in a mail
	}
	shown := escapeURL(u.String())
	if len(shown) > maxShownURL {
		return ""
	}
	return shown
}

// escapeURL writes every byte of a url that is not safe in one word as %XX.
// Letters, digits, the characters that a url uses to mark its parts, and an
// existing %XX are kept. Spaces, round brackets, quotes, angle brackets,
// backslashes, control characters and non-ASCII bytes are written as %XX.
func escapeURL(s string) string {
	const hex = "0123456789ABCDEF"
	safe := func(c byte) bool {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			return true
		}
		return strings.IndexByte("-._~:/?#[]@!$&'*+,;=%", c) >= 0
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; safe(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0F])
		}
	}
	return b.String()
}

// attributeValue returns the value of an attribute of the element.
func attributeValue(n *xhtml.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// --- hidden elements ---

// hasHiddenAttribute reports whether an attribute of the element hides it, with
// everything inside: the hidden attribute, aria-hidden="true", and a dialog
// that is not open. (A hiding style is read separately, see parseStyle.)
func hasHiddenAttribute(n *xhtml.Node) bool {
	hasOpen := false
	for _, a := range n.Attr {
		switch a.Key {
		case "hidden":
			return true
		case "aria-hidden":
			if strings.EqualFold(strings.TrimSpace(a.Val), "true") {
				return true
			}
		case "open":
			hasOpen = true
		}
	}
	return n.Data == "dialog" && !hasOpen
}

// --- SanitizeHTML ---

// detailsOpenValue is the value the open attribute of details may have.
var detailsOpenValue = regexp.MustCompile(`(?i)^(|open)$`)

// sanitizePolicy is built once. A bluemonday policy is safe to share between
// goroutines after it is built.
var sanitizePolicy = newSanitizePolicy()

// SanitizeHTML cleans HTML so that it is safe to show in a page.
//
// It follows the rules of bluemonday.UGCPolicy (a policy for user generated
// content), with these changes: there are no images, no image maps, and no
// relative links. The result never has an img, script or style element, a
// style attribute, or an href that is not http, https or mailto.
//
// UGCPolicy always allows images and has no way to take them out, so the
// policy is written out here.
func SanitizeHTML(markup string) string {
	return sanitizePolicy.Sanitize(markup)
}

func newSanitizePolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	// Attributes on all elements: id, title, dir and lang. Links: only
	// mailto, http and https, rel="nofollow" added, and an href must parse.
	p.AllowStandardAttributes()
	p.AllowStandardURLs()
	// A relative link has no meaning in a mail.
	p.AllowRelativeURLs(false)

	// Sections and headings.
	p.AllowElements("article", "aside", "figure", "section", "summary",
		"h1", "h2", "h3", "h4", "h5", "h6", "hgroup")
	p.AllowAttrs("open").Matching(detailsOpenValue).OnElements("details")

	// Blocks and links.
	p.AllowAttrs("cite").OnElements("blockquote")
	p.AllowElements("br", "div", "hr", "p", "span", "wbr")
	p.AllowAttrs("href").OnElements("a")

	// Inline text.
	p.AllowElements("abbr", "acronym", "cite", "code", "dfn", "em", "figcaption",
		"mark", "s", "samp", "strong", "sub", "sup", "var",
		"b", "i", "pre", "small", "strike", "tt", "u",
		"rp", "rt", "ruby")
	p.AllowAttrs("cite").OnElements("q")
	p.AllowAttrs("datetime").Matching(bluemonday.ISO8601).OnElements("time", "del", "ins")
	p.AllowAttrs("dir").Matching(bluemonday.Direction).OnElements("bdi", "bdo")
	p.AllowAttrs("cite").Matching(bluemonday.Paragraph).OnElements("del", "ins")

	p.AllowLists()
	p.AllowTables()

	return p
}
