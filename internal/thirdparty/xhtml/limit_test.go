package xhtml // email-mcp: tests of the cost limit (not from upstream)

import (
	"bytes"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	uhtml "golang.org/x/net/html"
)

// treeCost returns the cost of the nodes of a tree, the node itself included:
// NodeCost for each node, and AttrCost plus the bytes for each attribute. It
// is what ParseWithLimit counts, when the parser has not taken a node out of
// the tree (a frameset does) and no repeated <html> or <body> tag with
// attributes was merged into the element.
func treeCost(n *Node) int {
	c := NodeCost
	for _, attr := range n.Attr {
		c += AttrCost + len(attr.Namespace) + len(attr.Key) + len(attr.Val)
	}
	for k := n.FirstChild; k != nil; k = k.NextSibling {
		c += treeCost(k)
	}
	return c
}

// countTree returns the number of nodes of a tree, the node itself included.
func countTree(n *Node) int {
	c := 1
	for k := n.FirstChild; k != nil; k = k.NextSibling {
		c += countTree(k)
	}
	return c
}

// allocatedBy returns the bytes that f allocated, also those that were freed
// again. It is an upper bound of the memory that f used at its peak.
func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// parseCounted parses markup and returns the parser, so that a test can read its
// cost. The scripting flag is off, like in the mail code.
func parseCounted(markup string, limit int, limited bool) (doc *Node, p *parser, err error) {
	grab := func(q *parser) { p = q }
	opts := []ParseOption{ParseOptionEnableScripting(false), grab}
	if limited {
		doc, err = ParseWithLimit(strings.NewReader(markup), limit, opts...)
	} else {
		doc, err = ParseWithOptions(strings.NewReader(markup), opts...)
	}
	return doc, p, err
}

// amplified opens k formatting elements with different attributes in a
// paragraph, and then has m paragraphs with a text. The parser copies the k
// elements for each text.
func amplified(k, m int) string {
	var b strings.Builder
	b.WriteString("<p>")
	for i := 0; i < k; i++ {
		fmt.Fprintf(&b, "<b x=%d>", i)
	}
	for i := 0; i < m; i++ {
		b.WriteString("<p>x")
	}
	return b.String()
}

// tableAmplified opens the k formatting elements in a table. The parser puts
// them out of the table (foster parenting) and keeps them in its list, and so it
// copies them for each paragraph after the table.
func tableAmplified(k, m int) string {
	var b strings.Builder
	b.WriteString("<table>")
	for i := 0; i < k; i++ {
		fmt.Fprintf(&b, "<b x=%d>", i)
	}
	b.WriteString("</table>")
	for i := 0; i < m; i++ {
		b.WriteString("<p>y")
	}
	return b.String()
}

// longLinkCopied has a link with an address of s bytes in a paragraph, and then
// c paragraphs. The parser copies the link element for each paragraph. The
// copies share the address, so they are cheap for the parser, but the code that
// reads the tree reads the address once for each copy.
func longLinkCopied(s, c int) string {
	return `<p><a href="https://e.example/` + strings.Repeat("a", s) + `">` + strings.Repeat("<p>y", c)
}

// htmlTagsWithAttributes has n <html> start tags in the body, each with a new
// attribute. The parser merges each into the html element, and it goes over
// all the attributes that the element has for each tag.
func htmlTagsWithAttributes(n int) string {
	var b strings.Builder
	b.WriteString("<p>x")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "<html a%d>", i)
	}
	return b.String()
}

// copyWork is the cost of the merges of repeated <html> and <body> tags with
// attributes, for the documents that have them. The tree does not show it.
var copyWork = map[string]int{
	// <body b=2> is merged into a body with one attribute (a=1). <html c=3> is
	// merged into an html element with none.
	"body attributes merged": AttrCost * 1,
}

