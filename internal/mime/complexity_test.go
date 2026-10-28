package mime

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// countNodes returns the number of nodes of a tree.
func countNodes(n *html.Node) int {
	c := 1
	for k := n.FirstChild; k != nil; k = k.NextSibling {
		c += countNodes(k)
	}
	return c
}

// amplifiedHTML is the document of the review: it opens n formatting elements
// with different attributes inside a paragraph, and then it has m paragraphs
// with a text. The tree builder copies the n elements for each text.
func amplifiedHTML(n, m int) string {
	var b strings.Builder
	b.WriteString("<p>")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "<b x=%d>", i)
	}
	for i := 0; i < m; i++ {
		b.WriteString("<p>x")
	}
	return b.String()
}

// randomTokens makes the tokens of a document from a list that is chosen to
// stress the parser: formatting elements with different attributes, end tags
// that do not match, tables and other scope elements, select, raw text
// elements, and (with foreign true) svg, math and noscript.
func randomTokens(r *rand.Rand, foreign bool) []string {
	tokens := []string{
		"<b x=%d>", "<i y=%d>", "<a href=h%d>", "<font face=%d>", "<nobr>", "<u z=%d>", "<em e=%d>", "<s q=%d>",
		"<b x=%d>", "<i y=%d>", "<a href=h%d>", "<font face=%d>", "<b>", "<code c=%d>", "<small s=%d>", "<tt t=%d>",
		"<big k=%d>", "<strike w=%d>", "<strong g=%d>",
		"</b>", "</i>", "</a>", "</font>", "</nobr>", "</u>", "</em>", "</s>", "</b>", "</i>", "</strong>", "</code>",
		"<p>", "<p>", "<p>", "<div>", "<span>", "<li>", "<ul>", "<h1>", "<pre>", "<button>", "<hr>", "<br>", "</br>", "<img src=x>",
		"x", "x", "x", "text ", "&amp;", " ",
		"</p>", "</div>", "</span>", "</li>", "</ul>", "</h1>", "</pre>", "</button>",
		"<table>", "<tr>", "<td>", "<th>", "</td>", "</tr>", "</table>", "<caption>", "</caption>", "<tbody>",
		"<select>", "<option>", "</select>", "<textarea>", "</textarea>", "<optgroup>",
		"<template>", "</template>", "<object>", "</object>", "<applet>", "<marquee>", "</marquee>",
		"<title>", "</title>", "<style>", "</style>", "<script>", "</script>", "<iframe>", "</iframe>", "<xmp>", "<plaintext>",
		"<!-- c -->", "<!DOCTYPE html>", "<html>", "<body>", "<head>", "<form>", "</form>", "<frameset>", "<b/>",
		"<foreignObject>", "</foreignObject>", "<desc>", "<mi>", "<annotation-xml encoding=text/html>",
	}
	if foreign {
		tokens = append(tokens, "<svg>", "</svg>", "<math>", "</math>", "<noscript>", "</noscript>", "<svg/>", "<noscript>", "<math>", "<svg>")
	}
	var out []string
	n := 5 + r.Intn(250)
	for i := 0; i < n; i++ {
		t := tokens[r.Intn(len(tokens))]
		if strings.Contains(t, "%d") {
			t = fmt.Sprintf(t, r.Intn(1000))
		}
		out = append(out, t)
	}
	return out
}

