package mime

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// This file reads just enough CSS to find elements that a mail client hides.
// It is not a full CSS parser. It reads inline styles and the rules in style
// elements, and it knows about comments, strings, escapes and brackets, so a
// sender cannot hide a declaration from us by writing it in an odd way.

// --- what a list of declarations does ---

// fontSizeMode says how a font-size value is worked out.
type fontSizeMode uint8

const (
	fsPixels fontSizeMode = iota + 1 // a size in pixels, whatever the parent is
	fsParent                         // a multiple of the size of the parent (em, %, ex, larger, inherit)
	fsRoot                           // a multiple of the size of the html element (rem)
)

// fontSize is the value of a font-size declaration.
type fontSize struct {
	mode  fontSizeMode
	value float64
}

const (
	// initialFontSize is "medium", the size of the page when nothing sets it.
	initialFontSize = 16.0
	// minReadableFontSize is 1pt. Text of this size or smaller cannot be read.
	minReadableFontSize = 96.0 / 72.0
	// maxFontSize keeps sizes finite, so that a multiple of zero is zero and
	// never a number that is not a number.
	maxFontSize = 1e12
)

// smallest remembers the smallest of some numbers.
type smallest struct {
	set   bool
	value float64
}

func (s smallest) with(v float64) smallest {
	if !s.set || v < s.value {
		return smallest{true, v}
	}
	return s
}

func (s smallest) merge(o smallest) smallest {
	if o.set {
		return s.with(o.value)
	}
	return s
}

// sizeCandidates are the font sizes that the declarations for one element give.
// Only the smallest result matters, so the smallest value is kept for each of
// the three ways a size is worked out (pixels, multiple of the parent, multiple
// of the html element). The smallest result for any parent is then the
// smallest of these three results. This keeps the memory the same however many
// declarations there are.
type sizeCandidates struct {
	pixels smallest
	parent smallest
	root   smallest
}

func (m sizeCandidates) add(f fontSize) sizeCandidates {
	switch f.mode {
	case fsPixels:
		m.pixels = m.pixels.with(f.value)
	case fsParent:
		m.parent = m.parent.with(f.value)
	case fsRoot:
		m.root = m.root.with(f.value)
	}
	return m
}

func (m sizeCandidates) merge(o sizeCandidates) sizeCandidates {
	m.pixels = m.pixels.merge(o.pixels)
	m.parent = m.parent.merge(o.parent)
	m.root = m.root.merge(o.root)
	return m
}

func (m sizeCandidates) any() bool {
	return m.pixels.set || m.parent.set || m.root.set
}

// smallest returns the smallest size in pixels, given the size of the parent
// and the size of the html element.
func (m sizeCandidates) smallest(parent, root float64) float64 {
	size := float64(maxFontSize)
	if m.pixels.set {
		size = min(size, m.pixels.value)
	}
	if m.parent.set {
		size = min(size, parent*m.parent.value)
	}
	if m.root.set {
		size = min(size, root*m.root.value)
	}
	return size
}

// styleEffect is what the declarations that apply to an element do to it.
//
// For the inherited properties (visibility and font size) the effect holds
// every value that any declaration gives: from the style attribute and from the
// rules of the element, its classes and its id. It does not matter which one
// comes first, which source is stronger, or whether a declaration has
// !important. If any of them hides, the element is hidden. This is stricter
// than a browser, which picks one value. The cost is that text which a sender
// hides and then shows again on the same element is dropped. The gain is that
// no way of ordering or marking declarations can show hidden text.
type styleEffect struct {
	// drop means the element and everything in it is not shown, and nothing
	// inside can show itself again. display:none, a zero opacity and a zero
	// height with clipped overflow work like this.
	drop bool

	// Values of visibility from the declarations.
	hidden         bool // hidden or collapse
	visible        bool // visible or initial
	sameVisibility bool // inherit, unset, revert or revert-layer: the value of the parent
	sizes          sizeCandidates
}

// merge adds the effect of other declarations that apply to the same element.
func (e styleEffect) merge(o styleEffect) styleEffect {
	e.drop = e.drop || o.drop
	e.hidden = e.hidden || o.hidden
	e.visible = e.visible || o.visible
	e.sameVisibility = e.sameVisibility || o.sameVisibility
	e.sizes = e.sizes.merge(o.sizes)
	return e
}

