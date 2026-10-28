package imapsmtp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
)

// The test server (imapmemserver) writes every flag in lower case, so a tag
// "Urgent" is "urgent" on the server and in the summaries. The tests use lower
// case names where they compare.

// ---------------------------------------------------------------- helpers

// srvMsg is a message as the server holds it.
type srvMsg struct {
	UID     uint32
	Subject string
	Flags   []string
}

// has reports whether the message has the flag. Flags are not case sensitive.
func (m srvMsg) has(flag string) bool {
	return slices.ContainsFunc(m.Flags, func(f string) bool { return strings.EqualFold(f, flag) })
}

// lowerFlags returns the flags of the message in lower case, because the
// server does not keep the case of a flag.
func (m srvMsg) lowerFlags() []string {
	out := make([]string, len(m.Flags))
	for i, f := range m.Flags {
		out[i] = strings.ToLower(f)
	}
	return out
}

// serverMessages returns the messages of a mailbox, lowest UID first. It
// reads the state of the server on its own connection, so it does not depend
// on the code under test (and it sees messages with the \Deleted flag).
func serverMessages(t *testing.T, srv *imaptest.Server, mailbox string) []srvMsg {
	t.Helper()
	c := mustDial(t, pinnedConfig(srv, account.SecurityTLS))
	sel, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	require.NoError(t, err)
	if sel.NumMessages == 0 {
		return nil
	}
	msgs, err := c.Fetch(imap.UIDSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{
		UID: true, Flags: true, Envelope: true,
	}).Collect()
	require.NoError(t, err)
	out := make([]srvMsg, 0, len(msgs))
	for _, m := range msgs {
		sm := srvMsg{UID: uint32(m.UID)}
		if m.Envelope != nil {
			sm.Subject = m.Envelope.Subject
		}
		for _, f := range m.Flags {
			sm.Flags = append(sm.Flags, string(f))
		}
		out = append(out, sm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out
}

// serverSubjects returns the subjects of the messages of a mailbox, lowest UID
// first.
func serverSubjects(t *testing.T, srv *imaptest.Server, mailbox string) []string {
	t.Helper()
	var out []string
	for _, m := range serverMessages(t, srv, mailbox) {
		out = append(out, m.Subject)
	}
	return out
}

// serverMsgBySubject returns the one message with the subject.
func serverMsgBySubject(t *testing.T, srv *imaptest.Server, mailbox, subject string) srvMsg {
	t.Helper()
	var found []srvMsg
	for _, m := range serverMessages(t, srv, mailbox) {
		if m.Subject == subject {
			found = append(found, m)
		}
	}
	require.Len(t, found, 1, "the messages of %q with the subject %q", mailbox, subject)
	return found[0]
}

// refsBySubject returns the refs of the messages of a mailbox, by subject.
// Messages with the \Deleted flag are not in a search, so they are not here.
func refsBySubject(t *testing.T, b *Backend, mailbox string) map[string]mail.Ref {
	t.Helper()
	out := map[string]mail.Ref{}
	for _, it := range mustSearch(t, b, mail.Query{Folder: mailbox, Limit: 100}, "").Items {
		out[it.Subject] = it.Ref
	}
	return out
}

// withoutWildcard is a WithPermanentFlags function for a server that does not
// let clients make new keywords.
func withoutWildcard(_ string, _ bool, flags []imap.Flag) []imap.Flag {
	var out []imap.Flag
	for _, f := range flags {
		if f != imap.FlagWildcard {
			out = append(out, f)
		}
	}
	return out
}

// uidPlusOnly and neitherMoveNorUIDPlus make servers for the fallback of Move.
// Both are IMAP4rev1 servers, because IMAP4rev2 implies MOVE and UIDPLUS. They
// list the extensions that folder aliases need.
func uidPlusOnly() imaptest.Option {
	return imaptest.WithCaps(imap.CapIMAP4rev1, imap.CapUIDPlus, imap.CapListExtended, imap.CapSpecialUse)
}

func neitherMoveNorUIDPlus() imaptest.Option {
	return imaptest.WithCaps(imap.CapIMAP4rev1, imap.CapListExtended, imap.CapSpecialUse)
}

// requireNoMoveCap checks that the test server really does not advertise
// MOVE. Otherwise a test of the fallback would test the wrong thing.
func requireNoMoveCap(t *testing.T, b *Backend) {
	t.Helper()
	require.NoError(t, b.pool.with(t.Context(), func(c *imapclient.Client) error {
		require.False(t, c.Caps().Has(imap.CapMove), "the server must not advertise MOVE")
		return nil
	}))
}

// rwSelects returns how many read-write SELECT commands the server got.
func rwSelects(srv *imaptest.Server) int {
	rw, _ := srv.SelectCounts()
	return rw
}

// makeStale makes the refs of a mailbox stale: the mailbox is made again, so it
// has a new UIDVALIDITY. The messages are the ones given.
func makeStale(t *testing.T, srv *imaptest.Server, mailbox string, raws ...[]byte) {
	t.Helper()
	srv.Delete(mailbox)
	for _, raw := range raws {
		srv.Append(mailbox, raw)
	}
}

// expungeOnAnotherClient removes the message with the UID for good, as
// another mail client would.
func expungeOnAnotherClient(t *testing.T, srv *imaptest.Server, mailbox string, uid uint32) {
	t.Helper()
	other := mustDial(t, pinnedConfig(srv, account.SecurityTLS))
	_, err := other.Select(mailbox, nil).Wait()
	require.NoError(t, err)
	require.NoError(t, other.Store(imap.UIDSetNum(imap.UID(uid)),
		&imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close())
	require.NoError(t, other.Expunge().Close())
}

// ---------------------------------------------------------------- tag names

func TestIsTag(t *testing.T) {
	valid := []string{
		"project", "Urgent", "a", "A-b_C-1", "2026", "-", "_",
		strings.Repeat("a", 64),
		// The name of a system flag without the backslash is a plain keyword.
		"Seen", "Deleted",
	}
	for _, name := range valid {
		require.True(t, isTag(name), "%q", name)
	}

	invalid := []string{
		"", strings.Repeat("a", 65),
		"has space", "tab\t", "new\nline", "tag\n", "dot.ted", "a/b", "caf\U000000e9",
		"(paren)", "quote\"d", "star*", "pct%",
		`\Deleted`, `\Seen`, `\*`, `\Recent`,
		// A keyword that starts with $ is set by a mail client for its own use.
		"$Forwarded", "$label1", "$MDNSent", "$Junk", "$NotJunk", "$project",
		// The marks of spam filters, in any case.
		"Junk", "junk", "JUNK", "NonJunk", "nonjunk", "NotJunk", "NOTJUNK",
	}
	for _, name := range invalid {
		require.False(t, isTag(name), "%q", name)
	}
}

func TestCreateTagValidatesName(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)

	got, err := b.CreateTag(t.Context(), "Invoices-2026")
	require.NoError(t, err)
	require.Equal(t, mail.Tag{Name: "Invoices-2026"}, got)
	got, err = b.CreateTag(t.Context(), strings.Repeat("a", 64))
	require.NoError(t, err)
	require.Len(t, got.Name, 64)

	for _, name := range []string{
		"Invoices 2026", "", strings.Repeat("a", 65), "caf\U000000e9", "tag\n",
		`\Deleted`, "$Forwarded",
	} {
		got, err := b.CreateTag(t.Context(), name)
		require.ErrorIs(t, err, mail.ErrUnsupported, "%q", name)
		require.ErrorContains(t, err, "tag names on this account may only use letters, digits, - and _", "%q", name)
		require.Equal(t, mail.Tag{}, got)
	}
	for _, name := range []string{"Junk", "nonjunk", "NOTJUNK"} {
		_, err := b.CreateTag(t.Context(), name)
		require.ErrorIs(t, err, mail.ErrUnsupported, "%q", name)
		require.ErrorContains(t, err, "reserved", "%q", name)
	}

	// IMAP keywords exist when they are first used, so there is nothing to ask
	// the server.
	require.Zero(t, srv.TotalConns(), "CreateTag must not contact the server")
}

// ---------------------------------------------------------------- tags in summaries and searches

func TestSummaryTagsAreOnlyRealTags(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("m", "x"), imap.FlagSeen, imap.FlagFlagged,
		"project", "Urgent", "has_underscore",
		// Keywords that mail clients and spam filters set. They are not tags.
		"$Forwarded", "$label1", "$MDNSent", "Junk", "NonJunk", "NotJunk",
		// A valid IMAP keyword, but not a valid tag name.
		"with.dot")
	b := newTestBackend(t, srv)

	p := mustSearch(t, b, mail.Query{}, "")
	require.Len(t, p.Items, 1)
	require.Equal(t, []string{"has_underscore", "project", "urgent"}, p.Items[0].Tags)

	msg := mustGet(t, b, p.Items[0].Ref, mail.BodyOptions{Format: mail.BodyNone})
	require.Equal(t, []string{"has_underscore", "project", "urgent"}, msg.Tags)
}

func TestSearchTagRules(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("forwarded", "x"), "$Forwarded")
	srv.Append("INBOX", plainMail("junk", "x"), "Junk")
	srv.Append("INBOX", plainMail("both", "x"), "$label1", "NonJunk")
	srv.Append("INBOX", plainMail("project", "x"), "project", "$Forwarded")
	srv.Append("INBOX", plainMail("plain", "x"))
	b := newTestBackend(t, srv)

	cases := []struct {
		name string
		q    mail.Query
		want []string
	}{
		// System keywords are not tags, so they do not stop a message from
		// being untagged.
		{"untagged", mail.Query{Untagged: true}, []string{"plain", "both", "junk", "forwarded"}},
		{"a real tag", mail.Query{TagsAny: []string{"project"}}, []string{"project"}},
		// A name that is not a tag matches nothing, also when the keyword is on
		// a message.
		{"dollar keyword in TagsAny", mail.Query{TagsAny: []string{"$Forwarded"}}, nil},
		{"junk in TagsAny", mail.Query{TagsAny: []string{"Junk"}}, nil},
		{"invalid name beside a valid one", mail.Query{TagsAny: []string{"$Forwarded", "project"}}, []string{"project"}},
		{"a name with a space", mail.Query{TagsAny: []string{"has space"}}, nil},
		// In TagsNone, a name that is not a tag changes nothing.
		{"dollar keyword in TagsNone", mail.Query{TagsNone: []string{"$Forwarded"}}, []string{"plain", "project", "both", "junk", "forwarded"}},
		{"real tag in TagsNone", mail.Query{TagsNone: []string{"project", "$Forwarded"}}, []string{"plain", "both", "junk", "forwarded"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustSearch(t, b, tc.q, "")
			require.Equal(t, tc.want, append([]string(nil), subjects(p.Items)...))
		})
	}
}

