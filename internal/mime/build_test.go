package mime

import (
	"bytes"
	stdmime "mime"
	netmail "net/mail"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

var buildNow = time.Date(2026, time.October, 28, 9, 30, 0, 0, time.UTC)

// newContext is a BuildContext for a user called Me, with no original message.
func newContext() BuildContext {
	return BuildContext{
		From:            mail.Address{Name: "Me", Email: "me@x"},
		Self:            []string{"me@x"},
		Now:             buildNow,
		MessageIDDomain: "x.test",
	}
}

// originalMessage is a message that Alice sent to Me and Bob, with Carol in Cc.
func originalMessage() *mail.Message {
	return &mail.Message{
		Summary: mail.Summary{
			Ref:     "o1",
			From:    mail.Address{Name: "Alice", Email: "alice@x"},
			To:      []mail.Address{{Email: "me@x"}, {Email: "bob@x"}},
			Subject: "Plans",
			Date:    time.Date(2026, time.October, 27, 8, 0, 0, 0, time.UTC),
		},
		Cc:         []mail.Address{{Email: "carol@x"}},
		MessageID:  "<m2@x>",
		References: []string{"<m0@x>", "<m1@x>"},
		Text:       "original text",
		HTML:       "<p>original html</p>",
	}
}

func replyTo(all bool) *mail.ReplyRef { return &mail.ReplyRef{Ref: "o1", All: all} }

func forwardOf() *mail.Ref {
	r := mail.Ref("o1")
	return &r
}

func mustBuild(t *testing.T, c mail.Compose, bc BuildContext) *mail.RawMessage {
	t.Helper()
	raw, err := Build(c, bc)
	require.NoError(t, err)
	require.NotNil(t, raw)
	return raw
}

// headerOf reads the header of a built message with the standard library.
func headerOf(t *testing.T, data []byte) netmail.Header {
	t.Helper()
	m, err := netmail.ReadMessage(bytes.NewReader(data))
	require.NoError(t, err)
	return m.Header
}

// emails returns the addresses of one address header, or nil.
func emails(t *testing.T, h netmail.Header, key string) []string {
	t.Helper()
	if h.Get(key) == "" {
		return nil
	}
	list, err := h.AddressList(key)
	require.NoError(t, err)
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Address)
	}
	return out
}

func parsedOf(t *testing.T, data []byte) *Parsed {
	t.Helper()
	p, err := ParseMessage(bytes.NewReader(data), 0)
	require.NoError(t, err)
	return p
}

// headerBlock returns the header of a message, the text before the first
// empty line.
func headerBlock(data []byte) string {
	s := string(data)
	if i := strings.Index(s, "\r\n\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

func mediaTypeOf(t *testing.T, h netmail.Header) string {
	t.Helper()
	mt, _, err := stdmime.ParseMediaType(h.Get("Content-Type"))
	require.NoError(t, err)
	return mt
}

func TestReplyAllKeepsRecipientsExceptSelf(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(true), Body: "ok"}, bc)

	h := headerOf(t, raw.Data)
	require.Equal(t, []string{"alice@x"}, emails(t, h, "To"))
	require.Equal(t, []string{"bob@x", "carol@x"}, emails(t, h, "Cc"))
	require.Equal(t, []string{"alice@x", "bob@x", "carol@x"}, raw.Recipients)
	require.NotContains(t, raw.Recipients, "me@x")
	require.Equal(t, "me@x", raw.From)
}

func TestReplyAllRemovesSelfWithoutRegardToCase(t *testing.T) {
	bc := newContext()
	bc.Self = []string{"ME@X"}
	bc.Original = originalMessage()
	bc.Original.To = []mail.Address{{Email: "Me@x"}, {Email: "bob@x"}}
	raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(true)}, bc)

	require.Equal(t, []string{"alice@x", "bob@x", "carol@x"}, raw.Recipients)
	require.Equal(t, []string{"bob@x", "carol@x"}, emails(t, headerOf(t, raw.Data), "Cc"))
}