// invisible works out the visibility of the element, given the one of its
// parent. It returns true for hidden. An element with no declaration keeps the
// value of its parent.
func (e styleEffect) invisible(parent bool) bool {
	if !e.hidden && !e.visible && !e.sameVisibility {
		return parent
	}
	return e.hidden || (e.sameVisibility && parent)
}

// size works out the font size of the element in pixels, given the size of its
// parent and the size of the html element. It is the smallest size that any
// declaration gives. An element with no declaration keeps the size of its
// parent.
func (e styleEffect) size(parent, root float64) float64 {
	if !e.sizes.any() {
		return parent
	}
	return e.sizes.smallest(parent, root)
}

// isGlobalKeyword reports whether v is a keyword that takes the value of the
// parent: inherit, unset, revert or revert-layer. (Both inherited properties
// take the value of the parent for all four.)
func isGlobalKeyword(v string) bool {
	switch v {
	case "inherit", "unset", "revert", "revert-layer":
		return true
	}
	return false
}

// parseStyle reads a list of declarations: the value of a style attribute, or
// the inside of a rule. It looks for:
//   - display:none
//   - opacity of zero or less
//   - max-height:0 or height:0, together with overflow of hidden or clip
//   - visibility:hidden and visibility:collapse, and the other values
//   - the font size, from font-size and from the font shorthand
//
// Names and values are matched the way a browser does it: ignoring case,
// white space, comments and escapes. Whatever follows a "!" that is not inside
// a string or after a backslash is cut off, whatever it says: "!important"
// does not change anything here (see styleEffect).
func parseStyle(style string) styleEffect {
	var e styleEffect
	zeroHeight, clipped := false, false
	for _, decl := range declarations(stripCSSComments(style)) {
		name, value, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		name = lowerASCII(strings.TrimSpace(unescapeCSS(name)))
		value = value[:scanCSS(value, 0, "!")]
		value = lowerASCII(strings.TrimSpace(unescapeCSS(value)))

		switch name {
		case "display":
			if value == "none" {
				e.drop = true
			}
		case "visibility":
			switch {
			case value == "hidden" || value == "collapse":
				e.hidden = true
			case value == "visible" || value == "initial":
				e.visible = true
			case isGlobalKeyword(value):
				e.sameVisibility = true
			}
		case "opacity":
			if n, _, ok := numberWithUnit(value); ok && n <= 0 {
				e.drop = true
			}
		case "font-size":
			if f, ok := parseFontSize(value, true); ok {
				e.sizes = e.sizes.add(f)
			}
		case "font":
			if f, ok := fontShorthandSize(value); ok {
				e.sizes = e.sizes.add(f)
			}
		case "max-height", "height":
			if n, _, ok := numberWithUnit(value); ok && n <= 0 {
				zeroHeight = true
			}
		case "overflow", "overflow-y":
			// With two values the second one is for the vertical direction.
			if words := strings.Fields(value); len(words) > 0 {
				last := words[len(words)-1]
				clipped = clipped || last == "hidden" || last == "clip"
			}
		}
	}
	if zeroHeight && clipped {
		e.drop = true
	}
	return e
}

// numberWithUnit splits a value such as "0.5px" or "1e3px" into the number
// and the unit. The unit must already be in lower case. It returns false if
// v is not a number with an optional unit of letters or "%". A number that is
// too big for a size is kept at the limit.
func numberWithUnit(v string) (number float64, unit string, ok bool) {
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }

	i, digits := 0, 0
	if i < len(v) && (v[i] == '+' || v[i] == '-') {
		i++
	}
	for i < len(v) && isDigit(v[i]) {
		i++
		digits++
	}
	if i+1 < len(v) && v[i] == '.' && isDigit(v[i+1]) {
		i++
		for i < len(v) && isDigit(v[i]) {
			i++
			digits++
		}
	}
	if digits == 0 {
		return 0, "", false
	}
	// An exponent is "e" and digits, with an optional sign. "e" with no digit
	// after it is the start of a unit such as "em".
	if i < len(v) && (v[i] == 'e' || v[i] == 'E') {
		j := i + 1
		if j < len(v) && (v[j] == '+' || v[j] == '-') {
			j++
		}
		if j < len(v) && isDigit(v[j]) {
			for j < len(v) && isDigit(v[j]) {
				j++
			}
			i = j
		}
	}

	f, err := strconv.ParseFloat(v[:i], 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, "", false
	}
	for _, c := range v[i:] {
		if (c < 'a' || c > 'z') && c != '%' {
			return 0, "", false
		}
	}
	return max(-maxFontSize, min(f, maxFontSize)), v[i:], true
}

