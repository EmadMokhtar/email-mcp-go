package xhtml // email-mcp: tests of the node limit (not from upstream)

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	uhtml "golang.org/x/net/html"
)

// countTree returns the number of nodes of a tree, the node itself included.
func countTree(n *Node) int {
	c := 1
	for k := n.FirstChild; k != nil; k = k.NextSibling {
		c += countTree(k)
	}
	return c
}

// parseCounted parses markup and returns the parser, so that a test can read its
// counters. The scripting flag is off, like in the mail code.
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

// documents are the documents for the tests: normal ones, and one for each way
// that is known to make the parser build more nodes than the input is long.
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
		"table":                       "<table><caption>c</caption><colgroup><col></colgroup><thead><tr><th>h</th></tr></thead><tbody><tr><td>a</td><td>b</td></tr></tbody></table>",
		"table without rows":          "<table><td>a<td>b</table><table><tr>x<tr>y</table>",
		"text in a table":             "<table>abc <b>x</b> def<!--c--> ghi</table>after",
		"nested tables":               "<table><tr><td><table><tr><td>in</td></tr></table></td></tr></table>",
		"template":                    "<template><p>a</p><table><td>b</td></table><template><b>c</b></template></template><p>d</p>",
		"template in table":           "<table><template><tr><td>x</td></tr></template></table>",
		"select":                      "<select><option>a<optgroup><option>b</select><p>x",
		"form in table":               "<table><form><input><tr><td>x</td></tr></form></table>",
		"foreign content":             "<svg><circle/><foreignObject><p>x</p></foreignObject><title>t</title></svg><math><mi>x</mi><annotation-xml encoding='text/html'><p>y</p></annotation-xml></math>",
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
		"adoption agency, simple":           "<b><p>x</b>y",
		"adoption agency, nested":           rep("<b><div>x", 30) + rep("</b>", 30),
		"adoption agency, misnested":        rep("<a><b><div>", 20) + rep("</a></b>", 20),
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

// The count is the number of nodes of the tree. If one place that makes a node
// did not count it, a document would show it.
func TestNodeCountIsTheNumberOfNodesOfTheTree(t *testing.T) {
	for name, markup := range documents() {
		t.Run(name, func(t *testing.T) {
			doc, p, err := parseCounted(markup, 0, false)
			require.NoError(t, err)
			require.Equal(t, countTree(doc), p.nodes)
		})
	}
	for name, gen := range map[string]func(*rand.Rand) []string{"random tokens": randomTokens, "random attack": randomAttack} {
		t.Run(name, func(t *testing.T) {
			r := rand.New(rand.NewSource(20261028)) //nolint:gosec // a fixed seed makes the test repeat
			for i := 0; i < randomDocuments(); i++ {
				markup := strings.Join(gen(r), "")
				doc, p, err := parseCounted(markup, 0, false)
				if err != nil {
					continue // nested too deep
				}
				if got, want := p.nodes, countTree(doc); got != want {
					t.Fatalf("the count is %d, the tree has %d nodes, for:\n%s", got, want, markup)
				}
			}
		})
	}
}

func TestNodeCountOfAFragmentIsTheNumberOfNodesOfTheTree(t *testing.T) {
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
				want := countTree(p.doc)
				for _, n := range nodes {
					want += countTree(n)
				}
				if got := p.nodes; got != want {
					t.Fatalf("the count is %d, the tree has %d nodes, for:\n%s", got, want, markup)
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

// The patch must not change what the parser makes. The tree is the same as the
// tree of the upstream package, for every document of the tests.
func TestTreesAreTheSameAsUpstream(t *testing.T) {
	same := func(t *testing.T, markup string) {
		t.Helper()
		ours, err1 := ParseWithOptions(strings.NewReader(markup), ParseOptionEnableScripting(false))
		theirs, err2 := uhtml.ParseWithOptions(strings.NewReader(markup), uhtml.ParseOptionEnableScripting(false))
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("errors differ: %v and %v for:\n%s", err1, err2, markup)
		}
		if err1 != nil {
			return
		}
		var a, b bytes.Buffer
		errA, errB := Render(&a, ours), uhtml.Render(&b, theirs)
		if (errA == nil) != (errB == nil) || (errA != nil && errA.Error() != errB.Error()) {
			t.Fatalf("the errors of Render differ: %v and %v for:\n%s", errA, errB, markup)
		}
		if a.String() != b.String() {
			t.Fatalf("the trees differ for:\n%s\nours:   %s\ntheirs: %s", markup, a.String(), b.String())
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
			cost := p.nodes + p.copiedAttrs

			got, err := ParseWithLimit(strings.NewReader(markup), cost, ParseOptionEnableScripting(false))
			require.NoError(t, err, "a limit of exactly the number of nodes is not passed")
			var a, b bytes.Buffer
			require.NoError(t, Render(&a, got))
			require.NoError(t, Render(&b, want))
			require.Equal(t, b.String(), a.String())
		})
	}
}