func TestReplyGoesToReplyToThenFrom(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()

	t.Run("from", func(t *testing.T) {
		raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(false), Body: "ok"}, bc)
		h := headerOf(t, raw.Data)
		require.Equal(t, []string{"alice@x"}, emails(t, h, "To"))
		require.Nil(t, emails(t, h, "Cc"), "a plain reply does not copy the original To and Cc")
		require.Equal(t, []string{"alice@x"}, raw.Recipients)
	})

	t.Run("reply-to header", func(t *testing.T) {
		withReplyTo := originalMessage()
		withReplyTo.ReplyTo = []mail.Address{{Email: "list@x"}, {Email: "owner@x"}}
		bc := bc
		bc.Original = withReplyTo
		raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(false)}, bc)
		require.Equal(t, []string{"list@x", "owner@x"}, emails(t, headerOf(t, raw.Data), "To"))
	})

	t.Run("extra recipients come after", func(t *testing.T) {
		raw := mustBuild(t, mail.Compose{
			ReplyTo: replyTo(false),
			To:      []string{"dave@x"},
			Cc:      []string{"erin@x"},
			Bcc:     []string{"frank@x"},
		}, bc)
		h := headerOf(t, raw.Data)
		require.Equal(t, []string{"alice@x", "dave@x"}, emails(t, h, "To"))
		require.Equal(t, []string{"erin@x"}, emails(t, h, "Cc"))
		require.Equal(t, []string{"alice@x", "dave@x", "erin@x", "frank@x"}, raw.Recipients)
	})
}

func TestReplySubject(t *testing.T) {
	cases := []struct {
		name     string
		original string
		given    string
		want     string
	}{
		{"adds a prefix", "Plans", "", "Re: Plans"},
		{"keeps a prefix", "Re: Plans", "", "Re: Plans"},
		{"keeps an upper case prefix", "RE: Plans", "", "RE: Plans"},
		{"keeps a prefix after spaces", "  re: Plans", "", "re: Plans"},
		{"the caller's subject is used", "Plans", "Different", "Different"},
		{"no original subject", "", "", "Re:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bc := newContext()
			bc.Original = originalMessage()
			bc.Original.Subject = tc.original
			raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(false), Subject: tc.given}, bc)
			require.Equal(t, tc.want, headerOf(t, raw.Data).Get("Subject"))
		})
	}
}

func TestReplySetsThreadingHeaders(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(false), Body: "ok"}, bc)

	h := headerOf(t, raw.Data)
	require.Equal(t, "<m2@x>", h.Get("In-Reply-To"))
	require.Equal(t, "<m0@x> <m1@x> <m2@x>", h.Get("References"))
}

func TestReplyThreadingAcceptsIDsWithoutBrackets(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	bc.Original.MessageID = "m2@x"
	bc.Original.References = []string{"m0@x", " <m1@x> ", ""}
	raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(false)}, bc)

	h := headerOf(t, raw.Data)
	require.Equal(t, "<m2@x>", h.Get("In-Reply-To"))
	require.Equal(t, "<m0@x> <m1@x> <m2@x>", h.Get("References"))
}

func TestReplyToMessageWithoutIDHasNoThreadingHeaders(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	bc.Original.MessageID = ""
	raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(false)}, bc)

	h := headerOf(t, raw.Data)
	require.Empty(t, h.Get("In-Reply-To"))
	require.Equal(t, "<m0@x> <m1@x>", h.Get("References"))
}

func TestNewMessageHasNoThreadingHeaders(t *testing.T) {
	raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, Subject: "Hi"}, newContext())
	h := headerOf(t, raw.Data)
	require.Empty(t, h.Get("In-Reply-To"))
	require.Empty(t, h.Get("References"))
}

func TestReplyAndForwardNeedTheOriginal(t *testing.T) {
	_, err := Build(mail.Compose{ReplyTo: replyTo(false)}, newContext())
	require.ErrorIs(t, err, ErrNoOriginal)

	_, err = Build(mail.Compose{ForwardOf: forwardOf(), To: []string{"bob@x"}}, newContext())
	require.ErrorIs(t, err, ErrNoOriginal)
}

func TestReplyAndForwardTogetherAreRejected(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	_, err := Build(mail.Compose{ReplyTo: replyTo(false), ForwardOf: forwardOf(), To: []string{"bob@x"}}, bc)
	require.ErrorIs(t, err, ErrReplyAndForward)
}

func TestForwardSubjectAndRecipients(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	raw := mustBuild(t, mail.Compose{ForwardOf: forwardOf(), To: []string{"dave@x"}, Body: "fyi"}, bc)

	h := headerOf(t, raw.Data)
	require.Equal(t, "Fwd: Plans", h.Get("Subject"))
	require.Equal(t, []string{"dave@x"}, emails(t, h, "To"))
	require.Equal(t, []string{"dave@x"}, raw.Recipients, "a forward never goes to the original sender or recipients by itself")
	require.Empty(t, h.Get("In-Reply-To"))

	raw = mustBuild(t, mail.Compose{ForwardOf: forwardOf(), To: []string{"dave@x"}, Subject: "Look"}, bc)
	require.Equal(t, "Look", headerOf(t, raw.Data).Get("Subject"))
}