// ---------------------------------------------------------------- capabilities and tags list

func TestCapabilities(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)

	caps := b.Capabilities(t.Context())
	require.True(t, caps.Tags)
	require.Equal(t, 500, caps.MaxBatch)

	// The answer is cached: the server is asked once.
	before := rwSelects(srv)
	require.Equal(t, caps, b.Capabilities(t.Context()))
	require.Equal(t, before, rwSelects(srv))
	require.Equal(t, 1, srv.TotalConns())
}

func TestCapabilitiesWithoutPermanentKeywords(t *testing.T) {
	srv := imaptest.New(t, imaptest.WithPermanentFlags(withoutWildcard))
	b := newTestBackend(t, srv)

	caps := b.Capabilities(t.Context())
	require.False(t, caps.Tags)
	require.Equal(t, 500, caps.MaxBatch)

	// An answer of a read-write SELECT is final: it is cached too.
	before := rwSelects(srv)
	require.Equal(t, caps, b.Capabilities(t.Context()))
	require.Equal(t, before, rwSelects(srv))
}

func TestCapabilitiesLearnsFromAnExamineOfTheInbox(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("m", "x"))
	b := newTestBackend(t, srv)

	// A search opens the inbox read-only. The server reports \* for it, which
	// is enough to know that tags work. No second command is needed.
	mustSearch(t, b, mail.Query{}, "")
	supported, known := b.tags.get()
	require.True(t, known)
	require.True(t, supported)

	require.True(t, b.Capabilities(t.Context()).Tags)
	require.Zero(t, rwSelects(srv), "nothing was opened read-write")
}

func TestCapabilitiesEmptyPermanentFlagsOfExamineAreNotAnAnswer(t *testing.T) {
	// Some servers (Dovecot is one) report no permanent flags for a mailbox
	// that is open read-only, because a read-only mailbox cannot change. That
	// says nothing about keywords. The backend must not cache "no tags" from
	// it.
	srv := imaptest.New(t, imaptest.WithPermanentFlags(func(_ string, readOnly bool, flags []imap.Flag) []imap.Flag {
		if readOnly {
			return nil
		}
		return flags
	}))
	srv.Append("INBOX", plainMail("m", "x"))
	b := newTestBackend(t, srv)

	mustSearch(t, b, mail.Query{}, "")
	_, known := b.tags.get()
	require.False(t, known, "an empty list from EXAMINE is not an answer")

	require.True(t, b.Capabilities(t.Context()).Tags)
	require.Equal(t, 1, rwSelects(srv), "one read-write SELECT of the inbox answered it")

	// Tags work on that server.
	ref := refsBySubject(t, b, "INBOX")["m"]
	require.NoError(t, b.Tag(t.Context(), []mail.Ref{ref}, []string{"project"}, nil))
	require.True(t, serverMsgBySubject(t, srv, "INBOX", "m").has("project"))
}

func TestCapabilitiesNoWildcardFromExamineDoesNotDecide(t *testing.T) {
	srv := imaptest.New(t, imaptest.WithPermanentFlags(withoutWildcard))
	srv.Append("INBOX", plainMail("m", "x"))
	b := newTestBackend(t, srv)

	mustSearch(t, b, mail.Query{}, "")
	_, known := b.tags.get()
	require.False(t, known)
	require.False(t, b.Capabilities(t.Context()).Tags)
	_, known = b.tags.get()
	require.True(t, known, "a read-write SELECT decides")
}

func TestCapabilitiesProbeFailureIsNotCached(t *testing.T) {
	// Nothing listens on this port, so the connection is refused at once.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closedPort := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	up := imaptest.New(t)
	cfg := pinnedConfig(up, account.SecurityTLS)
	cfg.TokenScope = testScope
	good := cfg
	cfg.Server.IMAPPort = closedPort
	b := New(cfg, testSigner())
	t.Cleanup(func() { _ = b.Close() })

	// The server cannot be reached. The answer is "no tags", and it is not
	// remembered.
	require.False(t, b.Capabilities(t.Context()).Tags)
	_, known := b.tags.get()
	require.False(t, known)

	// Later the server can be reached, and the next call gives the right
	// answer.
	b.pool.Close()
	b.pool = newPool(good)
	require.True(t, b.Capabilities(t.Context()).Tags)
}

// silentServer returns the host and port of a TCP server that accepts
// connections and never sends a byte. A client that waits for its greeting or
// for the TLS handshake waits for ever.
func silentServer(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// Capabilities may ask the server. The question must end when the context of
// the caller ends, and it must not wait for the dial timeout (15 seconds).
func TestCapabilitiesHonoursTheContext(t *testing.T) {
	host, port := silentServer(t)
	cfg := baseConfig()
	cfg.Server.IMAPHost = host
	cfg.Server.IMAPPort = port
	cfg.Server.IMAPSecurity = account.SecurityTLS
	cfg.TokenScope = testScope
	b := New(cfg, testSigner())
	t.Cleanup(func() { _ = b.Close() })

	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(100*time.Millisecond, cancel)

		start := time.Now()
		caps := b.Capabilities(ctx)

		require.Less(t, time.Since(start), 3*time.Second, "the cancel must end the question")
		require.False(t, caps.Tags)
		require.Equal(t, maxBatch, caps.MaxBatch)
		_, known := b.tags.get()
		require.False(t, known, "a failed question is not remembered")
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()

		start := time.Now()
		caps := b.Capabilities(ctx)

		require.Less(t, time.Since(start), 3*time.Second, "the deadline must end the question")
		require.False(t, caps.Tags)
	})
}

func TestCapabilitiesIsSafeForManyGoroutines(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)

	done := make(chan mail.Capabilities, 8)
	for range 8 {
		go func() { done <- b.Capabilities(t.Context()) }()
	}
	for range 8 {
		require.True(t, (<-done).Tags)
	}
}

func TestListTags(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("a", "x"), imap.FlagSeen, imap.FlagFlagged,
		"project", "Urgent", "$Forwarded", "Junk", "NonJunk", "NotJunk", "$label1", "with.dot")
	srv.Append("INBOX", plainMail("b", "x"), "project", "alpha_2")
	b := newTestBackend(t, srv)

	tags, err := b.ListTags(t.Context())
	require.NoError(t, err)
	require.Equal(t, []mail.Tag{{Name: "alpha_2"}, {Name: "project"}, {Name: "urgent"}}, tags)
}

func TestListTagsOfAnInboxWithoutKeywords(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("a", "x"), imap.FlagSeen, "$Forwarded")
	b := newTestBackend(t, srv)

	tags, err := b.ListTags(t.Context())
	require.NoError(t, err)
	require.NotNil(t, tags)
	require.Empty(t, tags)
}