// absoluteUnits give the size of one unit in pixels.
var absoluteUnits = map[string]float64{
	"px": 1,
	"pt": 96.0 / 72.0,
	"pc": 16,
	"in": 96,
	"cm": 96 / 2.54,
	"mm": 96 / 25.4,
	"q":  96 / 101.6,
}

// fontSizeKeywords give the size of the keywords of font-size in pixels.
var fontSizeKeywords = map[string]float64{
	"xx-small":  9,
	"x-small":   10,
	"small":     13,
	"medium":    16,
	"large":     18,
	"x-large":   24,
	"xx-large":  32,
	"xxx-large": 48,
}

// parseFontSize reads the value of font-size. The value must already be in
// lower case. The keywords inherit, unset, revert and revert-layer are the size
// of the parent. It returns false for a value that a browser would not accept,
// so a declaration like that gives no size. Functions such as calc() are not
// worked out and are treated the same way.
//
// A number with no unit is a size in pixels if unitlessIsPixels is true (a page
// without a doctype does this). Zero never needs a unit.
func parseFontSize(v string, unitlessIsPixels bool) (fontSize, bool) {
	if isGlobalKeyword(v) {
		return fontSize{fsParent, 1}, true
	}
	return parseSizeValue(v, unitlessIsPixels)
}

// parseSizeValue reads a size: a keyword such as "small", or a number with a
// unit.
func parseSizeValue(v string, unitlessIsPixels bool) (fontSize, bool) {
	if px, ok := fontSizeKeywords[v]; ok {
		return fontSize{fsPixels, px}, true
	}
	switch v {
	case "initial":
		return fontSize{fsPixels, initialFontSize}, true
	case "larger":
		return fontSize{fsParent, 1.2}, true
	case "smaller":
		return fontSize{fsParent, 1 / 1.2}, true
	}

	n, unit, ok := numberWithUnit(v)
	if !ok || n < 0 {
		return fontSize{}, false
	}
	// Zero is zero in every unit, also in a unit that is not known here.
	if n == 0 {
		return fontSize{fsPixels, 0}, true
	}
	if unit == "" {
		if unitlessIsPixels {
			return fontSize{fsPixels, n}, true
		}
		return fontSize{}, false
	}
	if px, ok := absoluteUnits[unit]; ok {
		return fontSize{fsPixels, n * px}, true
	}
	switch unit {
	case "em":
		return fontSize{fsParent, n}, true
	case "%":
		return fontSize{fsParent, n / 100}, true
	case "ex", "ch":
		return fontSize{fsParent, n / 2}, true
	case "rem":
		return fontSize{fsRoot, n}, true
	case "vw", "vh", "vmin", "vmax":
		// The width of the window is not known. Take 1000 pixels.
		return fontSize{fsPixels, n * 10}, true
	}
	return fontSize{}, false
}

// fontShorthandSize reads the size from the value of the font shorthand:
// "font: [style] [weight] size[/line-height] family". The size is the first
// word, before any "/", that is zero or has a unit, or that is a size keyword.
// A number with no unit is a weight, and an angle is the slant of "oblique".
func fontShorthandSize(value string) (fontSize, bool) {
	// One of the keywords for the whole value is the size of the parent. As a
	// word in the middle it could be the name of a font.
	if isGlobalKeyword(value) {
		return fontSize{fsParent, 1}, true
	}
	for _, word := range strings.Fields(value) {
		word, _, _ = strings.Cut(word, "/")
		if word == "" {
			continue
		}
		if _, unit, ok := numberWithUnit(word); ok {
			switch unit {
			case "deg", "grad", "rad", "turn":
				continue
			}
		}
		if f, ok := parseSizeValue(word, false); ok {
			return f, true
		}
	}
	return fontSize{}, false
}

// --- rules in style elements ---

// styleRules are the effects of the rules in the style elements, by the name
// that the rule is for. A rule is used only if its selector is one class
// (.x), one id (#x) or one element name (p), alone or in a comma list.
// Everything else is ignored: selectors with more than one part, and at-rules
// such as @media and @import.
//
// A rule is kept if it hides, and also if it sets a font size or a
// visibility, because a child can use its own size to show its text in a
// parent that has none. The rules for one name are put together, and so are the
// rules for an element (see styleEffect), so the order of the rules does not
// matter.
type styleRules struct {
	classes  map[string]styleEffect
	ids      map[string]styleEffect
	elements map[string]styleEffect
}