// documents are the documents for the tests: normal ones, and one for each way
// that is known to make the parser do more work than the input is long.
func documents() map[string]string {
	rep := strings.Repeat
	return map[string]string{
		"empty":                       "",
		"text only":                   "hello &amp; goodbye",
		"whitespace":                  " \n\t ",
		"simple page":                 "<!DOCTYPE html><html><head><title>t</title></head><body><p>one</p><p>two <b>bold</b></p></body></html>",
		"comments everywhere":         "<!--a--><!DOCTYPE html><!--b--><html><!--c--><head><!--d--></head><!--e--><body><!--f--><p>x</p></body><!--g--></html><!--h-->",
		"comments after frameset":     "<!--a--><html><head></head><frameset><!--b--><frame></frameset><!--c--></html><!--d-->",
		"comments in foreign content": "<svg><!--a--><desc><!--b--></desc></svg><math><!--c--><mi>x</mi></math>",
		"doctype twice":               "<!DOCTYPE html><!DOCTYPE html><p>x</p>",
		"quirks doctype":              `<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 3.2 Final//EN"><p>x`,
		"doctype with identifiers":    `<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd"><p>x`,
		"table":                       "<table><caption>c</caption><colgroup><col></colgroup><thead><tr><th>h</th></tr></thead><tbody><tr><td>a</td><td>b</td></tr></tbody></table>",
		"table without rows":          "<table><td>a<td>b</table><table><tr>x<tr>y</table>",
		"text in a table":             "<table>abc <b>x</b> def<!--c--> ghi</table>after",
		"nested tables":               "<table><tr><td><table><tr><td>in</td></tr></table></td></tr></table>",
		"template":                    "<template><p>a</p><table><td>b</td></table><template><b>c</b></template></template><p>d</p>",
		"template in table":           "<table><template><tr><td>x</td></tr></template></table>",
		"select":                      "<select><option>a<optgroup><option>b</select><p>x",
		"form in table":               "<table><form><input><tr><td>x</td></tr></form></table>",
		"foreign content":             "<svg><circle/><foreignObject><p>x</p></foreignObject><title>t</title></svg><math><mi>x</mi><annotation-xml encoding='text/html'><p>y</p></annotation-xml></math>",
		"foreign attributes":          `<svg viewbox="0 0 1 1"><a xlink:href="https://e.example/" xml:lang="en">x</a></svg><math definitionurl="u"><mi>x</mi></math>`,
		"breakout from svg":           "<svg><b>x</b><p>y</p></svg>",
		"noscript":                    "<noscript><p>a</p></noscript><head><noscript><link></noscript></head>",
		"raw text":                    "<style>p{}</style><script>x</script><textarea>t</textarea><title>t</title><xmp>x</xmp><iframe>i</iframe><plaintext>p<b>",
		"frameset":                    "<frameset><frame><frameset><frame></frameset></frameset>",
		"implied elements":            "</p></br><td>x<li>y",
		"lists and definitions":       "<ul><li>a<li>b<ul><li>c</ul></ul><dl><dt>x<dd>y<dt>z</dl>",
		"button and form":             "<form><button>a<button>b</form><form>c",
		"body attributes merged":      "<body a=1><body b=2><html c=3>",
		"duplicate attributes":        "<p a=1 a=2 a=3>x</p>",
		"many attributes":             "<div " + rep("a b c ", 300) + ">x</div>",
		"attributes with long values": `<p title="` + rep("t", 3000) + `" style="` + rep("color:red;", 100) + `">x</p>`,

		// Formatting elements.
		"reconstruct, small":                "<p><b>1<i>2<p>3<p>4",
		"reconstruct, 50 elements":          amplified(50, 50),
		"reconstruct after block":           "<b><i><u>x<div>y</div>z<p>w</p></u></i></b>v",
		"a elements":                        "<a href=1>x<a href=2>y<table><a href=3>z</table>",
		"nobr":                              "<nobr>a<nobr>b<nobr>c",
		"noah's ark":                        rep("<b x=1>", 10) + "x",
		"foster-parented formatting":        tableAmplified(40, 40),
		"formatting in a cell":              "<b x=1><table><tr><td><b x=2>x</b></td></tr></table><p>y",
		"formatting closed in a table":      "<b x=1><i x=2><table><tr><td></b></i></td></tr></table><p>y",
		"formatting with a marker":          "<b x=1><marquee><b x=2>x</marquee><p>y",
		"long link copied":                  longLinkCopied(500, 20),
		"adoption agency, simple":           "<b><p>x</b>y",
		"adoption agency, nested":           rep("<b><div>x", 30) + rep("</b>", 30),
		"adoption agency, misnested":        rep("<a><b><div>", 20) + rep("</a></b>", 20),
		"adoption agency, attributes":       rep(`<a href="https://e.example/1"><b class="k"><div>`, 10) + rep("</a></b>", 10),
		"adoption agency in a table":        "<b><table><tr><td></b></td></tr></table>",
		"adoption agency with foster":       "<table><b><tr><td></b></td></tr></table>",
		"adoption agency, many iterations":  rep("<i><b><p>x</i></b>", 40),
		"furthest block with many children": "<b>" + rep("<p>x", 30) + "</b>",
	}
}

