package imapsmtp

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
)

// The names of the folders with non-ASCII letters. The \U escapes keep the
// source file ASCII: U+00DC is the capital U with a diaeresis, U+00E4 is the
// small a with a diaeresis.
const umlautFolder = "Rechnungen/\U000000dcberf\U000000e4llig"

const testScope = "tenant-1/account-1"

// testSigner returns a signer with a fixed key.
func testSigner() *signing.Signer { return signing.New(bytes.Repeat([]byte{7}, 32)) }

// newTestBackendScope returns a backend for srv. The backend is closed when
// the test ends.
func newTestBackendScope(t *testing.T, srv *imaptest.Server, signer *signing.Signer, scope string) *Backend {
	t.Helper()
	cfg := pinnedConfig(srv, account.SecurityTLS)
	cfg.TokenScope = scope
	b := New(cfg, signer)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func newTestBackend(t *testing.T, srv *imaptest.Server) *Backend {
	t.Helper()
	return newTestBackendScope(t, srv, testSigner(), testScope)
}

// crlf turns the line breaks of a test message into CRLF, as mail has them.
func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

// plainMail returns a simple message with a plain text body.
func plainMail(subject, body string) []byte {
	return crlf("From: Sender <sender@example.com>\n" +
		"To: Me <me@example.com>\n" +
		"Subject: " + subject + "\n" +
		"Date: Tue, 10 Mar 2026 10:00:00 +0000\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: text/plain; charset=utf-8\n" +
		"\n" + body + "\n")
}

// mailWith returns a message with the given extra header lines and body. The
// body is used as it is (apart from the line breaks). A blank line separates
// them.
func mailWith(headers, body string) []byte {
	return crlf("From: Sender <sender@example.com>\n" +
		"To: Me <me@example.com>\n" +
		"Subject: test\n" +
		"Date: Tue, 10 Mar 2026 10:00:00 +0000\n" +
		"MIME-Version: 1.0\n" +
		headers + "\n\n" + body)
}

func mustSearch(t *testing.T, b *Backend, q mail.Query, page mail.PageToken) mail.Page[mail.Summary] {
	t.Helper()
	p, err := b.Search(t.Context(), q, page)
	require.NoError(t, err)
	return p
}

func mustDecodeRef(t *testing.T, ref mail.Ref) imapRef {
	t.Helper()
	r, err := decodeRef(ref)
	require.NoError(t, err)
	return r
}

func subjects(items []mail.Summary) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Subject
	}
	return out
}

func uidsOf(t *testing.T, items []mail.Summary) []uint32 {
	t.Helper()
	out := make([]uint32, len(items))
	for i, it := range items {
		out[i] = mustDecodeRef(t, it.Ref).UID
	}
	return out
}

func boolPtr(v bool) *bool { return &v }

// ---------------------------------------------------------------- refs

func TestRefRoundTrip(t *testing.T) {
	in := imapRef{Mailbox: umlautFolder, UIDValidity: 7, UID: 4242}
	ref := encodeRef(in)

	require.NotContains(t, string(ref), "=", "base64url without padding")
	require.NotContains(t, string(ref), "+")
	require.NotContains(t, string(ref), "/")
	out, err := decodeRef(ref)
	require.NoError(t, err)
	require.Equal(t, in, out)
}

func TestDecodeRefRejectsMalformedRefs(t *testing.T) {
	enc := func(s string) mail.Ref { return mail.Ref(base64.RawURLEncoding.EncodeToString([]byte(s))) }
	cases := map[string]mail.Ref{
		"empty":            "",
		"not base64":       "this is not base64!!",
		"padded base64":    mail.Ref(base64.URLEncoding.EncodeToString([]byte(`{"m":"INBOX","v":1,"u":1}`))),
		"not json":         enc("hello"),
		"json array":       enc(`[1,2,3]`),
		"zero uid":         enc(`{"m":"INBOX","v":1,"u":0}`),
		"no mailbox":       enc(`{"m":"","v":1,"u":5}`),
		"zero uidvalidity": enc(`{"m":"INBOX","v":0,"u":5}`),
		"no uidvalidity":   enc(`{"m":"INBOX","u":5}`),
		"negative uid":     enc(`{"m":"INBOX","v":1,"u":-4}`),
		"uid too large":    enc(`{"m":"INBOX","v":1,"u":4294967296}`),
		"wrong type":       enc(`{"m":5,"v":1,"u":5}`),
		"huge ref":         mail.Ref(strings.Repeat("A", 1<<20)),
		"trailing garbage": enc(`{"m":"INBOX","v":1,"u":5}xx`),
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeRef(ref)
			require.ErrorIs(t, err, mail.ErrNotFound)
		})
	}
}

// ---------------------------------------------------------------- folders

func TestListFoldersAliases(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Deleted Items", imap.MailboxAttrTrash)
	srv.Create("Sent Items", imap.MailboxAttrSent)
	srv.Create("Entw\U000000fcrfe", imap.MailboxAttrDrafts)
	srv.Create("Old Mail", imap.MailboxAttrArchive)
	srv.Create("Spam", imap.MailboxAttrJunk)
	srv.Create("Projects")
	b := newTestBackend(t, srv)

	got, err := b.resolveFolder(t.Context(), mail.AliasTrash)
	require.NoError(t, err)
	require.Equal(t, "Deleted Items", got)

	want := map[string]string{
		mail.AliasInbox:   "INBOX",
		mail.AliasTrash:   "Deleted Items",
		mail.AliasSent:    "Sent Items",
		mail.AliasDrafts:  "Entw\U000000fcrfe",
		mail.AliasArchive: "Old Mail",
		mail.AliasJunk:    "Spam",
	}
	for alias, name := range want {
		got, err := b.resolveFolder(t.Context(), alias)
		require.NoError(t, err, alias)
		require.Equal(t, name, got, alias)
	}

	folders, err := b.ListFolders(t.Context())
	require.NoError(t, err)
	aliasOf := map[string]string{}
	for _, f := range folders {
		aliasOf[f.Name] = f.Alias
	}
	require.Equal(t, map[string]string{
		"INBOX":             mail.AliasInbox,
		"Deleted Items":     mail.AliasTrash,
		"Sent Items":        mail.AliasSent,
		"Entw\U000000fcrfe": mail.AliasDrafts,
		"Old Mail":          mail.AliasArchive,
		"Spam":              mail.AliasJunk,
		"Projects":          "",
	}, aliasOf)
}

