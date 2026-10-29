//go:build interop

// Package interop runs the mail scenario of email-mcp against real mail
// servers: Dovecot for IMAP and Postfix for SMTP submission, in Docker (see
// compose.yaml). The test drives the same provider code that the program uses
// (internal/provider/imapsmtp), and then the whole program through app.Open and
// an MCP client.
//
// The test needs the servers. Without them it fails, and the message says how
// to start them:
//
//	go run ./test/interop/gencerts test/interop/certs
//	docker compose -f test/interop/compose.yaml up -d --wait
//	go test -tags interop ./test/interop/ -v
//	docker compose -f test/interop/compose.yaml down -v
//
// The build tag keeps the test out of a plain "go test ./...".
package interop

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	netmail "net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/app"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mcpserver"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// The servers of compose.yaml. Every port is published on 127.0.0.1 only.
const (
	host             = "127.0.0.1"
	imapStartTLSPort = 31143
	imapTLSPort      = 31993
	smtpStartTLSPort = 31587
	smtpTLSPort      = 31465
)

// The one test account of Dovecot. The password is a test value. compose.yaml
// has the same value.
const (
	user     = "interop@example.test"
	password = "interop-test-only-password-7Hq2" //nolint:gosec // G101: a test password of the test servers, also in compose.yaml
)

// certsDir is the directory that gencerts writes, relative to this package.
// compose.yaml mounts the same directory into the containers.
const certsDir = "certs"

// deliveryWait is the longest time that the test waits for a sent message to
// arrive in INBOX.
const deliveryWait = 30 * time.Second

// ---------------------------------------------------------------- the servers

// servers is what the test knows about the running servers.
type servers struct {
	// roots holds only the test CA.
	roots *x509.CertPool
	// cert is the server certificate and key. The relay shows it to the
	// backend.
	cert tls.Certificate
	// pin is the SHA-256 of the server certificate in hex, as a pin of the
	// account settings.
	pin string
}

// needServers reads the test CA and checks that every server port answers. It
// fails the test with a message that says what to do: under the interop tag, a
// missing server is a failure, not a reason to skip.
func needServers(t *testing.T) *servers {
	t.Helper()
	caPath := filepath.Join(certsDir, "ca.crt")
	caPEM, err := os.ReadFile(caPath) //nolint:gosec // G304: a fixed path in this package
	if err != nil {
		t.Fatalf("the test CA %s cannot be read (%v). Make it first, from the repository root: go run ./test/interop/gencerts test/interop/certs", caPath, err)
	}
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(caPEM), "%s holds no PEM certificate", caPath)
	cert, err := tls.LoadX509KeyPair(filepath.Join(certsDir, "server.crt"), filepath.Join(certsDir, "server.key"))
	if err != nil {
		t.Fatalf("the server certificate in %s cannot be read (%v). Make it first: go run ./test/interop/gencerts test/interop/certs", certsDir, err)
	}
	sum := sha256.Sum256(cert.Certificate[0])

	for _, port := range []int{imapStartTLSPort, imapTLSPort, smtpStartTLSPort, smtpTLSPort} {
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatalf("no mail server answers on %s (%v). Start the servers first: docker compose -f test/interop/compose.yaml up -d --wait", addr, err)
		}
		_ = conn.Close()
	}
	return &servers{roots: roots, cert: cert, pin: hex.EncodeToString(sum[:])}
}

// config returns the backend settings of the test account. IMAP goes to
// imapPort and SMTP to smtpPort, with the given security. The certificates are
// checked against the test CA (RootCAs), not pinned.
func (s *servers) config(imapPort int, imapSec account.Security, smtpPort int, smtpSec account.Security) imapsmtp.Config {
	return imapsmtp.Config{
		Server: account.MailServer{
			IMAPHost: host, IMAPPort: imapPort, IMAPSecurity: imapSec,
			SMTPHost: host, SMTPPort: smtpPort, SMTPSecurity: smtpSec,
			Username:     user,
			SaveSentCopy: true,
		},
		Password:   secrets.Secret(password),
		RootCAs:    s.roots,
		TokenScope: "interop/account",
	}
}

