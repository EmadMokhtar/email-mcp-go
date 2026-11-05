package mime

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/EmadMokhtar/email-mcp-go/internal/thirdparty/xhtml"
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
	// minReadableFontSize is 4px. Text below this size cannot be read.
	minReadableFontSize = 4.0
	// minVisibleOpacity is the lowest opacity at which text can be seen.
	minVisibleOpacity = 0.1
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
//
// The same is true for the declarations that hide only together, such as a zero
// height and clipped overflow (see hideParts): each half can come from another
// source, for example the height from a rule and the overflow from the style
// attribute.
type styleEffect struct {
	// drop means the element and everything in it is not shown, and nothing
	// inside can show itself again. display:none, a zero opacity, a transparent
	// colour, an empty clip and the other declarations that parseStyle lists
	// work like this. So do the pairs in parts (see dropped).
	drop bool

	// parts are the halves of the pairs of declarations that hide only
	// together.
	parts hideParts

	// Values of visibility from the declarations.
	hidden         bool // hidden or collapse
	visible        bool // visible or initial
	sameVisibility bool // inherit, unset, revert or revert-layer: the value of the parent
	sizes          sizeCandidates

	// unsure means that a declaration or a rule can hide the element, and this
	// code cannot work out whether it does. The report says so (see HiddenReport).
	unsure bool
}

// merge adds the effect of other declarations that apply to the same element.
func (e styleEffect) merge(o styleEffect) styleEffect {
	e.drop = e.drop || o.drop
	e.parts |= o.parts
	e.hidden = e.hidden || o.hidden
	e.visible = e.visible || o.visible
	e.sameVisibility = e.sameVisibility || o.sameVisibility
	e.sizes = e.sizes.merge(o.sizes)
	e.unsure = e.unsure || o.unsure
	return e
}

// dropped reports whether the element and everything in it is not shown: a
// declaration drops it, or both halves of a pair in parts are there.
func (e styleEffect) dropped() bool {
	return e.drop || e.parts.hide()
}

// hides reports whether the effect can hide text of an element, in any of the
// ways that this file knows. A rule that does not hide needs no warning, however
// odd its selector is.
func (e styleEffect) hides() bool {
	return e.dropped() || e.hidden || e.unsure ||
		e.sizes.smallest(initialFontSize, initialFontSize) < minReadableFontSize
}

// hideParts are declarations that hide an element only together with another
// one. Each one is a bit.
type hideParts uint8

const (
	tinyHeight hideParts = 1 << iota // height or max-height of at most 2 pixels (see isTinyLength)
	tinyWidth                        // width or max-width of at most 2 pixels (see isTinyLength)
	clipY                            // overflow hidden or clip in the vertical direction
	clipX                            // overflow hidden or clip in the horizontal direction
	positioned                       // position absolute, fixed or relative
	farOffset                        // left, top, right or bottom far off the page (see farNegative, farPositive)
	farIndent                        // text-indent far to the right (see farPositive)
	noWrap                           // white-space or text-wrap that keeps text on one line
)