func TestResolveFolder(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Sent Items", imap.MailboxAttrSent)
	b := newTestBackend(t, srv)

	for in, want := range map[string]string{
		"":            "INBOX",
		"@inbox":      "INBOX",
		"INBOX":       "INBOX",
		"inbox":       "INBOX",
		"Projects/Go": "Projects/Go", // a plain name is not changed
	} {
		got, err := b.resolveFolder(t.Context(), in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}

	// A folder with the attribute is missing.
	_, err := b.resolveFolder(t.Context(), mail.AliasArchive)
	require.ErrorIs(t, err, mail.ErrNotFound)
	require.ErrorContains(t, err, "no @archive folder")
}

func TestResolveFolderNeedsAttribute(t *testing.T) {
	// Folders with the usual names but without SPECIAL-USE attributes are not
	// guessed.
	srv := imaptest.New(t)
	srv.Create("Sent")
	srv.Create("Trash")
	b := newTestBackend(t, srv)

	_, err := b.resolveFolder(t.Context(), mail.AliasSent)
	require.ErrorIs(t, err, mail.ErrNotFound)
	_, err = b.resolveFolder(t.Context(), mail.AliasTrash)
	require.ErrorIs(t, err, mail.ErrNotFound)
}

func TestListFoldersCounts(t *testing.T) {
	t.Run("with LIST-STATUS", func(t *testing.T) {
		srv := imaptest.New(t)
		srv.Append("INBOX", plainMail("a", "x"))
		srv.Append("INBOX", plainMail("b", "x"), imap.FlagSeen)
		srv.Append("INBOX", plainMail("c", "x"))
		srv.Create("Empty")
		b := newTestBackend(t, srv)

		folders, err := b.ListFolders(t.Context())
		require.NoError(t, err)
		byName := map[string]mail.Folder{}
		for _, f := range folders {
			byName[f.Name] = f
		}
		inbox := byName["INBOX"]
		require.NotNil(t, inbox.Total)
		require.NotNil(t, inbox.Unread)
		require.Equal(t, 3, *inbox.Total)
		require.Equal(t, 2, *inbox.Unread)
		empty := byName["Empty"]
		require.NotNil(t, empty.Total)
		require.Equal(t, 0, *empty.Total)
		require.Equal(t, 0, *empty.Unread)
	})

	t.Run("without LIST-STATUS", func(t *testing.T) {
		srv := imaptest.New(t, imaptest.WithCaps(imap.CapIMAP4rev1, imap.CapListExtended, imap.CapSpecialUse, imap.CapUIDPlus))
		srv.Append("INBOX", plainMail("a", "x"))
		b := newTestBackend(t, srv)

		folders, err := b.ListFolders(t.Context())
		require.NoError(t, err)
		require.NotEmpty(t, folders)
		for _, f := range folders {
			require.Nil(t, f.Total, f.Name)
			require.Nil(t, f.Unread, f.Name)
		}
	})
}

func TestFolderNamesWithNonASCII(t *testing.T) {
	// imapmemserver advertises IMAP4rev2, so go-imap sends the name as UTF-8.
	// An IMAP4rev1 server without UTF8=ACCEPT enabled uses modified UTF-7
	// ("&AMw-" for a capital U with a diaeresis). Both must give the same name.
	servers := map[string][]imaptest.Option{
		"IMAP4rev2 (UTF-8 names)":          nil,
		"IMAP4rev1 (modified UTF-7 names)": {imaptest.WithCaps(imap.CapIMAP4rev1, imap.CapListExtended, imap.CapListStatus, imap.CapSpecialUse, imap.CapUIDPlus)},
	}
	for name, opts := range servers {
		t.Run(name, func(t *testing.T) {
			srv := imaptest.New(t, opts...)
			srv.Append(umlautFolder, plainMail("Rechnung 17", "Bitte zahlen"))
			b := newTestBackend(t, srv)

			folders, err := b.ListFolders(t.Context())
			require.NoError(t, err)
			var names []string
			for _, f := range folders {
				names = append(names, f.Name)
			}
			require.Contains(t, names, umlautFolder)

			p := mustSearch(t, b, mail.Query{Folder: umlautFolder}, "")
			require.Len(t, p.Items, 1)
			require.Equal(t, "Rechnung 17", p.Items[0].Subject)
			require.Equal(t, umlautFolder, mustDecodeRef(t, p.Items[0].Ref).Mailbox)
		})
	}
}

func TestFolderNamesThatCannotExist(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)

	for _, name := range []string{"bad\x00name", "bad\r\nname", "tab\tname", strings.Repeat("x", 5000)} {
		_, err := b.resolveFolder(t.Context(), name)
		require.ErrorIs(t, err, mail.ErrNotFound, "%q", name)
		_, err = b.Search(t.Context(), mail.Query{Folder: name}, "")
		require.ErrorIs(t, err, mail.ErrNotFound, "%q", name)
		_, _, _, err = b.Changes(t.Context(), name, "")
		require.ErrorIs(t, err, mail.ErrNotFound, "%q", name)
	}
	require.Zero(t, srv.TotalConns(), "no connection was needed")
}

func TestSearchUnknownFolder(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)

	_, err := b.Search(t.Context(), mail.Query{Folder: "No/Such/Folder"}, "")
	require.ErrorIs(t, err, mail.ErrNotFound)
	_, err = b.Search(t.Context(), mail.Query{Folder: mail.AliasJunk}, "")
	require.ErrorIs(t, err, mail.ErrNotFound)
}

// ---------------------------------------------------------------- search

func appendMany(srv *imaptest.Server, mailbox string, n int, prefix string) {
	for i := 1; i <= n; i++ {
		srv.Append(mailbox, plainMail(fmt.Sprintf("%s %d", prefix, i), "body"))
	}
}

func TestSearchNewestFirstAndPaging(t *testing.T) {
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 120, "msg")
	b := newTestBackend(t, srv)

	first := mustSearch(t, b, mail.Query{Limit: 100}, "")
	require.Len(t, first.Items, 100)
	require.NotEmpty(t, first.Next)
	uids := uidsOf(t, first.Items)
	for i, uid := range uids {
		require.Equal(t, uint32(120-i), uid, "newest first, no gaps")
	}
	require.Equal(t, "msg 120", first.Items[0].Subject)

	second := mustSearch(t, b, mail.Query{Limit: 100}, first.Next)
	require.Len(t, second.Items, 20)
	require.Empty(t, second.Next)
	for i, uid := range uidsOf(t, second.Items) {
		require.Equal(t, uint32(20-i), uid)
	}
}

func TestSearchPagesDoNotRepeatWhenMailArrives(t *testing.T) {
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 10, "old")
	b := newTestBackend(t, srv)

	first := mustSearch(t, b, mail.Query{Limit: 4}, "")
	require.Equal(t, []string{"old 10", "old 9", "old 8", "old 7"}, subjects(first.Items))

	// New mail arrives between two pages. The next page continues below the
	// last UID of the first page.
	appendMany(srv, "INBOX", 3, "new")
	second := mustSearch(t, b, mail.Query{Limit: 4}, first.Next)
	require.Equal(t, []string{"old 6", "old 5", "old 4", "old 3"}, subjects(second.Items))
	third := mustSearch(t, b, mail.Query{Limit: 4}, second.Next)
	require.Equal(t, []string{"old 2", "old 1"}, subjects(third.Items))
	require.Empty(t, third.Next)
}