// ---------------------------------------------------------------- the relay

// literalPattern finds an IMAP literal at the end of a command line: {N},
// {N+} or {N-}. The N bytes after the line are data, not commands.
var literalPattern = regexp.MustCompile(`\{(\d+)[+-]?\}\r?\n$`)

// relay stands between the backend and the IMAPS port of Dovecot. It shows the
// backend the test server certificate, makes its own TLS connection to
// Dovecot, and passes the bytes both ways without a change. It notes the name
// of each IMAP command that the backend sends, so the test can prove which
// commands reached the server.
//
// It keeps only command names, never the arguments: a LOGIN line holds the
// password. It skips the data of literals (APPEND), which can hold any text.
// It assumes that the server accepts every literal: after a refused one, the
// client sends no data, and the relay would then skip real commands.
type relay struct {
	ln       net.Listener
	upstream string
	upTLS    *tls.Config

	mu       sync.Mutex
	commands []string
	conns    map[net.Conn]struct{}
	closed   bool
	err      error
	wg       sync.WaitGroup
}

// newRelay starts a relay to the IMAPS port of Dovecot. It stops at the end of
// the test.
func newRelay(t *testing.T, s *servers) *relay {
	t.Helper()
	ln, err := tls.Listen("tcp", net.JoinHostPort(host, "0"), &tls.Config{
		Certificates: []tls.Certificate{s.cert},
		MinVersion:   tls.VersionTLS12,
	})
	require.NoError(t, err)
	r := &relay{
		ln:       ln,
		upstream: net.JoinHostPort(host, strconv.Itoa(imapTLSPort)),
		upTLS:    &tls.Config{RootCAs: s.roots, ServerName: host, MinVersion: tls.VersionTLS12},
		conns:    map[net.Conn]struct{}{},
	}
	r.wg.Add(1)
	go r.serve()
	t.Cleanup(func() {
		r.close()
		require.NoError(t, r.failure(), "the relay could not reach Dovecot")
	})
	return r
}

// port is the port of the relay on host.
func (r *relay) port() int { return r.ln.Addr().(*net.TCPAddr).Port }

func (r *relay) serve() {
	defer r.wg.Done()
	for {
		down, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.wg.Add(1)
		go r.handle(down)
	}
}

// handle relays one connection of the backend.
func (r *relay) handle(down net.Conn) {
	defer r.wg.Done()
	defer func() { _ = down.Close() }()
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second}, Config: r.upTLS}
	up, err := dialer.Dial("tcp", r.upstream)
	if err != nil {
		r.fail(err)
		return
	}
	defer func() { _ = up.Close() }()
	if !r.track(down, up) {
		return
	}
	defer r.untrack(down, up)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(down, up)
		// Dovecot closed the connection: end the other direction too.
		_ = down.Close()
	}()
	r.forward(down, up)
	_ = up.Close()
	<-done
}

// forward copies the commands of the backend to Dovecot and notes their names.
func (r *relay) forward(down io.Reader, up io.Writer) {
	br := bufio.NewReader(down)
	// first is true when the next line starts a new command. It is false for
	// the rest of a command line after a literal.
	first := true
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			if first {
				r.record(line)
			}
			if _, werr := io.WriteString(up, line); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
		first = true
		if m := literalPattern.FindStringSubmatch(line); m != nil {
			n, perr := strconv.ParseInt(m[1], 10, 64)
			if perr != nil {
				return
			}
			if _, err := io.CopyN(up, br, n); err != nil {
				return
			}
			first = false
		}
	}
}

