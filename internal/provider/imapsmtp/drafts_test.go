package imapsmtp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
)

// draftMessage returns a draft to save. A draft may have no recipients.
func draftMessage(id, subject string) *mail.RawMessage { return outgoing(id, subject) }

// draftServer returns an IMAP server with the Drafts and Trash folders.
func draftServer(t *testing.T, opts ...imaptest.Option) *imaptest.Server {
	t.Helper()
	srv := imaptest.New(t, opts...)
	srv.Create("Drafts", imap.MailboxAttrDrafts)
	srv.Create("Deleted Items", imap.MailboxAttrTrash)
	return srv
}

// noUIDPlus is a server without UIDPLUS that does not report APPENDUID.
func noUIDPlus() []imaptest.Option {
	return []imaptest.Option{
		imaptest.WithCaps(imap.CapIMAP4rev1, imap.CapListExtended, imap.CapSpecialUse),
		imaptest.WithoutAppendUID(),
	}
}

func TestSaveDraftReturnsRef(t *testing.T) {
	srv := draftServer(t)
	b := newTestBackend(t, srv)
	raw := draftMessage("d1", "Plans for Friday")

	ref, err := b.SaveDraft(t.Context(), raw)

	require.NoError(t, err)
	r := mustDecodeRef(t, ref)
	require.Equal(t, "Drafts", r.Mailbox)
	require.NotZero(t, r.UIDValidity)
	require.Equal(t, uint32(1), r.UID)

	// The ref opens the draft that was saved.
	msg := mustGet(t, b, ref, mail.BodyOptions{Format: mail.BodyNone})
	require.Equal(t, "Plans for Friday", msg.Subject)
	got, err := b.GetRaw(t.Context(), ref, 1<<20)
	require.NoError(t, err)
	require.Equal(t, string(raw.Data), string(got))

	// The draft has the flags \Draft and \Seen, and only those.
	stored := serverMsgBySubject(t, srv, "Drafts", "Plans for Friday")
	require.ElementsMatch(t, []string{`\draft`, `\seen`}, stored.lowerFlags())
}

func TestSaveDraftKeepsEverythingInTheDraft(t *testing.T) {
	// A draft keeps its Bcc header: the user wants to see it when they open the
	// draft. The backend saves the bytes as they are.
	srv := draftServer(t)
	b := newTestBackend(t, srv)
	raw := draftMessage("d1", "With bcc")
	raw.Data = append([]byte("Bcc: secret@example.com\r\n"), raw.Data...)
	raw.Recipients = []string{"bob@example.com", "secret@example.com"}

	ref, err := b.SaveDraft(t.Context(), raw)

	require.NoError(t, err)
	got, err := b.GetRaw(t.Context(), ref, 1<<20)
	require.NoError(t, err)
	require.Equal(t, string(raw.Data), string(got))
}

func TestSaveDraftWithoutUIDPlusFindsTheDraftByMessageID(t *testing.T) {
	srv := draftServer(t, noUIDPlus()...)
	// Other drafts are there before, two of them with a Message-ID that looks
	// like ours.
	srv.Append("Drafts", draftMessage("older", "Older draft").Data, imap.FlagDraft)
	srv.Append("Drafts", draftMessage("xd1", "Similar id").Data, imap.FlagDraft)
	srv.Append("Drafts", draftMessage("d1y", "Another similar id").Data, imap.FlagDraft)
	b := newTestBackend(t, srv)
	requireNoUIDPlus(t, b)

	ref, err := b.SaveDraft(t.Context(), draftMessage("d1", "The new draft"))

	require.NoError(t, err)
	msg := mustGet(t, b, ref, mail.BodyOptions{Format: mail.BodyNone})
	require.Equal(t, "The new draft", msg.Subject)
	require.Len(t, serverMessages(t, srv, "Drafts"), 4)
}

func TestSaveDraftWithoutUIDPlusRefusesWhenTheDraftIsNotUnique(t *testing.T) {
	srv := draftServer(t, noUIDPlus()...)
	// A draft with the same Message-ID is there already.
	srv.Append("Drafts", draftMessage("d1", "Same id before").Data, imap.FlagDraft)
	b := newTestBackend(t, srv)

	ref, err := b.SaveDraft(t.Context(), draftMessage("d1", "Same id again"))

	require.ErrorIs(t, err, mail.ErrUnsupported)
	require.Empty(t, ref)
	// The text says what happened, so nobody saves the draft again.
	require.ErrorContains(t, err, "was saved")
	require.Len(t, serverMessages(t, srv, "Drafts"), 2)
}