func TestForwardPlainPreamble(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	raw := mustBuild(t, mail.Compose{ForwardOf: forwardOf(), To: []string{"dave@x"}, Body: "fyi"}, bc)

	want := strings.Join([]string{
		"fyi",
		"",
		"---------- Forwarded message ----------",
		"From: Alice <alice@x>",
		"Date: Tue, 27 Oct 2026 08:00:00 +0000",
		"Subject: Plans",
		"To: me@x, bob@x",
		"",
		"original text",
	}, "\n")
	got := strings.ReplaceAll(parsedOf(t, raw.Data).Text, "\r\n", "\n")
	require.Equal(t, want, got)
	require.Empty(t, parsedOf(t, raw.Data).HTML, "no HTML part unless the caller gave HTML")
}

func TestForwardHTMLPreambleEscaped(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	bc.Original.Subject = "<script>"
	bc.Original.From = mail.Address{Name: `A & "B"`, Email: "alice@x"}
	bc.Original.Text = "1 < 2 && <b>bold</b>"
	bc.Original.HTML = `<img src=x onerror="alert(1)">raw original html`
	raw := mustBuild(t, mail.Compose{
		ForwardOf: forwardOf(),
		To:        []string{"dave@x"},
		HTMLBody:  "<p>fyi</p>",
	}, bc)

	html := parsedOf(t, raw.Data).HTML
	require.True(t, strings.HasPrefix(html, "<p>fyi</p>"), "the caller's HTML comes first")
	require.Contains(t, html, "&lt;script&gt;")
	require.NotContains(t, html, "<script>")
	require.Contains(t, html, "A &amp; &#34;B&#34;")
	require.Contains(t, html, "<blockquote><pre>1 &lt; 2 &amp;&amp; &lt;b&gt;bold&lt;/b&gt;</pre></blockquote>")
	require.NotContains(t, html, "onerror", "the raw HTML of the original is never used")
	require.NotContains(t, html, "raw original html")
}

func TestForwardOfHTMLOnlyMessageUsesItsTextForm(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	bc.Original.Text = ""
	bc.Original.HTML = `<p>Hello</p><div style="display:none">hidden</div><script>alert(1)</script>`
	raw := mustBuild(t, mail.Compose{ForwardOf: forwardOf(), To: []string{"dave@x"}}, bc)

	text := parsedOf(t, raw.Data).Text
	require.Contains(t, text, "Hello")
	require.NotContains(t, text, "hidden")
	require.NotContains(t, text, "alert")
}

func TestForwardAttachmentsOnlyWhenAsked(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	bc.OriginalAttachments = []mail.Attachment{{
		AttachmentInfo: mail.AttachmentInfo{ID: "2", Filename: "plan.pdf", ContentType: "application/pdf"},
		Data:           []byte("%PDF-1.4 fake"),
	}}
	own := mail.OutgoingAttachment{Filename: "own.txt", ContentType: "text/plain", Data: []byte("mine")}

	without := mustBuild(t, mail.Compose{ForwardOf: forwardOf(), To: []string{"dave@x"}, Attachments: []mail.OutgoingAttachment{own}}, bc)
	names := attachmentNames(parsedOf(t, without.Data))
	require.Equal(t, []string{"own.txt"}, names)

	with := mustBuild(t, mail.Compose{
		ForwardOf: forwardOf(), To: []string{"dave@x"}, IncludeOriginalAttachments: true,
		Attachments: []mail.OutgoingAttachment{own},
	}, bc)
	require.Equal(t, []string{"plan.pdf", "own.txt"}, attachmentNames(parsedOf(t, with.Data)))

	// A reply never carries the attachments of the original.
	reply := mustBuild(t, mail.Compose{ReplyTo: replyTo(false), IncludeOriginalAttachments: true}, bc)
	require.Empty(t, parsedOf(t, reply.Data).Attachments)
}

func attachmentNames(p *Parsed) []string {
	var names []string
	for _, a := range p.Attachments {
		names = append(names, a.Filename)
	}
	return names
}

func TestHeaderInjectionRejected(t *testing.T) {
	_, err := Build(mail.Compose{To: []string{"bob@x"}, Subject: "hi\r\nBcc: evil@x"}, newContext())
	require.ErrorIs(t, err, ErrHeaderInjection)

	for _, subject := range []string{"hi\nBcc: evil@x", "hi\rBcc: evil@x", "hi\x00there"} {
		_, err := Build(mail.Compose{To: []string{"bob@x"}, Subject: subject}, newContext())
		require.ErrorIs(t, err, ErrHeaderInjection, "subject %q", subject)
	}
}