// hide reports whether the parts hide the element: a tiny height with vertical
// clipping, a tiny width with horizontal clipping, a position with an offset
// that moves the element far off the page, or one line of text that is indented
// past a box that clips it.
func (p hideParts) hide() bool {
	return p&(tinyHeight|clipY) == tinyHeight|clipY ||
		p&(tinyWidth|clipX) == tinyWidth|clipX ||
		p&(positioned|farOffset) == positioned|farOffset ||
		p&(farIndent|noWrap|clipX) == farIndent|noWrap|clipX
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
//   - opacity below 0.1
//   - content-visibility:hidden
//   - mso-hide:all
//   - a colour that cannot be seen: transparent, a colour function whose alpha
//     is zero or less or none (rgba(0,0,0,0), rgb(0 0 0 / 0%), hsla(…, 0) and
//     the other colour functions), #RGBA with alpha 0 and #RRGGBBAA with
//     alpha 00 (see invisibleColor)
//   - text-indent far below zero (see farNegative)
//   - clip:rect(…) with zero or negative width or height (see emptyClipRect)
//   - clip-path (and -webkit-clip-path) with inset(…) that has a value of 50%
//     or more, or circle(…) or ellipse(…) with a radius of zero (see
//     emptyClipPath), and any shape that is not read here (polygon, path, url
//     and so on, see unknownClipPath)
//   - mask and mask-image (and the -webkit- names) with any value but none: an
//     image decides what shows, and it is not read here
//   - filter (and -webkit-filter) with opacity(…) below 0.1
//   - transform (and -webkit-transform, -ms-transform) with scale(…),
//     scaleX(…), scaleY(…) or scale3d(…) that is zero in a direction of the
//     page, and the scale property with a zero (see scalesToZero)
//   - transform with a translate or a matrix that moves the element far away,
//     and the translate property (see translatesFar)
//   - a margin far below zero on the left or the top (see farNegativeMargin)
//   - a height of at most 2 pixels, together with overflow of hidden or clip in
//     the vertical direction; a width of at most 2 pixels, together with
//     overflow of hidden or clip in the horizontal direction (and the logical
//     names block-size, inline-size, overflow-block and overflow-inline, see
//     isTinyLength)
//   - position absolute, fixed or relative, together with left, top, right,
//     bottom or one of the inset properties far off the page (see farNegative
//     and farPositive)
//   - text-indent far to the right, together with a line that does not wrap and
//     horizontal clipping
//   - visibility:hidden and visibility:collapse, and the other values
//   - the font size, from font-size and from the font shorthand
//
// All but the last two hide the element with everything in it (see
// styleEffect.drop). A colour is inherited in a browser, so a child could set
// its own colour and show its text. Here it cannot: that is stricter than a
// browser, on the safe side.
//
// A value that needs a calculation is not worked out. For font-size and for
// opacity, calc() and var() count as hidden. A negative z-index, min(), max()
// or clamp() in a font size, and calc() or var() in a property that places or
// hides an element (see calculatedCounts), keep the element, but set
// styleEffect.unsure.
//
// Names and values are matched the way a browser does it: ignoring case,
// white space, comments and escapes.
//
// For display, visibility, opacity and font-size, the list works as it does in
// a browser: the last declaration of the property counts, and an !important one
// wins over one without it (see lastDeclarations). All other declarations in
// the list count, whatever their order.
func parseStyle(style string) styleEffect {
	var e styleEffect
	var last lastDeclarations
	for _, decl := range declarations(stripCSSComments(style)) {
		rawName, raw, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		raw, bang := splitBang(raw)
		name := lowerASCII(strings.TrimSpace(unescapeCSS(rawName)))
		value := lowerASCII(strings.TrimSpace(unescapeCSS(raw)))
		sure := bang != badBang && isPlainCSS(rawName) && isPlainCSS(raw)
		important := bang == importantBang

		if calculatedCounts[name] && hasCalculation(value) {
			e.unsure = true
		}
		switch name {
		case "display":
			if d, ok := displayEffect(value); ok {
				last.keep(lastDisplay, d, sure, important)
			}
		case "visibility":
			if d, ok := visibilityEffect(value); ok {
				last.keep(lastVisibility, d, sure, important)
			}
		case "opacity":
			if d, ok := opacityEffect(value); ok {
				last.keep(lastOpacity, d, sure, important)
			}
		case "font-size":
			if d, ok := fontSizeEffect(value); ok {
				last.keep(lastFontSize, d, sure && !isBareNumber(value), important)
			}
		case "font":
			if hasCalculation(value) {
				e = e.merge(styleEffect{unsure: true, sizes: sizeCandidates{}.add(fontSize{fsPixels, 0})})
			} else if f, ok := fontShorthandSize(value); ok {
				e.sizes = e.sizes.add(f)
			}
		case "content-visibility":
			if value == "hidden" {
				e.drop = true
			}
		case "mso-hide":
			if value == "all" {
				e.drop = true
			}
		case "z-index":
			if n, _, ok := numberWithUnit(value); ok && n < 0 {
				e.unsure = true
			}
		case "color", "-webkit-text-fill-color":
			// Apple Mail and Chromium paint the text with the fill colour when it
			// is set, so a transparent fill hides the text as color does.
			if invisibleColor(value) {
				e.drop = true
			}
		case "text-indent":
			// The length can come with the keywords hanging and each-line.
			if anyWord(value, farNegative) {
				e.drop = true
			}
			if anyWord(value, farPositive) {
				e.parts |= farIndent
			}
		case "white-space", "text-wrap", "text-wrap-mode":
			if value == "nowrap" || value == "pre" {
				e.parts |= noWrap
			}
		case "clip":
			if emptyClipRect(value) {
				e.drop = true
			}
		case "clip-path", "-webkit-clip-path":
			switch {
			case emptyClipPath(value):
				e.drop = true
			case unknownClipPath(value):
				e.drop, e.unsure = true, true
			}
		case "mask", "mask-image", "-webkit-mask", "-webkit-mask-image":
			if value != "none" && value != "initial" && !isGlobalKeyword(value) {
				e.drop, e.unsure = true, true
			}
		case "filter", "-webkit-filter":
			if filtersToNothing(value) {
				e.drop = true
			}
		case "transform", "-webkit-transform", "-ms-transform":
			if scalesToZero(value) || translatesFar(value) {
				e.drop = true
			}
		case "translate":
			if anyWord(value, farTranslate) {
				e.drop = true
			}
		case "scale":
			// "scale: x [y [z]]". Only x and y matter for text on the page.
			if words := strings.Fields(value); slices.ContainsFunc(words[:min(2, len(words))], isZeroScale) {
				e.drop = true
			}
		case "margin", "margin-left", "margin-top", "margin-inline", "margin-block",
			"margin-inline-start", "margin-block-start":
			// The shorthands have up to four values. A value for the right or the
			// bottom moves nothing, but no mail needs one this far below zero.
			if anyWord(value, farNegativeMargin) {
				e.drop = true
			}
		case "max-height", "height", "max-block-size", "block-size":
			if isTinyLength(value) {
				e.parts |= tinyHeight
			}
		case "max-width", "width", "max-inline-size", "inline-size":
			if isTinyLength(value) {
				e.parts |= tinyWidth
			}
		case "overflow":
			// With two values the first one is for the horizontal direction and
			// the second one for the vertical direction. One value is for both.
			if words := strings.Fields(value); len(words) > 0 {
				if clips(words[0]) {
					e.parts |= clipX
				}
				if clips(words[len(words)-1]) {
					e.parts |= clipY
				}
			}
		case "overflow-x", "overflow-inline":
			if clips(value) {
				e.parts |= clipX
			}
		case "overflow-y", "overflow-block":
			if clips(value) {
				e.parts |= clipY
			}
		case "position":
			switch value {
			case "absolute", "fixed", "relative":
				e.parts |= positioned
			}
		case "left", "top", "right", "bottom",
			"inset", "inset-block", "inset-inline",
			"inset-block-start", "inset-block-end", "inset-inline-start", "inset-inline-end":
			// The inset shorthands have up to four values.
			if anyWord(value, farNegative) || anyWord(value, farPositive) {
				e.parts |= farOffset
			}
		}
	}
	return e.merge(last.effect())
}

// calculatedCounts are the properties where a calculated value can hide an
// element, and where the size of the box does not come in. A width such as
// calc(100% - 20px) is common, so width and height are not here.
var calculatedCounts = tagSet("display", "visibility", "content-visibility", "text-indent",
	"left", "top", "right", "bottom", "inset", "inset-block", "inset-inline",
	"transform", "-webkit-transform", "-ms-transform", "translate")

// hasCalculation reports whether a value has calc() or var().
func hasCalculation(v string) bool {
	return strings.Contains(v, "calc(") || strings.Contains(v, "var(")
}

// bangState says what follows the value of a declaration.
type bangState uint8

const (
	noBang        bangState = iota // nothing
	importantBang                  // !important
	badBang                        // a "!" with anything else: a browser drops the declaration
)

// splitBang cuts off a "!" that is not inside a string or after a backslash,
// with everything after it, and says what that was. Only CSS white space may
// come around the word. The word can have escapes, as any name in CSS.
func splitBang(value string) (string, bangState) {
	cut := scanCSS(value, 0, "!")
	if cut >= len(value) {
		return value, noBang
	}
	word := strings.Trim(value[cut+1:], " \t\n\r\f")
	if lowerASCII(unescapeCSS(word)) == "important" {
		return value[:cut], importantBang
	}
	return value[:cut], badBang
}

// isPlainCSS reports whether a name or a value as it was written has only ASCII,
// no escape and no vertical tab. Then this code reads it as a browser does.
// "14px\20" is a unit that does not exist, and a no-break space or a vertical tab
// is not white space in CSS. A browser drops such a declaration, but parseStyle
// trims it and uses it.
func isPlainCSS(raw string) bool {
	for i := range len(raw) {
		if raw[i] == '\\' || raw[i] == '\v' || raw[i] >= 0x80 {
			return false
		}
	}
	return true
}

// isBareNumber reports whether v is a number that is not zero and has no unit.
// A page with a doctype drops such a font size, and a page without one reads it
// as pixels. So this code uses it, but does not let it replace another value.
func isBareNumber(v string) bool {
	n, unit, ok := numberWithUnit(v)
	return ok && unit == "" && n != 0
}

// lastProperty names a property that works like a browser in one list of
// declarations (see lastDeclarations).
type lastProperty uint8

const (
	lastDisplay lastProperty = iota
	lastVisibility
	lastOpacity
	lastFontSize
	lastPropertyCount
)

// lastDeclarations picks the declaration that a browser uses in one list of
// declarations: the last one, and among those with !important the last one.
// "font-size:0;font-size:16px" shows the text.
//
// This matters in one direction only. A declaration can take the place of an
// earlier one only if a browser accepts it too. If this code took a value that a
// browser drops, the text would come out here and stay hidden in the browser. So
// a declaration that is not sure (a value with an escape or with characters
// outside ASCII, or a bad "!") does not replace anything. It goes in others,
// where it still counts if it hides. An unknown value of the property is not
// passed in at all.
//
// The font shorthand is not here. It has too many forms to tell whether a browser
// accepts it, so it always counts.
//
// Two lists are not put in an order: the rules of the document are not ordered
// by specificity, so there any declaration that hides counts (see styleEffect).
type lastDeclarations struct {
	values [lastPropertyCount]struct {
		effect    styleEffect
		important bool
	}
	others styleEffect
}

// keep records a declaration of p. sure says that a browser accepts it.
func (l *lastDeclarations) keep(p lastProperty, d styleEffect, sure, important bool) {
	switch {
	case !sure:
		l.others = l.others.merge(d)
	case important || !l.values[p].important:
		l.values[p].effect, l.values[p].important = d, important
	}
}

// effect returns what the kept declarations do.
func (l *lastDeclarations) effect() styleEffect {
	e := l.others
	for i := range l.values {
		e = e.merge(l.values[i].effect)
	}
	return e
}

// displayValues are the values of display that show an element. A value that
// is not here does not take the place of an earlier display:none, so an unknown
// value is on the safe side.
var displayValues = tagSet("block", "inline", "inline-block", "flex", "inline-flex", "grid",
	"inline-grid", "flow-root", "contents", "list-item", "table", "inline-table", "table-row",
	"table-row-group", "table-header-group", "table-footer-group", "table-cell", "table-column",
	"table-column-group", "table-caption", "ruby", "initial")

// displayEffect returns what a value of display does. It returns false for a
// value that is not known.
func displayEffect(v string) (styleEffect, bool) {
	if v == "none" {
		return styleEffect{drop: true}, true
	}
	return styleEffect{}, displayValues[v] || isGlobalKeyword(v)
}

// visibilityEffect returns what a value of visibility does. It returns false
// for a value that a browser does not accept.
func visibilityEffect(v string) (styleEffect, bool) {
	switch {
	case v == "hidden" || v == "collapse":
		return styleEffect{hidden: true}, true
	case v == "visible" || v == "initial":
		return styleEffect{visible: true}, true
	case isGlobalKeyword(v):
		return styleEffect{sameVisibility: true}, true
	}
	return styleEffect{}, false
}

// opacityEffect returns what a value of opacity does. It returns false for a
// value that a browser does not accept. A function such as calc() is not
// worked out, and it counts as hidden.
func opacityEffect(v string) (styleEffect, bool) {
	if strings.Contains(v, "(") {
		return styleEffect{drop: true, unsure: true}, true
	}
	if v == "initial" || isGlobalKeyword(v) {
		return styleEffect{}, true
	}
	n, ok := opacityValue(v)
	return styleEffect{drop: ok && n < minVisibleOpacity}, ok
}

// opacityValue reads an opacity: a number, or a percentage.
func opacityValue(v string) (float64, bool) {
	n, unit, ok := numberWithUnit(v)
	switch {
	case !ok:
		return 0, false
	case unit == "%":
		return n / 100, true
	case unit == "":
		return n, true
	}
	return 0, false
}

// fontSizeEffect returns what a value of font-size does. It returns false for a
// value that a browser does not accept. calc() and var() are not worked out, and
// they count as hidden. Another function (min, max, clamp) does not hide, because
// clamp() is a normal way to write a size, but it leaves the size unknown.
func fontSizeEffect(v string) (styleEffect, bool) {
	if hasCalculation(v) {
		return styleEffect{unsure: true, sizes: sizeCandidates{}.add(fontSize{fsPixels, 0})}, true
	}
	if strings.Contains(v, "(") {
		return styleEffect{unsure: true}, true
	}
	f, ok := parseFontSize(v, true)
	if !ok {
		return styleEffect{}, false
	}
	return styleEffect{sizes: sizeCandidates{}.add(f)}, true
}

// clips reports whether an overflow value cuts off what does not fit.
func clips(v string) bool {
	return v == "hidden" || v == "clip"
}

// isZeroOrLess reports whether v is a number of zero or less, with or without
// a unit, such as "0", "0px", "-0" or "0.0em".
func isZeroOrLess(v string) bool {
	n, _, ok := numberWithUnit(v)
	return ok && n <= 0
}

// anyWord reports whether test is true for a word of v.
func anyWord(v string, test func(string) bool) bool {
	return slices.ContainsFunc(strings.Fields(v), test)
}

// farNegative reports whether v is a length that moves an element or a line
// of text far off the page: below zero by at least 100px (in any absolute unit,
// or as a number with no unit, which a page without a doctype reads as
// pixels), 5em, 5rem, 10ex, 10ch or 100%. Small negative values are used for
// normal layout, so they do not count. A unit that is not known here counts
// like pixels, which is the safe side.
func farNegative(v string) bool {
	n, unit, ok := numberWithUnit(v)
	if !ok || n >= 0 {
		return false
	}
	d := -n
	if px, ok := absoluteUnits[unit]; ok {
		return d*px >= 100
	}
	switch unit {
	case "em", "rem":
		return d >= 5
	case "ex", "ch":
		return d/2 >= 5
	case "%":
		return d >= 100
	case "vw", "vh", "vmin", "vmax":
		// The size of the window is not known. Take 1000 pixels.
		return d*10 >= 100
	}
	return d >= 100
}

// farPositive reports whether v is a length that moves an element or a line of
// text far off the page to the right or down: at least farPixels. A long mail
// can be some thousand pixels high, so this is more than for farNegative, where
// any element ends up above or left of the page.
func farPositive(v string) bool {
	n, unit, ok := numberWithUnit(v)
	return ok && n > 0 && approxPixels(n, unit) >= farPixels
}

// farNegativeMargin reports whether v is a margin far below zero. A mail can use
// a margin of some -100px to put text on an image, so this is more than for
// farNegative.
func farNegativeMargin(v string) bool {
	n, unit, ok := numberWithUnit(v)
	return ok && n < 0 && approxPixels(-n, unit) >= farMarginPixels
}

// farTranslate reports whether v moves an element far away, in any direction.
func farTranslate(v string) bool {
	n, unit, ok := numberWithUnit(v)
	return ok && approxPixels(max(n, -n), unit) >= farMarginPixels
}

const (
	farPixels       = 3000.0 // see farPositive
	farMarginPixels = 1000.0 // see farNegativeMargin and farTranslate
	tinyBoxPixels   = 2.0    // see isTinyLength
)

// approxPixels returns the size of a length of zero or more in pixels. The size
// of the font and of the window are not known here, so it takes the normal font
// size and a window of 1000 pixels. A unit that is not known counts like pixels.
func approxPixels(n float64, unit string) float64 {
	if px, ok := absoluteUnits[unit]; ok {
		return n * px
	}
	switch unit {
	case "em", "rem":
		return n * initialFontSize
	case "ex", "ch":
		return n * initialFontSize / 2
	case "%", "vw", "vh", "vmin", "vmax":
		return n * 10
	}
	return n
}

// isTinyLength reports whether v is a size that leaves no room for text: zero or
// less, or at most 2 pixels.
func isTinyLength(v string) bool {
	n, unit, ok := numberWithUnit(v)
	return ok && (n <= 0 || approxPixels(n, unit) <= tinyBoxPixels)
}

// invisibleColor reports whether a colour value cannot be seen: transparent, a
// hex colour with an alpha of zero (#RGBA or #RRGGBBAA), or a colour function
// whose alpha is zero or less, or none. The alpha of a function is the value
// after "/", or the fourth value of rgb, rgba, hsl and hsla with commas.
func invisibleColor(v string) bool {
	if v == "transparent" {
		return true
	}
	if hex, ok := strings.CutPrefix(v, "#"); ok {
		for i := range len(hex) {
			if !isHex(hex[i]) {
				return false
			}
		}
		switch len(hex) {
		case 4:
			return hex[3] == '0'
		case 8:
			return hex[6:] == "00"
		}
		return false
	}
	fns := cssFunctions(v)
	if len(fns) != 1 {
		return false
	}
	f := fns[0]
	alpha := ""
	switch f.name {
	case "rgb", "rgba", "hsl", "hsla":
		if parts := splitCSS(f.args, ','); len(parts) == 4 {
			alpha = parts[3]
		}
	case "hwb", "lab", "lch", "oklab", "oklch", "color":
	default:
		return false
	}
	if parts := splitCSS(f.args, '/'); len(parts) == 2 {
		alpha = parts[1]
	}
	alpha = strings.TrimSpace(alpha)
	if alpha == "none" {
		return true
	}
	n, unit, ok := numberWithUnit(alpha)
	return ok && (unit == "" || unit == "%") && n <= 0
}

// emptyClipRect reports whether a clip value is rect(top, right, bottom, left)
// with a width (right - left) or a height (bottom - top) of zero or less. The
// values may be written with commas or with spaces. auto is the edge of the
// element, which is not known here, so a size with auto does not count. Two
// values in units that cannot be compared (such as em and px) do not count
// either.
func emptyClipRect(v string) bool {
	fns := cssFunctions(v)
	if len(fns) != 1 || fns[0].name != "rect" {
		return false
	}
	edges := fns[0].values()
	if len(edges) != 4 {
		return false
	}
	top, right, bottom, left := edges[0], edges[1], edges[2], edges[3]
	if w, ok := lengthDifference(left, right); ok && w <= 0 {
		return true
	}
	h, ok := lengthDifference(top, bottom)
	return ok && h <= 0
}

// lengthDifference returns b - a for two lengths, and false when it cannot be
// worked out: a value is auto or not a length, or the units cannot be
// compared.
func lengthDifference(a, b string) (float64, bool) {
	na, ua, oka := numberWithUnit(a)
	nb, ub, okb := numberWithUnit(b)
	if !oka || !okb {
		return 0, false
	}
	pa, pxa := lengthPixels(na, ua)
	pb, pxb := lengthPixels(nb, ub)
	switch {
	case pxa && pxb:
		return pb - pa, true
	case ua == ub:
		return nb - na, true
	}
	return 0, false
}

// lengthPixels returns a length in pixels, and false when the unit is relative
// to something that is not known here. Zero is zero in every unit, and a number
// with no unit is pixels (as on a page without a doctype).
func lengthPixels(n float64, unit string) (float64, bool) {
	if n == 0 || unit == "" {
		return n, true
	}
	if px, ok := absoluteUnits[unit]; ok {
		return n * px, true
	}
	return 0, false
}

// emptyClipPath reports whether a clip-path value leaves nothing to see:
// inset(…) with a value of 50% or more, or circle(…) or ellipse(…) with a
// radius of zero or less. A value of 50% on two sides of a box that face each
// other leaves nothing. Any value of 50% is counted, which can also hide a box
// that keeps half of its width; that is the safe side. A length in pixels is
// not counted, because the size of the box is not known.
func emptyClipPath(v string) bool {
	for _, f := range cssFunctions(v) {
		switch f.name {
		case "inset":
			// The values before "round" are the four sides. The values after it
			// are the radius of the corners.
			for _, w := range f.values() {
				if w == "round" {
					break
				}
				if n, unit, ok := numberWithUnit(w); ok && unit == "%" && n >= 50 {
					return true
				}
			}
		case "circle", "ellipse":
			// The radii come before "at" and the position of the centre.
			for _, w := range f.values() {
				if w == "at" {
					break
				}
				if isZeroOrLess(w) {
					return true
				}
			}
		}
	}
	return false
}

// unknownClipPath reports whether a clip-path value has a shape that is not read
// here. Such a shape can leave nothing, and it is rare in mail.
func unknownClipPath(v string) bool {
	return slices.ContainsFunc(cssFunctions(v), func(f cssFunction) bool {
		switch f.name {
		case "polygon", "path", "shape", "url", "rect", "xywh":
			return true
		}
		return false
	})
}

// filtersToNothing reports whether a filter value has opacity(…) that makes the
// element too faint to see.
func filtersToNothing(v string) bool {
	return slices.ContainsFunc(cssFunctions(v), func(f cssFunction) bool {
		if f.name != "opacity" {
			return false
		}
		n, ok := opacityValue(strings.TrimSpace(f.args))
		return ok && n < minVisibleOpacity
	})
}

// translatesFar reports whether a transform value moves the element far away:
// translate(…), translate3d(…), translateX(…), translateY(…), or the two values
// of matrix(…) and matrix3d(…) that move it. The names are already in lower
// case. A move along the z axis does not move the element on the page.
func translatesFar(v string) bool {
	for _, f := range cssFunctions(v) {
		vals := f.values()
		var moves []string
		switch f.name {
		case "translate", "translate3d":
			moves = vals[:min(2, len(vals))]
		case "translatex", "translatey":
			moves = vals[:min(1, len(vals))]
		case "matrix":
			if len(vals) == 6 {
				moves = vals[4:]
			}
		case "matrix3d":
			if len(vals) == 16 {
				moves = vals[12:14]
			}
		}
		if slices.ContainsFunc(moves, farTranslate) {
			return true
		}
	}
	return false
}

// scalesToZero reports whether a transform value has a scale function that is
// zero in a direction of the page: scale(0), scale(0, y), scale(x, 0),
// scaleX(0), scaleY(0), or scale3d with a zero x or y. The names are already
// in lower case.
func scalesToZero(v string) bool {
	for _, f := range cssFunctions(v) {
		vals := f.values()
		switch f.name {
		case "scale", "scale3d":
			if slices.ContainsFunc(vals[:min(2, len(vals))], isZeroScale) {
				return true
			}
		case "scalex", "scaley":
			if len(vals) > 0 && isZeroScale(vals[0]) {
				return true
			}
		}
	}
	return false
}

// isZeroScale reports whether v is a scale of zero: "0", "-0", "0.0" or "0%".
func isZeroScale(v string) bool {
	n, unit, ok := numberWithUnit(v)
	return ok && (unit == "" || unit == "%") && n == 0
}

// cssFunction is a function in a CSS value, such as rgba(0, 0, 0, 0). The name
// is in lower case.
type cssFunction struct {
	name string
	args string // the text between the brackets
}

// values returns the arguments of the function, split at commas and white
// space. The empty parts are left out.
func (f cssFunction) values() []string {
	return strings.FieldsFunc(f.args, func(c rune) bool { return c == ',' || isASCIISpace(c) })
}

// cssFunctions returns the functions in a value, in order: "translateX(1px)
// scale(0)" gives translatex and scale. The words between the functions are
// left out, and so is everything after a function name with no "(". A
// function that is not closed runs to the end of the value, as in a browser.
// The value must already be in lower case.
func cssFunctions(v string) []cssFunction {
	var fns []cssFunction
	for i := 0; i < len(v); {
		open := openBracket(v, i)
		if open >= len(v) {
			break
		}
		start := open
		for start > i && isCSSNameByte(v[start-1]) {
			start--
		}
		end := closeBracket(v, open)
		fns = append(fns, cssFunction{name: v[start:open], args: v[open+1 : end]})
		i = end + 1
	}
	return fns
}

// openBracket returns the position of the first "(" at or after i that is not
// inside a string or after a backslash, or len(s) if there is none.
func openBracket(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '"', '\'':
			i = endOfString(s, i)
			continue
		case '(':
			return i
		}
		i++
	}
	return len(s)
}

