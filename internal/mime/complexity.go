package mime

import (
	"errors"
	"strings"

	"golang.org/x/net/html"
)

// ErrHTMLTooComplex means that HTML would make the parser build too many nodes,
// so it was not parsed. HTMLToText and VisibleHTML return it, also for a
// document that is nested too deep for the parser.
//
// The size of the input does not bound the work of the parser. The tree
// builder keeps a list of the formatting elements (a, b, i, font and so on)
// that it has not closed. For each piece of text it copies every element of
// that list into the tree again ("reconstruct the active formatting
// elements"). A few tens of kilobytes of input that opens hundreds of
// formatting elements with different attributes, and then has many paragraphs,
// make millions of nodes and gigabytes of memory.
var ErrHTMLTooComplex = errors.New("mime: html too complex to convert")

// maxHTMLNodes is the most nodes that the parser may be expected to build. A
// real mail has far fewer: about one node for every 20 to 50 bytes of HTML.
const maxHTMLNodes = 1_000_000

// formattingNames are the elements that the parser keeps in the list of active
// formatting elements. The position of a name is its index in the counts of a
// frame.
var formattingNames = [...]string{"a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u"}

// formattingIndex returns the index of a formatting element name, or -1.
func formattingIndex(name string) int {
	for i, n := range formattingNames {
		if n == name {
			return i
		}
	}
	return -1
}

// scopeTags are the elements that stop the search of an end tag for a
// formatting element: it is only found, and so only removed from the list,
// when no scope element lies between it and the end tag. The first eight also
// put a marker in the list, so the elements before them are not copied
// again inside them. (The others are the elements that SVG and MathML add to
// the scope. They are here for completeness: a document with svg or math is
// not counted by htmlNodeEstimate.)
var scopeTags = tagSet(
	"applet", "caption", "marquee", "object", "table", "td", "template", "th",
	"foreignobject", "desc", "title", "mi", "mo", "mn", "ms", "mtext", "annotation-xml",
)

// impliedTags are the elements that make the parser add elements of its own
// when they are not where they belong: a td or th without a tr, or a tr
// without a tbody. They cost two more nodes.
var impliedTags = tagSet("td", "th", "tr", "tbody", "thead", "tfoot", "col", "colgroup", "caption")

// startTagCost is the cost of a start tag, besides the copies of the formatting
// elements. It is one node for the element, and one more because the parser
// adds an element of its own now and then in places that are hard to follow
// (a form in a table, an element that was closed by mistake and is made
// again). A test compares the estimate with the parser for random documents.
const startTagCost = 2

// endFormattingCost is the cost of the end tag of a formatting element. An end
// tag makes no node, but when it closes formatting elements in the wrong order
// the parser copies some of them again ("adoption agency"). Random and made-up
// documents give at most about two nodes for each end tag.
const endFormattingCost = 3

// rawTokenSlack is added to the cost of each token by rawNodeBound, because
// that bound must hold for every document, also for one that was made to find
// a case that the tests did not.
const rawTokenSlack = 8

// endTagCost is the cost of an end tag. An end tag of br makes the parser put
// in a br, like a start tag.
func endTagCost(name string, open int) int {
	switch {
	case name == "br":
		return 1 + open
	case formattingIndex(name) >= 0:
		return endFormattingCost
	}
	return 1
}

// checkHTMLComplexity returns ErrHTMLTooComplex when markup would probably
// make the parser build more than maxHTMLNodes nodes. It reads the tokens of
// the markup and builds no tree, so its memory does not depend on the markup.
//
// There are two ways to count, and the first one is the usual one.
//
//   - htmlNodeEstimate follows the tokens the way the parser does, and it knows
//     which formatting elements the parser has open. It is close to the real
//     number, so documents of any size pass if they are simple. It reads the
//     tokens as the parser does only as long as the document has no svg, math
//     or noscript element. The parser changes how the tokenizer reads the text
//     inside them, in ways that depend on its state, so after the first one the
//     count would not be reliable.
//   - rawNodeBound is for the documents that have such an element. It does not
//     follow anything. It is an upper bound that holds for every document, and
//     so it is low only for small documents.
func checkHTMLComplexity(markup string) error {
	nodes, unsure := htmlNodeEstimate(markup, maxHTMLNodes)
	if nodes > maxHTMLNodes || (unsure && rawNodeBound(markup) > maxHTMLNodes) {
		return ErrHTMLTooComplex
	}
	return nil
}

