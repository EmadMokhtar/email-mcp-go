package mime

import (
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

// measured runs f and returns how long it took and how many bytes it allocated
// (all the allocations, also those that were freed).
func measured(f func()) (time.Duration, uint64) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	started := time.Now()
	f()
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	return elapsed, after.TotalAlloc - before.TotalAlloc
}

// The memory that the parser may use for one document is bounded by the limit
// on the nodes (about 1,000,000 nodes: some 150 MiB), whatever the markup is.
const maxRefusedAllocation = 400 << 20

// The attack of the review: 20 KB of input make 1.6 million nodes with the
// parser. The parser stops at 1,000,000 nodes.
func TestAmplifyingHTMLIsRefused(t *testing.T) {
	in := amplifiedHTML(500, 4000) // about 21 KB
	require.Less(t, len(in), 25<<10)

	for name, convert := range map[string]func(string) error{
		"HTMLToText":  func(s string) error { _, err := HTMLToText(s); return err },
		"VisibleHTML": func(s string) error { _, err := VisibleHTML(s); return err },
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			elapsed, allocated := measured(func() { err = convert(in) })
			require.ErrorIs(t, err, ErrHTMLTooComplex)
			require.ErrorIs(t, err, xhtml.ErrTooManyNodes)
			t.Logf("%d bytes refused in %v, %d MiB allocated", len(in), elapsed, allocated>>20)
			require.Less(t, elapsed, 3*time.Second)
			require.Less(t, allocated, uint64(maxRefusedAllocation))
		})
	}
}

// The tree builder puts the elements that are opened in a table out of the
// table, and it keeps them in its list. The estimate from the tokens thought that
// the end of the table took them away.
func TestAmplifyingHTMLInATableIsRefused(t *testing.T) {
	recipes := map[string]string{
		"200 elements, 10,000 paragraphs (41 KB)":    tableAmplifiedHTML(200, 10_000),
		"500 elements, 276,001 paragraphs (1.1 MiB)": tableAmplifiedHTML(500, 276_001),
	}
	for name, in := range recipes {
		t.Run(name, func(t *testing.T) {
			var err error
			elapsed, allocated := measured(func() { _, err = HTMLToText(in) })
			require.ErrorIs(t, err, ErrHTMLTooComplex)
			t.Logf("%d bytes refused in %v, %d MiB allocated", len(in), elapsed, allocated>>20)
			require.Less(t, elapsed, 3*time.Second)
			require.Less(t, allocated, uint64(maxRefusedAllocation))

			_, err = VisibleHTML(in)
			require.ErrorIs(t, err, ErrHTMLTooComplex)
		})
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
			elapsed, allocated := measured(func() { _, err = HTMLToText(tc.markup) })
			if tc.refused {
				require.ErrorIs(t, err, ErrHTMLTooComplex)
				require.Less(t, allocated, uint64(maxRefusedAllocation))
			} else {
				require.NoError(t, err)
			}
			require.Less(t, elapsed, 3*time.Second)
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

// A realistic mail still converts, with a margin. The numbers are in the log.
func TestRealisticDocumentsAreConverted(t *testing.T) {
	for _, size := range []int{50 << 10, 500 << 10, 4 << 20} {
		in := newsletterHTML(size, false)
		doc, err := xhtml.ParseWithLimit(strings.NewReader(in), maxHTMLNodes)
		require.NoError(t, err)
		nodes := 0
		var count func(n *xhtml.Node)
		count = func(n *xhtml.Node) {
			nodes++
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				count(c)
			}
		}
		count(doc)

		var text string
		elapsed, allocated := measured(func() { text, err = HTMLToText(in) })
		require.NoError(t, err)
		require.Contains(t, text, "Read more about this (https://example.com/item/0")
		t.Logf("%7d bytes: %6d nodes (limit %d), HTMLToText %v, %d MiB allocated", len(in), nodes, maxHTMLNodes, elapsed, allocated>>20)
		require.Less(t, nodes, maxHTMLNodes/2, "at least a half of the limit is left")

		html, err := VisibleHTML(in)
		require.NoError(t, err)
		require.Contains(t, html, "Read more about this")
	}
}

// Before, a document with svg or noscript was converted only when it was small,
// because the estimate could not follow the parser into them.
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