// closeBracket returns the position of the ")" that closes the "(" at s[open],
// or len(s) if it is missing. Brackets inside are skipped, like the braces in
// matchBrace.
func closeBracket(s string, open int) int {
	depth := 0
	for i := open; i < len(s); {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '"', '\'':
			i = endOfString(s, i)
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return len(s)
}

// isCSSNameByte reports whether c can be part of a function name.
func isCSSNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
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
// that the rule is for.
//
// A selector of one simple part goes in a map: one class (.x), one id (#x), one
// element name (p), or every element (*). A compound selector, such as p.x, p#y
// or .x.y, is kept with the rest of its parts (see compoundRule).
//
// A selector that cannot be worked out here (descendant, child, sibling,
// attribute or pseudo selectors, and rules in rules) never hides anything. But
// if its rule hides, the element that it would hide gets styleEffect.unsure, so
// the report warns. That element is found by the last part of the selector: for
// div .x it is each element with the class x. Without a class, an id or an element
// name in the last part, it is any element. So a rule for something that the
// document does not have gives no warning.
//
// A rule is kept if it hides, and also if it sets a font size or a
// visibility, because a child can use its own size to show its text in a
// parent that has none. The rules for one name are put together, and so are the
// rules for an element (see styleEffect), so the order of the rules does not
// matter.
type styleRules struct {
	universal styleEffect // the rules for every element
	classes   map[string]styleEffect
	ids       map[string]styleEffect
	elements  map[string]styleEffect

	// The compound rules, by the first of their classes, or by their id if they
	// have no class.
	classRules map[string][]compoundRule
	idRules    map[string][]compoundRule

	work      int  // what is left of the work for matching (see maxRuleWork)
	suspected bool // a part of the rules was not read, or not matched to the end
}

// compoundRule is a rule whose selector has several simple parts.
type compoundRule struct {
	selector compound
	effect   styleEffect
}

// compound is a selector for one element. The names are in lower case.
type compound struct {
	tag     string
	classes []string
	ids     []string
	size    int // the work to compare the classes with one class of an element
}

// maxRuleWork limits how many bytes of names are compared to match the compound
// rules of one document. Many rules for one class and many elements with that
// class would take quadratic time. No normal mail gets near this limit. A document
// that reaches it has its other compound rules left out, and the report warns.
const maxRuleWork = 64 << 20

// maxRuleDepth is how deep at-rules and rules in rules are followed. Each level
// reads its block again, so the depth limits the work to some passes over the
// sheet. A deeper block is not read, and the report warns.
const maxRuleDepth = 8

// ruleScope says how far the rules of a block are known to apply.
type ruleScope uint8

const (
	scopeApply  ruleScope = iota // the rules apply
	scopeUnsure                  // the rules are used, but a hiding rule gives a warning too
	scopeOpaque                  // it is not known whether they apply: they give a warning only
)

// collectStyleRules reads every style element of the document that a browser
// uses. A style element inside template is not used: that content is not part
// of the page. A media attribute works as a condition (see matchMedia).
func collectStyleRules(doc *xhtml.Node) *styleRules {
	h := &styleRules{
		classes:    map[string]styleEffect{},
		ids:        map[string]styleEffect{},
		elements:   map[string]styleEffect{},
		classRules: map[string][]compoundRule{},
		idRules:    map[string][]compoundRule{},
		work:       maxRuleWork,
	}
	for n := doc; n != nil; {
		if n.Type == xhtml.ElementNode && n.Data == "template" {
			n = nextAfter(n)
			continue
		}
		if n.Type == xhtml.ElementNode && n.Data == "style" {
			h.addStyleElement(n)
		}
		n = nextNode(n)
	}
	return h
}

// addStyleElement reads one style element. A browser uses it only without a type
// attribute, with an empty one, or with text/css.
func (h *styleRules) addStyleElement(n *xhtml.Node) {
	if t, ok := attributeValue(n, "type"); ok && t != "" && !strings.EqualFold(t, "text/css") {
		return
	}
	scope := scopeApply
	if media, ok := attributeValue(n, "media"); ok {
		switch matchMedia(media) {
		case mediaNo:
			return
		case mediaUnknown:
			scope = scopeOpaque
		}
	}
	var sheet strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.TextNode {
			sheet.WriteString(c.Data)
		}
	}
	h.addRules(stripCSSComments(sheet.String()), scope, 0)
}