// The parser stops at the first node over the limit, and it has not made more.
func TestParseWithLimitStopsJustOverTheLimit(t *testing.T) {
	for name, markup := range documents() {
		t.Run(name, func(t *testing.T) {
			_, full, err := parseCounted(markup, 0, false)
			require.NoError(t, err)
			cost := full.nodes + full.copiedAttrs

			for _, limit := range []int{cost - 1, cost / 2, 1} {
				if limit < 0 || limit >= cost {
					continue
				}
				doc, p, err := parseCounted(markup, limit, true)
				require.ErrorIs(t, err, ErrTooManyNodes, "limit %d of %d", limit, cost)
				require.Nil(t, doc)
				// The step that passed the limit is the last: the count is the
				// limit plus that step (a node, and the attributes of a clone).
				require.Greater(t, p.nodes+p.copiedAttrs, limit)
				require.LessOrEqual(t, p.nodes+p.copiedAttrs, limit+1+maxAttrs(markup))
			}
		})
	}
}

// maxAttrs is an upper bound of the attributes of one element in markup: the
// number of "=" and of spaces is not below it.
func maxAttrs(markup string) int { return strings.Count(markup, " ") + strings.Count(markup, "=") }

// Every way that is known to make the parser build many more nodes than the
// input is long is stopped by the limit, and the parser makes no more than the
// limit.
func TestAmplifyingDocumentsAreStoppedByTheLimit(t *testing.T) {
	const limit = 50_000
	attacks := map[string]string{
		"formatting elements copied for each paragraph": amplified(500, 4000),
		"the same in a table (foster parenting)":        tableAmplified(200, 10_000),
		"the same, large":                               tableAmplified(500, 276_001),
		"a clone with many attributes":                  "<p><b " + manyAttributes(2000) + ">" + strings.Repeat("<p>x", 2000),
		"formatting in a template":                      "<template><b x=1><b x=2>" + strings.Repeat("<p>z", 40_000) + "</template>",
	}
	for name, markup := range attacks {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			doc, p, err := parseCounted(markup, limit, true)
			elapsed := time.Since(started)

			require.ErrorIs(t, err, ErrTooManyNodes)
			require.Nil(t, doc)
			require.LessOrEqual(t, p.nodes, limit+1, "no more nodes than the limit were made")
			require.Less(t, elapsed, 2*time.Second)
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

func TestCopiedAttributesCountForTheLimit(t *testing.T) {
	// One element with 200 attributes in the list, and 100 paragraphs. The tree
	// has about 300 nodes, but the parser copies the 200 attributes 100 times.
	markup := "<p><b " + manyAttributes(200) + ">" + strings.Repeat("<p>x", 100)
	doc, p, err := parseCounted(markup, 0, false)
	require.NoError(t, err)
	require.Less(t, countTree(doc), 1000)
	require.Greater(t, p.copiedAttrs, 19_000)

	_, _, err = parseCounted(markup, 10_000, true)
	require.ErrorIs(t, err, ErrTooManyNodes, "20,000 copied attributes are more than 10,000")
	_, _, err = parseCounted(markup, 30_000, true)
	require.NoError(t, err)
}

func TestParseWithLimitRejectsEverythingWithALimitOfZero(t *testing.T) {
	_, err := ParseWithLimit(strings.NewReader("x"), 0)
	require.ErrorIs(t, err, ErrTooManyNodes)
	_, err = ParseWithLimit(strings.NewReader(""), -5)
	require.ErrorIs(t, err, ErrTooManyNodes)
}

func TestParseWithLimitKeepsOtherErrors(t *testing.T) {
	// A document nested too deep is an error of the parser, not of the limit.
	deep := strings.Repeat("<div>", 600)
	_, err := ParseWithLimit(strings.NewReader(deep), 1_000_000)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTooManyNodes)
}

func TestParseWithLimitDoesNotChangeTheOptionsOfTheCaller(t *testing.T) {
	opts := make([]ParseOption, 1, 10) // spare room in the slice
	opts[0] = ParseOptionEnableScripting(false)
	_, err := ParseWithLimit(strings.NewReader("<p>x"), 100, opts...)
	require.NoError(t, err)
	_, err = ParseWithOptions(strings.NewReader("<p>x"), opts...)
	require.NoError(t, err)
	spare := opts[1:cap(opts)]
	require.Nil(t, spare[0], "the slice of the caller got no option") //nolint:gosec // the slice has spare room (see above)
}

// The fuzz target looks for a document where the count is not the number of nodes
// of the tree, or where the tree is not the tree of upstream.
func FuzzNodeCountIsTheNumberOfNodesOfTheTree(f *testing.F) {
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
		if got, want := p.nodes, countTree(doc); got != want {
			t.Fatalf("the count is %d, the tree has %d nodes, for %q", got, want, markup)
		}
		// The limit stops the parser at the first step over it. The count can be
		// higher while the parser works than at the end (a frameset takes the body
		// out of the tree), so a limit of exactly the end count may stop it, and
		// twice the count does not.
		cost := p.nodes + p.copiedAttrs
		if _, err := ParseWithLimit(strings.NewReader(markup), 2*cost+10, ParseOptionEnableScripting(false)); err != nil {
			t.Fatalf("a limit of %d stops the parser for %q: %v", 2*cost+10, markup, err)
		}
		if cost > 1 {
			if _, err := ParseWithLimit(strings.NewReader(markup), cost-1, ParseOptionEnableScripting(false)); err != ErrTooManyNodes {
				t.Fatalf("a limit of %d does not stop the parser for %q: %v", cost-1, markup, err)
			}
		}
	})
}
