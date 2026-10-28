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