func TestSearchExactMultipleOfLimitHasNoNextPage(t *testing.T) {
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 6, "m")
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{Limit: 6}, "")
	require.Len(t, p.Items, 6)
	require.Empty(t, p.Next, "no empty last page")
}

func TestSearchDefaultLimitIs50(t *testing.T) {
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 60, "m")
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{}, "")
	require.Len(t, p.Items, 50)
	require.NotEmpty(t, p.Next)
	p = mustSearch(t, b, mail.Query{Limit: -3}, "")
	require.Len(t, p.Items, 50)
}

func TestSearchCapsAt100(t *testing.T) {
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 120, "m")
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{Limit: 500}, "")
	require.Len(t, p.Items, 100)
	require.NotEmpty(t, p.Next)
}

func TestSearchEmptyFolder(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{}, "")
	require.Empty(t, p.Items)
	require.Empty(t, p.Next)
}

func TestSearchByKeywordAndUntagged(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("m1", "x"), "project")
	srv.Append("INBOX", plainMail("m2", "x"), "urgent")
	srv.Append("INBOX", plainMail("m3", "x"), "project", "urgent")
	srv.Append("INBOX", plainMail("m4", "x"))
	srv.Append("INBOX", plainMail("m5", "x"), imap.FlagSeen)
	b := newTestBackend(t, srv)

	cases := []struct {
		name string
		q    mail.Query
		want []string
	}{
		{"one tag", mail.Query{TagsAny: []string{"project"}}, []string{"m3", "m1"}},
		{"two tags", mail.Query{TagsAny: []string{"project", "urgent"}}, []string{"m3", "m2", "m1"}},
		{"three tags", mail.Query{TagsAny: []string{"project", "nothing", "urgent"}}, []string{"m3", "m2", "m1"}},
		{"tags are case-insensitive", mail.Query{TagsAny: []string{"PROJECT"}}, []string{"m3", "m1"}},
		{"tags none", mail.Query{TagsNone: []string{"urgent"}}, []string{"m5", "m4", "m1"}},
		{"tags none, two", mail.Query{TagsNone: []string{"urgent", "project"}}, []string{"m5", "m4"}},
		{"any and none", mail.Query{TagsAny: []string{"project"}, TagsNone: []string{"urgent"}}, []string{"m1"}},
		{"untagged", mail.Query{Untagged: true}, []string{"m5", "m4"}},
		{"untagged and unread", mail.Query{Untagged: true, Unread: boolPtr(true)}, []string{"m4"}},
		// A name that cannot be an IMAP keyword is on no message.
		{"invalid tag only", mail.Query{TagsAny: []string{"has space"}}, nil},
		{"invalid tag with valid tag", mail.Query{TagsAny: []string{"has space", "project"}}, []string{"m3", "m1"}},
		{"invalid tag in tags none", mail.Query{TagsNone: []string{"has space", "urgent"}}, []string{"m5", "m4", "m1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustSearch(t, b, tc.q, "")
			require.Equal(t, tc.want, append([]string(nil), subjects(p.Items)...))
			if tc.want == nil {
				require.Empty(t, p.Items)
			}
		})
	}
}

func TestUntaggedOnFolderWithoutKeywords(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("a", "x"))
	srv.Append("INBOX", plainMail("b", "x"), imap.FlagFlagged)
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{Untagged: true}, "")
	require.Equal(t, []string{"b", "a"}, subjects(p.Items))
}

func TestSearchFilters(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", crlf("From: Alice <alice@example.com>\nTo: bob@example.com\nSubject: Quarterly report\nDate: Tue, 10 Mar 2026 10:00:00 +0000\n\nrevenue grew\n"), imap.FlagSeen)
	srv.Append("INBOX", crlf("From: carol@example.com\nTo: bob@example.com\nSubject: Lunch\nDate: Tue, 10 Mar 2026 10:00:00 +0000\n\npizza on friday?\n"), imap.FlagFlagged)
	srv.Append("INBOX", crlf("From: alice@example.com\nTo: dave@example.com\nSubject: Invoice 42\nDate: Tue, 10 Mar 2026 10:00:00 +0000\n\nplease pay\n"))
	b := newTestBackend(t, srv)

	cases := []struct {
		name string
		q    mail.Query
		want []string
	}{
		{"from", mail.Query{From: "alice"}, []string{"Invoice 42", "Quarterly report"}},
		{"to", mail.Query{To: "dave"}, []string{"Invoice 42"}},
		{"subject", mail.Query{Subject: "lunch"}, []string{"Lunch"}},
		{"text in body", mail.Query{Text: "pizza"}, []string{"Lunch"}},
		{"text in header", mail.Query{Text: "Quarterly"}, []string{"Quarterly report"}},
		{"unread", mail.Query{Unread: boolPtr(true)}, []string{"Invoice 42", "Lunch"}},
		{"read", mail.Query{Unread: boolPtr(false)}, []string{"Quarterly report"}},
		{"flagged", mail.Query{Flagged: boolPtr(true)}, []string{"Lunch"}},
		{"not flagged", mail.Query{Flagged: boolPtr(false)}, []string{"Invoice 42", "Quarterly report"}},
		{"from and unread", mail.Query{From: "alice", Unread: boolPtr(true)}, []string{"Invoice 42"}},
		{"no match", mail.Query{Subject: "does not exist"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustSearch(t, b, tc.q, "")
			require.Equal(t, tc.want, append([]string(nil), subjects(p.Items)...))
		})
	}
}

func TestSearchNonASCIIText(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("Rechnung", "Bitte \U000000fcberweisen Sie den Betrag"))
	srv.Append("INBOX", plainMail("Other", "nothing"))
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{Text: "\U000000fcberweisen"}, "")
	require.Equal(t, []string{"Rechnung"}, subjects(p.Items))
}