// frame counts the formatting start tags that are open in one part of the
// document, per tag name. A frame starts at a scope element and ends with its
// end tag. An end tag removes a formatting element only in its own frame.
type frame struct {
	name  string
	open  [len(formattingNames)]int
	total int
}

// htmlNodeEstimate estimates how many nodes the parser builds for markup. It
// stops and returns a number above limit as soon as the estimate passes limit.
// unsure is true when it stopped at an svg, math or noscript element (see
// checkHTMLComplexity). The number is then only what was counted until there.
//
// A text, a comment and a start tag make one node each (a start tag a little
// more, see startTagCost). A text and a start tag also make a copy of every
// formatting element that is open at that point (the list of the parser, see
// ErrHTMLTooComplex). An end tag makes no node in most cases (see endTagCost).
//
// A formatting element counts as open from its start tag until the end tag
// that removes it. The count must not be too low, because the parser keeps an
// element in its list when an end tag does not find it, and then copies it
// again for every piece of text. So an end tag only takes away an element of its
// own name in the frame it is in, and a frame ends only with its own end tag.
// Whatever the parser does in the other cases, it never has more elements in
// its list than this count. (A test compares the estimate with the parser for
// random documents that try to break this.)
func htmlNodeEstimate(markup string, limit int) (nodes int, unsure bool) {
	z := html.NewTokenizer(strings.NewReader(markup))
	frames := []frame{{}}
	open := 0 // formatting elements counted in all frames
	nodes = 4 // the document, html, head and body that the parser adds

	for nodes <= limit {
		switch z.Next() {
		case html.ErrorToken:
			return nodes, false
		case html.TextToken:
			nodes += 1 + open
		case html.CommentToken, html.DoctypeToken:
			nodes++
		case html.StartTagToken, html.SelfClosingTagToken:
			nameBytes, _ := z.TagName()
			name := string(nameBytes)
			if name == "svg" || name == "math" || name == "noscript" {
				return nodes, true
			}
			nodes += startTagCost + open
			if impliedTags[name] {
				nodes += 2
			}
			top := &frames[len(frames)-1]
			if i := formattingIndex(name); i >= 0 {
				top.open[i]++
				top.total++
				open++
			}
			if scopeTags[name] {
				frames = append(frames, frame{name: name})
			}
		case html.EndTagToken:
			nameBytes, _ := z.TagName()
			name := string(nameBytes)
			nodes += endTagCost(name, open)
			top := &frames[len(frames)-1]
			if i := formattingIndex(name); i >= 0 && top.open[i] > 0 {
				top.open[i]--
				top.total--
				open--
			}
			if scopeTags[name] && top.name == name {
				open -= top.total
				frames = frames[:len(frames)-1]
			}
		}
	}
	return nodes, false
}

// rawNodeBound returns a number of nodes that the parser does not pass, for
// every document. It does not read the markup as HTML, so the parser cannot
// read it differently.
//
// The parser makes a token of each "<" and of each piece of text between
// them, so there are at most 2n+1 tokens for n times "<". A token makes at most
// a few nodes of its own and a copy of every formatting element in the list of
// the parser. The list holds no more elements than there are start tags of
// formatting elements in the markup, wherever they are, also in a comment or
// in a text that the parser reads as text. So every start tag of that kind
// counts, and none is taken away.
func rawNodeBound(markup string) int {
	tokens, formatting := 1, 0
	for i := 0; i < len(markup); i++ {
		if markup[i] != '<' {
			continue
		}
		tokens += 2
		if isFormattingStart(markup[i+1:]) {
			formatting++
		}
	}
	// A start tag costs startTagCost, 2 more for an implied element, and the
	// copies of the formatting elements.
	return tokens * (startTagCost + 2 + rawTokenSlack + formatting)
}

// isFormattingStart reports whether s, the text after a "<", starts the start
// tag of a formatting element: the name in any case, and then a character that
// ends a tag name (or the end of s).
func isFormattingStart(s string) bool {
	n := 0
	for n < len(s) && 'a' <= s[n]|0x20 && s[n]|0x20 <= 'z' {
		n++
	}
	const longestName = 6 // strike and strong
	if n == 0 || n > longestName {
		return false
	}
	if n < len(s) && !strings.ContainsRune(" \t\n\f\r/>", rune(s[n])) {
		return false
	}
	return formattingIndex(strings.ToLower(s[:n])) >= 0
}