// record notes the name of the command on line: "SELECT", "UID MOVE" and so
// on. A STORE that adds or removes \Deleted is noted as "UID STORE \Deleted".
// A line that is not a command (DONE after IDLE, an AUTHENTICATE answer) has
// one word and is not noted.
func (r *relay) record(line string) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return
	}
	name := strings.ToUpper(fields[1])
	if name == "UID" && len(fields) > 2 {
		name += " " + strings.ToUpper(fields[2])
	}
	if strings.HasSuffix(name, "STORE") && strings.Contains(strings.ToLower(line), `\deleted`) {
		name += ` \Deleted`
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, name)
}

// commandNames returns the names of the commands so far.
func (r *relay) commandNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.commands)
}

func (r *relay) track(conns ...net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	for _, c := range conns {
		r.conns[c] = struct{}{}
	}
	return true
}

func (r *relay) untrack(conns ...net.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range conns {
		delete(r.conns, c)
	}
}

func (r *relay) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
}

func (r *relay) failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// close stops the relay and closes every connection.
func (r *relay) close() {
	r.mu.Lock()
	r.closed = true
	for c := range r.conns {
		_ = c.Close()
	}
	r.mu.Unlock()
	_ = r.ln.Close()
	r.wg.Wait()
}

// ---------------------------------------------------------------- the observer

// observer is a plain IMAP client of the test, apart from the backend. It
// puts the first message in INBOX and counts the messages of the account, so
// the test does not check the backend with the backend.
type observer struct {
	t      *testing.T
	client *imapclient.Client
}