func TestSearchDateFilterIsExact(t *testing.T) {
	// The IMAP SINCE and BEFORE keys compare only the date. The backend then
	// checks the exact time of the internal date.
	srv := imaptest.New(t)
	utc := func(h, m int, day int) time.Time { return time.Date(2026, 3, day, h, m, 0, 0, time.UTC) }
	srv.AppendAt("INBOX", plainMail("a", "x"), utc(23, 0, 9))
	srv.AppendAt("INBOX", plainMail("b", "x"), utc(10, 0, 10))
	srv.AppendAt("INBOX", plainMail("c", "x"), utc(12, 0, 10))
	srv.AppendAt("INBOX", plainMail("d", "x"), utc(14, 0, 10))
	srv.AppendAt("INBOX", plainMail("e", "x"), utc(1, 0, 11))
	b := newTestBackend(t, srv)

	since := utc(11, 0, 10)
	before := utc(13, 0, 10)
	cases := []struct {
		name string
		q    mail.Query
		want []string
	}{
		{"since and before", mail.Query{Since: &since, Before: &before}, []string{"c"}},
		{"since", mail.Query{Since: &since}, []string{"e", "d", "c"}},
		{"before", mail.Query{Before: &before}, []string{"c", "b", "a"}},
		{"since is inclusive", mail.Query{Since: ptr(utc(12, 0, 10))}, []string{"e", "d", "c"}},
		{"before is exclusive", mail.Query{Before: ptr(utc(12, 0, 10))}, []string{"b", "a"}},
		{"empty range", mail.Query{Since: &before, Before: &since}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustSearch(t, b, tc.q, "")
			require.Equal(t, tc.want, append([]string(nil), subjects(p.Items)...))
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestSearchDateLimitsOutsideTheIMAPDateRange(t *testing.T) {
	// The IMAP date has a four-digit year. A limit like this one must not make
	// the server answer BAD. The exact check still works.
	srv := imaptest.New(t)
	srv.AppendAt("INBOX", plainMail("a", "x"), time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC))
	b := newTestBackend(t, srv)

	farFuture := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	longAgo := time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC)
	for name, q := range map[string]mail.Query{
		"before far future": {Before: &farFuture},
		"since long ago":    {Since: &longAgo},
		"zero is no limit":  {Since: &time.Time{}, Before: &time.Time{}},
	} {
		p, err := b.Search(t.Context(), q, "")
		require.NoError(t, err, name)
		require.Equal(t, []string{"a"}, subjects(p.Items), name)
	}
	p := mustSearch(t, b, mail.Query{Since: &farFuture}, "")
	require.Empty(t, p.Items)
}

func TestSearchDateFilterAcrossTimeZones(t *testing.T) {
	// The internal date has its own time zone, and the server compares the date
	// in that zone. The query is an exact moment.
	srv := imaptest.New(t)
	east := time.FixedZone("east", 9*3600)
	west := time.FixedZone("west", -11*3600)
	// 2026-03-10 08:30 +09:00 is 2026-03-09 23:30 UTC. Its date in its own zone
	// is the 10th, but its date in UTC is the 9th.
	srv.AppendAt("INBOX", plainMail("east", "x"), time.Date(2026, 3, 10, 8, 30, 0, 0, east))
	// 2026-03-09 20:00 -11:00 is 2026-03-10 07:00 UTC. Its date in its own
	// zone is the 9th, but its date in UTC is the 10th.
	srv.AppendAt("INBOX", plainMail("west", "x"), time.Date(2026, 3, 9, 20, 0, 0, 0, west))
	b := newTestBackend(t, srv)

	day9 := func(h, m int) time.Time { return time.Date(2026, 3, 9, h, m, 0, 0, time.UTC) }
	day10 := func(h, m int) time.Time { return time.Date(2026, 3, 10, h, m, 0, 0, time.UTC) }

	cases := []struct {
		name string
		q    mail.Query
		want []string
	}{
		{"before the east message", mail.Query{Before: ptr(day9(23, 45))}, []string{"east"}},
		{"since the east message", mail.Query{Since: ptr(day9(23, 15)), Before: ptr(day10(0, 0))}, []string{"east"}},
		{"since the west message", mail.Query{Since: ptr(day10(6, 30)), Before: ptr(day10(7, 30))}, []string{"west"}},
		{"after the west message", mail.Query{Since: ptr(day10(7, 1))}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustSearch(t, b, tc.q, "")
			require.Equal(t, tc.want, append([]string(nil), subjects(p.Items)...))
		})
	}
}

func TestSearchDateFilterPagesAreFull(t *testing.T) {
	// Messages next to the boundary pass the date check of the server and fail
	// the exact check. They must not make a page short or break the paging.
	srv := imaptest.New(t)
	day := func(h int) time.Time { return time.Date(2026, 3, 10, h, 0, 0, 0, time.UTC) }
	for i := 1; i <= 12; i++ {
		srv.AppendAt("INBOX", plainMail(fmt.Sprintf("in %d", i), "x"), day(12).Add(time.Duration(i)*time.Minute))
		if i%2 == 0 {
			srv.AppendAt("INBOX", plainMail(fmt.Sprintf("out %d", i), "x"), day(9)) // too early
		}
	}
	srv.AppendAt("INBOX", plainMail("late", "x"), day(23)) // too late
	b := newTestBackend(t, srv)

	q := mail.Query{Since: ptr(day(12)), Before: ptr(day(13)), Limit: 5}
	var seen []string
	var sizes []int
	var token mail.PageToken
	for range 10 {
		p := mustSearch(t, b, q, token)
		sizes = append(sizes, len(p.Items))
		seen = append(seen, subjects(p.Items)...)
		if p.Next == "" {
			break
		}
		token = p.Next
	}
	require.Equal(t, []int{5, 5, 2}, sizes)
	require.Len(t, seen, 12)
	for i, s := range seen {
		require.Equal(t, fmt.Sprintf("in %d", 12-i), s)
	}
}

func TestSearchHidesDeletedMessages(t *testing.T) {
	// Another client marked two messages \Deleted. They wait for an expunge.
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("m1", "x"))
	srv.Append("INBOX", plainMail("m2", "x"), imap.FlagDeleted)
	srv.Append("INBOX", plainMail("m3", "x"), imap.FlagSeen)
	srv.Append("INBOX", plainMail("m4", "x"), imap.FlagDeleted, imap.FlagSeen, "project")
	srv.Append("INBOX", plainMail("m5", "x"), "project")
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{}, "")
	require.Equal(t, []string{"m5", "m3", "m1"}, subjects(p.Items))

	// The filters still work with the rule, and paging gives full pages.
	p = mustSearch(t, b, mail.Query{TagsAny: []string{"project"}}, "")
	require.Equal(t, []string{"m5"}, subjects(p.Items))
	p = mustSearch(t, b, mail.Query{Unread: boolPtr(false)}, "")
	require.Equal(t, []string{"m3"}, subjects(p.Items))
	p = mustSearch(t, b, mail.Query{Untagged: true}, "")
	require.Equal(t, []string{"m3", "m1"}, subjects(p.Items))

	first := mustSearch(t, b, mail.Query{Limit: 2}, "")
	require.Equal(t, []string{"m5", "m3"}, subjects(first.Items))
	require.NotEmpty(t, first.Next)
	second := mustSearch(t, b, mail.Query{Limit: 2}, first.Next)
	require.Equal(t, []string{"m1"}, subjects(second.Items))
	require.Empty(t, second.Next)
}