func TestHeaderInjectionRejectedInEveryHeaderValue(t *testing.T) {
	const bad = "x\r\nBcc: evil@x"
	pdf := func(name, contentType string) []mail.OutgoingAttachment {
		return []mail.OutgoingAttachment{{Filename: name, ContentType: contentType, Data: []byte("data")}}
	}

	t.Run("sender name", func(t *testing.T) {
		bc := newContext()
		bc.From.Name = bad
		_, err := Build(mail.Compose{To: []string{"bob@x"}}, bc)
		require.ErrorIs(t, err, ErrHeaderInjection)
	})
	t.Run("sender address", func(t *testing.T) {
		bc := newContext()
		bc.From.Email = "me@x\r\nBcc: evil@x"
		_, err := Build(mail.Compose{To: []string{"bob@x"}}, bc)
		require.Error(t, err)
	})
	t.Run("message id domain", func(t *testing.T) {
		bc := newContext()
		bc.MessageIDDomain = "x.test>\r\nBcc: evil@x"
		_, err := Build(mail.Compose{To: []string{"bob@x"}}, bc)
		require.Error(t, err)
	})
	t.Run("attachment file name", func(t *testing.T) {
		_, err := Build(mail.Compose{To: []string{"bob@x"}, Attachments: pdf(bad, "text/plain")}, newContext())
		require.ErrorIs(t, err, ErrHeaderInjection)
	})
	t.Run("attachment content type", func(t *testing.T) {
		_, err := Build(mail.Compose{To: []string{"bob@x"}, Attachments: pdf("a.txt", "text/plain\r\nBcc: evil@x")}, newContext())
		require.ErrorIs(t, err, ErrHeaderInjection)
	})
	t.Run("recipient", func(t *testing.T) {
		for _, entry := range []string{"bob@x\r\nBcc: evil@x", "\"Bob\r\nBcc: evil@x\" <bob@x>", "bob@x\x00"} {
			raw, err := Build(mail.Compose{To: []string{entry}}, newContext())
			require.Error(t, err, "recipient %q", entry)
			require.Nil(t, raw)
		}
	})
	t.Run("original subject of a reply", func(t *testing.T) {
		bc := newContext()
		bc.Original = originalMessage()
		bc.Original.Subject = bad
		_, err := Build(mail.Compose{ReplyTo: replyTo(false)}, bc)
		require.ErrorIs(t, err, ErrHeaderInjection)

		// The caller can still reply with a subject of their own.
		raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(false), Subject: "Re: safe"}, bc)
		require.Equal(t, "Re: safe", headerOf(t, raw.Data).Get("Subject"))
	})
	t.Run("original message id", func(t *testing.T) {
		bc := newContext()
		bc.Original = originalMessage()
		bc.Original.MessageID = "<m2@x>\r\nBcc: evil@x"
		_, err := Build(mail.Compose{ReplyTo: replyTo(false)}, bc)
		require.ErrorIs(t, err, ErrHeaderInjection)
	})
	t.Run("original reference", func(t *testing.T) {
		bc := newContext()
		bc.Original = originalMessage()
		bc.Original.References = []string{"<m0@x>\nBcc: evil@x"}
		_, err := Build(mail.Compose{ReplyTo: replyTo(false)}, bc)
		require.ErrorIs(t, err, ErrHeaderInjection)
	})
	t.Run("original sender name", func(t *testing.T) {
		bc := newContext()
		bc.Original = originalMessage()
		bc.Original.From.Name = bad
		_, err := Build(mail.Compose{ReplyTo: replyTo(false)}, bc)
		require.ErrorIs(t, err, ErrHeaderInjection)
	})
}

func TestBccNotInHeaders(t *testing.T) {
	raw := mustBuild(t, mail.Compose{
		To:      []string{"bob@x"},
		Cc:      []string{"carol@x"},
		Bcc:     []string{"secret@x", "Hidden Person <hidden@x>"},
		Subject: "Hi",
		Body:    "hello",
	}, newContext())

	require.Equal(t, []string{"bob@x", "carol@x", "secret@x", "hidden@x"}, raw.Recipients)
	require.NotContains(t, strings.ToLower(headerBlock(raw.Data)), "bcc")
	require.NotContains(t, string(raw.Data), "secret@x")
	require.NotContains(t, string(raw.Data), "hidden@x")
	require.NotContains(t, string(raw.Data), "Hidden Person")
	require.Empty(t, headerOf(t, raw.Data).Get("Bcc"))
}

func TestBccStaysHiddenInReplies(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	raw := mustBuild(t, mail.Compose{ReplyTo: replyTo(true), Bcc: []string{"secret@x"}}, bc)

	require.Contains(t, raw.Recipients, "secret@x")
	require.NotContains(t, string(raw.Data), "secret@x")
}