// randomTokens makes the tokens of a document from a list that stresses the
// parser: formatting elements, end tags that do not match, tables, select,
// templates, raw text, and foreign content.
func randomTokens(r *rand.Rand) []string {
	tokens := []string{
		"<b x=%d>", "<i y=%d>", "<a href=h%d>", "<font face=%d>", "<nobr>", "<u z=%d>", "<em e=%d>", "<s q=%d>",
		"<b x=%d>", "<i y=%d>", "<a href=h%d>", "<b>", "<code c=%d>", "<small s=%d>", "<tt t=%d>", "<big k=%d>", "<strike w=%d>", "<strong g=%d>",
		"</b>", "</i>", "</a>", "</font>", "</nobr>", "</u>", "</em>", "</s>", "</b>", "</i>", "</strong>", "</code>",
		"<p>", "<p>", "<p>", "<div>", "<span>", "<li>", "<ul>", "<h1>", "<pre>", "<button>", "<hr>", "<br>", "</br>", "<img src=x>",
		"x", "x", "x", "text ", "&amp;", " ",
		"</p>", "</div>", "</span>", "</li>", "</ul>", "</h1>", "</pre>", "</button>",
		"<table>", "<tr>", "<td>", "<th>", "</td>", "</tr>", "</table>", "<caption>", "</caption>", "<tbody>", "<colgroup>", "<col>",
		"<select>", "<option>", "</select>", "<textarea>", "</textarea>", "<optgroup>",
		"<template>", "</template>", "<object>", "</object>", "<applet>", "<marquee>", "</marquee>",
		"<title>", "</title>", "<style>", "</style>", "<script>", "</script>", "<iframe>", "</iframe>", "<xmp>", "<plaintext>",
		"<!-- c -->", "<!DOCTYPE html>", "<html>", "<body>", "<head>", "<form>", "</form>", "<frameset>", "<frame>", "</frameset>", "<b/>",
		"<foreignObject>", "</foreignObject>", "<desc>", "<mi>", "<annotation-xml encoding=text/html>",
		"<svg>", "</svg>", "<math>", "</math>", "<noscript>", "</noscript>", "<svg/>", "<noscript>", "<math>", "<svg>",
		"<svg viewbox='0 0 %d 1'>", "<a xlink:href=l%d>", "<p style='color:red;margin:%dpx'>", "<span title=\"t%d\" lang=en>",
	}
	var out []string
	for i, n := 0, 5+r.Intn(250); i < n; i++ {
		t := tokens[r.Intn(len(tokens))]
		if strings.Contains(t, "%d") {
			t = fmt.Sprintf(t, r.Intn(1000))
		}
		out = append(out, t)
	}
	return out
}

