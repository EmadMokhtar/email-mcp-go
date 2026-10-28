package mime

import (
	"bytes"
	netmail "net/mail"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFromDraftRemovesBccAndNothingElse(t *testing.T) {
	draft := msg(
		"From: Me <me@x>",
		"To: bob@x,",
		"\tdave@x",
		"Bcc: secret@x,",
		"  other@x",
		"Subject: Hi",
		"Bcc: second@x",
		"X-Keep: yes",
		"",
		"Bcc: this is body text",
		"body",
	)
	want := msg(
		"From: Me <me@x>",
		"To: bob@x,",
		"\tdave@x",
		"Subject: Hi",
		"X-Keep: yes",
		"",
		"Bcc: this is body text",
		"body",
	)

	raw, err := FromDraft([]byte(draft))
	require.NoError(t, err)
	require.Equal(t, want, string(raw.Data))
	require.Equal(t, "me@x", raw.From)
	require.Equal(t, []string{"bob@x", "dave@x", "secret@x", "other@x", "second@x"}, raw.Recipients)
}

func TestFromDraftFindsEverySpellingOfBcc(t *testing.T) {
	for _, field := range []string{
		"Bcc: secret@x",
		"BCC: secret@x",
		"bcc: secret@x",
		"bCc:secret@x",
		"Bcc : secret@x",
		"Bcc\t: secret@x",
		"Bcc:\r\n secret@x",
		"Bcc:\r\n\tsecret@x\r\n  ",
	} {
		draft := "From: me@x\r\nTo: bob@x\r\n" + field + "\r\nSubject: Hi\r\n\r\nbody"
		raw, err := FromDraft([]byte(draft))
		require.NoError(t, err, "field %q", field)
		require.Equal(t, []string{"bob@x", "secret@x"}, raw.Recipients, "field %q", field)
		require.Equal(t, "From: me@x\r\nTo: bob@x\r\nSubject: Hi\r\n\r\nbody", string(raw.Data), "field %q", field)
		h := headerOf(t, raw.Data)
		require.Empty(t, h.Get("Bcc"))
	}
}

func TestFromDraftKeepsLineEndingsAndBodyBytes(t *testing.T) {
	body := make([]byte, 256)
	for i := range body {
		body[i] = byte(i)
	}

	t.Run("line feed only", func(t *testing.T) {
		draft := []byte("From: me@x\nTo: bob@x\nBcc: secret@x\nSubject: Hi\n\n")
		draft = append(draft, body...)
		raw, err := FromDraft(draft)
		require.NoError(t, err)
		require.Equal(t, append([]byte("From: me@x\nTo: bob@x\nSubject: Hi\n\n"), body...), raw.Data)
		require.Equal(t, []string{"bob@x", "secret@x"}, raw.Recipients)
	})
	t.Run("no Bcc", func(t *testing.T) {
		draft := append([]byte("From: me@x\r\nTo: bob@x\r\n\r\n"), body...)
		raw, err := FromDraft(draft)
		require.NoError(t, err)
		require.Equal(t, draft, raw.Data)
	})
	t.Run("header only", func(t *testing.T) {
		raw, err := FromDraft([]byte("From: me@x\r\nBcc: secret@x"))
		require.NoError(t, err)
		require.Equal(t, "From: me@x\r\n", string(raw.Data))
		require.Equal(t, []string{"secret@x"}, raw.Recipients)
	})
}

func TestFromDraftStripsBccAfterAMalformedLine(t *testing.T) {
	// Some programs treat a line without a colon as the start of the body and
	// others skip it. The Bcc field is removed in both cases.
	draft := "From: me@x\r\nTo: bob@x\r\nthis line has no colon\r\nBcc: secret@x\r\nSubject: Hi\r\n\r\nbody"
	raw, err := FromDraft([]byte(draft))
	require.NoError(t, err)
	require.Equal(t, "From: me@x\r\nTo: bob@x\r\nthis line has no colon\r\nSubject: Hi\r\n\r\nbody", string(raw.Data))
	require.Contains(t, raw.Recipients, "secret@x")
}

func TestFromDraftReadsAllRecipientHeaders(t *testing.T) {
	draft := msg(
		"From: =?utf-8?q?J=C3=BCrgen?= <j@x>",
		"To: bob@x",
		"To: BOB@x, \"Dave D\" <dave@x>",
		"Cc: erin@x",
		"Bcc: Dave@x, frank@x",
		"",
		"body",
	)
	raw, err := FromDraft([]byte(draft))
	require.NoError(t, err)
	require.Equal(t, "j@x", raw.From)
	require.Equal(t, []string{"bob@x", "dave@x", "erin@x", "frank@x"}, raw.Recipients)
}

func TestFromDraftNeedsRecipients(t *testing.T) {
	for _, draft := range []string{
		"From: me@x\r\nSubject: Hi\r\n\r\nbody",
		"From: me@x\r\nTo:\r\nBcc:\r\n\r\nbody",
		"From: me@x\r\nTo: undisclosed-recipients:;\r\n\r\nbody",
		"From: me@x\r\n\r\nTo: bob@x",
	} {
		raw, err := FromDraft([]byte(draft))
		require.ErrorIs(t, err, ErrNoRecipients, "draft %q", draft)
		require.Nil(t, raw)
	}
}

func TestFromDraftNeedsASender(t *testing.T) {
	for _, draft := range []string{
		"",
		"To: bob@x\r\n\r\nbody",
		"From:\r\nTo: bob@x\r\n\r\nbody",
		"From: not an address\r\nTo: bob@x\r\n\r\nbody",
	} {
		raw, err := FromDraft([]byte(draft))
		require.ErrorIs(t, err, ErrInvalidAddress, "draft %q", draft)
		require.Nil(t, raw)
	}
}

func TestFromDraftRefusesABareCarriageReturnInTheHeader(t *testing.T) {
	// A program that reads a lone carriage return as a line break would see a
	// Bcc field here, and the scan would not.
	raw, err := FromDraft([]byte("From: me@x\r\nTo: bob@x\r\nSubject: Hi\rBcc: secret@x\r\n\r\nbody"))
	require.ErrorIs(t, err, ErrHeaderInjection)
	require.Nil(t, raw)

	// In the body it is only text.
	raw, err = FromDraft([]byte("From: me@x\r\nTo: bob@x\r\n\r\nbody\rBcc: text\r"))
	require.NoError(t, err)
	require.Equal(t, "From: me@x\r\nTo: bob@x\r\n\r\nbody\rBcc: text\r", string(raw.Data))
}

func TestFromDraftRefusesRecipientsItCannotRead(t *testing.T) {
	// A recipient that is skipped silently would be mail that is never
	// delivered. Fail instead.
	for _, field := range []string{"To: not an address", "Cc: a@x b@x", "Bcc: <secret@x"} {
		raw, err := FromDraft([]byte("From: me@x\r\nTo: bob@x\r\n" + field + "\r\n\r\nbody"))
		require.ErrorIs(t, err, ErrInvalidAddress, "field %q", field)
		require.Nil(t, raw)
	}
}

func TestFromDraftRefusesQuotedLocalParts(t *testing.T) {
	for _, address := range quotedLocalParts {
		for _, field := range []string{"From", "To", "Cc", "Bcc"} {
			draft := "From: me@x\r\nTo: bob@x\r\n" + field + ": " + address + "\r\n\r\nbody"
			if field == "From" {
				draft = "From: " + address + "\r\nTo: bob@x\r\n\r\nbody"
			}
			raw, err := FromDraft([]byte(draft))
			require.ErrorIs(t, err, ErrInvalidAddress, "%s %q", field, address)
			require.NotContains(t, err.Error(), "NOTIFY")
			require.Nil(t, raw)
		}
	}

	// Ordinary addresses still work, and the envelope is what the header shows.
	raw, err := FromDraft([]byte("From: Me <me@example.com>\r\nTo: first.last@example.com, \"Bob B.\" <bob@example.com>\r\n\r\nbody"))
	require.NoError(t, err)
	require.Equal(t, "me@example.com", raw.From)
	require.Equal(t, []string{"first.last@example.com", "bob@example.com"}, raw.Recipients)
}

func TestFromDraftDoesNotShareInput(t *testing.T) {
	draft := []byte("From: me@x\r\nTo: bob@x\r\n\r\nbody")
	keep := bytes.Clone(draft)
	raw, err := FromDraft(draft)
	require.NoError(t, err)

	raw.Data[0] = 'X'
	require.Equal(t, keep, draft)
}

func FuzzFromDraft(f *testing.F) {
	seeds := []string{
		"",
		"From: me@x\r\nTo: bob@x\r\n\r\nbody",
		"From: me@x\r\nTo: bob@x\r\nBcc: s@x\r\n\r\nbody",
		"From: me@x\nBcc: s@x,\n t@x\nTo: bob@x\n\n",
		"From: me@x\r\nBcc : s@x\r\nbcc\t:t@x\r\nTo: bob@x",
		"From: me@x\r\nTo: bob@x\r\nno colon\r\nBcc: s@x\r\n\r\nBcc: body",
		"From: me@x\r\nTo: bob@x\r\n Bcc: s@x\r\n\r\n",
		"From: me@x\rBcc: s@x\rTo: bob@x\r\r",
		"From: me@x\r\nTo: =?utf-8?q?a?= <a@x>\r\nBcc:\r\n\r\n",
		"\r\n\r\nFrom: me@x\r\nTo: bob@x\r\n",
		"From: me@x\r\nTo: \"a b\"@x\r\n\r\n",
		"From: me@x\r\nTo: bob@x\r\nBcc: \"x>\"@y\r\n\r\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		raw, err := FromDraft(in)
		if err != nil {
			require.Nil(t, raw)
			return
		}
		require.NotEmpty(t, raw.From)
		require.NotEmpty(t, raw.Recipients)
		requireEnvelopeSafe(t, raw.From)
		for _, address := range raw.Recipients {
			requireEnvelopeSafe(t, address)
		}

		// No Bcc field is left, as the standard library sees it and as
		// FromDraft sees it.
		if m, err := netmail.ReadMessage(bytes.NewReader(raw.Data)); err == nil {
			require.Empty(t, m.Header["Bcc"])
		}
		for _, field := range scanDraftHeader(raw.Data).fields {
			require.NotEqual(t, "bcc", field.name)
		}

		// Running it again changes nothing. It can only fail when the Bcc
		// field was the one source of recipients.
		again, err := FromDraft(raw.Data)
		if err != nil {
			require.ErrorIs(t, err, ErrNoRecipients)
			return
		}
		require.Equal(t, raw.Data, again.Data)
	})
}