func TestSearchDoesNotMarkSeen(t *testing.T) {
	// Regression 46d03b8: reading the snippet with BODY[] instead of
	// BODY.PEEK[] marks the message as read.
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 3, "m")
	appendMany(srv, "Archive", 2, "a")
	require.Equal(t, 3, srv.Unseen("INBOX"))
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{}, "")
	require.Len(t, p.Items, 3)
	for _, it := range p.Items {
		require.False(t, it.Seen)
		require.NotEmpty(t, it.Snippet, "the snippet was read")
	}
	_, cursor, _, err := b.Changes(t.Context(), "INBOX", "")
	require.NoError(t, err)
	appendMany(srv, "INBOX", 2, "n")
	items, _, _, err := b.Changes(t.Context(), "INBOX", cursor)
	require.NoError(t, err)
	require.Len(t, items, 2)
	mustSearch(t, b, mail.Query{Folder: "Archive"}, "")

	require.Equal(t, 5, srv.Unseen("INBOX"), "nothing was marked as read")
	require.Equal(t, 2, srv.Unseen("Archive"))
	readWrite, readOnly := srv.SelectCounts()
	require.Zero(t, readWrite, "mailboxes are opened with EXAMINE")
	require.Positive(t, readOnly)
}

func TestConcurrentSearchesInDifferentFolders(t *testing.T) {
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 5, "inbox")
	appendMany(srv, "Archive", 7, "archive")
	b := newTestBackend(t, srv)

	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			folder, prefix, want := "INBOX", "inbox", 5
			if i%2 == 1 {
				folder, prefix, want = "Archive", "archive", 7
			}
			p, err := b.Search(context.Background(), mail.Query{Folder: folder}, "")
			if err != nil {
				errs <- fmt.Errorf("worker %d: %w", i, err)
				return
			}
			if len(p.Items) != want {
				errs <- fmt.Errorf("worker %d: got %d items in %s, want %d", i, len(p.Items), folder, want)
				return
			}
			for _, it := range p.Items {
				ref, err := decodeRef(it.Ref)
				if err != nil {
					errs <- fmt.Errorf("worker %d: %w", i, err)
					return
				}
				if ref.Mailbox != folder || !strings.HasPrefix(it.Subject, prefix) {
					errs <- fmt.Errorf("worker %d: item %q from %q in a search of %q", i, it.Subject, ref.Mailbox, folder)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.LessOrEqual(t, srv.PeakConns(), 2, "the pool limit")
}

func TestOperationsLeaveTheConnectionReusable(t *testing.T) {
	// Every FETCH, SEARCH and LIST must be finished before an operation ends
	// (see pool.with). If one stayed open, the next operation on the same
	// connection would wait for the command timeout and then dial again.
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 30, "m")
	cfg := pinnedConfig(srv, account.SecurityTLS)
	cfg.TokenScope = testScope
	cfg.CommandTimeout = 3 * time.Second
	b := New(cfg, testSigner())
	t.Cleanup(func() { _ = b.Close() })

	started := time.Now()
	_, cursor, _, err := b.Changes(t.Context(), "INBOX", "")
	require.NoError(t, err)
	for range 3 {
		first := mustSearch(t, b, mail.Query{Limit: 10}, "")
		mustSearch(t, b, mail.Query{Limit: 10}, first.Next)
		mustSearch(t, b, mail.Query{Since: ptr(time.Now().Add(-24 * time.Hour)), Limit: 10}, "")
		_, err := b.ListFolders(t.Context())
		require.NoError(t, err)
		appendMany(srv, "INBOX", 2, "new")
		items, next, _, err := b.Changes(t.Context(), "INBOX", cursor)
		require.NoError(t, err)
		require.Len(t, items, 2)
		cursor = next
	}
	require.Less(t, time.Since(started), 3*time.Second)
	require.Equal(t, 1, srv.TotalConns(), "one connection served every operation")
}

func TestSearchAfterCloseFails(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)
	require.NoError(t, b.Close())

	_, err := b.Search(t.Context(), mail.Query{}, "")
	require.ErrorIs(t, err, errPoolClosed)
}

// ---------------------------------------------------------------- summaries

func TestSearchSummaryFields(t *testing.T) {
	srv := imaptest.New(t)
	raw := crlf("From: =?utf-8?q?J=C3=BCrgen_M=C3=BCller?= <juergen@example.org>\n" +
		"To: \"Bob\" <bob@example.com>, carol@example.com, undisclosed: ;\n" +
		"Subject: =?windows-1252?Q?Caf=E9_menu?=\n" +
		"Date: Tue, 10 Mar 2026 10:00:00 +0100\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: text/plain; charset=utf-8\n" +
		"\nHello\n")
	srv.Append("INBOX", raw, imap.FlagSeen, imap.FlagFlagged, "zeta", "alpha")
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{}, "")
	require.Len(t, p.Items, 1)
	got := p.Items[0]

	require.Equal(t, "Caf\U000000e9 menu", got.Subject)
	require.Equal(t, mail.Address{Name: "J\U000000fcrgen M\U000000fcller", Email: "juergen@example.org"}, got.From)
	require.Equal(t, []mail.Address{{Name: "Bob", Email: "bob@example.com"}, {Email: "carol@example.com"}}, got.To)
	require.True(t, got.Date.Equal(time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)), got.Date.String())
	require.True(t, got.Seen)
	require.True(t, got.Flagged)
	require.Equal(t, []string{"alpha", "zeta"}, got.Tags, "keywords only, sorted")
	require.Equal(t, int64(len(raw)), got.Size)
	require.False(t, got.HasAttachments)
	require.False(t, got.HasInvite)
	require.Equal(t, "Hello", got.Snippet)

	ref := mustDecodeRef(t, got.Ref)
	require.Equal(t, "INBOX", ref.Mailbox)
	require.Equal(t, uint32(1), ref.UID)
	require.NotZero(t, ref.UIDValidity)
}

func TestSummaryDateFallsBackToInternalDate(t *testing.T) {
	srv := imaptest.New(t)
	at := time.Date(2026, 1, 5, 8, 15, 30, 0, time.UTC)
	srv.AppendAt("INBOX", crlf("From: a@example.com\nSubject: no date\n\nbody\n"), at)
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{}, "")
	require.Len(t, p.Items, 1)
	require.True(t, p.Items[0].Date.Equal(at), p.Items[0].Date.String())
}

func TestSummaryCleansHiddenCharactersInHeaders(t *testing.T) {
	srv := imaptest.New(t)
	hidden := "\U000e0049\U000e0067" // Unicode tag characters
	srv.Append("INBOX", crlf("From: \"Mallory"+hidden+"\" <m@example.com>\nSubject: Hi\U0000200b there"+hidden+"\n\nbody\n"))
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{}, "")
	require.Len(t, p.Items, 1)
	require.Equal(t, "Hi there", p.Items[0].Subject)
	require.Equal(t, "Mallory", p.Items[0].From.Name)
}