func TestSaveDraftWithoutUIDPlusNeedsAMessageIDBeforeItSavesAnything(t *testing.T) {
	srv := draftServer(t, noUIDPlus()...)
	b := newTestBackend(t, srv)
	raw := draftMessage("d1", "No id")
	raw.Data = crlf("From: me@example.com\nSubject: No id\n\nbody\n")

	ref, err := b.SaveDraft(t.Context(), raw)

	require.ErrorIs(t, err, mail.ErrUnsupported)
	require.Empty(t, ref)
	require.Empty(t, serverMessages(t, srv, "Drafts"), "nothing was stored that could not be found again")
}

func TestSaveDraftWithUIDPlusDoesNotNeedAMessageID(t *testing.T) {
	srv := draftServer(t)
	b := newTestBackend(t, srv)
	raw := draftMessage("d1", "No id")
	raw.Data = crlf("From: me@example.com\nSubject: No id\n\nbody\n")

	ref, err := b.SaveDraft(t.Context(), raw)

	require.NoError(t, err)
	require.Equal(t, "No id", mustGet(t, b, ref, mail.BodyOptions{Format: mail.BodyNone}).Subject)
}

func TestPickDraftUID(t *testing.T) {
	got, err := pickDraftUID([]imap.UID{7})
	require.NoError(t, err)
	require.Equal(t, imap.UID(7), got)

	for name, uids := range map[string][]imap.UID{"none": nil, "two": {3, 4}, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			got, err := pickDraftUID(uids)
			require.ErrorIs(t, err, mail.ErrUnsupported)
			require.Zero(t, got)
		})
	}
}

func TestSaveDraftNeedsADraftsFolder(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)

	_, err := b.SaveDraft(t.Context(), draftMessage("d1", "Plans"))

	require.ErrorIs(t, err, mail.ErrNotFound)
	require.Equal(t, []string{"INBOX"}, folderNames(t, b), "no folder was made on the way")
}

func TestSaveDraftRefusesAnEmptyMessage(t *testing.T) {
	srv := draftServer(t)
	b := newTestBackend(t, srv)

	for _, raw := range []*mail.RawMessage{nil, {}, {From: "me@example.com"}} {
		_, err := b.SaveDraft(t.Context(), raw)
		require.Error(t, err)
	}
	require.Zero(t, srv.TotalConns())
}

func TestSaveDraftKeepsTheConnectionAfterAnAppendError(t *testing.T) {
	overQuota := func(string) error {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeOverQuota, Text: "full"}
	}
	for name, tc := range map[string]struct {
		opts []imaptest.Option
		// sameConn is true when the IMAP connection is still good. go-imap
		// closes it when the server refuses a message before the message is
		// sent, and the pool then makes a new one.
		sameConn bool
	}{
		// The server reads the message and then says no.
		"the server refuses after the message": {[]imaptest.Option{imaptest.WithAppendError(overQuota)}, true},
		// The server says no before the client sends the message.
		"the server refuses before the message": {[]imaptest.Option{imaptest.WithAppendLimit(10)}, false},
	} {
		t.Run(name, func(t *testing.T) {
			srv := draftServer(t, tc.opts...)
			b := newTestBackend(t, srv)

			_, err := b.SaveDraft(t.Context(), draftMessage("d1", "Plans"))

			var serverErr *imap.Error
			require.ErrorAs(t, err, &serverErr)
			require.Equal(t, imap.StatusResponseTypeNo, serverErr.Type)
			// The backend works on after the error.
			require.NoError(t, b.pool.with(t.Context(), noop))
			_, err = b.ListFolders(t.Context())
			require.NoError(t, err)
			if tc.sameConn {
				require.Equal(t, 1, srv.TotalConns(), "the pool did not need a new connection")
			}
			require.Empty(t, serverMessages(t, srv, "Drafts"))
		})
	}
}

func TestSaveDraftIsNotRetriedAfterAReset(t *testing.T) {
	// An APPEND that is sent again after a drop can store the draft twice.
	fake := newFakeIMAP(t, "* OK [CAPABILITY IMAP4rev1 UIDPLUS] ready\r\n", func(tag, cmd string) (string, fakeThen) {
		switch cmd {
		case "LOGIN":
			return tag + " OK LOGIN completed\r\n", fakeStay
		case "CAPABILITY":
			return "* CAPABILITY IMAP4rev1 UIDPLUS\r\n" + tag + " OK CAPABILITY completed\r\n", fakeStay
		case "LIST":
			return "* LIST (\\Drafts) \"/\" Drafts\r\n" + tag + " OK LIST completed\r\n", fakeStay
		case "APPEND":
			return "", fakeReset
		}
		return tag + " BAD unexpected\r\n", fakeStay
	})
	b := fakeBackend(t, fake)

	_, err := b.SaveDraft(t.Context(), draftMessage("d1", "Plans"))

	require.Error(t, err)
	require.Equal(t, 1, strings.Count(fake.Received(), " APPEND "), "APPEND was sent once")
}