func TestListTagsUsesAReadOnlyInboxOnceTheAnswerIsKnown(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("a", "x"), "project")
	b := newTestBackend(t, srv)
	require.True(t, b.Capabilities(t.Context()).Tags)
	before := rwSelects(srv)

	tags, err := b.ListTags(t.Context())
	require.NoError(t, err)
	require.Equal(t, []mail.Tag{{Name: "project"}}, tags)
	require.Equal(t, before, rwSelects(srv))
}

// ---------------------------------------------------------------- flags

func TestSetFlags(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("one", "x"), "project")
	srv.Append("INBOX", plainMail("two", "x"), imap.FlagFlagged)
	srv.Append("INBOX", plainMail("three", "x"), imap.FlagSeen, imap.FlagDeleted)
	b := newTestBackend(t, srv)
	refs := refsBySubject(t, b, "INBOX")
	one, two := refs["one"], refs["two"]
	ctx := t.Context()

	// Seen on two messages.
	require.NoError(t, b.SetFlags(ctx, []mail.Ref{one, two}, mail.FlagChange{Seen: boolPtr(true)}))
	require.True(t, serverMsgBySubject(t, srv, "INBOX", "one").has(`\Seen`))
	require.True(t, serverMsgBySubject(t, srv, "INBOX", "two").has(`\Seen`))
	require.False(t, serverMsgBySubject(t, srv, "INBOX", "one").has(`\Flagged`))

	// Flagged on one. A nil field leaves that flag as it is.
	require.NoError(t, b.SetFlags(ctx, []mail.Ref{one}, mail.FlagChange{Flagged: boolPtr(true)}))
	m := serverMsgBySubject(t, srv, "INBOX", "one")
	require.True(t, m.has(`\Seen`) && m.has(`\Flagged`))

	// Both off, in one call.
	require.NoError(t, b.SetFlags(ctx, []mail.Ref{one, two}, mail.FlagChange{Seen: boolPtr(false), Flagged: boolPtr(false)}))
	for _, subject := range []string{"one", "two"} {
		m := serverMsgBySubject(t, srv, "INBOX", subject)
		require.False(t, m.has(`\Seen`) || m.has(`\Flagged`), subject)
	}

	// The other flags are not touched: the keyword, and the message that was
	// not in any call (with its \Deleted and \Seen).
	require.True(t, serverMsgBySubject(t, srv, "INBOX", "one").has("project"))
	three := serverMsgBySubject(t, srv, "INBOX", "three")
	require.True(t, three.has(`\Seen`) && three.has(`\Deleted`))

	// The summaries show it.
	p := mustSearch(t, b, mail.Query{Limit: 10}, "")
	for _, it := range p.Items {
		require.False(t, it.Seen || it.Flagged, it.Subject)
	}
}

func TestSetFlagsWithNothingToChangeDoesNotContactTheServer(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)
	ref := encodeRef(imapRef{Mailbox: "INBOX", UIDValidity: 1, UID: 1})

	require.NoError(t, b.SetFlags(t.Context(), []mail.Ref{ref}, mail.FlagChange{}))
	require.NoError(t, b.SetFlags(t.Context(), nil, mail.FlagChange{Seen: boolPtr(true)}))
	require.Zero(t, srv.TotalConns())

	// A ref that cannot be one is still refused.
	err := b.SetFlags(t.Context(), []mail.Ref{"not a ref"}, mail.FlagChange{})
	require.ErrorIs(t, err, mail.ErrNotFound)
}

// ---------------------------------------------------------------- tags

func TestTagAddRemove(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("one", "x"), imap.FlagSeen, "old", "keep")
	srv.Append("INBOX", plainMail("two", "x"), "old")
	srv.Append("INBOX", plainMail("three", "x"), "old")
	b := newTestBackend(t, srv)
	refs := refsBySubject(t, b, "INBOX")
	ctx := t.Context()

	require.NoError(t, b.Tag(ctx, []mail.Ref{refs["one"], refs["two"]}, []string{"project", "Urgent-1"}, []string{"old"}))

	one := serverMsgBySubject(t, srv, "INBOX", "one")
	require.True(t, one.has("project") && one.has("urgent-1") && one.has("keep"))
	require.False(t, one.has("old"))
	require.True(t, one.has(`\Seen`), "system flags are not touched")
	two := serverMsgBySubject(t, srv, "INBOX", "two")
	require.True(t, two.has("project") && two.has("urgent-1"))
	require.False(t, two.has("old"))
	three := serverMsgBySubject(t, srv, "INBOX", "three")
	require.True(t, three.has("old"), "a message that was not in the call is not touched")
	require.False(t, three.has("project"))

	// The summaries show the tags.
	p := mustSearch(t, b, mail.Query{TagsAny: []string{"project"}}, "")
	require.ElementsMatch(t, []string{"one", "two"}, subjects(p.Items))
	for _, it := range p.Items {
		if it.Subject == "one" {
			require.Equal(t, []string{"keep", "project", "urgent-1"}, it.Tags)
		}
	}

	// Only removing.
	require.NoError(t, b.Tag(ctx, []mail.Ref{refs["one"]}, nil, []string{"project", "keep"}))
	one = serverMsgBySubject(t, srv, "INBOX", "one")
	require.False(t, one.has("project") || one.has("keep"))
	require.True(t, one.has("urgent-1"))

	// A name in both lists is removed first and then added: it ends up on the
	// message.
	require.NoError(t, b.Tag(ctx, []mail.Ref{refs["three"]}, []string{"x"}, []string{"x"}))
	require.True(t, serverMsgBySubject(t, srv, "INBOX", "three").has("x"))

	// Names that are twice in a list are fine.
	require.NoError(t, b.Tag(ctx, []mail.Ref{refs["three"]}, []string{"dup", "DUP"}, nil))
	require.True(t, serverMsgBySubject(t, srv, "INBOX", "three").has("dup"))
}

func TestTagWithNothingToDoChangesNothing(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("one", "x"), "keep")
	b := newTestBackend(t, srv)
	ref := refsBySubject(t, b, "INBOX")["one"]
	before := rwSelects(srv)

	require.NoError(t, b.Tag(t.Context(), []mail.Ref{ref}, nil, nil))
	require.Equal(t, before, rwSelects(srv))
	require.True(t, serverMsgBySubject(t, srv, "INBOX", "one").has("keep"))
}

func TestTagUnsupportedWithoutPermanentKeywords(t *testing.T) {
	srv := imaptest.New(t, imaptest.WithPermanentFlags(withoutWildcard))
	srv.Append("INBOX", plainMail("one", "x"), "old")
	b := newTestBackend(t, srv)
	ref := refsBySubject(t, b, "INBOX")["one"]

	require.False(t, b.Capabilities(t.Context()).Tags)

	err := b.Tag(t.Context(), []mail.Ref{ref}, []string{"project"}, nil)
	require.ErrorIs(t, err, mail.ErrUnsupported)
	err = b.Tag(t.Context(), []mail.Ref{ref}, nil, []string{"old"})
	require.ErrorIs(t, err, mail.ErrUnsupported)
	_, err = b.ListTags(t.Context())
	require.ErrorIs(t, err, mail.ErrUnsupported)

	m := serverMsgBySubject(t, srv, "INBOX", "one")
	require.True(t, m.has("old"), "nothing was removed")
	require.False(t, m.has("project"), "nothing was added")
}

func TestTagRefusesSystemFlags(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("one", "x"), imap.FlagSeen, imap.FlagFlagged)
	b := newTestBackend(t, srv)
	refs := []mail.Ref{refsBySubject(t, b, "INBOX")["one"]}
	ctx := t.Context()
	before := rwSelects(srv)

	bad := []string{
		// System flags. A tag named \Deleted is a delete in disguise.
		`\Deleted`, `\Seen`, `\Flagged`, `\Draft`, `\Answered`, `\Recent`, `\*`,
		// Keywords that are not tags.
		"Junk", "NonJunk", "NotJunk", "notjunk", "$Forwarded", "$label1", "$MDNSent",
		// Names that are not valid.
		"", "has space", "a.b", strings.Repeat("a", 65),
		// A try to add a second command to the line.
		"tag\r\nA1 UID EXPUNGE 1:*", "x) \\Deleted (y",
	}
	for _, name := range bad {
		err := b.Tag(ctx, refs, []string{name}, nil)
		require.ErrorIs(t, err, mail.ErrUnsupported, "add %q", name)
		err = b.Tag(ctx, refs, nil, []string{name})
		require.ErrorIs(t, err, mail.ErrUnsupported, "remove %q", name)
		// One bad name stops the whole call, also beside good ones.
		err = b.Tag(ctx, refs, []string{"good", name}, []string{"other"})
		require.ErrorIs(t, err, mail.ErrUnsupported, "add good and %q", name)
	}

	m := serverMsgBySubject(t, srv, "INBOX", "one")
	require.ElementsMatch(t, []string{`\seen`, `\flagged`}, m.lowerFlags(), "nothing changed on the server")
	require.Equal(t, before, rwSelects(srv), "no command that changes a message was sent")
}