func TestDraftKeepsBccAndFromDraftStripsIt(t *testing.T) {
	bc := newContext()
	bc.ForDraft = true
	draft := mustBuild(t, mail.Compose{
		To:      []string{"bob@x"},
		Cc:      []string{"carol@x"},
		Bcc:     []string{"secret@x"},
		Subject: "Hi",
		Body:    "hello",
	}, bc)

	require.Equal(t, []string{"secret@x"}, emails(t, headerOf(t, draft.Data), "Bcc"), "a draft keeps the Bcc header")

	sendable, err := FromDraft(draft.Data)
	require.NoError(t, err)
	require.Equal(t, "me@x", sendable.From)
	require.Equal(t, []string{"bob@x", "carol@x", "secret@x"}, sendable.Recipients)
	require.NotContains(t, strings.ToLower(headerBlock(sendable.Data)), "bcc")
	require.NotContains(t, string(sendable.Data), "secret@x")

	// Only the Bcc field is gone. Every other byte is the same.
	want := strings.Replace(string(draft.Data), "Bcc: secret@x\r\n", "", 1)
	require.Equal(t, want, string(sendable.Data))
}

func TestDraftAndSentMessageAreTheSameExceptBcc(t *testing.T) {
	c := mail.Compose{To: []string{"bob@x"}, Bcc: []string{"secret@x"}, Subject: "Hi", Body: "hello"}
	bc := newContext()
	sent := mustBuild(t, c, bc)
	bc.ForDraft = true
	draft := mustBuild(t, c, bc)

	// The Message-ID is random, so compare the rest.
	idPattern := regexp.MustCompile(`(?m)^Message-Id: .*\r\n`)
	stripped := func(b []byte) string { return idPattern.ReplaceAllString(string(b), "") }
	fromDraft, err := FromDraft(draft.Data)
	require.NoError(t, err)
	require.Equal(t, stripped(sent.Data), stripped(fromDraft.Data))
	require.Equal(t, sent.Recipients, fromDraft.Recipients)
}

func TestRecipientsAreDeduplicated(t *testing.T) {
	raw := mustBuild(t, mail.Compose{
		To:  []string{"Bob <bob@x>", "BOB@x", "dave@x"},
		Cc:  []string{"bob@X", "Carol <carol@x>", "carol@x"},
		Bcc: []string{"DAVE@x", "frank@x", "Carol@x", "frank@x"},
	}, newContext())

	h := headerOf(t, raw.Data)
	require.Equal(t, []string{"bob@x", "dave@x"}, emails(t, h, "To"))
	require.Equal(t, []string{"carol@x"}, emails(t, h, "Cc"))
	require.Equal(t, []string{"bob@x", "dave@x", "carol@x", "frank@x"}, raw.Recipients)
	require.Contains(t, string(raw.Data), "Bob", "the first display name is kept")
}

func TestInvalidRecipients(t *testing.T) {
	cases := []struct {
		name string
		c    mail.Compose
		part string
	}{
		{"to", mail.Compose{To: []string{"bob@x", "not an address"}}, "to[1]"},
		{"cc", mail.Compose{To: []string{"bob@x"}, Cc: []string{"carol@x", "", "x"}}, "cc[1]"},
		{"bcc", mail.Compose{To: []string{"bob@x"}, Bcc: []string{"a@x, b@x"}}, "bcc[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Build(tc.c, newContext())
			require.ErrorIs(t, err, ErrInvalidAddress)
			require.Contains(t, err.Error(), tc.part)
			require.NotContains(t, err.Error(), "not an address", "the address text stays out of the error")
			require.Nil(t, raw)
		})
	}

	t.Run("sender", func(t *testing.T) {
		bc := newContext()
		bc.From = mail.Address{Name: "Me"}
		_, err := Build(mail.Compose{To: []string{"bob@x"}}, bc)
		require.ErrorIs(t, err, ErrInvalidAddress)
	})
	t.Run("address of the original", func(t *testing.T) {
		bc := newContext()
		bc.Original = originalMessage()
		bc.Original.From = mail.Address{Email: "not an address"}
		_, err := Build(mail.Compose{ReplyTo: replyTo(false)}, bc)
		require.ErrorIs(t, err, ErrInvalidAddress)
	})
}

func TestNoRecipients(t *testing.T) {
	_, err := Build(mail.Compose{Subject: "Hi", Body: "hello"}, newContext())
	require.ErrorIs(t, err, ErrNoRecipients)

	// Everything in the reply is Self, so nobody is left.
	bc := newContext()
	bc.Original = originalMessage()
	bc.Original.From = mail.Address{Email: "me@x"}
	bc.Original.To = []mail.Address{{Email: "me@x"}}
	bc.Original.Cc = nil
	_, err = Build(mail.Compose{ReplyTo: replyTo(true)}, bc)
	require.ErrorIs(t, err, ErrNoRecipients)
}