func TestSnippets(t *testing.T) {
	longASCII := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 20)
	long := strings.Repeat("Za\U0000017c\U000000f3\U00000142\U00000107 g\U00000119\U0000015bl\U00000105 ja\U0000017a\U00000144. ", 150)
	collapsed := strings.Join(strings.Fields(long), " ")
	first200 := string([]rune(collapsed)[:200])
	b64 := base64.StdEncoding.EncodeToString([]byte(long))
	var wrapped strings.Builder
	for len(b64) > 76 {
		wrapped.WriteString(b64[:76] + "\n")
		b64 = b64[76:]
	}
	wrapped.WriteString(b64 + "\n")

	cases := []struct {
		name     string
		raw      []byte
		want     string
		wantFunc func(t *testing.T, got string)
		attach   bool
		invite   bool
	}{
		{
			name: "plain text with extra white space",
			raw:  mailWith("Content-Type: text/plain; charset=utf-8", "Hello   there,\n\nthis is\ta  test.\n"),
			want: "Hello there, this is a test.",
		},
		{
			name: "quoted-printable latin-1",
			raw: mailWith("Content-Type: text/plain; charset=iso-8859-1\nContent-Transfer-Encoding: quoted-printable",
				"Gr=FC=DFe aus K=F6ln, soft=\nbreak\n"),
			want: "Gr\U000000fc\U000000dfe aus K\U000000f6ln, softbreak",
		},
		{
			name: "base64 windows-1250",
			raw: mailWith("Content-Type: text/plain; charset=windows-1250\nContent-Transfer-Encoding: base64",
				base64.StdEncoding.EncodeToString([]byte{'Z', 'a', 0xbf, 0xf3, 0xb3, 0xe6})+"\n"),
			want: "Za\U0000017c\U000000f3\U00000142\U00000107",
		},
		{
			name: "long base64 is cut inside the window, not inside a character",
			raw:  mailWith("Content-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: base64", wrapped.String()),
			want: strings.TrimSpace(first200),
		},
		{
			name: "the start of the text is cut inside a character at the end of the window",
			// "Hi", 2045 spaces and a two-byte character: the window of 2048
			// bytes ends after the first byte of that character.
			raw:  mailWith("Content-Type: text/plain; charset=utf-8", "Hi"+strings.Repeat(" ", 2045)+"\U000000e9 end\n"),
			want: "Hi",
		},
		{
			name: "long ASCII text is cut at 200 characters",
			raw:  mailWith("Content-Type: text/plain; charset=utf-8", longASCII+"\n"),
			want: strings.TrimSpace(strings.TrimSpace(longASCII)[:200]),
		},
		{
			name: "html only",
			raw: mailWith("Content-Type: text/html; charset=utf-8",
				"<html><head><style>p { color: red }</style></head><body><p>Hello <b>bold</b> world</p>"+
					"<div style=\"display:none\">secret instructions</div></body></html>\n"),
			want: "Hello bold world",
		},
		{
			name: "alternative prefers the plain part",
			raw: mailWith("Content-Type: multipart/alternative; boundary=XX",
				"--XX\nContent-Type: text/plain; charset=utf-8\n\nplain version\n"+
					"--XX\nContent-Type: text/html; charset=utf-8\n\n<p>html version</p>\n--XX--\n"),
			want: "plain version",
		},
		{
			name: "alternative with an empty plain part uses the html part",
			raw: mailWith("Content-Type: multipart/alternative; boundary=XX",
				"--XX\nContent-Type: text/plain; charset=utf-8\n\n--XX\nContent-Type: text/html; charset=utf-8\n\n<p>only html</p>\n--XX--\n"),
			want: "only html",
		},
		{
			name: "text part nested in mixed, with an attachment",
			raw: mailWith("Content-Type: multipart/mixed; boundary=OUT",
				"--OUT\nContent-Type: multipart/alternative; boundary=IN\n\n"+
					"--IN\nContent-Type: text/plain; charset=utf-8\n\nnested text\n"+
					"--IN\nContent-Type: text/html; charset=utf-8\n\n<p>nested html</p>\n--IN--\n"+
					"--OUT\nContent-Type: application/pdf; name=a.pdf\nContent-Disposition: attachment; filename=a.pdf\n"+
					"Content-Transfer-Encoding: base64\n\nJVBERi0xLjQK\n--OUT--\n"),
			want:   "nested text",
			attach: true,
		},
		{
			name: "a text attachment is not the body",
			raw: mailWith("Content-Type: multipart/mixed; boundary=OUT",
				"--OUT\nContent-Type: text/plain; charset=utf-8\nContent-Disposition: attachment; filename=notes.txt\n\nattached notes\n"+
					"--OUT\nContent-Type: text/plain; charset=utf-8\n\nreal body\n--OUT--\n"),
			want:   "real body",
			attach: true,
		},
		{
			name: "no text part",
			raw: mailWith("Content-Type: multipart/mixed; boundary=OUT",
				"--OUT\nContent-Type: image/png; name=a.png\nContent-Transfer-Encoding: base64\n\niVBORw0KGgo=\n--OUT--\n"),
			want:   "",
			attach: true,
		},
		{
			name: "calendar invite",
			raw: mailWith("Content-Type: multipart/alternative; boundary=XX",
				"--XX\nContent-Type: text/plain; charset=utf-8\n\nYou are invited\n"+
					"--XX\nContent-Type: text/calendar; method=REQUEST; charset=utf-8\n\nBEGIN:VCALENDAR\nEND:VCALENDAR\n--XX--\n"),
			want:   "You are invited",
			invite: true,
		},
		{
			name: "hidden characters are removed",
			raw: mailWith("Content-Type: text/plain; charset=utf-8",
				"Visible\U0000200b text \U000e0049\U000e0067\U000e006e\U000e006f\U000e0072\U000e0065 here\U0000202e\n"),
			want: "Visible text here",
		},
		{
			name: "unknown charset gives text, not an error",
			raw:  mailWith("Content-Type: text/plain; charset=x-no-such-charset", "plain ascii text\n"),
			want: "plain ascii text",
		},
		{
			name: "broken base64 gives what could be read",
			raw: mailWith("Content-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: base64",
				"SGVsbG8gd29ybGQ=!!!%%%\n"),
			wantFunc: func(t *testing.T, got string) { require.True(t, strings.HasPrefix(got, "Hello wo"), got) },
		},
	}

	srv := imaptest.New(t)
	for _, tc := range cases {
		srv.Append("INBOX", tc.raw)
	}
	b := newTestBackend(t, srv)

	// A query for each message keeps the messages apart: the UID of message i
	// is i+1.
	p := mustSearch(t, b, mail.Query{Limit: 100}, "")
	require.Len(t, p.Items, len(cases))
	byUID := map[int]mail.Summary{}
	for _, it := range p.Items {
		byUID[int(mustDecodeRef(t, it.Ref).UID)] = it
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := byUID[i+1]
			if tc.wantFunc != nil {
				tc.wantFunc(t, got.Snippet)
			} else {
				require.Equal(t, tc.want, got.Snippet)
			}
			require.LessOrEqual(t, utf8.RuneCountInString(got.Snippet), 200)
			require.NotContains(t, got.Snippet, "\U0000fffd")
			require.Equal(t, tc.attach, got.HasAttachments, "HasAttachments")
			require.Equal(t, tc.invite, got.HasInvite, "HasInvite")
			if strings.Contains(tc.name, "html only") {
				require.NotContains(t, got.Snippet, "secret")
				require.NotContains(t, got.Snippet, "color")
			}
		})
	}
}