// randomAttack makes a document that first opens many formatting elements, and
// then mixes the things that the estimate has to follow: end tags that the
// parser does not find or does not use, scope elements that open and close,
// and text.
func randomAttack(r *rand.Rand) []string {
	names := []string{"a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u"}
	scopes := []string{"table", "tr", "td", "th", "caption", "template", "object", "applet", "marquee", "select", "form", "p", "div", "span", "li"}
	var out []string
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

// bound is the number that checkHTMLComplexity compares with the limit.
func bound(markup string) int {
	nodes, unsure := htmlNodeEstimate(markup, 1<<40)
	if unsure {
		return rawNodeBound(markup)
	}
	return nodes
}

// The number must never be below the number of nodes that the parser builds, or
// a document could make the parser use more memory than the limit allows.
// Random documents try to find a case where it is.
// randomRuns returns how many random documents a test makes: n, or a tenth of n
// when the race detector makes the code much slower.
func randomRuns(n int) int {
	if raceEnabled || testing.Short() {
		return n / 10
	}
	return n
}

func TestHTMLNodeEstimateIsNeverBelowTheParser(t *testing.T) {
	cases := []struct {
		name string
		n    int
		make func(r *rand.Rand) []string
	}{
		{"without svg, math and noscript", 30_000, func(r *rand.Rand) []string { return randomTokens(r, false) }},
		{"with svg, math and noscript", 20_000, func(r *rand.Rand) []string { return randomTokens(r, true) }},
		{"many formatting elements, then scope elements and end tags", 20_000, randomAttack},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := rand.New(rand.NewSource(20260101)) //nolint:gosec // a fixed seed makes the test repeat
			bad := 0
			for i := 0; i < randomRuns(tc.n); i++ {
				in := strings.Join(tc.make(r), "")
				doc, err := parseHTMLUnchecked(in)
				if err != nil {
					continue
				}
				if nodes, got := countNodes(doc), bound(in); got < nodes {
					bad++
					if bad <= 3 {
						t.Errorf("the number %d is below the %d nodes of the parser for:\n%s", got, nodes, in)
					}
				}
			}
			require.Zero(t, bad, "documents")
		})
	}
}

func TestRawNodeBoundIsNeverBelowTheParser(t *testing.T) {
	r := rand.New(rand.NewSource(7)) //nolint:gosec // a fixed seed makes the test repeat
	for i := 0; i < randomRuns(10_000); i++ {
		in := strings.Join(randomTokens(r, i%2 == 0), "")
		if i%3 == 0 {
			in = strings.Join(randomAttack(r), "")
		}
		doc, err := parseHTMLUnchecked(in)
		if err != nil {
			continue
		}
		if nodes := countNodes(doc); rawNodeBound(in) < nodes {
			t.Fatalf("the bound %d is below the %d nodes of the parser for:\n%s", rawNodeBound(in), nodes, in)
		}
	}
}

func TestIsFormattingStart(t *testing.T) {
	for _, s := range []string{"b>", "B>", "b ", "b/", "b\t", "b\n", "font face=1>", "STRONG>", "a href=x>", "nobr>", "strike>", "b"} {
		require.True(t, isFormattingStart(s), s)
	}
	for _, s := range []string{"", ">", "p>", "b2>", "bx>", "strikes>", "strong2", "/b>", "!--", "span>", "bb>", "b\x00>", "b!>", "tbody>", "i@>"} {
		require.False(t, isFormattingStart(s), s)
	}
}

// The attack of the review: 19.5 KB of input make 1.6 million nodes and about
// 250 MiB of memory with the parser. It must be refused without parsing, and
// fast.
func TestAmplifyingHTMLIsRefused(t *testing.T) {
	in := amplifiedHTML(500, 4000) // about 21 KB
	require.Less(t, len(in), 25<<10)

	started := time.Now()
	_, err := HTMLToText(in)
	require.ErrorIs(t, err, ErrHTMLTooComplex)
	_, err = VisibleHTML(in)
	require.ErrorIs(t, err, ErrHTMLTooComplex)
	require.Less(t, time.Since(started), time.Second, "refused without building the tree")
}

func TestAmplifyingHTMLAllocatesLittleWhenRefused(t *testing.T) {
	in := amplifiedHTML(500, 4000)
	allocated := testing.AllocsPerRun(3, func() { _ = checkHTMLComplexity(in) })
	// The check reads tokens and allocates little. The parser makes about a
	// million objects for this input.
	require.Less(t, allocated, 10_000.0)
}