// nextNode returns the node after n in document order, or nil at the end. It
// does not call itself, so a deep document cannot exhaust the stack.
func nextNode(n *xhtml.Node) *xhtml.Node {
	if n.FirstChild != nil {
		return n.FirstChild
	}
	return nextAfter(n)
}

// nextAfter returns the node after n and everything inside it, or nil at the end.
func nextAfter(n *xhtml.Node) *xhtml.Node {
	for ; n != nil; n = n.Parent {
		if n.NextSibling != nil {
			return n.NextSibling
		}
	}
	return nil
}

// effectFor returns what the rules do to the element: the rules for every
// element, for its name, for each of its classes and for its id, all put
// together. Names are compared ignoring case, because a page without a doctype
// matches classes and ids that way.
func (h *styleRules) effectFor(n *xhtml.Node) styleEffect {
	e := h.universal.merge(h.elements[n.Data])
	var class, id string
	for _, a := range n.Attr {
		switch a.Key {
		case "class":
			class = a.Val
		case "id":
			id = lowerASCII(a.Val)
		}
	}
	var classes []string
	if class != "" && (len(h.classes) > 0 || len(h.classRules) > 0) {
		classes = strings.FieldsFunc(lowerASCII(class), isASCIISpace)
		for _, c := range classes {
			e = e.merge(h.classes[c])
			e = e.merge(h.matchRules(h.classRules[c], n, classes, id))
		}
	}
	if id != "" {
		e = e.merge(h.ids[id])
		e = e.merge(h.matchRules(h.idRules[id], n, classes, id))
	}
	return e
}