// randomAttack makes a document that opens many formatting elements, and then
// mixes scope elements that open and close, end tags, and text.
func randomAttack(r *rand.Rand) []string {
	names := []string{"a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u"}
	scopes := []string{"table", "tr", "td", "th", "caption", "template", "object", "applet", "marquee", "select", "form", "p", "div", "span", "li"}
	out := []string{"<p>"}
	for i, n := 0, 3+r.Intn(60); i < n; i++ {
		out = append(out, fmt.Sprintf("<%s x=%d>", names[r.Intn(len(names))], i))
	}
	for i, n := 0, 10+r.Intn(300); i < n; i++ {
		switch r.Intn(7) {
		case 0:
			out = append(out, "<"+scopes[r.Intn(len(scopes))]+">")
		case 1:
			out = append(out, "</"+scopes[r.Intn(len(scopes))]+">")
		case 2:
			out = append(out, "</"+names[r.Intn(len(names))]+">")
		case 3:
			out = append(out, fmt.Sprintf("<%s y=%d>", names[r.Intn(len(names))], i))
		default:
			out = append(out, "<p>x", "x", " text")
		}
	}
	return out
}

// randomDocuments returns the number of random documents for each generator.
func randomDocuments() int {
	switch {
	case raceEnabled:
		return 400
	case testing.Short():
		return 1000
	}
	return 6000
}

// hasFrameset says if the tree has a frameset element. A frameset can take the
// body out of the tree, and the cost of the body stays.
func hasFrameset(n *Node) bool { return findElement(n, "frameset") != nil }

// The cost is the cost of the nodes and attributes of the tree. If one place that
// makes a node or adds an attribute did not count it, a document would show it.
func TestCostIsTheCostOfTheTree(t *testing.T) {
	for name, markup := range documents() {
		t.Run(name, func(t *testing.T) {
			doc, p, err := parseCounted(markup, 0, false)
			require.NoError(t, err)
			require.Equal(t, treeCost(doc)+copyWork[name], p.cost)
		})
	}
	for name, gen := range map[string]func(*rand.Rand) []string{"random tokens": randomTokens, "random attack": randomAttack} {
		t.Run(name, func(t *testing.T) {
			r := rand.New(rand.NewSource(20261028)) //nolint:gosec // a fixed seed makes the test repeat
			framesets := 0
			for i := 0; i < randomDocuments(); i++ {
				markup := strings.Join(gen(r), "")
				doc, p, err := parseCounted(markup, 0, false)
				if err != nil {
					continue // nested too deep
				}
				// The random tokens have no <html> or <body> tag with attributes, so
				// nothing is merged.
				want := treeCost(doc)
				if hasFrameset(doc) {
					framesets++
					if p.cost < want {
						t.Fatalf("the cost is %d, below the cost %d of the tree, for:\n%s", p.cost, want, markup)
					}
					continue
				}
				if p.cost != want {
					t.Fatalf("the cost is %d, the tree costs %d, for:\n%s", p.cost, want, markup)
				}
			}
			t.Logf("%d documents, %d with a frameset", randomDocuments(), framesets)
		})
	}
}

func TestCostOfAFragmentIsTheCostOfTheTree(t *testing.T) {
	for _, context := range []string{"", "div", "table", "tr", "td", "template", "select", "body", "head", "frameset"} {
		t.Run("context "+context, func(t *testing.T) {
			var ctx *Node
			if context != "" {
				// Parse a document to get a context element with a consistent atom.
				d, err := ParseWithOptions(strings.NewReader("<" + context + ">"))
				require.NoError(t, err)
				ctx = findElement(d, context)
				if ctx == nil {
					t.Skip("the parser made no such element")
				}
			}
			r := rand.New(rand.NewSource(5)) //nolint:gosec // a fixed seed makes the test repeat
			for i := 0; i < randomDocuments()/4; i++ {
				markup := strings.Join(randomTokens(r), "")
				var p *parser
				nodes, err := ParseFragmentWithOptions(strings.NewReader(markup), ctx, func(q *parser) { p = q })
				if err != nil {
					continue
				}
				// The result is cut out of the tree. The rest of the tree (the document
				// node, the root element, and what the parser put outside the
				// result) is in p.doc.
				want := treeCost(p.doc)
				framesets := hasFrameset(p.doc)
				for _, n := range nodes {
					want += treeCost(n)
					framesets = framesets || hasFrameset(n)
				}
				if got := p.cost; got != want && (!framesets || got < want) {
					t.Fatalf("the cost is %d, the tree costs %d, for:\n%s", got, want, markup)
				}
			}
		})
	}
}