func TestAmplifyingHTMLInEveryWayThatIsStillOpen(t *testing.T) {
	// The parser keeps a formatting element in its list when an end tag does not
	// remove it. Each document here ends the elements in a way that the parser
	// ignores, and then has many paragraphs. The estimate must not forget them.
	opens := func() string {
		// The paragraph matters: the next paragraph ends it and takes the
		// elements off the stack. They stay in the list of the parser, and it
		// copies them for each text.
		var b strings.Builder
		b.WriteString("<p>")
		for i := 0; i < 400; i++ {
			fmt.Fprintf(&b, "<b x=%d>", i)
		}
		return b.String()
	}
	closes := strings.Repeat("</b>", 400)
	paragraphs := strings.Repeat("<p>x", 6000)
	docs := map[string]struct {
		markup string
		// amplifies is true when the parser keeps the elements in its list and
		// copies them for each paragraph. In the other documents the parser
		// removes them (the end tags work), and the estimate may be high.
		amplifies bool
	}{
		"no end tags":                   {opens() + paragraphs, true},
		"end tags of other elements":    {opens() + strings.Repeat("</i>", 400) + paragraphs, true},
		"end tags inside a table":       {opens() + "<table>" + closes + "</table>" + paragraphs, true},
		"end tags inside a cell":        {opens() + "<table><tr><td>" + closes + "</td></tr></table>" + paragraphs, true},
		"end tags inside a caption":     {opens() + "<table><caption>" + closes + "</caption></table>" + paragraphs, true},
		"end tags inside a template":    {opens() + "<template>" + closes + "</template>" + paragraphs, true},
		"end tags inside an object":     {opens() + "<object>" + closes + "</object>" + paragraphs, true},
		"cell closed in a nested table": {opens() + "<table><tr><td><table></td>" + closes + "</table></td></tr></table>" + paragraphs, true},
		// The parser closes the cell with the end tag of the table and removes the
		// elements. The estimate does not know this: it keeps a frame until its own
		// end tag. So it is high here, and the document is refused. That is the
		// safe error.
		"cell closed by a wrong end tag":   {opens() + "<table><tr><td></table>" + closes + "</table>" + paragraphs, false},
		"the count of one name taken away": {opens() + "</b><i>" + strings.Repeat("</b>", 399) + paragraphs, false},
		"the end tags work":                {opens() + closes + paragraphs, false},
	}
	for name, tc := range docs {
		t.Run(name, func(t *testing.T) {
			doc, err := parseHTMLUnchecked(tc.markup)
			require.NoError(t, err)
			nodes := countNodes(doc)
			got, unsure := htmlNodeEstimate(tc.markup, 1<<40)
			require.False(t, unsure)
			require.GreaterOrEqual(t, got, nodes, "the estimate is not below the parser")
			if tc.amplifies {
				require.Greater(t, nodes, 400*5000, "the document does make the parser copy the elements")
			} else {
				require.Less(t, nodes, 100_000, "the parser does not copy the elements")
			}
			if tc.amplifies || name == "cell closed by a wrong end tag" {
				require.ErrorIs(t, checkHTMLComplexity(tc.markup), ErrHTMLTooComplex)
			} else {
				require.NoError(t, checkHTMLComplexity(tc.markup))
			}
		})
	}
}