// matchRules returns the effect of the rules that match the element. classes and
// id are the ones of the element, in lower case. A rule is found by a name of the
// element, so that name matches.
func (h *styleRules) matchRules(rules []compoundRule, n *xhtml.Node, classes []string, id string) styleEffect {
	var e styleEffect
	for i := range rules {
		matched, ok := h.matches(&rules[i].selector, n, classes, id)
		if !ok {
			break
		}
		if matched {
			e = e.merge(rules[i].effect)
		}
	}
	return e
}

// matches reports whether a selector matches the element. ok is false when the
// work limit is reached. Then the report warns.
func (h *styleRules) matches(c *compound, n *xhtml.Node, classes []string, id string) (matched, ok bool) {
	h.work -= 1 + c.size*(1+len(classes))
	if h.work < 0 {
		h.suspected = true
		return false, false
	}
	if c.tag != "" && c.tag != n.Data {
		return false, true
	}
	for _, want := range c.ids {
		if want != id {
			return false, true
		}
	}
	for _, want := range c.classes {
		if !slices.Contains(classes, want) {
			return false, true
		}
	}
	return true, true
}

func isASCIISpace(c rune) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// addRules reads a list of rules: a style sheet without comments, or the inside
// of an at-rule or of another rule. depth is how many blocks are around it.
func (h *styleRules) addRules(s string, scope ruleScope, depth int) {
	if depth > maxRuleDepth {
		h.suspected = true
		return
	}
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

		prelude = trimPrelude(prelude)
		if strings.HasPrefix(prelude, "@") {
			h.addAtRule(prelude, body, scope, depth)
			continue
		}
		if effect := parseStyle(body); effect != (styleEffect{}) {
			for _, sel := range splitCSS(prelude, ',') {
				h.addSelector(strings.TrimSpace(sel), effect, scope)
			}
		}
		// Rules inside the rule (CSS nesting). Their selectors depend on the
		// selector around them, so they are not worked out.
		if strings.IndexByte(body, '{') >= 0 {
			h.addRules(body, scopeOpaque, depth+1)
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

// addAtRule reads a rule such as @media. An at-rule that is not known here can
// hold anything, so its rules are read for a warning only. So is one that
// depends on something that is not known here, such as @container.
func (h *styleRules) addAtRule(prelude, body string, scope ruleScope, depth int) {
	name, condition := splitAtRule(prelude)
	switch name {
	case "media":
		switch matchMedia(condition) {
		case mediaYes:
			h.addRules(body, scope, depth+1)
		case mediaUnknown:
			h.addRules(body, scopeOpaque, depth+1)
		}
	case "layer":
		// A layer changes the order of the rules, which is not used here.
		h.addRules(body, scope, depth+1)
	case "supports":
		// Most conditions hold in a mail client, but this code cannot tell.
		h.addRules(body, max(scope, scopeUnsure), depth+1)
	case "font-face", "keyframes", "-webkit-keyframes", "-moz-keyframes", "-o-keyframes",
		"page", "counter-style", "property", "viewport", "-ms-viewport",
		"font-feature-values", "font-palette-values", "color-profile", "position-try", "view-transition":
		// These hold no rules for elements.
	default:
		h.addRules(body, scopeOpaque, depth+1)
	}
}

// splitAtRule returns the name of an at-rule in lower case, and what follows it.
func splitAtRule(prelude string) (name, rest string) {
	s := prelude[1:]
	end := strings.IndexFunc(s, func(c rune) bool { return isASCIISpace(c) || c == '(' })
	if end < 0 {
		end = len(s)
	}
	return lowerASCII(unescapeCSS(s[:end])), s[end:]
}

// --- media queries ---

// mediaMatch is the answer to a media query. It can be unknown.
type mediaMatch uint8

const (
	mediaNo mediaMatch = iota
	mediaYes
	mediaUnknown
)

func (m mediaMatch) not() mediaMatch {
	switch m {
	case mediaNo:
		return mediaYes
	case mediaYes:
		return mediaNo
	}
	return mediaUnknown
}

// and combines two answers: no wins, then unknown.
func (m mediaMatch) and(o mediaMatch) mediaMatch {
	switch {
	case m == mediaNo || o == mediaNo:
		return mediaNo
	case m == mediaUnknown || o == mediaUnknown:
		return mediaUnknown
	}
	return mediaYes
}

// or combines two answers: yes wins, then unknown.
func (m mediaMatch) or(o mediaMatch) mediaMatch {
	return m.not().and(o.not()).not()
}

const (
	// mediaWideWindow is the width of the window that a rule is worked out for,
	// in pixels. It stands for a mail client on a computer.
	mediaWideWindow = 1000.0
	// mediaCommonWindow is the width of a narrow reading pane. A rule that holds
	// here but not in mediaWideWindow hides text from many readers, so the check
	// must warn. A rule for phones and tablets (768 pixels and less) does not
	// hold here: nearly every marketing mail has one, so it gives no warning.
	mediaCommonWindow = 800.0
	// maxMediaDepth is how deep brackets in a media query are read.
	maxMediaDepth = 8
)

// matchMedia works out a media query list for a mail client on a computer: a
// screen, light mode, and a window of mediaWideWindow.
//
// A rule that holds only in a narrower window does not hide text here. It is not
// reported either, unless the window is one of normal size (mediaCommonWindow).
func matchMedia(list string) mediaMatch {
	m := evalMediaList(list, mediaWideWindow)
	if m == mediaNo && evalMediaList(list, mediaCommonWindow) != mediaNo {
		return mediaUnknown
	}
	return m
}

// evalMediaList works out a media query list for a window of the given width.
// One query that holds is enough. An empty list holds.
func evalMediaList(list string, width float64) mediaMatch {
	list = lowerASCII(unescapeCSS(list))
	if strings.TrimSpace(list) == "" {
		return mediaYes
	}
	m := mediaNo
	for _, query := range splitCSS(list, ',') {
		m = m.or(evalMediaQuery(query, width, 0))
	}
	return m
}

// evalMediaQuery works out one query: an optional "only" or "not", then media
// types and conditions in brackets, joined by "and" or by "or". Anything else
// is unknown.
func evalMediaQuery(query string, width float64, depth int) mediaMatch {
	tokens := mediaTokens(query)
	negate := false
	if len(tokens) > 0 && (tokens[0] == "only" || tokens[0] == "not") {
		negate = tokens[0] == "not"
		tokens = tokens[1:]
	}
	m := evalMediaTerms(tokens, width, depth)
	if negate {
		return m.not()
	}
	return m
}

// mediaTokens splits a query into words and bracket groups.
func mediaTokens(q string) []string {
	var tokens []string
	for i := 0; i < len(q); {
		switch {
		case isASCIISpace(rune(q[i])):
			i++
		case q[i] == '(':
			end := min(closeBracket(q, i)+1, len(q))
			tokens = append(tokens, q[i:end])
			i = end
		default:
			start := i
			for i < len(q) && q[i] != '(' && !isASCIISpace(rune(q[i])) {
				i++
			}
			tokens = append(tokens, q[start:i])
		}
	}
	return tokens
}

// evalMediaTerms works out "term and term and term", or the same with "or". The
// two words cannot be mixed. Other forms are unknown.
func evalMediaTerms(tokens []string, width float64, depth int) mediaMatch {
	if len(tokens)%2 == 0 {
		return mediaUnknown
	}
	m := evalMediaTerm(tokens[0], width, depth)
	for i := 1; i < len(tokens); i += 2 {
		if tokens[i] != tokens[1] {
			return mediaUnknown
		}
		next := evalMediaTerm(tokens[i+1], width, depth)
		switch tokens[i] {
		case "and":
			m = m.and(next)
		case "or":
			m = m.or(next)
		default:
			return mediaUnknown
		}
	}
	return m
}

// evalMediaTerm works out a media type or a condition in brackets.
func evalMediaTerm(term string, width float64, depth int) mediaMatch {
	if !strings.HasPrefix(term, "(") {
		return mediaType(term)
	}
	if depth >= maxMediaDepth {
		return mediaUnknown
	}
	inner := strings.TrimSpace(strings.TrimSuffix(term[1:], ")"))
	if strings.HasPrefix(inner, "(") || strings.HasPrefix(inner, "not ") || strings.HasPrefix(inner, "not(") {
		return evalMediaQuery(inner, width, depth+1)
	}
	name, value, ok := strings.Cut(inner, ":")
	if !ok {
		return mediaUnknown
	}
	return mediaFeature(strings.TrimSpace(name), strings.TrimSpace(value), width)
}

// mediaType answers for a media type. A mail is read on a screen.
func mediaType(t string) mediaMatch {
	switch t {
	case "all", "screen":
		return mediaYes
	case "print", "speech", "tty", "tv", "projection", "handheld", "braille", "embossed", "aural":
		return mediaNo
	}
	return mediaUnknown
}

// mediaFeature answers for one feature. Only the width and the colour scheme are
// read here. Light mode is the default of a mail client.
func mediaFeature(name, value string, width float64) mediaMatch {
	var holds func(limit float64) bool
	switch name {
	case "prefers-color-scheme":
		switch value {
		case "light":
			return mediaYes
		case "dark":
			return mediaNo
		}
		return mediaUnknown
	case "min-width", "min-device-width":
		holds = func(limit float64) bool { return width >= limit }
	case "max-width", "max-device-width":
		holds = func(limit float64) bool { return width <= limit }
	case "width", "device-width":
		holds = func(limit float64) bool { return width == limit }
	default:
		return mediaUnknown
	}
	n, unit, ok := numberWithUnit(value)
	switch {
	case !ok, n < 0:
		return mediaUnknown
	case unit != "" && unit != "em" && unit != "rem" && absoluteUnits[unit] == 0:
		return mediaUnknown
	case holds(approxPixels(n, unit)):
		return mediaYes
	}
	return mediaNo
}

// --- selectors ---

// addSelector adds the effect of a rule to one selector of the rule. The effects
// of all the rules for a name are put together. scope says how far the rule is
// known to apply.
func (h *styleRules) addSelector(sel string, effect styleEffect, scope ruleScope) {
	if sel == "" {
		return
	}
	target, exact := splitSelector(sel)
	switch {
	case !exact || scope == scopeOpaque:
		if !effect.hides() {
			return
		}
		effect = styleEffect{unsure: true}
	case scope == scopeUnsure && effect.hides():
		effect.unsure = true
	}
	h.add(target, effect)
}

// add stores the effect for a selector.
func (h *styleRules) add(c compound, e styleEffect) {
	names := len(c.classes) + len(c.ids)
	switch {
	case names == 0 && c.tag == "":
		h.universal = h.universal.merge(e)
	case names == 0:
		h.elements[c.tag] = h.elements[c.tag].merge(e)
	case c.tag == "" && len(c.classes) == 1 && len(c.ids) == 0:
		h.classes[c.classes[0]] = h.classes[c.classes[0]].merge(e)
	case c.tag == "" && len(c.classes) == 0 && len(c.ids) == 1:
		h.ids[c.ids[0]] = h.ids[c.ids[0]].merge(e)
	case len(c.classes) > 0:
		h.classRules[c.classes[0]] = append(h.classRules[c.classes[0]], compoundRule{c, e})
	default:
		h.idRules[c.ids[0]] = append(h.idRules[c.ids[0]], compoundRule{c, e})
	}
}

// splitSelector reads the last part of a selector, after the last combinator.
// exact is true when the selector has no other part, and when this code can read
// all of that part. When it is false, the result only says which elements the
// selector can match at most.
func splitSelector(sel string) (target compound, exact bool) {
	start := 0
	for i := 0; i < len(sel); {
		i = scanCSS(sel, i, " \t\n\r\f>+~")
		if i >= len(sel) {
			break
		}
		i++
		start = i
	}
	target, exact = parseCompound(sel[start:])
	return target, exact && start == 0
}

// parseCompound reads a selector for one element: an optional name or "*", then
// classes and ids. It stops at anything else, such as an attribute or a pseudo
// selector, and returns false. The names that it read until then are returned.
func parseCompound(s string) (c compound, ok bool) {
	i := 0
	switch {
	case s == "":
		return c, false
	case s[0] == '*':
		i = 1
	case s[0] == '&':
		// The selector of the rule around this one. It says more than the rest
		// of the part, so this is not an exact match.
		c, _ = parseCompound(s[1:])
		return c, false
	default:
		i = endOfName(s, 0)
		c.tag = lowerASCII(unescapeCSS(s[:i]))
	}
	for i < len(s) {
		kind := s[i]
		if kind != '.' && kind != '#' {
			return c, false
		}
		end := endOfName(s, i+1)
		if end == i+1 {
			return c, false
		}
		name := lowerASCII(unescapeCSS(s[i+1 : end]))
		c.size += 1 + len(name)
		if kind == '.' {
			c.classes = append(c.classes, name)
		} else {
			c.ids = append(c.ids, name)
		}
		i = end
	}
	return c, true
}

// endOfName returns the position after the name that starts at s[i]. A name has
// letters, digits, "-", "_", non-ASCII characters and escapes.
func endOfName(s string, i int) int {
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\':
			_, next, ok := readEscape(s, i)
			if !ok {
				return i
			}
			i = next
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c >= 0x80:
			i++
		default:
			return i
		}
	}
	return i
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