func TestTagRefusedNameDoesNotContactTheServer(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)
	ref := encodeRef(imapRef{Mailbox: "INBOX", UIDValidity: 1, UID: 1})

	err := b.Tag(t.Context(), []mail.Ref{ref}, []string{`\Deleted`}, nil)
	require.ErrorIs(t, err, mail.ErrUnsupported)
	require.Zero(t, srv.TotalConns())
}

// ---------------------------------------------------------------- move

func TestMoveReturnsNewRefs(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Archive")
	srv.Append("INBOX", plainMail("first", "body one"), imap.FlagSeen, "project")
	srv.Append("INBOX", plainMail("second", "body two"))
	b := newTestBackend(t, srv)
	refs := refsBySubject(t, b, "INBOX")

	got, err := b.Move(t.Context(), []mail.Ref{refs["first"]}, "Archive")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotEqual(t, refs["first"], got[0])

	// The old ref is gone.
	_, err = b.GetMessage(t.Context(), refs["first"], mail.BodyOptions{Format: mail.BodyNone})
	require.True(t, errors.Is(err, mail.ErrRefStale) || errors.Is(err, mail.ErrNotFound), "%v", err)
	// The new ref names the message in the new folder. The flags came with it.
	msg := mustGet(t, b, got[0], mail.BodyOptions{})
	require.Equal(t, "first", msg.Subject)
	require.Equal(t, "body one\n", msg.Text)
	require.True(t, msg.Seen)
	require.Equal(t, []string{"project"}, msg.Tags)
	require.Equal(t, "Archive", mustDecodeRef(t, got[0]).Mailbox)

	require.Equal(t, []string{"second"}, serverSubjects(t, srv, "INBOX"))
	require.Equal(t, []string{"first"}, serverSubjects(t, srv, "Archive"))
	// The other message still has its ref.
	require.Equal(t, "second", mustGet(t, b, refs["second"], mail.BodyOptions{Format: mail.BodyNone}).Subject)
}

func TestMoveFallbackWithUIDPLUS(t *testing.T) {
	srv := imaptest.New(t, uidPlusOnly())
	srv.Create("Archive")
	srv.Append("INBOX", plainMail("deleted by another client", "x"), imap.FlagDeleted)
	srv.Append("INBOX", plainMail("moved", "x"), imap.FlagSeen)
	srv.Append("INBOX", plainMail("stays", "x"))
	b := newTestBackend(t, srv)
	requireNoMoveCap(t, b)
	refs := refsBySubject(t, b, "INBOX")

	got, err := b.Move(t.Context(), []mail.Ref{refs["moved"]}, "Archive")
	require.NoError(t, err)
	require.Len(t, got, 1)

	// Only the moved UID was expunged. A plain EXPUNGE would have removed the
	// message that another client marked \Deleted.
	require.Equal(t, []string{"deleted by another client", "stays"}, serverSubjects(t, srv, "INBOX"))
	require.True(t, serverMsgBySubject(t, srv, "INBOX", "deleted by another client").has(`\Deleted`))
	require.False(t, serverMsgBySubject(t, srv, "INBOX", "stays").has(`\Deleted`))
	// The copy has no \Deleted flag, and the flags of the message came along.
	moved := serverMsgBySubject(t, srv, "Archive", "moved")
	require.False(t, moved.has(`\Deleted`))
	require.True(t, moved.has(`\Seen`))

	// The new ref works.
	require.Equal(t, "moved", mustGet(t, b, got[0], mail.BodyOptions{Format: mail.BodyNone}).Subject)
	_, err = b.GetMessage(t.Context(), refs["moved"], mail.BodyOptions{Format: mail.BodyNone})
	require.Error(t, err)
}

func TestMoveFallbackMovesSeveralMessagesAndOnlyThose(t *testing.T) {
	srv := imaptest.New(t, uidPlusOnly())
	srv.Create("Archive")
	for i := 1; i <= 6; i++ {
		flags := []imap.Flag{}
		if i == 4 {
			flags = append(flags, imap.FlagDeleted)
		}
		srv.Append("INBOX", plainMail(fmt.Sprintf("m%d", i), "x"), flags...)
	}
	b := newTestBackend(t, srv)
	refs := refsBySubject(t, b, "INBOX") // m4 is not in a search

	got, err := b.Move(t.Context(), []mail.Ref{refs["m5"], refs["m1"], refs["m3"]}, "Archive")
	require.NoError(t, err)
	for i, subject := range []string{"m5", "m1", "m3"} {
		require.Equal(t, subject, mustGet(t, b, got[i], mail.BodyOptions{Format: mail.BodyNone}).Subject)
	}
	require.Equal(t, []string{"m2", "m4", "m6"}, serverSubjects(t, srv, "INBOX"))
	require.True(t, serverMsgBySubject(t, srv, "INBOX", "m4").has(`\Deleted`))
	require.Equal(t, []string{"m1", "m3", "m5"}, serverSubjects(t, srv, "Archive"))
}

func TestMoveFallbackKeepsTheSourceWhenTheServerGivesNoCopyUID(t *testing.T) {
	// Without the COPYUID code, the backend cannot make the new refs. It must
	// not delete the source then: the message would be gone for good, with
	// nothing to show where it went.
	srv := imaptest.New(t, uidPlusOnly(), imaptest.WithoutCopyUID())
	srv.Create("Archive")
	srv.Append("INBOX", plainMail("m", "x"))
	b := newTestBackend(t, srv)
	ref := refsBySubject(t, b, "INBOX")["m"]

	got, err := b.Move(t.Context(), []mail.Ref{ref}, "Archive")
	require.ErrorIs(t, err, mail.ErrUnsupported)
	require.ErrorContains(t, err, "COPYUID")
	require.Nil(t, got)

	m := serverMsgBySubject(t, srv, "INBOX", "m")
	require.False(t, m.has(`\Deleted`), "the source must not be marked deleted")
	// The copy was made before the answer was known. The error says so.
	require.ErrorContains(t, err, "copied")
	require.Equal(t, []string{"m"}, serverSubjects(t, srv, "Archive"))
}

func TestMoveRefusedWithoutMoveOrUIDPLUS(t *testing.T) {
	srv := imaptest.New(t, neitherMoveNorUIDPlus())
	srv.Create("Archive")
	srv.Create("Deleted Items", imap.MailboxAttrTrash)
	srv.Append("INBOX", plainMail("m", "x"))
	b := newTestBackend(t, srv)
	requireNoMoveCap(t, b)
	require.NoError(t, b.pool.with(t.Context(), func(c *imapclient.Client) error {
		require.False(t, c.Caps().Has(imap.CapUIDPlus), "the server must not advertise UIDPLUS")
		return nil
	}))
	ref := refsBySubject(t, b, "INBOX")["m"]

	got, err := b.Move(t.Context(), []mail.Ref{ref}, "Archive")
	require.ErrorIs(t, err, mail.ErrUnsupported)
	require.ErrorContains(t, err, "server lacks MOVE and UIDPLUS")
	require.Nil(t, got)
	err = b.Trash(t.Context(), []mail.Ref{ref})
	require.ErrorIs(t, err, mail.ErrUnsupported)

	// Nothing was changed: no copy, no flag, and no mailbox opened read-write.
	require.Equal(t, []string{"m"}, serverSubjects(t, srv, "INBOX"))
	require.Empty(t, serverSubjects(t, srv, "Archive"))
	require.Empty(t, serverSubjects(t, srv, "Deleted Items"))
	require.False(t, serverMsgBySubject(t, srv, "INBOX", "m").has(`\Deleted`))
	require.Zero(t, rwSelects(srv))
}

func TestMoveToTheFolderOfTheMessageIsANoOp(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Archive")
	srv.Append("INBOX", plainMail("in inbox", "x"))
	srv.Append("Archive", plainMail("in archive", "x"))
	b := newTestBackend(t, srv)
	inbox := refsBySubject(t, b, "INBOX")["in inbox"]
	archive := refsBySubject(t, b, "Archive")["in archive"]

	for _, target := range []string{"@inbox", "INBOX", "inbox", ""} {
		got, err := b.Move(t.Context(), []mail.Ref{inbox}, target)
		require.NoError(t, err, target)
		require.Equal(t, []mail.Ref{inbox}, got, target)
	}
	require.Zero(t, rwSelects(srv), "no folder was opened read-write")
	require.Equal(t, []string{"in inbox"}, serverSubjects(t, srv, "INBOX"))

	// A mix: the message in the target keeps its ref, the other gets a new one.
	got, err := b.Move(t.Context(), []mail.Ref{inbox, archive}, "Archive")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NotEqual(t, inbox, got[0])
	require.Equal(t, archive, got[1])
	require.Equal(t, "in inbox", mustGet(t, b, got[0], mail.BodyOptions{Format: mail.BodyNone}).Subject)
	require.Equal(t, 1, rwSelects(srv))
}