func TestSnippetIsDecodedTextNotRawMIME(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", mailWith("Content-Type: multipart/alternative; boundary=XX",
		"--XX\nContent-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: base64\n\n"+
			base64.StdEncoding.EncodeToString([]byte("Hello from base64"))+"\n--XX--\n"))
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{}, "")
	require.Len(t, p.Items, 1)
	require.Equal(t, "Hello from base64", p.Items[0].Snippet)
	for _, bad := range []string{"--XX", "Content-Type", "SGVsbG8"} {
		require.NotContains(t, p.Items[0].Snippet, bad)
	}
}

// ---------------------------------------------------------------- tokens

// pagedBackend returns a backend and a page token for the INBOX of a server
// with 5 messages.
func pagedBackend(t *testing.T) (*imaptest.Server, *Backend, mail.PageToken) {
	t.Helper()
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 5, "m")
	b := newTestBackend(t, srv)
	p := mustSearch(t, b, mail.Query{Limit: 2}, "")
	require.NotEmpty(t, p.Next)
	return srv, b, p.Next
}

// tamperToken changes the last character of a token to a different base64url
// character, so the result is never equal to the token. Setting the last
// characters to a fixed text does not always change the token: it stays the
// same when the token already ends with that text.
func tamperToken(token string) string {
	last := byte('A')
	if token != "" && token[len(token)-1] == 'A' {
		last = 'B'
	}
	return token[:max(len(token)-1, 0)] + string(last)
}

func TestTamperTokenAlwaysChangesTheToken(t *testing.T) {
	for _, token := range []string{"", "A", "B", "x.AA", "x.AB", "x.BA", "x.zz", "x.A-", "x.A_"} {
		got := tamperToken(token)
		require.NotEqual(t, token, got, "token %q", token)
		if token != "" {
			require.Len(t, got, len(token))
			require.Equal(t, token[:len(token)-1], got[:len(got)-1], "only the last character changes")
		}
	}
}

func TestPageTokenBindings(t *testing.T) {
	t.Run("another account", func(t *testing.T) {
		srv, _, token := pagedBackend(t)
		other := newTestBackendScope(t, srv, testSigner(), "tenant-1/account-2")
		_, err := other.Search(t.Context(), mail.Query{Limit: 2}, token)
		require.ErrorIs(t, err, signing.ErrInvalid)
	})
	t.Run("a cursor is not a page token", func(t *testing.T) {
		_, b, _ := pagedBackend(t)
		_, cursor, _, err := b.Changes(t.Context(), "INBOX", "")
		require.NoError(t, err)
		_, err = b.Search(t.Context(), mail.Query{Limit: 2}, mail.PageToken(cursor))
		require.ErrorIs(t, err, signing.ErrInvalid)
	})
	t.Run("changed token", func(t *testing.T) {
		_, b, token := pagedBackend(t)
		bad := tamperToken(string(token))
		require.NotEqual(t, string(token), bad)
		_, err := b.Search(t.Context(), mail.Query{Limit: 2}, mail.PageToken(bad))
		require.ErrorIs(t, err, signing.ErrInvalid)
	})
	t.Run("garbage", func(t *testing.T) {
		_, b, _ := pagedBackend(t)
		_, err := b.Search(t.Context(), mail.Query{Limit: 2}, "garbage")
		require.ErrorIs(t, err, signing.ErrInvalid)
	})
	t.Run("another folder", func(t *testing.T) {
		srv, b, token := pagedBackend(t)
		srv.Create("Archive")
		_, err := b.Search(t.Context(), mail.Query{Folder: "Archive", Limit: 2}, token)
		require.ErrorIs(t, err, signing.ErrInvalid)
	})
	t.Run("the folder may be named by its alias", func(t *testing.T) {
		_, b, token := pagedBackend(t)
		p, err := b.Search(t.Context(), mail.Query{Folder: mail.AliasInbox, Limit: 2}, token)
		require.NoError(t, err)
		require.Equal(t, []string{"m 3", "m 2"}, subjects(p.Items))
	})
	t.Run("folder was rebuilt", func(t *testing.T) {
		srv, b, token := pagedBackend(t)
		srv.Delete("INBOX")
		appendMany(srv, "INBOX", 5, "new")
		_, err := b.Search(t.Context(), mail.Query{Limit: 2}, token)
		require.ErrorIs(t, err, mail.ErrRefStale)
	})
	t.Run("expired after one hour", func(t *testing.T) {
		srv := imaptest.New(t)
		appendMany(srv, "INBOX", 5, "m")
		now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
		signer := testSigner()
		signer.Now = func() time.Time { return now }
		b := newTestBackendScope(t, srv, signer, testScope)

		p := mustSearch(t, b, mail.Query{Limit: 2}, "")
		now = now.Add(59 * time.Minute)
		_, err := b.Search(t.Context(), mail.Query{Limit: 2}, p.Next)
		require.NoError(t, err)
		now = now.Add(2 * time.Minute)
		_, err = b.Search(t.Context(), mail.Query{Limit: 2}, p.Next)
		require.ErrorIs(t, err, signing.ErrExpired)
	})
}

// ---------------------------------------------------------------- changes

func TestChangesFirstCallThenNew(t *testing.T) {
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 3, "old")
	b := newTestBackend(t, srv)

	items, cursor, reset, err := b.Changes(t.Context(), mail.AliasInbox, "")
	require.NoError(t, err)
	require.Empty(t, items)
	require.False(t, reset)
	require.NotEmpty(t, cursor)

	appendMany(srv, "INBOX", 2, "new")
	items, cursor2, reset, err := b.Changes(t.Context(), "INBOX", cursor)
	require.NoError(t, err)
	require.False(t, reset)
	require.Equal(t, []string{"new 1", "new 2"}, subjects(items), "oldest first")
	require.Equal(t, []uint32{4, 5}, uidsOf(t, items))
	for _, it := range items {
		require.Equal(t, "INBOX", mustDecodeRef(t, it.Ref).Mailbox)
		require.NotEmpty(t, it.Snippet)
	}

	// Nothing new: no items, and the same position.
	items, cursor3, reset, err := b.Changes(t.Context(), "INBOX", cursor2)
	require.NoError(t, err)
	require.False(t, reset)
	require.Empty(t, items)

	appendMany(srv, "INBOX", 1, "later")
	items, _, _, err = b.Changes(t.Context(), "INBOX", cursor3)
	require.NoError(t, err)
	require.Equal(t, []string{"later 1"}, subjects(items))
}