func TestHTMLWithRawTextOrForeignContentIsStillBounded(t *testing.T) {
	// The tokenizer reads some elements as text where the parser reads them as
	// markup, and the other way round (select, svg, math, noscript). A document
	// that tries this is counted with an upper bound that does not depend on
	// how the markup is read.
	payload := amplifiedHTML(500, 4000)
	tricks := map[string]string{
		"select, math, textarea, xmp": "<select><math></select><textarea></math><xmp></textarea>" + payload,
		"noscript in a select":        "<select><noscript></select>" + payload + "</noscript>",
		"svg title":                   "<svg><title>" + payload + "</title></svg>",
		"svg style":                   "<svg><style>" + payload + "</style></svg>",
		"math textarea":               "<math><textarea>" + payload + "</textarea></math>",
		"noscript":                    "<noscript>" + payload + "</noscript>",
		"decrement trick in noscript": strings.Repeat("<b x=1>", 500) + "<style></b></b></b></style><svg>" + payload,
	}
	for name, in := range tricks {
		t.Run(name, func(t *testing.T) {
			_, err := HTMLToText(in)
			require.ErrorIs(t, err, ErrHTMLTooComplex)
			_, err = VisibleHTML(in)
			require.ErrorIs(t, err, ErrHTMLTooComplex)
		})
	}
}

// A realistic mail has a low number. The test builds newsletter-style
// documents of different sizes and checks that they pass, with a margin.
func TestRealisticDocumentsAreNotRefused(t *testing.T) {
	for _, size := range []int{50 << 10, 500 << 10, 4 << 20} {
		in := newsletterHTML(size)
		doc, err := parseHTMLUnchecked(in)
		require.NoError(t, err)
		nodes := countNodes(doc)
		est, unsure := htmlNodeEstimate(in, 1<<40)
		require.False(t, unsure)
		t.Logf("%7d bytes: parser %7d nodes, estimate %7d", len(in), nodes, est)
		require.GreaterOrEqual(t, est, nodes)
		require.NoError(t, checkHTMLComplexity(in), "%d bytes", size)
		require.Less(t, est, maxHTMLNodes*3/4, "a quarter of the budget is left")
	}
}

// newsletterHTML makes a document like a marketing mail of about n bytes:
// nested tables, divs, links, spans and inline styles, with some formatting
// elements.
func newsletterHTML(n int) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><title>News</title><style>.h{display:none} td{padding:0}</style></head><body style="margin:0">`)
	b.WriteString(`<table width="600" cellpadding="0" cellspacing="0" style="background:#fff">`)
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, `<tr><td style="padding:10px;font-family:Arial"><div style="font-size:14px;color:#333"><span style="font-weight:bold">Item %d</span> `+
			`<a href="https://example.com/item/%d?utm_source=news&amp;utm_medium=email" style="color:#06c;text-decoration:none">Read more about this</a> `+
			`<b>bold</b> <i>italic</i> <font color="#999" size="2">small print</font> <strong>strong</strong></div>`+
			`<p style="margin:0">Some paragraph text with <em>emphasis</em> and a <a href="https://example.com/x/%d">link</a>.</p></td></tr>`, i, i, i)
	}
	b.WriteString(`</table></body></html>`)
	return b.String()
}

// The fuzz target looks for a document where the number is below the parser.
func FuzzHTMLNodeEstimateIsNeverBelowTheParser(f *testing.F) {
	r := rand.New(rand.NewSource(3)) //nolint:gosec // a fixed seed makes the test repeat
	for i := 0; i < 20; i++ {
		f.Add(strings.Join(randomTokens(r, i%2 == 0), ""))
		f.Add(strings.Join(randomAttack(r), ""))
	}
	for _, s := range []string{
		amplifiedHTML(40, 60),
		"<select><math></select><textarea></math><xmp></textarea>" + amplifiedHTML(30, 40),
		"<b x=1><b x=2><table><b x=3></b></b></table><p>x<p>x",
		"<svg><title><b x=1><b x=2></title></svg><p>x<p>x",
		"<noscript><b x=1><b x=2></noscript><p>x<p>x",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if len(in) > 4096 {
			return
		}
		doc, err := parseHTMLUnchecked(in)
		if err != nil {
			return
		}
		if nodes, got := countNodes(doc), bound(in); got < nodes {
			t.Fatalf("the number %d is below the %d nodes of the parser for %q", got, nodes, in)
		}
	})
}