func TestMoveToAnUnknownFolderChangesNothing(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("m", "x"))
	b := newTestBackend(t, srv)
	ref := refsBySubject(t, b, "INBOX")["m"]

	got, err := b.Move(t.Context(), []mail.Ref{ref}, "No such folder")
	require.ErrorIs(t, err, mail.ErrNotFound)
	require.Nil(t, got)
	require.Equal(t, []string{"m"}, serverSubjects(t, srv, "INBOX"))
	require.Zero(t, rwSelects(srv))

	// An alias without a folder is the same.
	_, err = b.Move(t.Context(), []mail.Ref{ref}, mail.AliasArchive)
	require.ErrorIs(t, err, mail.ErrNotFound)
	require.Equal(t, []string{"m"}, serverSubjects(t, srv, "INBOX"))
}

func TestMoveRefusesFolderNamesThatCannotExist(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)
	ref := encodeRef(imapRef{Mailbox: "INBOX", UIDValidity: 1, UID: 1})

	for _, name := range []string{"bad\r\nA1 EXPUNGE", "nul\x00", strings.Repeat("a", maxFolderNameLen+1)} {
		_, err := b.Move(t.Context(), []mail.Ref{ref}, name)
		require.ErrorIs(t, err, mail.ErrNotFound)
	}
	require.Zero(t, srv.TotalConns())
}

func TestMoveToAliasFolders(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Old Mail", imap.MailboxAttrArchive)
	srv.Append("INBOX", plainMail("m", "x"))
	b := newTestBackend(t, srv)
	ref := refsBySubject(t, b, "INBOX")["m"]

	got, err := b.Move(t.Context(), []mail.Ref{ref}, mail.AliasArchive)
	require.NoError(t, err)
	require.Equal(t, "Old Mail", mustDecodeRef(t, got[0]).Mailbox)
	require.Equal(t, []string{"m"}, serverSubjects(t, srv, "Old Mail"))
}

func TestMoveDuplicateRefsMoveOnce(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Archive")
	srv.Append("INBOX", plainMail("m", "x"))
	srv.Append("INBOX", plainMail("n", "x"))
	b := newTestBackend(t, srv)
	refs := refsBySubject(t, b, "INBOX")

	got, err := b.Move(t.Context(), []mail.Ref{refs["m"], refs["n"], refs["m"]}, "Archive")
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, got[0], got[2])
	require.NotEqual(t, got[0], got[1])
	require.Equal(t, []string{"m", "n"}, serverSubjects(t, srv, "Archive"))
}

func TestMoveWithNoRefs(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)

	got, err := b.Move(t.Context(), nil, "Archive")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)
	require.NoError(t, b.Trash(t.Context(), nil))
	require.Zero(t, srv.TotalConns())
}

func TestMoveRefusesAMalformedRefAndChangesNothing(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Archive")
	srv.Append("INBOX", plainMail("m", "x"))
	b := newTestBackend(t, srv)
	ref := refsBySubject(t, b, "INBOX")["m"]

	_, err := b.Move(t.Context(), []mail.Ref{ref, "garbage"}, "Archive")
	require.ErrorIs(t, err, mail.ErrNotFound)
	require.Equal(t, []string{"m"}, serverSubjects(t, srv, "INBOX"))
	require.Empty(t, serverSubjects(t, srv, "Archive"))
}

// ---------------------------------------------------------------- trash

func TestTrashMovesToTrashFolder(t *testing.T) {
	servers := map[string][]imaptest.Option{
		"with MOVE":    nil,
		"with UIDPLUS": {uidPlusOnly()},
	}
	for name, opts := range servers {
		t.Run(name, func(t *testing.T) {
			srv := imaptest.New(t, opts...)
			srv.Create("Deleted Items", imap.MailboxAttrTrash)
			srv.Append("INBOX", plainMail("waiting for expunge", "x"), imap.FlagDeleted)
			srv.Append("INBOX", plainMail("victim", "x"), imap.FlagSeen)
			srv.Append("INBOX", plainMail("other", "x"))
			b := newTestBackend(t, srv)
			ref := refsBySubject(t, b, "INBOX")["victim"]

			require.NoError(t, b.Trash(t.Context(), []mail.Ref{ref}))

			// The message is in the Trash folder, with its flags. The other
			// messages of the inbox are still there, also the one that was
			// marked \Deleted by another client: nothing is expunged for good.
			require.Equal(t, []string{"waiting for expunge", "other"}, serverSubjects(t, srv, "INBOX"))
			require.True(t, serverMsgBySubject(t, srv, "INBOX", "waiting for expunge").has(`\Deleted`))
			require.Equal(t, []string{"victim"}, serverSubjects(t, srv, "Deleted Items"))
			trashed := serverMsgBySubject(t, srv, "Deleted Items", "victim")
			require.True(t, trashed.has(`\Seen`))
			require.False(t, trashed.has(`\Deleted`))
		})
	}
}

func TestTrashWithoutATrashFolderChangesNothing(t *testing.T) {
	// A folder named "Trash" without the attribute is not guessed.
	srv := imaptest.New(t)
	srv.Create("Trash")
	srv.Append("INBOX", plainMail("m", "x"))
	b := newTestBackend(t, srv)
	ref := refsBySubject(t, b, "INBOX")["m"]

	err := b.Trash(t.Context(), []mail.Ref{ref})
	require.ErrorIs(t, err, mail.ErrNotFound)
	require.Equal(t, []string{"m"}, serverSubjects(t, srv, "INBOX"))
	require.Empty(t, serverSubjects(t, srv, "Trash"))
}

func TestTrashOfAMessageInTheTrashIsANoOp(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Deleted Items", imap.MailboxAttrTrash)
	srv.Append("Deleted Items", plainMail("m", "x"))
	b := newTestBackend(t, srv)
	ref := refsBySubject(t, b, "Deleted Items")["m"]

	require.NoError(t, b.Trash(t.Context(), []mail.Ref{ref}))
	require.Equal(t, []string{"m"}, serverSubjects(t, srv, "Deleted Items"))
	require.False(t, serverMsgBySubject(t, srv, "Deleted Items", "m").has(`\Deleted`))
	require.Zero(t, rwSelects(srv))
}

// ---------------------------------------------------------------- batches

func TestBatchAcrossMailboxes(t *testing.T) {
	setup := func(t *testing.T, opts ...imaptest.Option) (*imaptest.Server, *Backend, []mail.Ref, []string) {
		srv := imaptest.New(t, opts...)
		srv.Create("Archive")
		srv.Append("INBOX", plainMail("a1", "x"))
		srv.Append("INBOX", plainMail("a2", "x"))
		srv.Append("Work", plainMail("w1", "x"))
		srv.Append("Work", plainMail("w2", "x"))
		srv.Append("Projects/Go", plainMail("p1", "x"))
		b := newTestBackend(t, srv)
		inbox, work, projects := refsBySubject(t, b, "INBOX"), refsBySubject(t, b, "Work"), refsBySubject(t, b, "Projects/Go")
		// The mailboxes are mixed in the order of the call.
		order := []string{"a1", "w1", "p1", "a2", "w2"}
		refs := []mail.Ref{inbox["a1"], work["w1"], projects["p1"], inbox["a2"], work["w2"]}
		return srv, b, refs, order
	}

	t.Run("move", func(t *testing.T) {
		for name, opts := range map[string][]imaptest.Option{"with MOVE": nil, "with UIDPLUS": {uidPlusOnly()}} {
			t.Run(name, func(t *testing.T) {
				srv, b, refs, order := setup(t, opts...)
				before := rwSelects(srv)

				got, err := b.Move(t.Context(), refs, "Archive")
				require.NoError(t, err)
				require.Len(t, got, len(refs))
				// The new refs are in the order of the old ones.
				for i, subject := range order {
					require.Equal(t, subject, mustGet(t, b, got[i], mail.BodyOptions{Format: mail.BodyNone}).Subject)
					require.Equal(t, "Archive", mustDecodeRef(t, got[i]).Mailbox)
				}
				require.Equal(t, 3, rwSelects(srv)-before, "one read-write SELECT for each mailbox")
				for _, mailbox := range []string{"INBOX", "Work", "Projects/Go"} {
					require.Empty(t, serverSubjects(t, srv, mailbox), mailbox)
				}
				require.ElementsMatch(t, order, serverSubjects(t, srv, "Archive"))
			})
		}
	})

	t.Run("flags", func(t *testing.T) {
		srv, b, refs, order := setup(t)
		before := rwSelects(srv)

		require.NoError(t, b.SetFlags(t.Context(), refs, mail.FlagChange{Seen: boolPtr(true), Flagged: boolPtr(true)}))
		require.Equal(t, 3, rwSelects(srv)-before)
		for i, mailbox := range []string{"INBOX", "Work", "Projects/Go", "INBOX", "Work"} {
			m := serverMsgBySubject(t, srv, mailbox, order[i])
			require.True(t, m.has(`\Seen`) && m.has(`\Flagged`), order[i])
		}
	})

	t.Run("tag", func(t *testing.T) {
		srv, b, refs, order := setup(t)
		before := rwSelects(srv)

		require.NoError(t, b.Tag(t.Context(), refs, []string{"batch"}, nil))
		require.Equal(t, 3, rwSelects(srv)-before)
		for i, mailbox := range []string{"INBOX", "Work", "Projects/Go", "INBOX", "Work"} {
			require.True(t, serverMsgBySubject(t, srv, mailbox, order[i]).has("batch"), order[i])
		}
	})
}