func TestTextMessage(t *testing.T) {
	raw := mustBuild(t, mail.Compose{
		To:      []string{"Bob <bob@x>"},
		Subject: "Caf\U000000E9 \U00004F60\U00006211",
		Body:    "line one\nline two \U00004F60\U00006211\n",
	}, newContext())

	h := headerOf(t, raw.Data)
	require.Equal(t, "text/plain", mediaTypeOf(t, h))
	require.Equal(t, "quoted-printable", h.Get("Content-Transfer-Encoding"))
	require.Equal(t, "1.0", h.Get("Mime-Version"))
	require.Equal(t, "Wed, 28 Oct 2026 09:30:00 +0000", h.Get("Date"))
	require.Equal(t, `"Me" <me@x>`, h.Get("From"))
	require.Equal(t, []string{"bob@x"}, emails(t, h, "To"))
	require.Empty(t, h.Get("Cc"))
	require.Equal(t, "Caf\U000000E9 \U00004F60\U00006211", DecodeHeaderValue(h.Get("Subject")))
	require.NotContains(t, h.Get("Subject"), "\U000000E9", "a non-ASCII subject is encoded")

	p := parsedOf(t, raw.Data)
	require.Equal(t, "line one\r\nline two \U00004F60\U00006211\r\n", p.Text)
	require.Empty(t, p.HTML)
	require.Empty(t, p.Attachments)
}

func TestNonASCIINamesAreEncoded(t *testing.T) {
	bc := newContext()
	bc.From.Name = "J\U000000FCrgen M\U000000FCller"
	raw := mustBuild(t, mail.Compose{To: []string{"\"\U00004F60\U00006211\" <bob@x>"}}, bc)

	require.True(t, isASCII(headerBlock(raw.Data)), "the header is ASCII")
	h := headerOf(t, raw.Data)
	require.Equal(t, "J\U000000FCrgen M\U000000FCller", DecodeHeaderValue(strings.Trim(strings.SplitN(h.Get("From"), " <", 2)[0], `"`)))
	require.Equal(t, "\U00004F60\U00006211", DecodeHeaderValue(strings.Trim(strings.SplitN(h.Get("To"), " <", 2)[0], `"`)))
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func TestLineEndingsAreCRLF(t *testing.T) {
	raw := mustBuild(t, mail.Compose{
		To:       []string{"bob@x"},
		Body:     "unix\nold mac\rwindows\r\nend",
		HTMLBody: "<p>a</p>\n<p>b</p>",
		Attachments: []mail.OutgoingAttachment{
			{Filename: "a.bin", ContentType: "application/octet-stream", Data: bytes.Repeat([]byte{1, 2, 3}, 100)},
		},
	}, newContext())

	rest := strings.ReplaceAll(string(raw.Data), "\r\n", "")
	require.NotContains(t, rest, "\n", "a bare line feed")
	require.NotContains(t, rest, "\r", "a bare carriage return")
}

func TestHTMLBodyBuildsAlternative(t *testing.T) {
	raw := mustBuild(t, mail.Compose{
		To:       []string{"bob@x"},
		Body:     "plain",
		HTMLBody: "<p>rich \U00004F60</p>",
	}, newContext())

	require.Equal(t, "multipart/alternative", mediaTypeOf(t, headerOf(t, raw.Data)))
	p := parsedOf(t, raw.Data)
	require.Equal(t, "plain", p.Text)
	require.Equal(t, "<p>rich \U00004F60</p>", p.HTML)
	require.True(t, strings.Index(string(raw.Data), "text/plain") < strings.Index(string(raw.Data), "text/html"),
		"the HTML part comes last, so mail programs prefer it")
}

func TestHTMLOnlyBodyGetsTextForm(t *testing.T) {
	raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, HTMLBody: "<p>Hello</p><script>alert(1)</script>"}, newContext())

	p := parsedOf(t, raw.Data)
	require.Equal(t, "Hello", strings.TrimSpace(p.Text))
	require.Equal(t, "<p>Hello</p><script>alert(1)</script>", p.HTML)
}

