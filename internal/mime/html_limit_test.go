package mime

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/thirdparty/xhtml"
)

// amplifiedHTML opens n formatting elements with different attributes inside a
// paragraph, and then it has m paragraphs with a text. The tree builder copies
// the n elements for each text.
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

// tableAmplifiedHTML opens the k formatting elements in a table. The tree
// builder puts them out of the table (foster parenting) and keeps them in its
// list, so it copies them for each paragraph after the table.
func tableAmplifiedHTML(k, m int) string {
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

// longLinkCopiedHTML has a link with an address of s bytes, and then c
// paragraphs. The tree builder copies the link element into each paragraph. The
// copies share the address, but HTMLToText reads it, and VisibleHTML writes it,
// once for each copy.
func longLinkCopiedHTML(s, c int) string {
	return `<p><a href="https://e.example/` + strings.Repeat("a", s) + `">` + strings.Repeat("<p>y", c)
}

// longStyleCopiedHTML has a bold element with an inline style of about s
// bytes, and then c paragraphs. The style is read again for each copy.
func longStyleCopiedHTML(s, c int) string {
	return `<p><b style="` + strings.Repeat("color:red;", s/10) + `">` + strings.Repeat("<p>y", c)
}

// repeatedHTMLTags has n <html> start tags in the body, each with a new
// attribute. The tree builder merges each into the html element, and it goes
// over all the attributes of the element for each tag.
func repeatedHTMLTags(n int) string {
	var b strings.Builder
	b.WriteString("<p>x")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "<html a%d>", i)
	}
	return b.String()
}

// framesetHTML opens 42 formatting elements without attributes (three of each
// name), and then has c paragraphs, each with an element that makes the tree
// builder copy the 42. Then a frameset takes the body out of the tree, and each
// "</>" after it is an empty comment.
func framesetHTML(c, comments int) string {
	var b strings.Builder
	b.WriteString("<p>")
	for _, name := range []string{"a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u"} {
		b.WriteString(strings.Repeat("<"+name+">", 3))
	}
	b.WriteString(strings.Repeat("<p><x>", c))
	b.WriteString("<frameset>")
	b.WriteString(strings.Repeat("</>", comments))
	return b.String()
}

// attributesInManyTagsHTML repeats a br element with 26 attributes to about
// size bytes.
func attributesInManyTagsHTML(size int) string {
	tag := "<br a b c d e f g h i j k l m n o p q r s t u v w x y z>"
	return strings.Repeat(tag, size/len(tag))
}

// attributesInOneTagHTML is one p element with different attributes, about
// size bytes long.
func attributesInOneTagHTML(size int) string {
	var b strings.Builder
	b.WriteString("<p")
	for i := 0; b.Len() < size-2; i++ {
		fmt.Fprintf(&b, " a%d", i)
	}
	b.WriteString(">x")
	return b.String()
}