// batchSetup is a server with two messages in the inbox and one in "Work".
// The ref of the message in "Work" can be made stale or the message can be
// removed. The folder "Deleted Items" is the Trash folder.
type batchSetup struct {
	srv  *imaptest.Server
	b    *Backend
	refs []mail.Ref // two in the inbox, then one in Work
}

func newBatchSetup(t *testing.T) batchSetup {
	t.Helper()
	srv := imaptest.New(t)
	srv.Create("Archive")
	srv.Create("Deleted Items", imap.MailboxAttrTrash)
	srv.Append("INBOX", plainMail("m1", "x"))
	srv.Append("INBOX", plainMail("m2", "x"))
	srv.Append("Work", plainMail("w1", "x"))
	b := newTestBackend(t, srv)
	inbox := refsBySubject(t, b, "INBOX")
	work := refsBySubject(t, b, "Work")
	return batchSetup{srv: srv, b: b, refs: []mail.Ref{inbox["m1"], inbox["m2"], work["w1"]}}
}

// operations are the calls that change messages. Each takes a batch of refs.
func (s batchSetup) operations(ctx context.Context) map[string]func(refs []mail.Ref) error {
	return map[string]func([]mail.Ref) error{
		"SetFlags": func(refs []mail.Ref) error {
			return s.b.SetFlags(ctx, refs, mail.FlagChange{Seen: boolPtr(true), Flagged: boolPtr(true)})
		},
		"Tag": func(refs []mail.Ref) error { return s.b.Tag(ctx, refs, []string{"batch"}, nil) },
		"Move": func(refs []mail.Ref) error {
			_, err := s.b.Move(ctx, refs, "Archive")
			return err
		},
		"Trash": func(refs []mail.Ref) error { return s.b.Trash(ctx, refs) },
	}
}

// requireUntouched checks that no message was changed or moved.
func (s batchSetup) requireUntouched(t *testing.T, msg string) {
	t.Helper()
	for _, m := range serverMessages(t, s.srv, "INBOX") {
		require.Empty(t, m.Flags, "%s: the flags of %s", msg, m.Subject)
	}
	require.Equal(t, []string{"m1", "m2"}, serverSubjects(t, s.srv, "INBOX"), msg)
	require.Empty(t, serverSubjects(t, s.srv, "Archive"), msg)
	require.Empty(t, serverSubjects(t, s.srv, "Deleted Items"), msg)
}

func TestBatchWithOneStaleRefChangesNothing(t *testing.T) {
	for _, operation := range []string{"SetFlags", "Tag", "Move", "Trash"} {
		t.Run(operation, func(t *testing.T) {
			s := newBatchSetup(t)
			// The folder "Work" is made again. The ref of w1 is stale. The
			// two refs before it are fine.
			makeStale(t, s.srv, "Work", plainMail("w1", "x"))

			err := s.operations(t.Context())[operation](s.refs)
			require.ErrorIs(t, err, mail.ErrRefStale)
			s.requireUntouched(t, operation)
			require.Zero(t, rwSelects(s.srv), "no folder was opened read-write")

			// The same call without the stale ref works.
			require.NoError(t, s.operations(t.Context())[operation](s.refs[:2]))
		})
	}
}

func TestBatchWithAMissingMessageChangesNothing(t *testing.T) {
	for _, operation := range []string{"SetFlags", "Tag", "Move", "Trash"} {
		t.Run(operation, func(t *testing.T) {
			s := newBatchSetup(t)
			// Another client removes m2 for good, after the search.
			expungeOnAnotherClient(t, s.srv, "INBOX", mustDecodeRef(t, s.refs[1]).UID)
			before := rwSelects(s.srv) // the other client opened INBOX read-write

			err := s.operations(t.Context())[operation](s.refs)
			require.ErrorIs(t, err, mail.ErrNotFound)
			require.NotErrorIs(t, err, mail.ErrRefStale)
			require.Equal(t, []string{"m1"}, serverSubjects(t, s.srv, "INBOX"))
			require.Empty(t, serverMsgBySubject(t, s.srv, "INBOX", "m1").Flags)
			require.Empty(t, serverSubjects(t, s.srv, "Archive"))
			require.Empty(t, serverSubjects(t, s.srv, "Deleted Items"))
			require.Equal(t, []string{"w1"}, serverSubjects(t, s.srv, "Work"))
			require.Equal(t, before, rwSelects(s.srv), "no folder was opened read-write")
		})
	}
}

func TestBatchWithAMessageMarkedDeletedChangesNothing(t *testing.T) {
	// A message that another client marked \Deleted is waiting for an expunge.
	// A search does not show it, so it is as good as gone.
	for _, operation := range []string{"SetFlags", "Tag", "Move", "Trash"} {
		t.Run(operation, func(t *testing.T) {
			s := newBatchSetup(t)
			other := mustDial(t, pinnedConfig(s.srv, account.SecurityTLS))
			_, err := other.Select("INBOX", nil).Wait()
			require.NoError(t, err)
			require.NoError(t, other.Store(imap.UIDSetNum(imap.UID(mustDecodeRef(t, s.refs[1]).UID)),
				&imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close())
			before := rwSelects(s.srv) // the other client opened INBOX read-write

			err = s.operations(t.Context())[operation](s.refs[:2])
			require.ErrorIs(t, err, mail.ErrNotFound)
			require.Empty(t, serverMsgBySubject(t, s.srv, "INBOX", "m1").Flags)
			require.Equal(t, []string{`\deleted`}, serverMsgBySubject(t, s.srv, "INBOX", "m2").lowerFlags())
			require.Equal(t, before, rwSelects(s.srv), "no folder was opened read-write")
		})
	}
}

func TestBatchLimit(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)
	// The refs are not valid on purpose: the size is checked first, so the
	// error is about the size and not about the refs.
	refs := make([]mail.Ref, maxBatch+1)
	operations := map[string]func() error{
		"SetFlags": func() error { return b.SetFlags(t.Context(), refs, mail.FlagChange{Seen: boolPtr(true)}) },
		"Tag":      func() error { return b.Tag(t.Context(), refs, []string{"x"}, nil) },
		"Move": func() error {
			_, err := b.Move(t.Context(), refs, "Archive")
			return err
		},
		"Trash": func() error { return b.Trash(t.Context(), refs) },
	}
	for name, call := range operations {
		err := call()
		require.ErrorIs(t, err, mail.ErrUnsupported, name)
		require.NotErrorIs(t, err, mail.ErrNotFound, name)
		require.ErrorContains(t, err, "500", name)
	}
	require.Zero(t, srv.TotalConns(), "the server was not contacted")
	require.Equal(t, maxBatch, b.Capabilities(t.Context()).MaxBatch)
}

func TestBatchOfExactlyMaxBatchRefsWorks(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Archive")
	appendMany(srv, "INBOX", maxBatch, "m")
	b := newTestBackend(t, srv)
	validity := mustDecodeRef(t, mustSearch(t, b, mail.Query{Limit: 1}, "").Items[0].Ref).UIDValidity
	refs := make([]mail.Ref, maxBatch)
	for i := range refs {
		refs[i] = encodeRef(imapRef{Mailbox: "INBOX", UIDValidity: validity, UID: uint32(i + 1)})
	}

	require.NoError(t, b.SetFlags(t.Context(), refs, mail.FlagChange{Seen: boolPtr(true)}))
	require.Zero(t, srv.Unseen("INBOX"))

	got, err := b.Move(t.Context(), refs, "Archive")
	require.NoError(t, err)
	require.Len(t, got, maxBatch)
	seen := map[mail.Ref]bool{}
	for i, ref := range got {
		require.False(t, seen[ref], "every new ref is different")
		seen[ref] = true
		require.Equal(t, uint32(i+1), mustDecodeRef(t, ref).UID, "the new UIDs follow the old ones in order")
	}
	require.Empty(t, serverSubjects(t, srv, "INBOX"))
	require.Len(t, serverSubjects(t, srv, "Archive"), maxBatch)
}