func TestAttachmentsBuildMixedMessage(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 40)...)
	raw := mustBuild(t, mail.Compose{
		To:       []string{"bob@x"},
		Body:     "see attached",
		HTMLBody: "<p>see attached</p>",
		Attachments: []mail.OutgoingAttachment{
			{Filename: "pic.png", Data: png},
			{Filename: "notes \U00004F60.txt", ContentType: "text/plain; charset=utf-8", Data: []byte("some notes\nhere")},
			{Filename: "report.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4")},
		},
	}, newContext())

	require.Equal(t, "multipart/mixed", mediaTypeOf(t, headerOf(t, raw.Data)))
	p := parsedOf(t, raw.Data)
	require.Equal(t, "see attached", p.Text)
	require.Equal(t, "<p>see attached</p>", p.HTML)
	require.Len(t, p.Attachments, 3)

	require.Equal(t, "pic.png", p.Attachments[0].Filename)
	require.Equal(t, "image/png", p.Attachments[0].ContentType, "an empty content type is detected from the data")
	require.EqualValues(t, len(png), p.Attachments[0].Size)

	require.Equal(t, "notes \U00004F60.txt", p.Attachments[1].Filename)
	require.Equal(t, "text/plain", p.Attachments[1].ContentType)
	require.EqualValues(t, len("some notes\nhere"), p.Attachments[1].Size)

	require.Equal(t, "application/pdf", p.Attachments[2].ContentType)
	require.Contains(t, string(raw.Data), "Content-Disposition: attachment")
	require.Contains(t, string(raw.Data), "Content-Transfer-Encoding: base64")
}

func TestAttachmentWithoutBodyHasEmptyText(t *testing.T) {
	raw := mustBuild(t, mail.Compose{
		To:          []string{"bob@x"},
		Attachments: []mail.OutgoingAttachment{{Filename: "a.bin", Data: []byte{0, 1, 2, 3}}},
	}, newContext())

	p := parsedOf(t, raw.Data)
	require.Empty(t, p.Text)
	require.Len(t, p.Attachments, 1)
	require.Equal(t, "application/octet-stream", p.Attachments[0].ContentType)
}

func TestAttachmentWithoutNameGetsOne(t *testing.T) {
	raw := mustBuild(t, mail.Compose{
		To:          []string{"bob@x"},
		Attachments: []mail.OutgoingAttachment{{Data: []byte{0, 1, 2, 3}}},
	}, newContext())
	require.Equal(t, []string{"attachment"}, attachmentNames(parsedOf(t, raw.Data)))
}

func TestInvalidAttachmentContentType(t *testing.T) {
	for _, contentType := range []string{
		"pdf",
		"multipart/mixed; boundary=x",
		"text/plain; charset=iso-8859-1",
		"text/plain; charset",
		"text/ plain",
	} {
		_, err := Build(mail.Compose{
			To:          []string{"bob@x"},
			Attachments: []mail.OutgoingAttachment{{Filename: "a.txt", ContentType: contentType, Data: []byte("x")}},
		}, newContext())
		require.ErrorIs(t, err, ErrInvalidContentType, "content type %q", contentType)
	}
}

func TestAttachmentsOverLimit(t *testing.T) {
	const limit = 10 * 1024 * 1024
	attachment := func(n int) mail.OutgoingAttachment {
		return mail.OutgoingAttachment{Filename: "big.bin", ContentType: "application/octet-stream", Data: make([]byte, n)}
	}

	t.Run("one byte over", func(t *testing.T) {
		raw, err := Build(mail.Compose{To: []string{"bob@x"}, Attachments: []mail.OutgoingAttachment{attachment(limit + 1)}}, newContext())
		require.ErrorIs(t, err, mail.ErrTooLarge)
		require.Nil(t, raw)
	})
	t.Run("exactly at the limit", func(t *testing.T) {
		raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}, Attachments: []mail.OutgoingAttachment{attachment(limit)}}, newContext())
		require.Greater(t, len(raw.Data), limit)
	})
	t.Run("the total counts", func(t *testing.T) {
		_, err := Build(mail.Compose{
			To:          []string{"bob@x"},
			Attachments: []mail.OutgoingAttachment{attachment(limit / 2), attachment(limit/2 + 1)},
		}, newContext())
		require.ErrorIs(t, err, mail.ErrTooLarge)
	})
	t.Run("included originals count", func(t *testing.T) {
		bc := newContext()
		bc.Original = originalMessage()
		bc.OriginalAttachments = []mail.Attachment{{
			AttachmentInfo: mail.AttachmentInfo{Filename: "o.bin", ContentType: "application/octet-stream"},
			Data:           make([]byte, limit/2+1),
		}}
		c := mail.Compose{
			ForwardOf:   forwardOf(),
			To:          []string{"bob@x"},
			Attachments: []mail.OutgoingAttachment{attachment(limit / 2)},
		}
		_, err := Build(c, bc)
		require.NoError(t, err, "originals are ignored unless they are included")

		c.IncludeOriginalAttachments = true
		_, err = Build(c, bc)
		require.ErrorIs(t, err, mail.ErrTooLarge)
	})
}

