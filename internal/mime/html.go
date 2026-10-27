package mime

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// HTMLToText turns HTML mail into plain text for an agent to read.
//
// The text keeps what a person sees in a mail client and leaves out what a
// person does not see. A sender can use the hidden parts to give instructions
// to an agent without the reader noticing. HTMLToText removes:
//   - elements that browsers do not show or that only hold other documents
//     (script, style, head, title, template, iframe, object, embed, svg and
//     more), and images, including their alt text;
//   - elements with the hidden attribute or aria-hidden="true", and elements
//     whose inline style hides them (see styleHides);
//   - comments.
//
// Links are written as "text (url)". The url is only shown for the http, https
// and mailto schemes. Block elements become line breaks. The result is passed
// through CleanUntrusted, so it is always valid UTF-8 with no invisible
// characters.
func HTMLToText(markup string) string {
	// Scripting is off because a mail client does not run scripts. With it on,
	// the parser would treat the content of noscript as raw text.
	doc, err := html.ParseWithOptions(strings.NewReader(markup), html.ParseOptionEnableScripting(false))
	if err != nil {
		// The parser refuses a document that is nested more than 512 elements
		// deep. A string reader cannot fail. Return nothing, instead of text
		// that was not checked.
		return ""
	}
	var r textRenderer
	r.walk(doc)
	return CleanUntrusted(tidyLines(string(r.buf)))
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

// textRenderer collects the text of a parsed document.
type textRenderer struct {
	buf    []byte
	pre    int        // how many pre elements we are inside
	bullet bool       // a list item started and its "- " is not written yet
	links  []linkMark // links that are open, innermost last
}

// linkMark remembers where the text of an open link starts.
type linkMark struct {
	node  *html.Node
	start int    // position in buf where the link text starts
	url   string // the url to show after the link text
}

// walk visits every node below root, in document order. It does not call
// itself, so the depth of the document cannot exhaust the stack. (The parser
// also refuses documents that are nested more than 512 elements deep.)
func (r *textRenderer) walk(root *html.Node) {
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
func (r *textRenderer) enter(n *html.Node) bool {
	switch n.Type {
	case html.DocumentNode:
		return true
	case html.TextNode:
		r.text(CleanUntrusted(n.Data))
		return false
	case html.ElementNode:
		// Handled below.
	default:
		return false // comments, doctype and so on
	}

	tag := n.Data
	if dropTags[tag] || isHidden(n) {
		return false
	}
	switch {
	case tag == "br":
		r.hardBreak()
		return false
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
		if u := linkURL(n); u != "" {
			// Write a pending "- " first, so it is not counted as link text.
			r.flushBullet()
			r.links = append(r.links, linkMark{node: n, start: len(r.buf), url: u})
		}
	}
	return true
}

// exit ends an element that enter returned true for.
func (r *textRenderer) exit(n *html.Node) {
	if n.Type != html.ElementNode {
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
}

// endLink writes the url after the text of a link.
func (r *textRenderer) endLink(n *html.Node) {
	last := len(r.links) - 1
	if last < 0 || r.links[last].node != n {
		return // this link has no url to show
	}
	m := r.links[last]
	r.links = r.links[:last]

	shown := strings.TrimSpace(string(r.buf[m.start:]))
	switch shown {
	case "":
		// No visible text, for example a link around an image. Show the url
		// alone, so the reader still sees where the link goes.
		r.space()
		r.text(m.url)
	case m.url:
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

// linkURL returns the url to show for an a element, or "" when none is shown.
// Only http, https and mailto urls are shown.
func linkURL(n *html.Node) string {
	href := ""
	found := false
	for _, a := range n.Attr {
		if a.Key == "href" {
			href, found = a.Val, true
			break
		}
	}
	if !found {
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

	scheme, _, ok := strings.Cut(href, ":")
	if !ok {
		return "" // a relative url has no meaning in a mail
	}
	switch lowerASCII(scheme) {
	case "http", "https", "mailto":
		return href
	}
	return ""
}

// --- hidden elements ---

// isHidden reports whether a reader of a mail client cannot see the element.
func isHidden(n *html.Node) bool {
	hasOpen := false
	for _, a := range n.Attr {
		switch a.Key {
		case "hidden":
			return true
		case "aria-hidden":
			if strings.EqualFold(strings.TrimSpace(a.Val), "true") {
				return true
			}
		case "style":
			if styleHides(a.Val) {
				return true
			}
		case "open":
			hasOpen = true
		}
	}
	return n.Data == "dialog" && !hasOpen
}

// styleHides reports whether an inline style hides the element. It finds:
//   - display:none
//   - visibility:hidden
//   - opacity:0
//   - a font size of zero (0, 0px, 0pt, 0em, 0rem, 0% and so on)
//   - max-height:0 together with overflow:hidden
//
// Before it looks, it reads the style the way a browser does: it ignores
// comments, decodes escapes, ignores case and white space, and ignores
// "!important".
func styleHides(style string) bool {
	maxHeightZero, overflowHidden := false, false
	for _, decl := range strings.Split(normalizeCSS(style), ";") {
		name, value, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		name = lowerASCII(strings.TrimSpace(name))
		value, _, _ = strings.Cut(value, "!")
		value = lowerASCII(strings.TrimSpace(value))

		switch name {
		case "display":
			if value == "none" {
				return true
			}
		case "visibility":
			if value == "hidden" {
				return true
			}
		case "opacity", "font-size":
			if isZeroNumber(value) {
				return true
			}
		case "max-height":
			maxHeightZero = maxHeightZero || isZeroNumber(value)
		case "overflow", "overflow-y":
			overflowHidden = overflowHidden || value == "hidden"
		}
	}
	return maxHeightZero && overflowHidden
}

// isZeroNumber reports whether v is the number zero with an optional unit,
// for example "0", "0.0", "0px" or "0%".
func isZeroNumber(v string) bool {
	end := 0
	for end < len(v) && strings.IndexByte("+-.0123456789", v[end]) >= 0 {
		end++
	}
	if end == 0 {
		return false
	}
	f, err := strconv.ParseFloat(v[:end], 64)
	if err != nil || f != 0 {
		return false
	}
	for _, c := range v[end:] {
		if (c < 'a' || c > 'z') && c != '%' {
			return false
		}
	}
	return true
}

// normalizeCSS removes comments and decodes escapes, as a CSS parser does.
// A comment becomes one space, because in CSS it separates two names.
func normalizeCSS(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "/*"):
			b.WriteByte(' ')
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return b.String() // a comment without an end runs to the end
			}
			i += 2 + end + 2

		case s[i] == '\\':
			i++
			if i >= len(s) {
				b.WriteRune(utf8.RuneError)
				break
			}
			// One to six hex digits give a code point, and one white space
			// character after them belongs to the escape.
			var c rune
			digits := 0
			for i+digits < len(s) && digits < 6 {
				d, ok := hexValue(s[i+digits])
				if !ok {
					break
				}
				c = c*16 + d
				digits++
			}
			if digits > 0 {
				if c == 0 || c > unicode.MaxRune || (c >= 0xD800 && c <= 0xDFFF) {
					c = utf8.RuneError
				}
				b.WriteRune(c)
				i += digits
				if strings.HasPrefix(s[i:], "\r\n") {
					i += 2
				} else if i < len(s) && strings.IndexByte(" \t\n\r\f", s[i]) >= 0 {
					i++
				}
				break
			}
			// A backslash before a line break is not an escape. Any other
			// character after a backslash stands for itself.
			if strings.IndexByte("\n\r\f", s[i]) >= 0 {
				break
			}
			c, size := utf8.DecodeRuneInString(s[i:])
			b.WriteRune(c)
			i += size

		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// hexValue returns the value of a hex digit, and false if c is not one.
func hexValue(c byte) (rune, bool) {
	switch {
	case c >= '0' && c <= '9':
		return rune(c - '0'), true
	case c >= 'a' && c <= 'f':
		return rune(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return rune(c-'A') + 10, true
	}
	return 0, false
}

// lowerASCII lowercases the letters A to Z only. CSS ignores case for ASCII
// letters only, and unicode.ToLower would also change some other characters.
func lowerASCII(s string) string {
	return strings.Map(func(c rune) rune {
		if c >= 'A' && c <= 'Z' {
			return c + 'a' - 'A'
		}
		return c
	}, s)
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