// ---------------------------------------------------------------- the connection after errors

func TestOrganizeOperationsLeaveTheConnectionReusable(t *testing.T) {
	// A refused call and a failed call must leave the connection fit for the
	// next call (see pool.with). If not, the next call waits for the command
	// timeout and then dials again.
	srv := imaptest.New(t)
	srv.Create("Archive")
	srv.Create("Deleted Items", imap.MailboxAttrTrash)
	appendMany(srv, "INBOX", 6, "m")
	srv.Append("Work", plainMail("w", "x"))
	cfg := pinnedConfig(srv, account.SecurityTLS)
	cfg.TokenScope = testScope
	cfg.CommandTimeout = 3 * time.Second
	b := New(cfg, testSigner())
	t.Cleanup(func() { _ = b.Close() })
	ctx := t.Context()

	started := time.Now()
	inbox := refsBySubject(t, b, "INBOX")
	work := refsBySubject(t, b, "Work")["w"]
	m := func(n int) mail.Ref { return inbox[fmt.Sprintf("m %d", n)] }
	// Make the ref of "Work" stale.
	makeStale(t, srv, "Work", plainMail("w", "x"))

	_, err := b.Move(ctx, []mail.Ref{m(1)}, "No such folder")
	require.ErrorIs(t, err, mail.ErrNotFound)
	require.ErrorIs(t, b.SetFlags(ctx, []mail.Ref{m(1), work}, mail.FlagChange{Seen: boolPtr(true)}), mail.ErrRefStale)
	require.ErrorIs(t, b.Tag(ctx, []mail.Ref{m(1), work}, []string{"x"}, nil), mail.ErrRefStale)
	require.ErrorIs(t, b.Tag(ctx, []mail.Ref{m(1)}, []string{`\Deleted`}, nil), mail.ErrUnsupported)
	require.NoError(t, b.SetFlags(ctx, []mail.Ref{m(1)}, mail.FlagChange{Seen: boolPtr(true)}))
	require.NoError(t, b.Tag(ctx, []mail.Ref{m(1)}, []string{"x"}, []string{"y"}))
	_, err = b.Move(ctx, []mail.Ref{m(2), work}, "Archive")
	require.ErrorIs(t, err, mail.ErrRefStale)
	moved, err := b.Move(ctx, []mail.Ref{m(2)}, "Archive")
	require.NoError(t, err)
	_, err = b.Move(ctx, moved, "@inbox")
	require.NoError(t, err)
	require.NoError(t, b.Trash(ctx, []mail.Ref{m(3)}))
	require.NoError(t, b.Trash(ctx, []mail.Ref{m(4)}))
	_, err = b.CreateFolder(ctx, "Projects", "")
	require.NoError(t, err)
	_, err = b.CreateFolder(ctx, "Projects", "")
	require.ErrorIs(t, err, mail.ErrConflict)
	_, err = b.CreateFolder(ctx, "x", "No such parent")
	require.ErrorIs(t, err, mail.ErrNotFound)
	_, err = b.CreateFolder(ctx, "a/b", "")
	require.ErrorIs(t, err, mail.ErrUnsupported)
	_, err = b.ListTags(ctx)
	require.NoError(t, err)
	require.True(t, b.Capabilities(t.Context()).Tags)
	// m 3 and m 4 are in the Trash folder. m 2 went to Archive and came back.
	require.ElementsMatch(t, []string{"m 1", "m 2", "m 5", "m 6"}, subjects(mustSearch(t, b, mail.Query{Limit: 10}, "").Items))

	require.Less(t, time.Since(started), 3*time.Second)
	require.Equal(t, 1, srv.TotalConns(), "one connection served every call")
}

// ---------------------------------------------------------------- folders

func TestCreateFolder(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Old Mail", imap.MailboxAttrArchive)
	b := newTestBackend(t, srv)
	ctx := t.Context()

	top, err := b.CreateFolder(ctx, "Projects", "")
	require.NoError(t, err)
	require.Equal(t, mail.Folder{Name: "Projects"}, top)

	// The parent is a name. The server's delimiter joins the two.
	nested, err := b.CreateFolder(ctx, "Go", "Projects")
	require.NoError(t, err)
	require.Equal(t, mail.Folder{Name: "Projects/Go"}, nested)

	// The parent is an alias.
	year, err := b.CreateFolder(ctx, "2026", mail.AliasArchive)
	require.NoError(t, err)
	require.Equal(t, mail.Folder{Name: "Old Mail/2026"}, year)

	// The parent is the inbox.
	sub, err := b.CreateFolder(ctx, "Receipts", mail.AliasInbox)
	require.NoError(t, err)
	require.Equal(t, mail.Folder{Name: "INBOX/Receipts"}, sub)

	// Non-ASCII names work.
	uml, err := b.CreateFolder(ctx, "\U000000dcberf\U000000e4llig", "Projects")
	require.NoError(t, err)
	require.Equal(t, "Projects/\U000000dcberf\U000000e4llig", uml.Name)

	folders, err := b.ListFolders(ctx)
	require.NoError(t, err)
	names := map[string]bool{}
	for _, f := range folders {
		names[f.Name] = true
	}
	for _, want := range []string{"Projects", "Projects/Go", "Old Mail/2026", "INBOX/Receipts", "Projects/\U000000dcberf\U000000e4llig"} {
		require.True(t, names[want], want)
	}

	// Mail can be moved into the new folder.
	srv.Append("INBOX", plainMail("m", "x"))
	ref := refsBySubject(t, b, "INBOX")["m"]
	got, err := b.Move(ctx, []mail.Ref{ref}, nested.Name)
	require.NoError(t, err)
	require.Equal(t, "Projects/Go", mustDecodeRef(t, got[0]).Mailbox)
}

func TestCreateFolderThatExists(t *testing.T) {
	srv := imaptest.New(t)
	srv.Create("Projects")
	b := newTestBackend(t, srv)

	for _, tc := range []struct{ name, parent string }{
		{"Projects", ""},
		{"INBOX", ""},
	} {
		_, err := b.CreateFolder(t.Context(), tc.name, tc.parent)
		require.ErrorIs(t, err, mail.ErrConflict, tc.name)
	}
	_, err := b.CreateFolder(t.Context(), "Go", "Projects")
	require.NoError(t, err)
	_, err = b.CreateFolder(t.Context(), "Go", "Projects")
	require.ErrorIs(t, err, mail.ErrConflict)
}

func TestCreateFolderValidatesTheName(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)

	for _, name := range []string{
		"", "   ", "tab\there", "line\nbreak", "nul\x00", "bad\r\nA1 EXPUNGE",
		"a/b", "/a", "a/", // the delimiter of the server
		"star*", "percent%", // wildcards of LIST
		"@trash", "@inbox", // the aliases mean folders, so no folder may have them as a name
		strings.Repeat("a", maxFolderNameLen+1),
	} {
		got, err := b.CreateFolder(t.Context(), name, "")
		require.ErrorIs(t, err, mail.ErrUnsupported, "%q", name)
		require.Equal(t, mail.Folder{}, got)
	}
	require.Equal(t, []string{"INBOX"}, folderNames(t, b), "no folder was made")

	// A parent that cannot be one.
	for _, parent := range []string{"bad\nparent", "star*", "per%cent", strings.Repeat("a", maxFolderNameLen+1)} {
		_, err := b.CreateFolder(t.Context(), "x", parent)
		require.Error(t, err, "%q", parent)
	}
	require.Equal(t, []string{"INBOX"}, folderNames(t, b))
}