// collectStyleRules reads every style element of the document.
func collectStyleRules(doc *html.Node) *styleRules {
	h := &styleRules{
		classes:  map[string]styleEffect{},
		ids:      map[string]styleEffect{},
		elements: map[string]styleEffect{},
	}
	for n := doc; n != nil; n = nextNode(n) {
		if n.Type != html.ElementNode || n.Data != "style" {
			continue
		}
		var sheet strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				sheet.WriteString(c.Data)
			}
		}
		h.addStyleSheet(sheet.String())
	}
	return h
}

// nextNode returns the node after n in document order, or nil at the end. It
// does not call itself, so a deep document cannot exhaust the stack.
func nextNode(n *html.Node) *html.Node {
	if n.FirstChild != nil {
		return n.FirstChild
	}
	for ; n != nil; n = n.Parent {
		if n.NextSibling != nil {
			return n.NextSibling
		}
	}
	return nil
}

// effectFor returns what the rules do to the element: the rules for its name,
// for each of its classes and for its id, all put together. Names are compared
// ignoring case, because a page without a doctype matches classes and ids that
// way.
func (h *styleRules) effectFor(n *html.Node) styleEffect {
	e := h.elements[n.Data]
	var class, id string
	for _, a := range n.Attr {
		switch a.Key {
		case "class":
			class = a.Val
		case "id":
			id = a.Val
		}
	}
	if class != "" && len(h.classes) > 0 {
		for _, c := range strings.FieldsFunc(class, isASCIISpace) {
			e = e.merge(h.classes[lowerASCII(c)])
		}
	}
	if id != "" && len(h.ids) > 0 {
		e = e.merge(h.ids[lowerASCII(id)])
	}
	return e
}

func isASCIISpace(c rune) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// addStyleSheet reads the rules of one style element.
func (h *styleRules) addStyleSheet(css string) {
	s := stripCSSComments(css)
	for i := 0; i < len(s); {
		j := scanCSS(s, i, "{;")
		if j >= len(s) {
			return // text that is not followed by a block
		}
		prelude := s[i:j]
		if s[j] == ';' {
			i = j + 1 // a statement such as @import
			continue
		}
		end := matchBrace(s, j) // the closing brace, or len(s) if it is missing
		body := s[j+1 : end]
		i = end + 1

		// An at-rule such as @media has a prelude that starts with "@". That is
		// not a name, so addSelector ignores it, and its block has been read
		// past without looking inside.
		prelude = trimPrelude(prelude)
		effect := parseStyle(body)
		if effect == (styleEffect{}) {
			continue
		}
		for _, sel := range splitCSS(prelude, ',') {
			h.addSelector(strings.TrimSpace(sel), effect)
		}
	}
}

// trimPrelude removes white space and the <!-- and --> marks that old mails
// put around style sheets, from the start of a rule.
func trimPrelude(s string) string {
	for {
		t := strings.TrimSpace(s)
		t = strings.TrimPrefix(t, "<!--")
		t = strings.TrimPrefix(t, "-->")
		if t == s {
			return s
		}
		s = t
	}
}

// addSelector adds the effect of a rule to a selector that is one class, one
// id or one element name. The effects of all the rules for a name are put
// together.
func (h *styleRules) addSelector(sel string, effect styleEffect) {
	var names map[string]styleEffect
	var name string
	switch {
	case strings.HasPrefix(sel, "."):
		names, name = h.classes, sel[1:]
	case strings.HasPrefix(sel, "#"):
		names, name = h.ids, sel[1:]
	default:
		names, name = h.elements, sel
	}
	if !isCSSIdent(name) {
		return
	}
	name = lowerASCII(unescapeCSS(name))
	names[name] = names[name].merge(effect)
}

// isCSSIdent reports whether s is one name: letters, digits, "-", "_",
// non-ASCII characters and escapes.
func isCSSIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\':
			_, next, ok := readEscape(s, i)
			if !ok {
				return false
			}
			i = next
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c >= 0x80:
			i++
		default:
			return false
		}
	}
	return true
}

// --- reading the structure of CSS text ---

