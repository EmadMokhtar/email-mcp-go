package mime

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// markupTags matches a tag. Tests use it to get the text out of the markup.
var markupTags = regexp.MustCompile(`<[^>]*>`)

// The hiding rules of HTMLToText have many cases (see hiddenVariants and
// droppedElements in html_test.go). VisibleHTML must leave out all of them.

func TestVisibleHTMLDropsEveryHiddenVariant(t *testing.T) {
	for _, tc := range hiddenVariants {
		t.Run(tc.name, func(t *testing.T) {
			out := VisibleHTML(`<p>visible</p>` + tc.markup)
			require.NotContains(t, out, "secret")
			require.Contains(t, out, "<p>visible</p>", "visible content must stay: %q", out)
		})
	}
}

func TestVisibleHTMLDropsEveryDroppedElement(t *testing.T) {
	for _, tc := range droppedElements {
		t.Run(tc.name, func(t *testing.T) {
			out := VisibleHTML(`<p>visible</p>` + tc.markup)
			require.NotContains(t, out, "secret")
			// An empty wrapper element can stay (for example the ruby around a
			// dropped rp). The text is only the visible text.
			require.Equal(t, "visible", markupTags.ReplaceAllString(out, ""))
		})
	}
}

func TestVisibleHTMLHiddenBody(t *testing.T) {
	require.Empty(t, VisibleHTML(`<html><body style="display:none">secret</body></html>`))
	require.Empty(t, VisibleHTML(`<div hidden><p>secret</p></div>`))
}

// SanitizeHTML alone keeps the content of hidden elements. This is the case
// that VisibleHTML is for.
func TestVisibleHTMLRemovesWhatSanitizeHTMLKeeps(t *testing.T) {
	in := `<style>.x{display:none}</style><p>Visible</p>` +
		`<div style="display:none">HIDDEN-STYLE</div>` +
		`<p hidden>HIDDEN-ATTRIBUTE</p>` +
		`<p aria-hidden="true">HIDDEN-ARIA</p>` +
		`<p class="x">HIDDEN-CLASS</p>` +
		`<span style="font-size:0">HIDDEN-SIZE</span>` +
		`<div style="visibility:hidden"><b>HIDDEN-VISIBILITY</b></div>`

	require.Contains(t, SanitizeHTML(in), "HIDDEN-STYLE", "the sanitizer alone keeps hidden content")
	out := VisibleHTML(in)
	require.NotContains(t, out, "HIDDEN")
	require.Contains(t, out, "<p>Visible</p>")
}

func TestVisibleHTMLStyleRuleAfterTheHiddenElement(t *testing.T) {
	// The rule comes last, as in the mail of an attacker who hopes that the
	// reader of the start does not see it.
	in := `<p class="x">HIDDEN-EARLY</p><p>Shown</p><style>.x{display:none}</style>`
	out := VisibleHTML(in)
	require.NotContains(t, out, "HIDDEN")
	require.Contains(t, out, "Shown")
}

func TestVisibleHTMLKeepsContentAndLinks(t *testing.T) {
	in := `<html><head><title>Title</title></head><body>` +
		`<h1>Heading</h1><p>Hello <b>bold</b> and <i>italic</i>, see <a href="https://x.test/a?b=1">the site</a> ` +
		`or <a href="mailto:me@x.test">mail me</a>.</p>` +
		`<ul><li>one</li><li>two</li></ul><table><tr><td>cell</td></tr></table><blockquote>quote</blockquote>` +
		`</body></html>`

	out := VisibleHTML(in)

	for _, want := range []string{
		"<h1>Heading</h1>", "<b>bold</b>", "<i>italic</i>",
		`<a href="https://x.test/a?b=1" rel="nofollow">the site</a>`,
		`<a href="mailto:me@x.test" rel="nofollow">mail me</a>`,
		"<ul><li>one</li><li>two</li></ul>", "<td>cell</td>", "<blockquote>quote</blockquote>",
	} {
		require.Contains(t, out, want)
	}
	require.NotContains(t, out, "Title", "the head is not content")
}

func TestVisibleHTMLKeepsVisibleStyles(t *testing.T) {
	// The same styles that HTMLToText keeps (see TestHTMLToTextKeepsVisibleStyles).
	for _, style := range []string{"color:red", "display:block", "visibility:visible", "opacity:0.5",
		"font-size:12px", "font-size:2px", "font:12px/1.5 Arial", ""} {
		require.Equal(t, "<p>shown</p>", VisibleHTML(`<p style="`+style+`">shown</p>`), "style %q", style)
	}
	require.Equal(t, "<p>shown</p>", VisibleHTML(`<p aria-hidden="false">shown</p>`))
}

func TestVisibleHTMLChildCanSetItsOwnSize(t *testing.T) {
	// Like HTMLToText: a child is another element and can show its text inside
	// a hidden parent. The text of the parent stays hidden.
	out := VisibleHTML(`<div style="font-size:0">HIDDEN-PARENT<span style="font-size:14px">shown</span></div>`)
	require.NotContains(t, out, "HIDDEN")
	require.Contains(t, out, "<span>shown</span>")
	require.Equal(t, "shown", HTMLToText(`<div style="font-size:0">HIDDEN-PARENT<span style="font-size:14px">shown</span></div>`))
}

func TestVisibleHTMLHiddenLinkLosesItsHref(t *testing.T) {
	out := VisibleHTML(`<a href="https://evil.test/track" style="font-size:0">x</a>` +
		`<a href="https://ok.test/">ok</a>` +
		`<a href="https://evil.test/two" style="visibility:hidden"><span style="visibility:visible">seen</span></a>`)
	require.NotContains(t, out, "evil.test")
	require.Contains(t, out, `<a href="https://ok.test/" rel="nofollow">ok</a>`)
	require.Contains(t, out, "seen")
}