// findElement returns the first element with the given name.
func findElement(n *Node, name string) *Node {
	if n.Type == ElementNode && n.Data == name {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findElement(c, name); f != nil {
			return f
		}
	}
	return nil
}

// The costs of small documents, worked out by hand.
func TestCostOfSmallDocuments(t *testing.T) {
	const n = NodeCost
	attr := func(key, val string) int { return AttrCost + len(key) + len(val) }
	cases := map[string]struct {
		markup string
		cost   int
	}{
		// The document, html, head and body.
		"empty": {"", 4 * n},
		// The four, a p with one attribute, and a text.
		"one attribute": {"<p a=bc>x", 4*n + n + attr("a", "bc") + n},
		// Each br is a node with three attributes.
		"input attributes": {"<br a b c><br a b c>", 4*n + 2*(n+3*attr("a", ""))},
		// The b is copied into the second paragraph, with its attribute.
		"a copy costs its attributes": {"<p><b x=12>1<p>2", 4*n + (n + n + attr("x", "12") + n) + (n + n + attr("x", "12") + n)},
		// A foreign attribute has a namespace: xlink, href.
		"namespace": {"<svg><a xlink:href=u></a></svg>", 4*n + n + n + AttrCost + len("xlink") + len("href") + len("u")},
		// <html b> is merged into an html element with one attribute: the work
		// for that attribute, and the new attribute.
		"merge": {"<html a><p>x<html b>", 4*n + attr("a", "") + n + n + AttrCost*1 + attr("b", "")},
		// The frameset takes the body (with the p and its text) out of the tree,
		// and the cost stays.
		"frameset": {"<p><frameset>", 4*n + n + n},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, p, err := parseCounted(tc.markup, 0, false)
			require.NoError(t, err)
			require.Equal(t, tc.cost, p.cost)
		})
	}
}

// The patch must not change what the parser makes. The tree is the same as the
// tree of the upstream package, for every document of the tests, also with a
// limit that is not reached.
func TestTreesAreTheSameAsUpstream(t *testing.T) {
	same := func(t *testing.T, markup string) {
		t.Helper()
		theirs, err2 := uhtml.ParseWithOptions(strings.NewReader(markup), uhtml.ParseOptionEnableScripting(false))
		var b bytes.Buffer
		var errB error
		if err2 == nil {
			errB = uhtml.Render(&b, theirs)
		}
		for _, limited := range []bool{false, true} {
			ours, _, err1 := parseCounted(markup, 1<<40, limited)
			if (err1 == nil) != (err2 == nil) {
				t.Fatalf("errors differ (limit %v): %v and %v for:\n%s", limited, err1, err2, markup)
			}
			if err1 != nil {
				continue
			}
			var a bytes.Buffer
			errA := Render(&a, ours)
			if (errA == nil) != (errB == nil) || (errA != nil && errA.Error() != errB.Error()) {
				t.Fatalf("the errors of Render differ: %v and %v for:\n%s", errA, errB, markup)
			}
			if a.String() != b.String() {
				t.Fatalf("the trees differ (limit %v) for:\n%s\nours:   %s\ntheirs: %s", limited, markup, a.String(), b.String())
			}
		}
	}
	for name, markup := range documents() {
		t.Run(name, func(t *testing.T) { same(t, markup) })
	}
	r := rand.New(rand.NewSource(99)) //nolint:gosec // a fixed seed makes the test repeat
	for i := 0; i < randomDocuments(); i++ {
		same(t, strings.Join(randomTokens(r), ""))
		same(t, strings.Join(randomAttack(r), ""))
	}
}