func TestSaveDraftSaysItWasSavedWhenTheRefCannotBeFound(t *testing.T) {
	// The draft is in the folder as soon as APPEND works. Whatever fails after
	// that, the error must say so, or an agent saves the draft a second time.
	selectReply := func(tag string) string {
		return "* 1 EXISTS\r\n* OK [UIDVALIDITY 7] ok\r\n* OK [UIDNEXT 6] ok\r\n* FLAGS (\\Seen)\r\n" +
			"* OK [PERMANENTFLAGS (\\Seen \\*)] ok\r\n" + tag + " OK [READ-ONLY] done\r\n"
	}
	for name, fail := range map[string]string{
		"the folder cannot be opened": "examine",
		"the search fails":            "search",
		"the fetch fails":             "fetch",
		"the server stops answering":  "silent",
	} {
		t.Run(name, func(t *testing.T) {
			// The server has no UIDPLUS, so it never reports the UID of the draft.
			// It reads the message, and the line "Closing words now" is the last
			// line of it. Then it says that APPEND worked.
			var fake *fakeIMAP
			var appendTag string
			inLiteral := false
			fake = newFakeIMAP(t, "* OK [CAPABILITY IMAP4rev1] ready\r\n", func(tag, cmd string) (string, fakeThen) {
				if inLiteral {
					if cmd == "WORDS" {
						inLiteral = false
						return appendTag + " OK APPEND completed\r\n", fakeStay
					}
					return "", fakeStay
				}
				switch cmd {
				case "LOGIN":
					return tag + " OK LOGIN completed\r\n", fakeStay
				case "CAPABILITY":
					return "* CAPABILITY IMAP4rev1\r\n" + tag + " OK CAPABILITY completed\r\n", fakeStay
				case "LIST":
					return "* LIST (\\Drafts) \"/\" Drafts\r\n" + tag + " OK LIST completed\r\n", fakeStay
				case "APPEND":
					appendTag, inLiteral = tag, true
					return "+ ready for the message\r\n", fakeStay
				case "EXAMINE":
					if fail == "examine" {
						return tag + " NO [NONEXISTENT] no such folder\r\n", fakeStay
					}
					return selectReply(tag), fakeStay
				case "UID":
					last := lastCommand(fake)
					switch {
					case strings.HasPrefix(last, tag+" UID SEARCH") && fail == "search":
						return tag + " NO [SERVERBUG] search failed\r\n", fakeStay
					case strings.HasPrefix(last, tag+" UID SEARCH") && fail == "silent":
						return "", fakeStay
					case strings.HasPrefix(last, tag+" UID SEARCH"):
						return "* SEARCH 5\r\n" + tag + " OK SEARCH completed\r\n", fakeStay
					case strings.HasPrefix(last, tag+" UID FETCH") && fail == "fetch":
						return tag + " NO [SERVERBUG] fetch failed\r\n", fakeStay
					}
				}
				return tag + " BAD unexpected\r\n", fakeStay
			})
			cfg := fake.plainConfig()
			cfg.TokenScope = testScope
			cfg.CommandTimeout = time.Second
			b := New(cfg, testSigner())
			t.Cleanup(func() { _ = b.Close() })
			raw := draftMessage("d1", "Plans")
			raw.Data = append(raw.Data, "Closing words now\r\n"...)

			ref, err := b.SaveDraft(t.Context(), raw)

			require.Error(t, err)
			require.Empty(t, ref)
			require.ErrorContains(t, err, "the draft was saved, but its reference is not known")
			require.ErrorIs(t, err, mail.ErrPartial, "the draft exists, so the caller must not save it again")
			require.Equal(t, 1, strings.Count(fake.Received(), " APPEND "), "APPEND was sent once")
		})
	}
}

// requireNoUIDPlus checks that the test server really lacks UIDPLUS.
func requireNoUIDPlus(t *testing.T, b *Backend) {
	t.Helper()
	require.NoError(t, b.pool.with(t.Context(), func(c *imapclient.Client) error {
		require.False(t, c.Caps().Has(imap.CapUIDPlus), "the server must not advertise UIDPLUS")
		return nil
	}))
}

// ---------------------------------------------------------------- DiscardDraft

func TestDiscardDraftMovesTheDraftToTrash(t *testing.T) {
	srv := draftServer(t)
	b := newTestBackend(t, srv)
	ref, err := b.SaveDraft(t.Context(), draftMessage("d1", "Plans"))
	require.NoError(t, err)
	other, err := b.SaveDraft(t.Context(), draftMessage("d2", "Keep me"))
	require.NoError(t, err)

	require.NoError(t, b.DiscardDraft(t.Context(), ref))

	require.Equal(t, []string{"Keep me"}, serverSubjects(t, srv, "Drafts"))
	require.Equal(t, []string{"Plans"}, serverSubjects(t, srv, "Deleted Items"), "it is in the Trash and not deleted for good")
	// The other draft is still there with its ref.
	require.Equal(t, "Keep me", mustGet(t, b, other, mail.BodyOptions{Format: mail.BodyNone}).Subject)
}