// The result must have all the properties of SanitizeHTML.
func TestVisibleHTMLIsSanitized(t *testing.T) {
	in := `<p onclick="steal()" style="color:red" class="c" id="i">text</p>` +
		`<script>alert(1)</script><style>p{color:red}</style>` +
		`<img src="https://t.test/p.gif" alt="a"><svg><a href="https://x.test">s</a></svg>` +
		`<iframe src="https://x.test"></iframe><object data="x"></object><form action="https://x.test"><input name="n"></form>` +
		`<a href="javascript:alert(1)">js</a><a href="data:text/html,x">data</a><a href="/relative">rel</a>` +
		`<a href="HTTPS://X.test/up">up</a><a href="ftp://x.test/f">ftp</a>`

	out := VisibleHTML(in)

	for _, bad := range []string{"<img", "<script", "<style", "<iframe", "<object", "<svg", "<form", "<input",
		"style=", "onclick", "javascript:", "data:", "ftp:", `href="/relative"`, "alert("} {
		require.NotContains(t, strings.ToLower(out), bad)
	}
	hrefs := regexp.MustCompile(`href="([^"]*)"`).FindAllStringSubmatch(out, -1)
	for _, h := range hrefs {
		require.Regexp(t, `(?i)^(https?://|mailto:)`, h[1])
	}
}

// html.Render writes the text of noscript, xmp and plaintext as it is. A text
// that holds escaped markup must not come out as markup.
func TestVisibleHTMLRawTextElementsDoNotTurnTextIntoMarkup(t *testing.T) {
	inputs := []string{
		`<noscript>&lt;/noscript&gt;&lt;img src=x onerror=alert(1)&gt;&lt;script&gt;alert(1)&lt;/script&gt;</noscript>`,
		`<xmp>&lt;/xmp&gt;<img src=x onerror=alert(1)></xmp><p>after</p>`,
		`<plaintext><img src=x onerror=alert(1)><script>alert(1)</script>`,
	}
	for _, in := range inputs {
		out := strings.ToLower(VisibleHTML(in))
		require.NotContains(t, out, "<img")
		require.NotContains(t, out, "<script")
		require.NotContains(t, out, "onerror=alert(1)>")
	}
	// The visible content of noscript stays: HTMLToText shows it too.
	require.Contains(t, VisibleHTML(`<noscript><p>please enable it</p></noscript>`), "please enable it")
	require.Equal(t, "please enable it", HTMLToText(`<noscript><p>please enable it</p></noscript>`))
}

func TestVisibleHTMLNeverShowsWhatHTMLToTextHides(t *testing.T) {
	// The text of the result is the text of the input: HTMLToText of the two
	// agree on a page with several mechanisms.
	in := `<style>.h{display:none} .t{font-size:0}</style>` +
		`<h2>Title</h2><p>One <span class="t">HIDDEN-A</span>two</p>` +
		`<div class="h">HIDDEN-B</div><ul><li>a</li><li hidden>HIDDEN-C</li><li>b</li></ul>` +
		`<p style="opacity:0">HIDDEN-D</p><p>The end</p>`
	want := HTMLToText(in)
	require.NotContains(t, want, "HIDDEN")
	require.Equal(t, want, HTMLToText(VisibleHTML(in)))
}

func TestVisibleHTMLEdgeCases(t *testing.T) {
	require.Empty(t, VisibleHTML(""))
	require.Empty(t, VisibleHTML("   \n"))
	require.Equal(t, "plain text &amp; more", VisibleHTML("plain text & more"))
	require.Equal(t, "<p>x</p>", VisibleHTML("<!-- c --><!DOCTYPE html><p>x</p>"))
	// Broken markup does not fail.
	require.Contains(t, VisibleHTML(`<p>open <b>bold <i>both</p> after`), "both")
	require.Contains(t, VisibleHTML("<p>bad \xff\xfe bytes</p>"), "bad")
}

func TestVisibleHTMLDeeplyNested(t *testing.T) {
	nest := func(depth int) string {
		return strings.Repeat("<div>", depth) + "deep" + strings.Repeat("</div>", depth)
	}
	require.Contains(t, VisibleHTML(nest(400)), "deep")
	// The parser refuses documents nested more than 512 elements deep. As
	// HTMLToText, VisibleHTML returns nothing for them and does not crash.
	require.Equal(t, "", HTMLToText(nest(100_000)))
	require.Equal(t, "", VisibleHTML(nest(100_000)))
}

func FuzzVisibleHTML(f *testing.F) {
	for _, s := range []string{
		``,
		`<p>hi</p>`,
		`<style>.x{display:none}</style><p class="x">secret</p>`,
		`<noscript><p>x</p></noscript><xmp><b></xmp>`,
		`<div style="font-size:0"><a href="https://x.test">y</a></div>`,
		`<a href="javascript:x">z</a><img src=x onerror=y>`,
		`<noscript>&lt;/noscript&gt;&lt;script&gt;x&lt;/script&gt;</noscript>`,
		`<plaintext><script>x</script>`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, markup string) {
		out := strings.ToLower(VisibleHTML(markup))
		// Text is escaped in the output, so a tag name that follows "<" is a
		// real element.
		for _, bad := range []string{"<script", "<style", "<img", "<iframe", "<object", "<svg", `href="javascript:`} {
			if strings.Contains(out, bad) {
				t.Fatalf("the output has %q: %q", bad, out)
			}
		}
	})
}