func TestParseWithLimitReturnsTheSameTreeBelowTheLimit(t *testing.T) {
	for name, markup := range documents() {
		t.Run(name, func(t *testing.T) {
			want, p, err := parseCounted(markup, 0, false)
			require.NoError(t, err)

			got, err := ParseWithLimit(strings.NewReader(markup), p.cost, ParseOptionEnableScripting(false))
			require.NoError(t, err, "a limit of exactly the cost is not passed")
			var a, b bytes.Buffer
			require.NoError(t, Render(&a, got))
			require.NoError(t, Render(&b, want))
			require.Equal(t, b.String(), a.String())
		})
	}
}

// maxAttrs is an upper bound of the attributes of one element in markup. Two
// attributes are apart by a space, a slash, or a quote that ends a value.
func maxAttrs(markup string) int {
	return 1 + strings.Count(markup, " ") + strings.Count(markup, "/") + strings.Count(markup, `"`) + strings.Count(markup, "'")
}

// The parser stops at the first step over the limit. That step is one node with
// its attributes, one attribute, or the merge of a repeated <html> or <body>
// tag, so the cost is at most that much over the limit.
func TestParseWithLimitStopsJustOverTheLimit(t *testing.T) {
	for name, markup := range documents() {
		t.Run(name, func(t *testing.T) {
			_, full, err := parseCounted(markup, 0, false)
			require.NoError(t, err)
			cost := full.cost

			maxStep := NodeCost + (AttrCost+len(markup))*maxAttrs(markup)
			for _, limit := range []int{cost - 1, cost / 2, 1} {
				if limit < 0 || limit >= cost {
					continue
				}
				doc, p, err := parseCounted(markup, limit, true)
				require.ErrorIs(t, err, ErrTooComplex, "limit %d of %d", limit, cost)
				require.Nil(t, doc)
				require.Greater(t, p.cost, limit)
				require.LessOrEqual(t, p.cost, limit+maxStep)
			}
		})
	}
}

// Every way that is known to make the parser or the code after it do much more
// work than the input is long is stopped by the limit.
func TestAmplifyingDocumentsAreStoppedByTheLimit(t *testing.T) {
	const limit = 800_000
	attacks := map[string]string{
		"formatting elements copied for each paragraph": amplified(500, 4000),
		"the same in a table (foster parenting)":        tableAmplified(200, 10_000),
		"the same, large":                               tableAmplified(500, 276_001),
		"a clone with many attributes":                  "<p><b " + manyAttributes(900) + ">" + strings.Repeat("<p>x", 2000),
		"formatting in a template":                      "<template><b x=1><b x=2>" + strings.Repeat("<p>z", 40_000) + "</template>",
		"a long link copied":                            longLinkCopied(64<<10, 1000),
		"repeated html tags":                            htmlTagsWithAttributes(20_000),
		"many attributes in many tags":                  strings.Repeat("<br a b c d e f g h i j k l m n o p q r s t u v w x y z>", 20_000),
	}
	for name, markup := range attacks {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			doc, p, err := parseCounted(markup, limit, true)
			elapsed := time.Since(started)

			require.ErrorIs(t, err, ErrTooComplex)
			require.Nil(t, doc)
			require.LessOrEqual(t, p.cost, limit+NodeCost+(AttrCost+len(markup))*maxAttrs(markup))
			if !raceEnabled {
				require.Less(t, elapsed, 2*time.Second)
			}
		})
	}
}

// manyAttributes returns n attributes with different names.
func manyAttributes(n int) string {
	attrs := make([]string, n)
	for i := range attrs {
		attrs[i] = fmt.Sprintf("a%d=1", i)
	}
	return strings.Join(attrs, " ")
}