func newObserver(t *testing.T, s *servers) *observer {
	t.Helper()
	c, err := imapclient.DialTLS(net.JoinHostPort(host, strconv.Itoa(imapTLSPort)), &imapclient.Options{
		TLSConfig: &tls.Config{RootCAs: s.roots, ServerName: host, MinVersion: tls.VersionTLS12},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.Login(user, password).Wait())
	return &observer{t: t, client: c}
}

// add stores a message in a folder.
func (o *observer) add(folder string, data []byte) {
	o.t.Helper()
	cmd := o.client.Append(folder, int64(len(data)), nil)
	_, err := cmd.Write(data)
	require.NoError(o.t, err)
	require.NoError(o.t, cmd.Close())
	_, err = cmd.Wait()
	require.NoError(o.t, err)
}

// total returns the number of messages in each folder of the account. A
// message that was expunged is in no folder.
func (o *observer) total() map[string]uint32 {
	o.t.Helper()
	boxes, err := o.client.List("", "*", nil).Collect()
	require.NoError(o.t, err)
	counts := map[string]uint32{}
	for _, box := range boxes {
		if slices.Contains(box.Attrs, imap.MailboxAttrNoSelect) {
			continue
		}
		st, err := o.client.Status(box.Mailbox, &imap.StatusOptions{NumMessages: true}).Wait()
		require.NoError(o.t, err)
		require.NotNil(o.t, st.NumMessages)
		counts[box.Mailbox] = *st.NumMessages
	}
	return counts
}

// sum adds up the counts of all folders.
func sum(counts map[string]uint32) uint32 {
	var n uint32
	for _, c := range counts {
		n += c
	}
	return n
}

// ---------------------------------------------------------------- helpers

// newMarker returns a random word. Each run puts it in its subjects and
// folder names, so the test finds its own messages also when the servers keep
// mail of an earlier run.
func newMarker(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

// seedMessage is the first message of INBOX.
func seedMessage(subject, body, messageID string) []byte {
	return []byte("From: Seed Sender <seed@example.test>\r\n" +
		"To: " + user + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n" +
		"Message-ID: " + messageID + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		body + "\r\n")
}

// compose builds a message to the test account as the service builds one.
func compose(t *testing.T, subject, body string, forDraft bool) *mail.RawMessage {
	t.Helper()
	raw, err := mime.Build(mail.Compose{To: []string{user}, Subject: subject, Body: body}, mime.BuildContext{
		From:     mail.Address{Email: user},
		ForDraft: forDraft,
	})
	require.NoError(t, err)
	return raw
}

// messageID returns the Message-ID header of a message.
func messageID(t *testing.T, data []byte) string {
	t.Helper()
	msg, err := netmail.ReadMessage(bytes.NewReader(data))
	require.NoError(t, err)
	id := msg.Header.Get("Message-Id")
	require.NotEmpty(t, id)
	return id
}

// find searches a folder for a subject word.
func find(t *testing.T, ctx context.Context, b *imapsmtp.Backend, folder, word string) []mail.Summary {
	t.Helper()
	page, err := b.Search(ctx, mail.Query{Folder: folder, Subject: word, Limit: 10}, "")
	require.NoError(t, err)
	return page.Items
}

// findOne searches a folder for a subject word and expects one message.
func findOne(t *testing.T, ctx context.Context, b *imapsmtp.Backend, folder, word string) mail.Summary {
	t.Helper()
	items := find(t, ctx, b, folder, word)
	require.Len(t, items, 1, "one message in %s has %q in its subject", folder, word)
	return items[0]
}

// step runs a part of the scenario. The parts depend on each other, so the
// scenario stops at the first part that fails.
func step(t *testing.T, name string, fn func(t *testing.T)) {
	t.Helper()
	if !t.Run(name, fn) {
		t.FailNow()
	}
}

// ---------------------------------------------------------------- the scenario

// TestInteropFullScenario runs the mail scenario against Dovecot and Postfix:
// probe; search, then get; tag, then untag; move to a folder and back; save a
// draft and discard it; send to self through Postfix; wait for the message in
// INBOX; move it to Trash. Then it checks that no message left the account:
// only MOVE took messages out of a folder, and every message is still in one.
// Last, it opens the whole program with app.Open and searches through an MCP
// client.
func TestInteropFullScenario(t *testing.T) {
	s := needServers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	marker := newMarker(t)
	// Each word is letters and digits only, so a search index of the server
	// keeps it as one word.
	seedWord, draftWord, sentWord := "seed"+marker, "draft"+marker, "sent"+marker
	seedID := "<seed." + marker + "@example.test>"
	seedBody := "Seed body " + marker

	obs := newObserver(t, s)
	obs.add("INBOX", seedMessage("Interop "+seedWord, seedBody, seedID))
	before := obs.total()

	step(t, "probe", func(t *testing.T) {
		modes := []struct {
			name               string
			imapPort, smtpPort int
			security           account.Security
		}{
			{"starttls", imapStartTLSPort, smtpStartTLSPort, account.SecuritySTARTTLS},
			{"implicit tls", imapTLSPort, smtpTLSPort, account.SecurityTLS},
		}
		for _, m := range modes {
			t.Run(m.name, func(t *testing.T) {
				res, err := imapsmtp.Probe(ctx, s.config(m.imapPort, m.security, m.smtpPort, m.security))
				require.NoError(t, err)
				require.True(t, res.IMAPOK, "IMAP login")
				require.True(t, res.SMTPOK, "SMTP login")
				require.Nil(t, res.IMAPCert)
				require.Nil(t, res.SMTPCert)
				for _, c := range []string{"MOVE", "UIDPLUS", "SPECIAL-USE"} {
					require.Contains(t, res.Capabilities, c)
				}
			})
		}
		t.Run("no password before TLS", func(t *testing.T) {
			// IMAP: the plain port says LOGINDISABLED until STARTTLS.
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(imapStartTLSPort)), 5*time.Second)
			require.NoError(t, err)
			defer func() { _ = conn.Close() }()
			require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
			greeting, err := bufio.NewReader(conn).ReadString('\n')
			require.NoError(t, err)
			require.Contains(t, greeting, "LOGINDISABLED")
			require.Contains(t, greeting, "STARTTLS")

			// SMTP: before STARTTLS, EHLO offers no AUTH.
			sconn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(smtpStartTLSPort)), 5*time.Second)
			require.NoError(t, err)
			defer func() { _ = sconn.Close() }()
			require.NoError(t, sconn.SetDeadline(time.Now().Add(10*time.Second)))
			tp := textproto.NewConn(sconn)
			_, _, err = tp.ReadResponse(220)
			require.NoError(t, err)
			_, err = tp.Cmd("EHLO interop.example.test")
			require.NoError(t, err)
			_, ehlo, err := tp.ReadResponse(250)
			require.NoError(t, err)
			require.Contains(t, ehlo, "STARTTLS")
			require.NotContains(t, ehlo, "AUTH")
			_, _ = tp.Cmd("QUIT")
		})
		t.Run("untrusted without the test CA", func(t *testing.T) {
			cfg := s.config(imapTLSPort, account.SecurityTLS, smtpStartTLSPort, account.SecuritySTARTTLS)
			cfg.RootCAs = nil
			res, err := imapsmtp.Probe(ctx, cfg)
			require.NoError(t, err)
			require.False(t, res.IMAPOK)
			require.False(t, res.SMTPOK)
			require.NotNil(t, res.IMAPCert, "the system roots do not trust the test CA")
			require.NotNil(t, res.SMTPCert, "the system roots do not trust the test CA")
			require.Equal(t, s.pin, res.IMAPCert.FingerprintSHA256)
		})
	})

	// The backend of the scenario. IMAP goes through the relay, which notes the
	// commands. SMTP goes straight to Postfix, with STARTTLS.
	rel := newRelay(t, s)
	b := imapsmtp.New(s.config(rel.port(), account.SecurityTLS, smtpStartTLSPort, account.SecuritySTARTTLS),
		signing.New(bytes.Repeat([]byte{7}, 32)))
	t.Cleanup(func() { _ = b.Close() })

	var ref mail.Ref
	step(t, "capabilities and folders", func(t *testing.T) {
		require.True(t, b.Capabilities(ctx).Tags, "Dovecot lets clients make keywords")
		folders, err := b.ListFolders(ctx)
		require.NoError(t, err)
		aliases := map[string]string{}
		for _, f := range folders {
			if f.Alias != "" {
				aliases[f.Alias] = f.Name
			}
		}
		require.Equal(t, map[string]string{
			mail.AliasInbox: "INBOX", mail.AliasDrafts: "Drafts", mail.AliasSent: "Sent",
			mail.AliasTrash: "Trash", mail.AliasJunk: "Junk",
		}, aliases)
	})

	step(t, "search and get", func(t *testing.T) {
		seed := findOne(t, ctx, b, mail.AliasInbox, seedWord)
		require.Equal(t, "Interop "+seedWord, seed.Subject)
		require.False(t, seed.Seen)
		ref = seed.Ref

		msg, err := b.GetMessage(ctx, ref, mail.BodyOptions{Format: mail.BodyText, MaxChars: 1000})
		require.NoError(t, err)
		require.Equal(t, seedID, msg.MessageID)
		require.Contains(t, msg.Text, seedBody)
		require.False(t, findOne(t, ctx, b, mail.AliasInbox, seedWord).Seen, "reading a message does not mark it as read")
	})

	step(t, "tag and untag", func(t *testing.T) {
		tag := "interop" + marker
		require.NoError(t, b.Tag(ctx, []mail.Ref{ref}, []string{tag}, nil))
		page, err := b.Search(ctx, mail.Query{Folder: mail.AliasInbox, TagsAny: []string{tag}}, "")
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		require.Equal(t, ref, page.Items[0].Ref)
		require.Contains(t, page.Items[0].Tags, tag)

		require.NoError(t, b.Tag(ctx, []mail.Ref{ref}, nil, []string{tag}))
		page, err = b.Search(ctx, mail.Query{Folder: mail.AliasInbox, TagsAny: []string{tag}}, "")
		require.NoError(t, err)
		require.Empty(t, page.Items)
		require.NotContains(t, findOne(t, ctx, b, mail.AliasInbox, seedWord).Tags, tag)
	})

	step(t, "move to a folder and back", func(t *testing.T) {
		folder := "Interop" + marker
		_, err := b.CreateFolder(ctx, folder, "")
		require.NoError(t, err)

		moved, err := b.Move(ctx, []mail.Ref{ref}, folder)
		require.NoError(t, err)
		require.Len(t, moved, 1)
		require.Empty(t, find(t, ctx, b, mail.AliasInbox, seedWord))
		require.Equal(t, moved[0], findOne(t, ctx, b, folder, seedWord).Ref, "Move returns the ref in the new folder")

		back, err := b.Move(ctx, moved, mail.AliasInbox)
		require.NoError(t, err)
		require.Len(t, back, 1)
		require.Empty(t, find(t, ctx, b, folder, seedWord))
		require.Equal(t, back[0], findOne(t, ctx, b, mail.AliasInbox, seedWord).Ref)
		ref = back[0]
	})

	step(t, "save a draft and discard it", func(t *testing.T) {
		draftRef, err := b.SaveDraft(ctx, compose(t, "Interop "+draftWord, "Draft body "+marker, true))
		require.NoError(t, err)
		draft := findOne(t, ctx, b, mail.AliasDrafts, draftWord)
		require.Equal(t, draftRef, draft.Ref)
		require.True(t, draft.Draft)

		require.NoError(t, b.DiscardDraft(ctx, draftRef))
		require.Empty(t, find(t, ctx, b, mail.AliasDrafts, draftWord))
		findOne(t, ctx, b, mail.AliasTrash, draftWord)
	})

	var delivered mail.Ref
	step(t, "send to self", func(t *testing.T) {
		raw := compose(t, "Interop "+sentWord, "Sent body "+marker, false)
		sentID := messageID(t, raw.Data)
		require.NoError(t, b.Send(ctx, raw))

		deadline := time.Now().Add(deliveryWait)
		var got []mail.Summary
		for {
			got = find(t, ctx, b, mail.AliasInbox, sentWord)
			if len(got) > 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		require.Len(t, got, 1, "Postfix delivered the message to INBOX within %s", deliveryWait)
		delivered = got[0].Ref
		msg, err := b.GetMessage(ctx, delivered, mail.BodyOptions{Format: mail.BodyText, MaxChars: 1000})
		require.NoError(t, err)
		require.Equal(t, sentID, msg.MessageID, "the message in INBOX is the one that was sent")
		require.Contains(t, msg.Text, "Sent body "+marker)

		// SaveSentCopy is on, so a copy is in Sent too.
		findOne(t, ctx, b, mail.AliasSent, sentWord)
	})

	step(t, "move to trash", func(t *testing.T) {
		require.NoError(t, b.Trash(ctx, []mail.Ref{delivered}))
		require.Empty(t, find(t, ctx, b, mail.AliasInbox, sentWord))
		findOne(t, ctx, b, mail.AliasTrash, sentWord)
		// The first message is still in INBOX.
		require.Equal(t, ref, findOne(t, ctx, b, mail.AliasInbox, seedWord).Ref)
	})

	step(t, "nothing was expunged", func(t *testing.T) {
		commands := rel.commandNames()
		require.NotEmpty(t, commands, "the backend used the relay")
		t.Logf("IMAP commands of the backend: %s", strings.Join(commands, ", "))
		for _, c := range commands {
			require.NotContains(t, []string{"EXPUNGE", "UID EXPUNGE", "CLOSE", `STORE \Deleted`, `UID STORE \Deleted`}, c,
				"Dovecot offers MOVE, so the backend never needs the fallback that expunges")
		}
		moves := 0
		for _, c := range commands {
			if c == "UID MOVE" {
				moves++
			}
		}
		// To the folder, back, the draft to Trash, the sent message to Trash.
		require.Equal(t, 4, moves, "every message left its folder by UID MOVE: %v", commands)

		// The account has the messages of the start, plus the draft (now in
		// Trash), the sent message (now in Trash) and its copy in Sent.
		after := obs.total()
		require.Equal(t, sum(before)+3, sum(after), "no message was removed from the account: before %v, after %v", before, after)
		require.Equal(t, before["INBOX"], after["INBOX"], "the first message is back in INBOX")
		require.Equal(t, before["Trash"]+2, after["Trash"])
		require.Equal(t, before["Sent"]+1, after["Sent"])
		require.Equal(t, before["Drafts"], after["Drafts"])
	})

	step(t, "the whole program over MCP", func(t *testing.T) {
		appScenario(t, s, seedWord)
	})
}