// endOfString returns the position after the string that starts at s[i]. A
// string ends at its closing quote. A string that is not closed ends before the
// next line break, like in a browser. The result is always greater than i.
func endOfString(s string, i int) int {
	quote := s[i]
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++ // skip the escaped byte
		case quote:
			return j + 1
		case '\n', '\r', '\f':
			return j
		}
	}
	return len(s)
}

// scanCSS returns the position of the first byte at or after i that is one of
// stops. It does not stop inside a string, after a backslash, or inside round
// or square brackets. It returns len(s) if there is no such byte.
func scanCSS(s string, i int, stops string) int {
	depth := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\':
			i += 2
			continue
		case c == '"' || c == '\'':
			i = endOfString(s, i)
			continue
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			if depth > 0 {
				depth--
			}
		case depth == 0 && strings.IndexByte(stops, c) >= 0:
			return i
		}
		i++
	}
	return len(s)
}

// matchBrace returns the position of the "}" that closes the "{" at s[open],
// or len(s) if it is missing. Blocks inside the block are skipped.
func matchBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '"', '\'':
			i = endOfString(s, i)
			continue
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return len(s)
}

// splitCSS splits s at every sep that is not inside a string, after a
// backslash, or inside brackets.
func splitCSS(s string, sep byte) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); {
		i = scanCSS(s, i, string(sep))
		if i >= len(s) {
			break
		}
		parts = append(parts, s[start:i])
		i++
		start = i
	}
	return append(parts, s[start:])
}

// declarations splits the inside of a rule into declarations. A rule inside
// the rule (CSS nesting) is left out, and the declarations after it are kept.
func declarations(s string) []string {
	var decls []string
	start := 0
	for i := 0; i < len(s); {
		i = scanCSS(s, i, ";{")
		if i >= len(s) {
			break
		}
		if s[i] == ';' {
			decls = append(decls, s[start:i])
		} else {
			i = matchBrace(s, i)
		}
		i++
		start = i
	}
	if start < len(s) {
		decls = append(decls, s[start:])
	}
	return decls
}

// --- comments and escapes ---

// stripCSSComments replaces each comment with one space. In CSS a comment
// separates two names, so it is not the same as removing it. Comment marks
// inside a string, or after a backslash, are not comments.
func stripCSSComments(s string) string {
	if !strings.Contains(s, "/*") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '/' && strings.HasPrefix(s[i:], "/*"):
			b.WriteByte(' ')
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return b.String() // a comment without an end runs to the end
			}
			i += 2 + end + 2
		case c == '\\':
			end := min(i+2, len(s))
			b.WriteString(s[i:end])
			i = end
		case c == '"' || c == '\'':
			end := endOfString(s, i)
			b.WriteString(s[i:end])
			i = end
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// unescapeCSS decodes CSS escapes: a backslash with one to six hex digits is a
// code point, and a backslash with any other character stands for that
// character.
func unescapeCSS(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			i++
			continue
		}
		c, next, ok := readEscape(s, i)
		if ok {
			b.WriteRune(c)
		}
		i = next
	}
	return b.String()
}

// readEscape reads the escape that starts with the backslash at s[i]. It
// returns the character, and the position after the escape. ok is false for a
// backslash before a line break, which is not an escape; next is then the
// position of the line break.
func readEscape(s string, i int) (c rune, next int, ok bool) {
	i++ // the backslash
	if i >= len(s) {
		return utf8.RuneError, i, true
	}
	// One to six hex digits give a code point.
	var v rune
	digits := 0
	for i+digits < len(s) && digits < 6 {
		d, isHex := hexValue(s[i+digits])
		if !isHex {
			break
		}
		v = v*16 + d
		digits++
	}
	if digits > 0 {
		i += digits
		// One white space character after the digits belongs to the escape.
		if strings.HasPrefix(s[i:], "\r\n") {
			i += 2
		} else if i < len(s) && strings.IndexByte(" \t\n\r\f", s[i]) >= 0 {
			i++
		}
		if v == 0 || v > unicode.MaxRune || (v >= 0xD800 && v <= 0xDFFF) {
			v = utf8.RuneError
		}
		return v, i, true
	}
	if strings.IndexByte("\n\r\f", s[i]) >= 0 {
		return 0, i, false
	}
	r, size := utf8.DecodeRuneInString(s[i:])
	return r, i + size, true
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