// This pins the contract of mail.Backend.DiscardDraft: a message that is not a
// draft is refused with ErrUnsupported, before anything changes.
func TestDiscardDraftRefusesNonDraft(t *testing.T) {
	srv := draftServer(t)
	srv.Append("INBOX", plainMail("In the inbox", "x"), imap.FlagSeen)
	// A message in the Drafts folder without the \Draft flag is not a draft.
	srv.Append("Drafts", plainMail("Not marked", "x"))
	b := newTestBackend(t, srv)
	inbox := refsBySubject(t, b, "INBOX")["In the inbox"]
	unmarked := refsBySubject(t, b, "Drafts")["Not marked"]
	rwBefore := rwSelects(srv)

	for name, ref := range map[string]mail.Ref{"inbox message": inbox, "unmarked message in Drafts": unmarked} {
		t.Run(name, func(t *testing.T) {
			err := b.DiscardDraft(t.Context(), ref)

			require.ErrorIs(t, err, mail.ErrUnsupported)
			require.ErrorContains(t, err, "draft")
		})
	}

	require.Equal(t, []string{"In the inbox"}, serverSubjects(t, srv, "INBOX"))
	require.Equal(t, []string{"Not marked"}, serverSubjects(t, srv, "Drafts"))
	require.Empty(t, serverSubjects(t, srv, "Deleted Items"))
	require.Equal(t, []string{`\seen`}, serverMsgBySubject(t, srv, "INBOX", "In the inbox").lowerFlags(), "no flag changed")
	require.Equal(t, rwBefore, rwSelects(srv), "no folder was opened for writing")
}

func TestDiscardDraftTakesAnyMessageWithTheDraftFlag(t *testing.T) {
	// The flag decides, not the folder.
	srv := draftServer(t)
	srv.Append("INBOX", plainMail("Flagged as draft", "x"), imap.FlagDraft)
	b := newTestBackend(t, srv)
	ref := refsBySubject(t, b, "INBOX")["Flagged as draft"]

	require.NoError(t, b.DiscardDraft(t.Context(), ref))

	require.Empty(t, serverSubjects(t, srv, "INBOX"))
	require.Equal(t, []string{"Flagged as draft"}, serverSubjects(t, srv, "Deleted Items"))
}

func TestDiscardDraftChecksTheRef(t *testing.T) {
	t.Run("stale ref", func(t *testing.T) {
		srv := draftServer(t)
		b := newTestBackend(t, srv)
		ref, err := b.SaveDraft(t.Context(), draftMessage("d1", "Plans"))
		require.NoError(t, err)
		makeStale(t, srv, "Drafts", draftMessage("d1", "Plans").Data)

		err = b.DiscardDraft(t.Context(), ref)

		require.ErrorIs(t, err, mail.ErrRefStale)
		require.Empty(t, serverSubjects(t, srv, "Deleted Items"))
	})

	t.Run("message is gone", func(t *testing.T) {
		srv := draftServer(t)
		b := newTestBackend(t, srv)
		ref, err := b.SaveDraft(t.Context(), draftMessage("d1", "Plans"))
		require.NoError(t, err)
		expungeOnAnotherClient(t, srv, "Drafts", mustDecodeRef(t, ref).UID)

		err = b.DiscardDraft(t.Context(), ref)

		require.ErrorIs(t, err, mail.ErrNotFound)
	})

	t.Run("not a ref", func(t *testing.T) {
		srv := draftServer(t)
		b := newTestBackend(t, srv)

		for _, ref := range []mail.Ref{"", "garbage", mail.Ref(strings.Repeat("a", 5000))} {
			require.ErrorIs(t, b.DiscardDraft(t.Context(), ref), mail.ErrNotFound)
		}
		require.Zero(t, srv.TotalConns())
	})
}

func TestDiscardDraftNeedsATrashFolder(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Drafts", imap.MailboxAttrDrafts)
	b := newTestBackend(t, srv)
	ref, err := b.SaveDraft(t.Context(), draftMessage("d1", "Plans"))
	require.NoError(t, err)

	err = b.DiscardDraft(t.Context(), ref)

	require.True(t, errors.Is(err, mail.ErrNotFound), "%v", err)
	require.Equal(t, []string{"Plans"}, serverSubjects(t, srv, "Drafts"), "the draft stays: it is never deleted for good")
}