// ---------------------------------------------------------------- the program

// appScenario opens the whole program with app.Open, as "serve" opens it,
// connects the test account, and then lists the accounts and searches through
// an MCP client on an in-memory connection, as an agent does on stdio. The
// program has no setting for a test CA, so the account pins the certificate of
// the servers, as a user does for a server with its own certificate.
func appScenario(t *testing.T, s *servers, seedWord string) {
	dir := t.TempDir()
	var logs bytes.Buffer
	var logsMu sync.Mutex
	a, err := app.Open(context.Background(), app.Config{
		DataDir:   filepath.Join(dir, "data"),
		KEKSource: "file:" + filepath.Join(dir, "master.key"),
		Local:     true,
		LogOutput: writerFunc(func(p []byte) (int, error) {
			logsMu.Lock()
			defer logsMu.Unlock()
			return logs.Write(p)
		}),
		LogLevel: slog.LevelDebug,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = a.CloseNow(ctx)
		if t.Failed() {
			logsMu.Lock()
			defer logsMu.Unlock()
			t.Logf("program log:\n%s", logs.String())
		}
	})

	ctx := service.WithPrincipal(context.Background(), service.Principal{Tenant: store.LocalTenantID, Client: "interop"})
	acct, err := a.Service.CreateIMAPAccount(ctx, user, account.MailServer{
		IMAPHost: host, IMAPPort: imapTLSPort, IMAPSecurity: account.SecurityTLS,
		SMTPHost: host, SMTPPort: smtpStartTLSPort, SMTPSecurity: account.SecuritySTARTTLS,
		Username:       user,
		PinnedIMAPCert: s.pin, PinnedSMTPCert: s.pin,
	}, secrets.Secret(password))
	require.NoError(t, err)

	srv := a.MCPServer(mcpserver.Options{
		InBandLegacyElicitation: true,
		Principal: func(_ context.Context, _ *mcp.CallToolRequest) (service.Principal, error) {
			return service.Principal{Tenant: store.LocalTenantID, Client: "interop"}, nil
		},
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "interop-client", Version: "1"}, nil).
		Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Wait()
	})

	accounts := callOK(t, cs, "list_accounts", map[string]any{})
	list, _ := accounts["accounts"].([]any)
	require.Len(t, list, 1)
	first := list[0].(map[string]any)
	require.Equal(t, string(acct.ID), first["id"])
	require.Equal(t, user, first["address"])
	require.Equal(t, true, first["capabilities"].(map[string]any)["tags"], "the program sees that Dovecot supports tags")

	found := callOK(t, cs, "search_messages", map[string]any{
		"account": string(acct.ID), "folder": mail.AliasInbox, "subject": seedWord,
	})
	msgs, _ := found["messages"].([]any)
	require.Len(t, msgs, 1)
	untrusted := msgs[0].(map[string]any)["untrusted"].(map[string]any)
	require.Equal(t, "Interop "+seedWord, untrusted["content"].(map[string]any)["subject"])
}

// callOK calls a tool and fails the test when the call or the tool fails. It
// returns the structured content.
func callOK(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	require.NoError(t, err, "tool %s answers with a result", tool)
	if res.IsError {
		var text string
		if len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcp.TextContent); ok {
				text = tc.Text
			}
		}
		t.Fatalf("tool %s failed: %s", tool, text)
	}
	out, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "tool %s returns an object, got %T", tool, res.StructuredContent)
	return out
}

// writerFunc turns a function into an io.Writer.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