func folderNames(t *testing.T, b *Backend) []string {
	t.Helper()
	folders, err := b.ListFolders(t.Context())
	require.NoError(t, err)
	var out []string
	for _, f := range folders {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func TestCreateFolderWithoutParentFolder(t *testing.T) {
	srv := imaptest.New(t)
	b := newTestBackend(t, srv)

	_, err := b.CreateFolder(t.Context(), "Go", "Projects")
	require.ErrorIs(t, err, mail.ErrNotFound)
	_, err = b.CreateFolder(t.Context(), "Go", mail.AliasArchive)
	require.ErrorIs(t, err, mail.ErrNotFound)
	require.Equal(t, []string{"INBOX"}, folderNames(t, b), "the parent was not made on the way")
}

// lastCommand returns the last line that the fake server got.
func lastCommand(f *fakeIMAP) string {
	lines := strings.Split(strings.TrimRight(f.Received(), "\r\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// fakeBackend returns a backend for a fake server.
func fakeBackend(t *testing.T, f *fakeIMAP) *Backend {
	t.Helper()
	cfg := f.plainConfig()
	cfg.TokenScope = testScope
	b := New(cfg, testSigner())
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestCreateFolderIsNotRetriedAfterAReset(t *testing.T) {
	// CREATE is not safe to repeat: after a drop the folder may exist already,
	// and a second CREATE would report a conflict that the user did not cause.
	var fake *fakeIMAP
	fake = newFakeIMAP(t, "* OK [CAPABILITY IMAP4rev1] ready\r\n", func(tag, cmd string) (string, fakeThen) {
		last := lastCommand(fake)
		switch {
		case cmd == "LOGIN":
			return tag + " OK LOGIN completed\r\n", fakeStay
		case cmd == "CAPABILITY":
			return "* CAPABILITY IMAP4rev1\r\n" + tag + " OK CAPABILITY completed\r\n", fakeStay
		case cmd == "LIST" && strings.Contains(last, `LIST "" ""`):
			return "* LIST (\\Noselect) \"/\" \"\"\r\n" + tag + " OK LIST completed\r\n", fakeStay
		case cmd == "LIST":
			return tag + " OK LIST completed\r\n", fakeStay
		case cmd == "CREATE":
			return "", fakeReset
		}
		return tag + " BAD unexpected\r\n", fakeStay
	})
	b := fakeBackend(t, fake)

	_, err := b.CreateFolder(t.Context(), "Projects", "")
	require.Error(t, err)
	require.Equal(t, 1, strings.Count(fake.Received(), "CREATE"), "CREATE was sent once")
}

func TestCreateFolderConflictDoesNotDependOnTheServerText(t *testing.T) {
	// Servers word the refusal of CREATE in their own way, and some send no
	// response code. The caller must still be able to tell a conflict.
	newFake := func(listsTheFolder bool) *fakeIMAP {
		var fake *fakeIMAP
		fake = newFakeIMAP(t, "* OK [CAPABILITY IMAP4rev1] ready\r\n", func(tag, cmd string) (string, fakeThen) {
			last := lastCommand(fake)
			switch {
			case cmd == "LOGIN":
				return tag + " OK LOGIN completed\r\n", fakeStay
			case cmd == "CAPABILITY":
				return "* CAPABILITY IMAP4rev1\r\n" + tag + " OK CAPABILITY completed\r\n", fakeStay
			case cmd == "LIST" && strings.Contains(last, `LIST "" ""`):
				return "* LIST (\\Noselect) \"/\" \"\"\r\n" + tag + " OK LIST completed\r\n", fakeStay
			case cmd == "LIST" && listsTheFolder:
				return "* LIST () \"/\" Projects\r\n" + tag + " OK LIST completed\r\n", fakeStay
			case cmd == "LIST":
				return tag + " OK LIST completed\r\n", fakeStay
			case cmd == "CREATE":
				return tag + " NO [ALREADYEXISTS] Mailbox exists\r\n", fakeStay
			}
			return tag + " BAD unexpected\r\n", fakeStay
		})
		return fake
	}

	t.Run("the folder is listed", func(t *testing.T) {
		fake := newFake(true)
		_, err := fakeBackend(t, fake).CreateFolder(t.Context(), "Projects", "")
		require.ErrorIs(t, err, mail.ErrConflict)
		require.Zero(t, strings.Count(fake.Received(), "CREATE"), "no CREATE is sent for a folder that is there")
	})
	t.Run("the folder appears between LIST and CREATE", func(t *testing.T) {
		fake := newFake(false)
		_, err := fakeBackend(t, fake).CreateFolder(t.Context(), "Projects", "")
		require.ErrorIs(t, err, mail.ErrConflict)
		require.Equal(t, 1, strings.Count(fake.Received(), "CREATE"))
	})
}

func TestMoveIsNotRetriedAfterAReset(t *testing.T) {
	// A MOVE or COPY that is sent again after a drop can copy the messages a
	// second time. The checks before it may be repeated, the change may not.
	selectReply := func(tag, code string) string {
		return "* 1 EXISTS\r\n* OK [UIDVALIDITY 7] ok\r\n* OK [UIDNEXT 2] ok\r\n* FLAGS (\\Seen)\r\n" +
			"* OK [PERMANENTFLAGS (\\Seen \\*)] ok\r\n" + tag + " OK [" + code + "] done\r\n"
	}
	var fake *fakeIMAP
	fake = newFakeIMAP(t, "* OK [CAPABILITY IMAP4rev1 MOVE UIDPLUS] ready\r\n", func(tag, cmd string) (string, fakeThen) {
		last := lastCommand(fake)
		switch {
		case cmd == "LOGIN":
			return tag + " OK LOGIN completed\r\n", fakeStay
		case cmd == "CAPABILITY":
			return "* CAPABILITY IMAP4rev1 MOVE UIDPLUS\r\n" + tag + " OK CAPABILITY completed\r\n", fakeStay
		case cmd == "EXAMINE":
			return selectReply(tag, "READ-ONLY"), fakeStay
		case cmd == "SELECT":
			return selectReply(tag, "READ-WRITE"), fakeStay
		case strings.HasPrefix(last, tag+" UID SEARCH"):
			return "* SEARCH 1\r\n" + tag + " OK SEARCH completed\r\n", fakeStay
		case strings.HasPrefix(last, tag+" UID MOVE"):
			return "", fakeReset
		}
		return tag + " BAD unexpected\r\n", fakeStay
	})
	b := fakeBackend(t, fake)
	ref := encodeRef(imapRef{Mailbox: "INBOX", UIDValidity: 7, UID: 1})

	_, err := b.Move(t.Context(), []mail.Ref{ref}, "Archive")
	require.Error(t, err)
	require.Equal(t, 1, strings.Count(fake.Received(), "UID MOVE"), "MOVE was sent once")
	require.Equal(t, 1, strings.Count(fake.Received(), "SELECT"), "the inbox was selected once")
}

// ---------------------------------------------------------------- COPYUID mapping

func TestNewUIDs(t *testing.T) {
	uids := func(n ...imap.UID) []imap.UID { return n }
	set := func(n ...imap.UID) imap.UIDSet { return imap.UIDSetNum(n...) }

	t.Run("one to one, in ascending order", func(t *testing.T) {
		got, err := newUIDs(uids(3, 5, 9), set(3, 5, 9), set(30, 31, 32))
		require.NoError(t, err)
		require.Equal(t, map[imap.UID]imap.UID{3: 30, 5: 31, 9: 32}, got)
	})
	t.Run("ranges", func(t *testing.T) {
		src := imap.UIDSet{{Start: 1, Stop: 3}, {Start: 7, Stop: 7}}
		dst := imap.UIDSet{{Start: 100, Stop: 103}}
		got, err := newUIDs(uids(1, 2, 3, 7), src, dst)
		require.NoError(t, err)
		require.Equal(t, map[imap.UID]imap.UID{1: 100, 2: 101, 3: 102, 7: 103}, got)
	})
	t.Run("duplicate wanted UIDs", func(t *testing.T) {
		got, err := newUIDs(uids(3, 3), set(3), set(30))
		require.NoError(t, err)
		require.Equal(t, map[imap.UID]imap.UID{3: 30}, got)
	})
	t.Run("the server names more messages than were asked for", func(t *testing.T) {
		got, err := newUIDs(uids(3), set(3, 4), set(30, 31))
		require.NoError(t, err)
		require.Equal(t, map[imap.UID]imap.UID{3: 30}, got)
	})

	failures := map[string]struct {
		want     []imap.UID
		src, dst imap.NumSet
	}{
		"no COPYUID at all":       {uids(3), nil, nil},
		"empty sets":              {uids(3), imap.UIDSet{}, imap.UIDSet{}},
		"a sequence number set":   {uids(3), imap.SeqSetNum(3), imap.SeqSetNum(30)},
		"a message is missing":    {uids(3, 5), set(3), set(30)},
		"counts differ":           {uids(3, 5), set(3, 5), set(30)},
		"destination is missing":  {uids(3), set(3), nil},
		"another message in COPY": {uids(3), set(4), set(30)},
		"a huge range":            {uids(3), imap.UIDSet{{Start: 1, Stop: 4294967295}}, imap.UIDSet{{Start: 1, Stop: 4294967295}}},
	}
	for name, tc := range failures {
		t.Run(name, func(t *testing.T) {
			got, err := newUIDs(tc.want, tc.src, tc.dst)
			require.Error(t, err)
			require.ErrorIs(t, err, mail.ErrUnsupported)
			require.Nil(t, got)
		})
	}
}