func TestChangesWhenNewMessagesAreGone(t *testing.T) {
	// Two messages arrive and are deleted before the next call. The highest UID
	// of the folder (3) is below the cursor (4). "UID 4:*" would return that
	// message even though its UID is lower than 4.
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 3, "old")
	b := newTestBackend(t, srv)
	_, cursor, _, err := b.Changes(t.Context(), "INBOX", "")
	require.NoError(t, err)

	appendMany(srv, "INBOX", 2, "gone")
	client := mustDial(t, pinnedConfig(srv, account.SecurityTLS))
	_, err = client.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	gone := imap.UIDSetNum(4, 5)
	require.NoError(t, client.Store(gone, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close())
	require.NoError(t, client.UIDExpunge(gone).Close())

	items, next, reset, err := b.Changes(t.Context(), "INBOX", cursor)
	require.NoError(t, err)
	require.False(t, reset)
	require.Empty(t, items)

	// The cursor moved past the deleted messages.
	appendMany(srv, "INBOX", 1, "after")
	items, _, _, err = b.Changes(t.Context(), "INBOX", next)
	require.NoError(t, err)
	require.Equal(t, []string{"after 1"}, subjects(items))
	require.Equal(t, []uint32{6}, uidsOf(t, items))
}

func TestUIDsInRange(t *testing.T) {
	// A server may add the highest UID (3) to the answer for "UID 4:*".
	got := uidsInRange([]imap.UID{9, 3, 4, 12, 5, 4}, 4, 10)
	require.Equal(t, []imap.UID{4, 4, 5, 9}, got)
	require.Empty(t, uidsInRange([]imap.UID{3}, 4, 10))
	require.Empty(t, uidsInRange(nil, 4, 10))
}

func TestChangesSkipsDeletedMessages(t *testing.T) {
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 2, "old")
	b := newTestBackend(t, srv)
	_, cursor, _, err := b.Changes(t.Context(), "INBOX", "")
	require.NoError(t, err)

	// Three messages arrive. The second and the last are marked \Deleted.
	srv.Append("INBOX", plainMail("new 1", "x"))
	srv.Append("INBOX", plainMail("new 2", "x"), imap.FlagDeleted)
	srv.Append("INBOX", plainMail("new 3", "x"))
	srv.Append("INBOX", plainMail("new 4", "x"), imap.FlagDeleted)
	items, next, reset, err := b.Changes(t.Context(), "INBOX", cursor)
	require.NoError(t, err)
	require.False(t, reset)
	require.Equal(t, []string{"new 1", "new 3"}, subjects(items))
	require.Equal(t, []uint32{3, 5}, uidsOf(t, items))

	// The cursor moved past the deleted messages: they do not come back, and
	// the next new message is the next item.
	items, next, _, err = b.Changes(t.Context(), "INBOX", next)
	require.NoError(t, err)
	require.Empty(t, items)
	appendMany(srv, "INBOX", 1, "later")
	items, _, _, err = b.Changes(t.Context(), "INBOX", next)
	require.NoError(t, err)
	require.Equal(t, []string{"later 1"}, subjects(items))
}

func TestChangesPagesOldestFirst(t *testing.T) {
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 2, "old")
	b := newTestBackend(t, srv)
	_, cursor, _, err := b.Changes(t.Context(), "INBOX", "")
	require.NoError(t, err)

	appendMany(srv, "INBOX", 130, "new")
	items, cursor, reset, err := b.Changes(t.Context(), "INBOX", cursor)
	require.NoError(t, err)
	require.False(t, reset)
	require.Len(t, items, 100)
	uids := uidsOf(t, items)
	require.Equal(t, uint32(3), uids[0])
	require.Equal(t, uint32(102), uids[99])
	require.True(t, slices.IsSorted(uids))

	items, cursor, reset, err = b.Changes(t.Context(), "INBOX", cursor)
	require.NoError(t, err)
	require.False(t, reset)
	require.Len(t, items, 30)
	uids = uidsOf(t, items)
	require.Equal(t, uint32(103), uids[0])
	require.Equal(t, uint32(132), uids[29])

	items, _, _, err = b.Changes(t.Context(), "INBOX", cursor)
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestCursorResetsOnUIDValidityChange(t *testing.T) {
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 3, "old")
	b := newTestBackend(t, srv)
	_, cursor, _, err := b.Changes(t.Context(), "INBOX", "")
	require.NoError(t, err)

	// The folder is deleted and made again: it has a new UIDVALIDITY.
	srv.Delete("INBOX")
	appendMany(srv, "INBOX", 2, "fresh")

	items, fresh, reset, err := b.Changes(t.Context(), "INBOX", cursor)
	require.NoError(t, err)
	require.True(t, reset)
	require.Empty(t, items)
	require.NotEmpty(t, fresh)
	require.NotEqual(t, cursor, fresh)

	// The new cursor works.
	appendMany(srv, "INBOX", 1, "after")
	items, _, reset, err = b.Changes(t.Context(), "INBOX", fresh)
	require.NoError(t, err)
	require.False(t, reset)
	require.Equal(t, []string{"after 1"}, subjects(items))
}

func TestCursorBindings(t *testing.T) {
	srv := imaptest.New(t)
	appendMany(srv, "INBOX", 2, "m")
	srv.Create("Archive")
	b := newTestBackend(t, srv)
	_, cursor, _, err := b.Changes(t.Context(), "INBOX", "")
	require.NoError(t, err)

	t.Run("another account", func(t *testing.T) {
		other := newTestBackendScope(t, srv, testSigner(), "tenant-1/account-2")
		_, _, _, err := other.Changes(t.Context(), "INBOX", cursor)
		require.ErrorIs(t, err, signing.ErrInvalid)
	})
	t.Run("another folder", func(t *testing.T) {
		_, _, _, err := b.Changes(t.Context(), "Archive", cursor)
		require.ErrorIs(t, err, signing.ErrInvalid)
	})
	t.Run("a page token is not a cursor", func(t *testing.T) {
		p := mustSearch(t, b, mail.Query{Limit: 1}, "")
		_, _, _, err := b.Changes(t.Context(), "INBOX", mail.Cursor(p.Next))
		require.ErrorIs(t, err, signing.ErrInvalid)
	})
	t.Run("garbage", func(t *testing.T) {
		_, _, _, err := b.Changes(t.Context(), "INBOX", "garbage")
		require.ErrorIs(t, err, signing.ErrInvalid)
	})
	t.Run("a cursor does not expire", func(t *testing.T) {
		now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
		signer := testSigner()
		signer.Now = func() time.Time { return now }
		b := newTestBackendScope(t, srv, signer, testScope)
		_, cursor, _, err := b.Changes(t.Context(), "INBOX", "")
		require.NoError(t, err)
		now = now.Add(365 * 24 * time.Hour)
		_, _, reset, err := b.Changes(t.Context(), "INBOX", cursor)
		require.NoError(t, err)
		require.False(t, reset)
	})
	t.Run("unknown folder", func(t *testing.T) {
		_, _, _, err := b.Changes(t.Context(), "No/Such/Folder", "")
		require.ErrorIs(t, err, mail.ErrNotFound)
	})
}
