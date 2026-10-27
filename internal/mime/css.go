package mime

import (
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

// --- hiding declarations ---

// styleHides reports whether a list of declarations hides the element. The
// list is the value of a style attribute, or the inside of a rule. It finds:
//   - display:none
//   - visibility:hidden
//   - opacity:0
//   - a font size of zero (0, 0px, 0em, 0% and so on), or of 1px or 1pt or
//     less
//   - max-height:0 together with overflow:hidden
//
// Names and values are matched the way a browser does it: ignoring case,
// white space, comments, escapes and "!important".
func styleHides(style string) bool {
	maxHeightZero, overflowHidden := false, false
	for _, decl := range declarations(stripCSSComments(style)) {
		name, value, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		name = lowerASCII(strings.TrimSpace(unescapeCSS(name)))
		value, _, _ = strings.Cut(value, "!")
		value = lowerASCII(strings.TrimSpace(unescapeCSS(value)))

		switch name {
		case "display":
			if value == "none" {
				return true
			}
		case "visibility":
			if value == "hidden" {
				return true
			}
		case "opacity":
			if isZeroNumber(value) {
				return true
			}
		case "font-size":
			if isUnreadableFontSize(value) {
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

// numberWithUnit splits a value such as "0.5px" into the number and the unit.
// It returns false if v is not a number with an optional unit of letters or
// "%".
func numberWithUnit(v string) (number float64, unit string, ok bool) {
	end := 0
	for end < len(v) && strings.IndexByte("+-.0123456789", v[end]) >= 0 {
		end++
	}
	if end == 0 {
		return 0, "", false
	}
	f, err := strconv.ParseFloat(v[:end], 64)
	if err != nil {
		return 0, "", false
	}
	for _, c := range v[end:] {
		if (c < 'a' || c > 'z') && c != '%' {
			return 0, "", false
		}
	}
	return f, v[end:], true
}

// isZeroNumber reports whether v is the number zero with an optional unit,
// for example "0", "0.0", "0px" or "0%".
func isZeroNumber(v string) bool {
	f, _, ok := numberWithUnit(v)
	return ok && f == 0
}

// isUnreadableFontSize reports whether text of this font size cannot be read:
// zero in any unit, or 1px or 1pt or less.
func isUnreadableFontSize(v string) bool {
	f, unit, ok := numberWithUnit(v)
	if !ok {
		return false
	}
	if f == 0 {
		return true
	}
	return (unit == "px" || unit == "pt") && f > 0 && f <= 1
}

// --- rules in style elements ---

// hidingRules are the names that style elements hide. A rule is used only if
// its selector is one class (.x), one id (#x) or one element name (p), alone
// or in a comma list, and its declarations hide (see styleHides). Everything
// else is ignored: selectors with more than one part, and at-rules such as
// @media and @import.
//
// A name is hidden even if another rule, or the style attribute, would show
// the element again. That is the safe choice, because it shows less.
type hidingRules struct {
	classes  map[string]bool
	ids      map[string]bool
	elements map[string]bool
}

// collectHidingRules reads every style element of the document.
func collectHidingRules(doc *html.Node) *hidingRules {
	h := &hidingRules{
		classes:  map[string]bool{},
		ids:      map[string]bool{},
		elements: map[string]bool{},
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

// matches reports whether a rule hides the element.
func (h *hidingRules) matches(n *html.Node) bool {
	if h.elements[n.Data] {
		return true
	}
	for _, a := range n.Attr {
		switch a.Key {
		case "id":
			// Names are compared ignoring case, because a page without a
			// doctype matches classes and ids that way.
			if h.ids[lowerASCII(a.Val)] {
				return true
			}
		case "class":
			for _, c := range strings.FieldsFunc(a.Val, isASCIISpace) {
				if h.classes[lowerASCII(c)] {
					return true
				}
			}
		}
	}
	return false
}

func isASCIISpace(c rune) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// addStyleSheet reads the rules of one style element.
func (h *hidingRules) addStyleSheet(css string) {
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
		if !styleHides(body) {
			continue
		}
		for _, sel := range splitCSS(prelude, ',') {
			h.addSelector(strings.TrimSpace(sel))
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

// addSelector remembers a selector if it is one class, one id or one element
// name.
func (h *hidingRules) addSelector(sel string) {
	switch {
	case strings.HasPrefix(sel, "."):
		if name := sel[1:]; isCSSIdent(name) {
			h.classes[lowerASCII(unescapeCSS(name))] = true
		}
	case strings.HasPrefix(sel, "#"):
		if name := sel[1:]; isCSSIdent(name) {
			h.ids[lowerASCII(unescapeCSS(name))] = true
		}
	case isCSSIdent(sel):
		h.elements[lowerASCII(unescapeCSS(sel))] = true
	}
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