// newsletterHTML makes a document like a marketing mail of about n bytes:
// nested tables, divs, links, spans and inline styles, with some formatting
// elements. With extras, each row has an inline svg icon, and the document has a
// noscript element with a tracking image.
func newsletterHTML(n int, extras bool) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><title>News</title><style>.h{display:none} td{padding:0}</style></head><body style="margin:0">`)
	if extras {
		b.WriteString(`<noscript><img src="https://t.example/p.gif" width="1" height="1"></noscript>`)
	}
	b.WriteString(`<table width="600" cellpadding="0" cellspacing="0" style="background:#fff">`)
	for i := 0; b.Len() < n; i++ {
		icon := ""
		if extras {
			icon = `<svg width="16" height="16" viewBox="0 0 16 16"><path d="M0 0h16v16H0z"/><title>icon</title></svg>`
		}
		fmt.Fprintf(&b, `<tr><td style="padding:10px;font-family:Arial">%s<div style="font-size:14px;color:#333"><span style="font-weight:bold">Item %d</span> `+
			`<a href="https://example.com/item/%d?utm_source=news&amp;utm_medium=email" style="color:#06c;text-decoration:none">Read more about this</a> `+
			`<b>bold</b> <i>italic</i> <font color="#999" size="2">small print</font> <strong>strong</strong></div>`+
			`<p style="margin:0">Some paragraph text with <em>emphasis</em> and a <a href="https://example.com/x/%d">link</a>.</p></td></tr>`, icon, i, i, i)
	}
	b.WriteString(`</table></body></html>`)
	return b.String()
}

// usage is what a call cost.
type usage struct {
	elapsed time.Duration
	// allocated is every byte that was allocated, also the bytes that were freed
	// again. It does not depend on when the garbage collector runs, and the heap
	// cannot grow by more, so it is an upper bound of peak.
	allocated uint64
	// peak is the highest heap seen while the call ran, above the heap before it.
	// It is sampled every millisecond, so it can miss a short peak.
	peak uint64
}

func (u usage) String() string {
	return fmt.Sprintf("%v, peak heap +%d MiB, %d MiB allocated", u.elapsed.Round(time.Millisecond), u.peak>>20, u.allocated>>20)
}

// measured runs f and returns what it cost.
func measured(f func()) usage {
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	stop, done := make(chan struct{}), make(chan struct{})
	highest := before.HeapAlloc
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
			runtime.ReadMemStats(&m)
			highest = max(highest, m.HeapAlloc)
		}
	}()
	started := time.Now()
	f()
	elapsed := time.Since(started)
	close(stop)
	<-done
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	highest = max(highest, after.HeapAlloc)
	return usage{elapsed: elapsed, allocated: after.TotalAlloc - before.TotalAlloc, peak: highest - before.HeapAlloc}
}

// converters are the two functions that parse HTML.
var converters = map[string]func(string) (string, error){
	"HTMLToText":  HTMLToText,
	"VisibleHTML": VisibleHTML,
}

const (
	// maxRefusedPeak bounds the heap of a refused document. The parser stops
	// at the limit: at most a million nodes, about 110 MiB.
	maxRefusedPeak = 128 << 20
	// maxRefusedAllocation bounds every byte that is allocated for a refused
	// document. Before the limit counted every node and attribute, the documents
	// of the tests took from 190 MiB to 27 GB.
	maxRefusedAllocation = 192 << 20
	// maxRefusedTime bounds the time of a refused document, without the race
	// detector. The documents of the tests took up to 36 s before.
	maxRefusedTime = 3 * time.Second
)

// requireRefused checks that both converters refuse markup with
// ErrHTMLTooComplex, and that they use less memory and time than the bounds.
func requireRefused(t *testing.T, markup string, maxAllocated uint64) {
	t.Helper()
	for name, convert := range converters {
		var out string
		var err error
		u := measured(func() { out, err = convert(markup) })
		t.Logf("%s: %d bytes, %v, refused: %v", name, len(markup), u, errors.Is(err, ErrHTMLTooComplex))
		require.ErrorIs(t, err, ErrHTMLTooComplex, name)
		require.ErrorIs(t, err, xhtml.ErrTooComplex, name)
		require.Empty(t, out, name)
		require.Less(t, u.allocated, maxAllocated, name)
		require.Less(t, u.peak, uint64(maxRefusedPeak), name)
		if !raceEnabled {
			require.Less(t, u.elapsed, maxRefusedTime, name)
		}
	}
}

// The attack of the first review: 20 KB of input make 2 million nodes with the
// parser (the formatting elements are copied for each paragraph).
func TestAmplifyingHTMLIsRefused(t *testing.T) {
	in := amplifiedHTML(500, 4000)
	require.Less(t, len(in), 25<<10)
	requireRefused(t, in, maxRefusedAllocation)
}

// The tree builder puts the elements that are opened in a table out of the
// table, and it keeps them in its list.
func TestAmplifyingHTMLInATableIsRefused(t *testing.T) {
	recipes := map[string]string{
		"200 elements, 10,000 paragraphs (41 KB)":    tableAmplifiedHTML(200, 10_000),
		"500 elements, 276,001 paragraphs (1.1 MiB)": tableAmplifiedHTML(500, 276_001),
	}
	for name, in := range recipes {
		t.Run(name, func(t *testing.T) { requireRefused(t, in, maxRefusedAllocation) })
	}
}

// A copy of an element shares the attributes of the element. The parser makes
// it cheaply, but the converters read the attributes for each copy, so the
// bytes of the attributes of each copy count.
func TestCopiedLongLinksAreRefused(t *testing.T) {
	// Before: VisibleHTML took 2.5 GB for the first, HTMLToText 2.1 GB for the
	// second.
	for _, sc := range [][2]int{{1 << 20, 500}, {1990, 240_000}, {64 << 10, 1000}, {16 << 10, 2000}} {
		t.Run(fmt.Sprintf("%d bytes, %d copies", sc[0], sc[1]), func(t *testing.T) {
			requireRefused(t, longLinkCopiedHTML(sc[0], sc[1]), 64<<20)
		})
	}
}

func TestCopiedLongStyleIsRefused(t *testing.T) {
	// Before: 36 s and 27 GB of allocations for HTMLToText.
	requireRefused(t, longStyleCopiedHTML(3<<20, 1000), 64<<20)
}

// Each repeated <html> tag made the tree builder go over every attribute of the
// html element: 20,000 tags (249 KB) took 13 s.
func TestRepeatedHTMLTagsAreRefused(t *testing.T) {
	in := repeatedHTMLTags(20_000)
	require.Less(t, len(in), 256<<10)
	requireRefused(t, in, maxRefusedAllocation)
}

// The frameset takes the body out of the tree, but the tree builder can still
// copy its formatting elements. The cost of the body stays.
func TestFramesetDoesNotGiveTheCostOfTheBodyBack(t *testing.T) {
	// Before: 3.1 MB, not refused, 199 MiB.
	requireRefused(t, framesetHTML(22_000, 990_000), 160<<20)
}

// The attributes that come from the input count too, not only the copies.
func TestManyAttributesInManyTagsAreRefused(t *testing.T) {
	// Before: 4 MiB, not refused, 190 MiB.
	in := attributesInManyTagsHTML(4 << 20)
	requireRefused(t, in, maxRefusedAllocation)
}

// The tokenizer keeps several copies of the attributes of a tag before the
// parser sees the tag. A tag with more than a thousand different attributes is
// stopped in the tokenizer.
func TestManyAttributesInOneTagAreRefused(t *testing.T) {
	// Before: 4 MiB, not refused, 160 MiB.
	requireRefused(t, attributesInOneTagHTML(4<<20), 8<<20)
}

// One long attribute is cheap. It is not refused, and it uses little memory.
func TestOneLongAttributeIsConverted(t *testing.T) {
	in := `<p title="` + strings.Repeat("t", 4<<20-64) + `">x`
	for name, convert := range converters {
		var out string
		var err error
		u := measured(func() { out, err = convert(in) })
		t.Logf("%s: %d bytes converted in %v", name, len(in), u)
		require.NoError(t, err, name)
		require.Contains(t, out, "x", name)
		require.NotContains(t, out, "ttt", name)
		require.Less(t, u.allocated, uint64(64<<20), name)
	}
}

// Each document opens 400 formatting elements in a paragraph, and then has
// paragraphs. In what way the elements are ended decides if the tree builder
// keeps them in its list. The parser counts what it makes, so it does not matter
// to us. The documents that the parser does not amplify are converted.
func TestAmplifyingHTMLInEveryWayThatIsStillOpen(t *testing.T) {
	var b strings.Builder
	b.WriteString("<p>")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "<b x=%d>", i)
	}
	opens := b.String()
	closes := strings.Repeat("</b>", 400)
	paragraphs := strings.Repeat("<p>x", 6000)
	docs := map[string]struct {
		markup  string
		refused bool
	}{
		"no end tags":                      {opens + paragraphs, true},
		"end tags of other elements":       {opens + strings.Repeat("</i>", 400) + paragraphs, true},
		"end tags inside a table":          {opens + "<table>" + closes + "</table>" + paragraphs, true},
		"end tags inside a cell":           {opens + "<table><tr><td>" + closes + "</td></tr></table>" + paragraphs, true},
		"end tags inside a caption":        {opens + "<table><caption>" + closes + "</caption></table>" + paragraphs, true},
		"end tags inside a template":       {opens + "<template>" + closes + "</template>" + paragraphs, true},
		"end tags inside an object":        {opens + "<object>" + closes + "</object>" + paragraphs, true},
		"cell closed in a nested table":    {opens + "<table><tr><td><table></td>" + closes + "</table></td></tr></table>" + paragraphs, true},
		"elements opened in a table":       {"<table>" + opens + "</table>" + paragraphs, true},
		"elements opened in a cell":        {"<table><tr><td>" + opens + "</td></tr></table>" + paragraphs, false},
		"cell closed by a wrong end tag":   {opens + "<table><tr><td></table>" + closes + "</table>" + paragraphs, false},
		"the count of one name taken away": {opens + "</b><i>" + strings.Repeat("</b>", 399) + paragraphs, false},
		"the end tags work":                {opens + closes + paragraphs, false},
	}
	for name, tc := range docs {
		t.Run(name, func(t *testing.T) {
			var err error
			u := measured(func() { _, err = HTMLToText(tc.markup) })
			if tc.refused {
				require.ErrorIs(t, err, ErrHTMLTooComplex)
				require.Less(t, u.allocated, uint64(maxRefusedAllocation))
			} else {
				require.NoError(t, err)
			}
			if !raceEnabled {
				require.Less(t, u.elapsed, maxRefusedTime)
			}
		})
	}
}

// What the old estimate could not follow: the tokenizer and the tree builder read
// the text of select, svg, math and noscript in different ways. The parser does
// not depend on the tokens.
func TestHTMLWithRawTextOrForeignContentIsStillBounded(t *testing.T) {
	payload := amplifiedHTML(500, 4000)
	tricks := map[string]string{
		"select, math, textarea, xmp": "<select><math></select><textarea></math><xmp></textarea>" + payload,
		"noscript in a select":        "<select><noscript></select>" + payload + "</noscript>",
		"svg title":                   "<svg><title>" + payload + "</title></svg>",
		"svg style":                   "<svg><style>" + payload + "</style></svg>",
		"math textarea":               "<math><textarea>" + payload + "</textarea></math>",
		"noscript":                    "<noscript>" + payload + "</noscript>",
		"end tags in a style":         amplifiedHTML(400, 0) + "<style>" + strings.Repeat("</b>", 400) + "</style>" + strings.Repeat("<p>x", 6000),
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

// treeCost returns what xhtml.ParseWithLimit counts for a tree: NodeCost for
// each node, and AttrCost plus the bytes for each attribute. For a document
// without a frameset and without repeated <html> or <body> tags, it is the
// cost of the parser (the tests of xhtml check that).
func treeCost(n *xhtml.Node) (cost, nodes, attrs, attrBytes int) {
	nodes, cost = 1, xhtml.NodeCost
	for _, a := range n.Attr {
		size := len(a.Namespace) + len(a.Key) + len(a.Val)
		attrs++
		attrBytes += size
		cost += xhtml.AttrCost + size
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		c1, n1, a1, b1 := treeCost(c)
		cost, nodes, attrs, attrBytes = cost+c1, nodes+n1, attrs+a1, attrBytes+b1
	}
	return cost, nodes, attrs, attrBytes
}

// A realistic mail still converts, with at least a half of the limit left. The
// numbers are in the log.
func TestRealisticDocumentsAreConverted(t *testing.T) {
	for _, tc := range []struct {
		size   int
		extras bool
	}{{50 << 10, false}, {500 << 10, false}, {4 << 20, false}, {2 << 20, true}} {
		in := newsletterHTML(tc.size, tc.extras)
		doc, err := xhtml.ParseWithOptions(strings.NewReader(in), xhtml.ParseOptionEnableScripting(false))
		require.NoError(t, err)
		cost, nodes, attrs, attrBytes := treeCost(doc)
		t.Logf("%7d bytes (svg and noscript: %v): cost %d = %.1f%% of %d (%d nodes, %d attributes, %d bytes of attributes)",
			len(in), tc.extras, cost, 100*float64(cost)/maxHTMLCost, maxHTMLCost, nodes, attrs, attrBytes)
		require.LessOrEqual(t, cost, maxHTMLCost/2, "at least a half of the limit is left")
		_, err = xhtml.ParseWithLimit(strings.NewReader(in), maxHTMLCost/2, xhtml.ParseOptionEnableScripting(false))
		require.NoError(t, err, "the parser agrees: half of the limit is enough")

		var text, html string
		u := measured(func() { text, err = HTMLToText(in) })
		require.NoError(t, err)
		require.Contains(t, text, "Read more about this (https://example.com/item/0")
		t.Logf("        HTMLToText %v", u)
		u = measured(func() { html, err = VisibleHTML(in) })
		require.NoError(t, err)
		require.Contains(t, html, "Read more about this")
		require.NotContains(t, html, "<svg", "the svg is not content")
		t.Logf("        VisibleHTML %v", u)
	}
}

// Before the parser counted its work, a document with svg or noscript was
// converted only when it was small, because the estimate could not follow the
// parser into them.
func TestLargeHTMLWithSVGAndNoscriptIsConverted(t *testing.T) {
	in := newsletterHTML(2<<20, true)
	require.Contains(t, in, "<noscript>")
	require.Contains(t, in, "<svg ")

	text, err := HTMLToText(in)
	require.NoError(t, err)
	require.Contains(t, text, "Read more about this (https://example.com/item/0")
	html, err := VisibleHTML(in)
	require.NoError(t, err)
	require.Contains(t, html, "Read more about this")
	require.NotContains(t, html, "<svg", "the svg is not content")
}

// escapedLinkCopiedHTML has a link whose address repeats esc s times, and then
// c paragraphs. The parser copies the link into each paragraph. The work budget
// counts the bytes of the address once for each copy, but the HTML written
// from the tree is longer: xhtml.Render writes "&" as "&amp;" and a quote as
// "&#34;", five bytes for one.
func escapedLinkCopiedHTML(esc string, s, c int) string {
	return `<p><a href="https://e.example/` + strings.Repeat(esc, s) + `">` + strings.Repeat("<p>y", c)
}

// encodedLinkCopiedHTML has a link with 338 "é" (676 bytes), copied into c
// paragraphs. The sanitizer writes each "é" of the url as "%C3%A9", three bytes
// for one, so the result of the sanitizer is three times longer than its input.
func encodedLinkCopiedHTML(c int) string {
	return `<p><a href="https://e.example/` + strings.Repeat("\U000000e9", 338) + `x">` + strings.Repeat("<p>y", c)
}

// shownLinkCopiedHTML has a link with 676 spaces in its url, copied into c
// paragraphs. HTMLToText shows the url for each copy, with each space written
// as %20: 2,047 characters.
func shownLinkCopiedHTML(c int) string {
	return `<p><a href="https://e.example/` + strings.Repeat(" ", 676) + `x">` + strings.Repeat("<p>y", c)
}

// renderedVisibleHTML returns the tree of VisibleHTML as it goes into the
// sanitizer, without a limit.
func renderedVisibleHTML(t *testing.T, markup string) string {
	t.Helper()
	doc, err := parseHTML(markup)
	require.NoError(t, err)
	v := newVisibility(doc)
	v.prune(doc)
	var b strings.Builder
	require.NoError(t, xhtml.Render(&b, doc))
	return b.String()
}

// requireTooLargeRefused checks that convert refuses markup because its output
// would be larger than the limit, and that it uses little memory and time.
func requireTooLargeRefused(t *testing.T, convert func(string) (string, error), markup string, maxAllocated uint64) usage {
	t.Helper()
	_, err := parseHTML(markup)
	require.NoError(t, err, "the document is under the work budget of the parser")

	var out string
	u := measured(func() { out, err = convert(markup) })
	require.ErrorIs(t, err, ErrHTMLTooComplex)
	require.ErrorIs(t, err, errConvertedTooLarge)
	require.NotErrorIs(t, err, xhtml.ErrTooComplex, "the output limit refuses it, not the parser")
	require.Empty(t, out)
	require.Less(t, u.allocated, maxAllocated)
	require.Less(t, u.peak, uint64(maxRefusedPeak))
	if !raceEnabled {
		require.Less(t, u.elapsed, time.Second)
	}
	return u
}

// These documents are under the work budget of the parser, but the HTML written
// from them was 80 MB, 48 MB and 44 MB, from 19 KB, 89 KB and 85 KB of markup.
// For the first, VisibleHTML took 1.65 s, a peak heap of +395 MiB, and 1,251
// MiB of allocations. Now the rendered tree, or the result of the sanitizer,
// reaches the limit and VisibleHTML stops.
func TestVisibleHTMLThatWouldBeTooLargeIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name         string
		markup       string
		maxAllocated uint64
		// renderedFits: the rendered tree is under the limit, and the sanitizer
		// stops. Else Render stops. The url of this link is longer than 2,048
		// characters, so HTMLToText does not show it, and the text is small.
		renderedFits bool
	}{
		{"14,000 & in a link, 1,134 copies (19 KB)", escapedLinkCopiedHTML("&", 14_000, 1134), 160 << 20, false},
		{"14,000 &quot; in a link, 1,134 copies (89 KB)", escapedLinkCopiedHTML("&quot;", 14_000, 1134), 160 << 20, false},
		// Before: 0.44 s, +196 MiB, 769 MiB allocated, 44 MB of HTML. HTMLToText
		// shows this url (see TestHTMLToTextThatWouldBeTooLongIsRefused).
		{"338 non-ASCII letters in a link, 20,968 copies (85 KB)", encodedLinkCopiedHTML(20_968), 512 << 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := requireTooLargeRefused(t, VisibleHTML, tc.markup, tc.maxAllocated)
			t.Logf("VisibleHTML: %d bytes refused in %v", len(tc.markup), u)

			if tc.renderedFits {
				rendered := renderedVisibleHTML(t, tc.markup)
				t.Logf("rendered tree: %d bytes (limit %d)", len(rendered), maxVisibleHTMLBytes)
				require.Less(t, len(rendered), maxVisibleHTMLBytes)
				return
			}
			text, err := HTMLToText(tc.markup)
			require.NoError(t, err)
			require.Less(t, len(text), 1<<20)
		})
	}
}

// The url of the link is shown for each copy that the parser makes. The text
// would be 43 MB from 85 KB of markup, which took 0.98 s, +204 MiB and 764 MiB
// of allocations.
func TestHTMLToTextThatWouldBeTooLongIsRefused(t *testing.T) {
	for name, in := range map[string]string{
		"text in each copy (85 KB)": shownLinkCopiedHTML(20_968),
		"the url alone (147 KB)":    strings.ReplaceAll(shownLinkCopiedHTML(20_968), "<p>y", "<p><br>"),
		"non-ASCII letters (85 KB)": encodedLinkCopiedHTML(20_968),
	} {
		t.Run(name, func(t *testing.T) {
			u := requireTooLargeRefused(t, HTMLToText, in, 384<<20)
			t.Logf("HTMLToText: %d bytes refused in %v", len(in), u)
		})
	}
}

// The limit on the text is exact, also for a letter of several bytes. It
// counts the text as it is built, with the line breaks of the blocks, so the
// text here is not in a paragraph.
func TestHTMLToTextIsLimitedToMaxConvertedTextBytes(t *testing.T) {
	const euro = "\U000020ac" // three bytes in UTF-8
	atTheLimit := strings.Repeat("a", maxConvertedTextBytes-3) + euro
	out, err := HTMLToText(atTheLimit)
	require.NoError(t, err)
	require.Equal(t, maxConvertedTextBytes, len(out))
	require.True(t, out == atTheLimit, "the text is kept as it is")

	out, err = HTMLToText("a" + atTheLimit)
	require.ErrorIs(t, err, ErrHTMLTooComplex)
	require.ErrorIs(t, err, errConvertedTooLarge)
	require.Empty(t, out)
}

// The limit on the rendered tree is exact. Render writes the html and body
// elements around the paragraph, and the sanitizer takes them away.
func TestVisibleHTMLIsLimitedToMaxVisibleHTMLBytes(t *testing.T) {
	frame := len(renderedVisibleHTML(t, "<p>"))
	text := strings.Repeat("a", maxVisibleHTMLBytes-frame)
	require.Len(t, renderedVisibleHTML(t, "<p>"+text), maxVisibleHTMLBytes)

	out, err := VisibleHTML("<p>" + text)
	require.NoError(t, err)
	require.True(t, out == "<p>"+text+"</p>", "the paragraph is kept as it is")

	out, err = VisibleHTML("<p>a" + text)
	require.ErrorIs(t, err, ErrHTMLTooComplex)
	require.ErrorIs(t, err, errConvertedTooLarge)
	require.Empty(t, out)
}

func TestLimitedBuilderNeverPassesItsLimit(t *testing.T) {
	w := &limitedBuilder{max: 10}
	n, err := w.WriteString("12345678")
	require.NoError(t, err)
	require.Equal(t, 8, n)

	// Writes that do not fit write nothing.
	n, err = w.WriteString("abc")
	require.ErrorIs(t, err, errConvertedTooLarge)
	require.Zero(t, n)
	n, err = w.Write([]byte("abc"))
	require.ErrorIs(t, err, errConvertedTooLarge)
	require.Zero(t, n)
	require.Equal(t, "12345678", w.String())

	// Writes that fit exactly are kept.
	require.NoError(t, w.WriteByte('9'))
	n, err = w.Write([]byte("0"))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.ErrorIs(t, w.WriteByte('x'), errConvertedTooLarge)
	_, err = w.WriteString("")
	require.NoError(t, err)
	require.Equal(t, "1234567890", w.String())
}

// xhtml.Render returns the error of the first write that does not fit, so it
// stops there. What it wrote is the start of the whole HTML.
func TestRenderStopsAtTheLimit(t *testing.T) {
	doc, err := parseHTML(newsletterHTML(50<<10, false))
	require.NoError(t, err)
	var whole strings.Builder
	require.NoError(t, xhtml.Render(&whole, doc))

	for _, limit := range []int{0, 1, 100, 4096, whole.Len() / 2, whole.Len() - 1} {
		w := &limitedBuilder{max: limit}
		require.ErrorIs(t, xhtml.Render(w, doc), errConvertedTooLarge, limit)
		require.LessOrEqual(t, len(w.String()), limit)
		require.True(t, strings.HasPrefix(whole.String(), w.String()), limit)
	}
	w := &limitedBuilder{max: whole.Len()}
	require.NoError(t, xhtml.Render(w, doc))
	require.True(t, w.String() == whole.String())
}