// A copy of an element costs the bytes of its attributes, although it shares
// them with the element: the code that reads the tree reads them for each copy.
func TestCopiesCountTheBytesOfTheirAttributes(t *testing.T) {
	short, p1, err := parseCounted(longLinkCopied(10, 100), 0, false)
	require.NoError(t, err)
	long, p2, err := parseCounted(longLinkCopied(10_010, 100), 0, false)
	require.NoError(t, err)
	require.Equal(t, countTree(short), countTree(long), "the same nodes")
	// The original and 100 copies each have 10,000 more bytes.
	require.Equal(t, 101*10_000, p2.cost-p1.cost)

	// A link of 1 MiB copied 500 times is stopped after a few copies.
	_, p, err := parseCounted(longLinkCopied(1<<20, 500), 16_000_000, true)
	require.ErrorIs(t, err, ErrTooComplex)
	require.Less(t, p.cost, 16_000_000+2<<20)
}

// A repeated <html> tag makes the parser go over every attribute of the html
// element. The cost grows with the square of the number of tags.
func TestRepeatedHTMLTagsCostTheAttributesThatTheyGoOver(t *testing.T) {
	const k = 100
	_, base, err := parseCounted("<p>x", 0, false)
	require.NoError(t, err)
	_, p, err := parseCounted(htmlTagsWithAttributes(k), 0, false)
	require.NoError(t, err)
	want := base.cost
	for i := 0; i < k; i++ {
		// The tag goes over the i attributes that the element has, and adds one.
		want += AttrCost*i + AttrCost + len(fmt.Sprintf("a%d", i))
	}
	require.Equal(t, want, p.cost)

	// 20,000 tags (249 KB) were 13 seconds of work. The limit stops them early.
	started := time.Now()
	_, _, err = parseCounted(htmlTagsWithAttributes(20_000), 16_000_000, true)
	require.ErrorIs(t, err, ErrTooComplex)
	if !raceEnabled {
		require.Less(t, time.Since(started), time.Second)
	}
}

// A frameset takes the body out of the tree, but the parser can still copy its
// formatting elements. So the cost does not go down.
func TestFramesetDoesNotLowerTheCost(t *testing.T) {
	var b strings.Builder
	b.WriteString("<p>")
	for _, name := range []string{"a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u"} {
		b.WriteString(strings.Repeat("<"+name+">", 3))
	}
	b.WriteString(strings.Repeat("<p><x>", 200))
	body := b.String()

	_, before, err := parseCounted(body, 0, false)
	require.NoError(t, err)
	doc, after, err := parseCounted(body+"<frameset>", 0, false)
	require.NoError(t, err)
	require.Equal(t, before.cost+NodeCost, after.cost, "only the frameset is added")
	require.Less(t, treeCost(doc), before.cost, "the tree lost the body")

	// The recipe of the review: the body is close to the limit, and comments
	// after the frameset (each "</>" is an empty comment) add more.
	recipe := body + strings.Repeat("<p><x>", 21_800) + "<frameset>" + strings.Repeat("</>", 990_000)
	_, p, err := parseCounted(recipe, 16_000_000, true)
	require.ErrorIs(t, err, ErrTooComplex)
	require.LessOrEqual(t, p.cost, 16_000_000+NodeCost)
}

// The tokenizer keeps every attribute of a tag, in several copies, before the
// parser sees the tag. ParseWithLimit stops a tag with more than maxTagAttrs
// different attributes in the tokenizer.
func TestTagWithTooManyAttributesIsRefused(t *testing.T) {
	tag := func(n int) string { return "<p " + manyAttributes(n) + ">x" }

	_, err := ParseWithLimit(strings.NewReader(tag(maxTagAttrs)), 1<<40)
	require.NoError(t, err)
	_, err = ParseWithLimit(strings.NewReader(tag(maxTagAttrs+1)), 1<<40)
	require.ErrorIs(t, err, ErrTooComplex)

	// The same attribute many times is kept once.
	_, err = ParseWithLimit(strings.NewReader("<p"+strings.Repeat(" a", 50_000)+">x"), 1<<40)
	require.NoError(t, err)
	// End tags keep no attributes.
	_, err = ParseWithLimit(strings.NewReader("<p>x</p "+manyAttributes(5000)+">"), 1<<40)
	require.NoError(t, err)
	// Without a limit, the parser is as upstream.
	doc, err := ParseWithOptions(strings.NewReader(tag(5000)))
	require.NoError(t, err)
	require.Len(t, findElement(doc, "p").Attr, 5000)

	// 4 MiB of attributes in one tag: refused before the tokenizer keeps them.
	var b strings.Builder
	b.WriteString("<p")
	for i := 0; b.Len() < 4<<20; i++ {
		fmt.Fprintf(&b, " a%d", i)
	}
	b.WriteString(">x")
	allocated := allocatedBy(func() {
		_, err = ParseWithLimit(strings.NewReader(b.String()), 1<<40)
	})
	require.ErrorIs(t, err, ErrTooComplex)
	require.Less(t, allocated, uint64(1<<20), "the tokenizer stopped early")
}