func TestMessageID(t *testing.T) {
	pattern := regexp.MustCompile(`^<[0-9a-f]{32}@x\.test>$`)
	seen := map[string]bool{}
	for range 20 {
		raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}}, newContext())
		id := headerOf(t, raw.Data).Get("Message-Id")
		require.Regexp(t, pattern, id)
		require.False(t, seen[id], "message IDs must not repeat")
		seen[id] = true
	}

	t.Run("domain of the sender by default", func(t *testing.T) {
		bc := newContext()
		bc.MessageIDDomain = ""
		bc.From.Email = "me@mail.example.org"
		raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}}, bc)
		require.Regexp(t, `^<[0-9a-f]{32}@mail\.example\.org>$`, headerOf(t, raw.Data).Get("Message-Id"))
	})
	t.Run("international domain", func(t *testing.T) {
		bc := newContext()
		bc.MessageIDDomain = "b\U000000FCcher.example"
		raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}}, bc)
		require.Regexp(t, `^<[0-9a-f]{32}@xn--bcher-kva\.example>$`, headerOf(t, raw.Data).Get("Message-Id"))
	})
	t.Run("bad domain", func(t *testing.T) {
		for _, domain := range []string{"a b", "a>b", "a@b", "[1.2.3.4]"} {
			bc := newContext()
			bc.MessageIDDomain = domain
			_, err := Build(mail.Compose{To: []string{"bob@x"}}, bc)
			require.Error(t, err, "domain %q", domain)
		}
	})
}

func TestZeroNowUsesTheClock(t *testing.T) {
	bc := newContext()
	bc.Now = time.Time{}
	raw := mustBuild(t, mail.Compose{To: []string{"bob@x"}}, bc)

	date, err := headerOf(t, raw.Data).Date()
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), date, time.Minute)
}

func TestBuildDoesNotChangeItsInput(t *testing.T) {
	bc := newContext()
	bc.Original = originalMessage()
	c := mail.Compose{
		ReplyTo: replyTo(true),
		To:      []string{"dave@x"},
		Cc:      []string{"erin@x"},
		Bcc:     []string{"frank@x"},
	}
	mustBuild(t, c, bc)

	require.Equal(t, originalMessage(), bc.Original)
	require.Equal(t, []string{"dave@x"}, c.To)
	require.Equal(t, []string{"erin@x"}, c.Cc)
	require.Equal(t, []string{"frank@x"}, c.Bcc)
}

// FuzzBuildHeaders checks that no input can add a header or a recipient that
// the caller did not give. A built message must have only the headers that
// Build writes, and no Bcc header.
func FuzzBuildHeaders(f *testing.F) {
	f.Add("Hi", "bob@x", "Bob", "a.txt", "text/plain", "body")
	f.Add("hi\r\nBcc: evil@x", "bob@x", "Bob", "a.txt", "text/plain", "body")
	f.Add("hi", "bob@x\r\nBcc: evil@x", "Bob", "a.txt", "text/plain", "body")
	f.Add("hi", "bob@x", "Bob\r\nBcc: evil@x", "a.txt", "text/plain", "body")
	f.Add("hi", "bob@x", "Bob", "a\r\nBcc: evil@x", "text/plain", "body")
	f.Add("hi", "bob@x", "Bob", "a.txt", "text/plain\r\nBcc: evil@x", "body")
	f.Add("=?utf-8?q?hi=0D=0ABcc:_evil@x?=", "\"Bob\" <bob@x>", "=?utf-8?q?B=0D=0A?=", "a.txt", "text/plain; name=\"x\"", "\r\nBcc: evil@x\r\n")
	f.Add("hi", "bob@x", "J\U000000FCrgen", "\U00004F60.txt", "application/pdf; x=y", "Bcc: evil@x")

	allowed := map[string]bool{
		"From": true, "To": true, "Cc": true, "Subject": true, "Date": true,
		"Message-Id": true, "Mime-Version": true, "Content-Type": true,
		"Content-Transfer-Encoding": true,
	}
	f.Fuzz(func(t *testing.T, subject, to, name, filename, contentType, body string) {
		bc := newContext()
		bc.From.Name = name
		raw, err := Build(mail.Compose{
			To:          []string{to},
			Bcc:         []string{"hidden@x"},
			Subject:     subject,
			Body:        body,
			Attachments: []mail.OutgoingAttachment{{Filename: filename, ContentType: contentType, Data: []byte("data")}},
		}, bc)
		if err != nil {
			require.Nil(t, raw)
			return
		}

		m, err := netmail.ReadMessage(bytes.NewReader(raw.Data))
		require.NoError(t, err)
		for key := range m.Header {
			require.True(t, allowed[key], "unexpected header %q", key)
		}
		require.Contains(t, raw.Recipients, "hidden@x")
		require.NotContains(t, strings.ToLower(headerBlock(raw.Data)), "hidden@x")
		for _, address := range raw.Recipients {
			require.False(t, strings.ContainsAny(address, "\r\n\x00 <>,"), "recipient %q", address)
		}
	})
}