func TestParseWithLimitRejectsEverythingWithALimitOfZero(t *testing.T) {
	_, err := ParseWithLimit(strings.NewReader("x"), 0)
	require.ErrorIs(t, err, ErrTooComplex)
	_, err = ParseWithLimit(strings.NewReader(""), -5)
	require.ErrorIs(t, err, ErrTooComplex)
}

func TestParseWithLimitKeepsOtherErrors(t *testing.T) {
	// A document nested too deep is an error of the parser, not of the limit.
	deep := strings.Repeat("<div>", 600)
	_, err := ParseWithLimit(strings.NewReader(deep), 16_000_000)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTooComplex)
}

func TestParseWithLimitDoesNotChangeTheOptionsOfTheCaller(t *testing.T) {
	opts := make([]ParseOption, 1, 10) // spare room in the slice
	opts[0] = ParseOptionEnableScripting(false)
	_, err := ParseWithLimit(strings.NewReader("<p>x"), 1000, opts...)
	require.NoError(t, err)
	_, err = ParseWithOptions(strings.NewReader("<p>x"), opts...)
	require.NoError(t, err)
	spare := opts[1:cap(opts)]
	require.Nil(t, spare[0], "the slice of the caller got no option") //nolint:gosec // the slice has spare room (see above)
}

// The fuzz target looks for a document where the cost is not the cost of the
// tree, or where the limit does not stop the parser at the cost.
func FuzzCostIsTheCostOfTheTree(f *testing.F) {
	for _, markup := range documents() {
		f.Add(markup)
	}
	r := rand.New(rand.NewSource(11)) //nolint:gosec // a fixed seed makes the test repeat
	for i := 0; i < 20; i++ {
		f.Add(strings.Join(randomTokens(r), ""))
		f.Add(strings.Join(randomAttack(r), ""))
	}
	f.Fuzz(func(t *testing.T, markup string) {
		if len(markup) > 8192 {
			return
		}
		doc, p, err := parseCounted(markup, 0, false)
		if err != nil {
			return
		}
		// A merge of a repeated <html> or <body> tag, and a frameset, make the cost
		// higher than the tree. Without them it is the same.
		lower := strings.ToLower(markup)
		merges := strings.Count(lower, "<html")+strings.Count(lower, "<body") > 1 || hasFrameset(doc)
		if want := treeCost(doc); p.cost < want || (!merges && p.cost != want) {
			t.Fatalf("the cost is %d, the tree costs %d, for %q", p.cost, want, markup)
		}
		if maxAttrs(markup) > maxTagAttrs {
			return // a tag may have too many attributes for ParseWithLimit
		}
		if _, err := ParseWithLimit(strings.NewReader(markup), p.cost, ParseOptionEnableScripting(false)); err != nil {
			t.Fatalf("a limit of %d stops the parser for %q: %v", p.cost, markup, err)
		}
		if _, err := ParseWithLimit(strings.NewReader(markup), p.cost-1, ParseOptionEnableScripting(false)); err != ErrTooComplex {
			t.Fatalf("a limit of %d does not stop the parser for %q: %v", p.cost-1, markup, err)
		}
	})
}
