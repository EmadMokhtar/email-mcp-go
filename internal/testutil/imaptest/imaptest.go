// Package imaptest runs a small in-memory IMAP server for tests.
//
// The server listens on 127.0.0.1 with a random port. It has one user and
// one mailbox (INBOX). It uses a new self-signed certificate every time, so
// a test can check pinning (CertSHA256) and normal verification (CertPool).
//
// Use New to start a server. The server stops when the test ends.
//
//	srv := imaptest.New(t)                                  // TLS from the first byte
//	srv := imaptest.New(t, imaptest.WithMode(imaptest.ModeSTARTTLS))
//	srv := imaptest.New(t, imaptest.WithMode(imaptest.ModePlain))
//
// A test can also make mailboxes with special-use attributes (see Create),
// choose the capabilities that the server advertises (see WithCaps), change
// the PERMANENTFLAGS answer of SELECT and EXAMINE (see WithPermanentFlags),
// leave the COPYUID code out of COPY answers (see WithoutCopyUID), leave the
// APPENDUID code out of APPEND answers (see WithoutAppendUID), make APPEND
// fail (see WithAppendError and WithAppendLimit), add messages with a given
// date (see AppendAt), choose the login (see WithLogin) and read the server
// state (see Unseen, SelectCounts, FetchedSections, Commands, WriteCommands
// and BytesSent).
//
// This package is for tests only. Do not import it from production code.
package imaptest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// Mode is how the server protects its connections.
type Mode int

// The server modes.
const (
	// ModeTLS starts TLS on connect (implicit TLS). This is the default.
	ModeTLS Mode = iota
	// ModeSTARTTLS starts in plain text, advertises STARTTLS and refuses
	// LOGIN until the connection is upgraded.
	ModeSTARTTLS
	// ModePlain never uses TLS and allows LOGIN on the plain connection.
	ModePlain
)

// Option changes how New starts the server.
type Option func(*options)

type options struct {
	mode           Mode
	user, pass     string
	caps           imap.CapSet
	permanentFlags func(mailbox string, readOnly bool, flags []imap.Flag) []imap.Flag
	noCopyUID      bool
	noAppendUID    bool
	appendErr      func(mailbox string) error
	appendLimit    uint32
}

// WithMode sets the server mode. The default is ModeTLS.
func WithMode(m Mode) Option {
	return func(o *options) { o.mode = m }
}

// WithLogin sets the user name and the password that the server accepts. The
// default is "tester" and "s3cret-pass". A test that looks for the password in
// a log or a file uses it to choose a password that cannot match by chance.
func WithLogin(user, pass string) Option {
	return func(o *options) { o.user, o.pass = user, pass }
}

// WithCaps sets the capabilities that the server advertises, instead of the
// default (IMAP4rev1 and IMAP4rev2). Give at least IMAP4rev1 or IMAP4rev2.
//
// IMAP4rev2 implies LIST-EXTENDED, LIST-STATUS, UIDPLUS, MOVE and more, so a
// test that wants a server without LIST-STATUS, or one that uses modified
// UTF-7 folder names (an IMAP4rev1 server without UTF8=ACCEPT enabled), gives
// IMAP4rev1 and lists the extensions it needs.
func WithCaps(caps ...imap.Cap) Option {
	return func(o *options) {
		o.caps = make(imap.CapSet, len(caps))
		for _, c := range caps {
			o.caps[c] = struct{}{}
		}
	}
}

// WithPermanentFlags lets a test change the PERMANENTFLAGS that the server
// reports when a client opens a mailbox. fn gets the mailbox name, whether the
// client used EXAMINE (readOnly), and the flags the server would report. It
// returns the flags to report.
//
// imapmemserver always reports the wildcard \* (clients may create new
// keywords). A test uses this option to make a server that does not, or one
// that reports no permanent flags for a read-only mailbox (as Dovecot does).
// It changes only the answer. The server still stores every flag it is given.
func WithPermanentFlags(fn func(mailbox string, readOnly bool, flags []imap.Flag) []imap.Flag) Option {
	return func(o *options) { o.permanentFlags = fn }
}

// WithoutCopyUID makes the server answer COPY without the COPYUID response
// code (RFC 4315), as a server without UIDPLUS does. The copy is still made.
// MOVE is not changed: imapmemserver writes its COPYUID itself.
func WithoutCopyUID() Option {
	return func(o *options) { o.noCopyUID = true }
}

// WithoutAppendUID makes the server answer APPEND without the APPENDUID
// response code (RFC 4315), as a server without UIDPLUS does. The message is
// still stored.
func WithoutAppendUID() Option {
	return func(o *options) { o.noAppendUID = true }
}

// WithAppendError makes APPEND fail. fn gets the mailbox name. When it returns
// an error, the server reads the message and refuses it with that error, and
// stores nothing. An *imap.Error gives its own status and response code. Any
// other error gives NO. When fn returns nil, APPEND works as usual.
//
// The server answers after it has read the whole message, so a client that
// sends the message does not notice anything until the answer.
func WithAppendError(fn func(mailbox string) error) Option {
	return func(o *options) { o.appendErr = fn }
}

// WithAppendLimit sets the largest message that APPEND accepts, in bytes. A
// larger message is refused with NO [TOOBIG] before the client sends it, which
// is how a server refuses a message that is too large.
func WithAppendLimit(n uint32) Option {
	return func(o *options) { o.appendLimit = n }
}

// Server is a running test IMAP server.
//
// The exported fields are set by New and must not be changed.
type Server struct {
	// Addr is the listen address, for example "127.0.0.1:41233".
	Addr string
	// Host is the host part of Addr ("127.0.0.1").
	Host string
	// Port is the port part of Addr.
	Port int
	// CertSHA256 is the lowercase hex SHA-256 of the server certificate (the
	// DER bytes). Use it as a pin.
	CertSHA256 string
	// CertPool holds the server certificate. Use it as the root CAs of a
	// client to test normal certificate verification without a pin.
	CertPool *x509.CertPool
	// User and Pass are the only accepted login.
	User string
	Pass string

	t    testing.TB
	user *imapmemserver.User
	srv  *imapserver.Server
	// permanentFlags is the option WithPermanentFlags. It is nil by default.
	permanentFlags func(mailbox string, readOnly bool, flags []imap.Flag) []imap.Flag
	// noCopyUID is the option WithoutCopyUID.
	noCopyUID bool
	// noAppendUID is the option WithoutAppendUID.
	noAppendUID bool
	// appendErr is the option WithAppendError. It is nil by default.
	appendErr func(mailbox string) error

	closeOnce sync.Once

	mu    sync.Mutex
	live  map[*trackedConn]struct{}
	total int
	peak  int

	// boxMu guards the fields below. They describe the mailboxes, because
	// imapmemserver cannot list its mailboxes with special-use attributes.
	boxMu      sync.Mutex
	boxes      map[string]struct{}
	specialUse map[string][]imap.MailboxAttr
	selects    int // SELECT commands
	examines   int // EXAMINE commands
	fetched    []string
	// commands are the names of the commands of clients, in order (see
	// Commands). writes are the ones that change the mailboxes (see
	// WriteCommands).
	commands []string
	writes   []string

	// sent counts the bytes that the server wrote to its connections.
	sent atomic.Int64
}

// New starts a server and registers its shutdown with t.Cleanup. It stops
// the test with t.Fatal if the server cannot start.
func New(t testing.TB, opts ...Option) *Server {
	t.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	cert, der, err := selfSignedCert()
	if err != nil {
		t.Fatalf("imaptest: create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("imaptest: parse certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	sum := sha256.Sum256(der)

	if o.user == "" {
		o.user, o.pass = "tester", "s3cret-pass"
	}
	s := &Server{
		CertSHA256: hex.EncodeToString(sum[:]),
		CertPool:   pool,
		User:       o.user,
		Pass:       o.pass,
		t:          t,
		live:       make(map[*trackedConn]struct{}),
		boxes:      make(map[string]struct{}),
		specialUse: make(map[string][]imap.MailboxAttr),

		permanentFlags: o.permanentFlags,
		noCopyUID:      o.noCopyUID,
		noAppendUID:    o.noAppendUID,
		appendErr:      o.appendErr,
	}

	mem := imapmemserver.New()
	s.user = imapmemserver.NewUser(s.User, s.Pass)
	mem.AddUser(s.user)
	s.Create("INBOX")

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	caps := o.caps
	if caps == nil {
		caps = imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}}
	}
	srvOpts := &imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			// mem.NewSession returns a session that also supports NAMESPACE
			// and MOVE (imapserver.SessionIMAP4rev2). This runs in a server
			// goroutine, so it must not call t.Fatal.
			sess := mem.NewSession()
			inner, ok := sess.(imapserver.SessionIMAP4rev2)
			if !ok {
				t.Errorf("imaptest: the session of imapmemserver lost its IMAP4rev2 methods")
				return sess, nil, nil
			}
			base := &session{SessionIMAP4rev2: inner, s: s}
			if o.appendLimit > 0 {
				return &limitedSession{session: base, limit: o.appendLimit}, nil, nil
			}
			return base, nil, nil
		},
		Caps:   caps,
		Logger: discardLogger{},
	}
	switch o.mode {
	case ModeSTARTTLS:
		srvOpts.TLSConfig = tlsConfig
	case ModePlain:
		srvOpts.InsecureAuth = true
	}
	s.srv = imapserver.New(srvOpts)

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("imaptest: listen: %v", err)
	}
	s.Addr = raw.Addr().String()
	host, portText, err := net.SplitHostPort(s.Addr)
	if err != nil {
		t.Fatalf("imaptest: parse address: %v", err)
	}
	s.Host = host
	s.Port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("imaptest: parse port: %v", err)
	}

	// The tracking listener sits under the TLS listener, so it sees the raw
	// connection. That is the connection DropConnections closes.
	tracker := &trackingListener{Listener: raw, s: s, accepting: make(chan struct{})}
	var ln net.Listener = tracker
	if o.mode == ModeTLS {
		ln = tls.NewListener(ln, tlsConfig)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		// Serve returns after Close. Nothing can use its result.
		_ = s.srv.Serve(ln)
	}()
	// Wait until Serve is ready to accept. Serve registers its listener and
	// then calls WaitGroup.Add. Server.Close calls WaitGroup.Wait. If Close
	// ran before the Add call, that would be a data race inside imapserver.
	// Serve calls Accept after the Add call, so once Accept has been called
	// it is safe to close the server.
	select {
	case <-tracker.accepting:
	case <-served:
	}

	t.Cleanup(s.Close)
	return s
}

// Append stores a raw message (RFC 5322 text) in a mailbox with the given
// flags. It creates the mailbox if it does not exist. It stops the test with
// t.Fatal on an error.
func (s *Server) Append(mailbox string, raw []byte, flags ...imap.Flag) {
	s.t.Helper()
	s.AppendAt(mailbox, raw, time.Time{}, flags...)
}

// AppendAt is Append with an internal date (the date the server received the
// message). A zero time means the current time.
func (s *Server) AppendAt(mailbox string, raw []byte, at time.Time, flags ...imap.Flag) {
	s.t.Helper()
	s.Create(mailbox)
	// bytes.Reader has Read and Size, so it is a valid imap.LiteralReader.
	opts := &imap.AppendOptions{Flags: flags, Time: at}
	if _, err := s.user.Append(mailbox, bytes.NewReader(raw), opts); err != nil {
		s.t.Fatalf("imaptest: append: %v", err)
	}
}

// Create makes an empty mailbox. It does nothing if the mailbox exists. It
// stops the test with t.Fatal on an error.
//
// The optional attributes are special-use attributes (RFC 6154, for example
// imap.MailboxAttrTrash) that LIST reports for the mailbox. imapmemserver
// cannot set them. When the first mailbox with such an attribute is made,
// this package answers LIST itself, from the mailboxes made through this
// package and through CREATE. That answer has no \Subscribed attribute.
func (s *Server) Create(mailbox string, specialUse ...imap.MailboxAttr) {
	s.t.Helper()
	if err := s.user.Create(mailbox, nil); err != nil {
		var imapErr *imap.Error
		if !errors.As(err, &imapErr) || imapErr.Code != imap.ResponseCodeAlreadyExists {
			s.t.Fatalf("imaptest: create mailbox: %v", err)
		}
	}
	s.boxMu.Lock()
	defer s.boxMu.Unlock()
	s.boxes[mailbox] = struct{}{}
	if len(specialUse) > 0 {
		s.specialUse[mailbox] = append([]imap.MailboxAttr(nil), specialUse...)
	}
}

// Delete removes a mailbox with its messages. When a mailbox with the same
// name is made again, it has a new UIDVALIDITY, as on a real server. It stops
// the test with t.Fatal on an error.
func (s *Server) Delete(mailbox string) {
	s.t.Helper()
	if err := s.user.Delete(mailbox); err != nil {
		s.t.Fatalf("imaptest: delete mailbox: %v", err)
	}
	s.forget(mailbox)
}

func (s *Server) forget(mailbox string) {
	s.boxMu.Lock()
	defer s.boxMu.Unlock()
	delete(s.boxes, mailbox)
	delete(s.specialUse, mailbox)
}

// Unseen returns how many messages of a mailbox do not have the \Seen flag. It
// reads the state of the server, not the answer to a command, so a test can
// check that a client did not mark mail as read. It stops the test with
// t.Fatal if the mailbox does not exist.
func (s *Server) Unseen(mailbox string) int {
	s.t.Helper()
	data, err := s.user.Status(mailbox, &imap.StatusOptions{NumUnseen: true})
	if err != nil || data.NumUnseen == nil {
		s.t.Fatalf("imaptest: status of %q: %v", mailbox, err)
	}
	return int(*data.NumUnseen)
}

// SelectCounts returns how many SELECT commands (readWrite) and how many
// EXAMINE commands (readOnly) the server has received. A client that only
// reads mail must use EXAMINE, so that nothing changes in the mailbox.
func (s *Server) SelectCounts() (readWrite, readOnly int) {
	s.boxMu.Lock()
	defer s.boxMu.Unlock()
	return s.selects, s.examines
}

// FetchedSections returns every body section that clients asked for with
// FETCH, in the order of the commands. Each entry is written as the client
// sends it, for example "BODY.PEEK[1]<0.2048>" or "BODY[]". A test uses it to
// check which parts of a message the client read, and that it used PEEK.
func (s *Server) FetchedSections() []string {
	s.boxMu.Lock()
	defer s.boxMu.Unlock()
	return append([]string(nil), s.fetched...)
}

// WriteCommands returns the names of the commands of clients that change the
// mailboxes, in the order that the server got them: APPEND, COPY, CREATE,
// DELETE, EXPUNGE, MOVE, RENAME, STORE, SUBSCRIBE and UNSUBSCRIBE. A command
// is recorded when it arrives, also when it then fails. The methods of Server
// that set up a test (Append, Create, Delete) are not commands of a client and
// are not recorded. A test uses it to show that a refused request never
// changed the mail server.
func (s *Server) WriteCommands() []string {
	s.boxMu.Lock()
	defer s.boxMu.Unlock()
	return append([]string(nil), s.writes...)
}

// Commands returns the names of the commands of clients, in the order that the
// server got them: LOGIN, and every command that reads or changes the
// mailboxes (LIST, STATUS, SELECT, EXAMINE, SEARCH, FETCH, NAMESPACE,
// UNSELECT, IDLE, and the commands of WriteCommands). A command is recorded
// when it arrives, also when it then fails. Commands that do not reach the
// mailboxes (CAPABILITY, NOOP, LOGOUT, STARTTLS) are not recorded, so a pooled
// connection that is kept alive adds nothing. A test uses it to show that a
// refused request never reached the mail server at all.
func (s *Server) Commands() []string {
	s.boxMu.Lock()
	defer s.boxMu.Unlock()
	return append([]string(nil), s.commands...)
}

// record adds a command to the list of Commands.
func (s *Server) record(name string) {
	s.boxMu.Lock()
	defer s.boxMu.Unlock()
	s.commands = append(s.commands, name)
}

// recordWrite adds a command to the lists of Commands and WriteCommands.
func (s *Server) recordWrite(name string) {
	s.boxMu.Lock()
	defer s.boxMu.Unlock()
	s.commands = append(s.commands, name)
	s.writes = append(s.writes, name)
}

// BytesSent returns how many bytes the server has written to its client
// connections so far. The count is taken under TLS, so it is a little larger
// than the IMAP data alone. A test uses the difference between two calls to
// check that a client did not download a large part of a message.
func (s *Server) BytesSent() int64 { return s.sent.Load() }

// DropConnections closes every connection the server has open, as a network
// failure or a server restart would. The server keeps accepting new
// connections.
func (s *Server) DropConnections() {
	s.mu.Lock()
	conns := make([]*trackedConn, 0, len(s.live))
	for c := range s.live {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		// A close error means the connection is already gone.
		_ = c.Close()
	}
}

// OpenConns returns how many connections the server has open now.
//
// The count drops a short time after a client closes its side, because the
// server notices the close in its own goroutine. Use require.Eventually to
// wait for a count.
func (s *Server) OpenConns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.live)
}

// TotalConns returns how many connections the server has accepted since it
// started.
func (s *Server) TotalConns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// PeakConns returns the largest number of connections the server had open
// at the same time.
func (s *Server) PeakConns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

// Close stops the server and closes all connections. It is safe to call more
// than once. New registers it with t.Cleanup, so a test does not need to
// call it.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		// Close returns the listener close error. The test is over, so the
		// error cannot change anything.
		_ = s.srv.Close()
	})
}

func (s *Server) track(c *trackedConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live[c] = struct{}{}
	s.total++
	if len(s.live) > s.peak {
		s.peak = len(s.live)
	}
}

func (s *Server) untrack(c *trackedConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.live, c)
}

// trackingListener counts the connections it accepts.
type trackingListener struct {
	net.Listener
	s *Server
	// accepting is closed when Accept is called for the first time. It shows
	// that Serve has started its accept loop.
	accepting chan struct{}
	once      sync.Once
}

func (l *trackingListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.accepting) })
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tc := &trackedConn{Conn: c, s: l.s}
	l.s.track(tc)
	return tc, nil
}

// trackedConn removes itself from the live set when it is closed.
type trackedConn struct {
	net.Conn
	s    *Server
	once sync.Once
}

func (c *trackedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.s.sent.Add(int64(n))
	return n, err
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.s.untrack(c) })
	return err
}

// discardLogger hides the server's log lines, so test output stays clean.
type discardLogger struct{}

func (discardLogger) Printf(string, ...interface{}) {}

// selfSignedCert makes an ECDSA P-256 certificate for 127.0.0.1 (common name
// and IP address). It returns the TLS certificate and its DER bytes.
func selfSignedCert() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, der, nil
}

// session wraps the session of imapmemserver. It records SELECT and EXAMINE,
// keeps the list of mailboxes, and answers LIST when a mailbox has special-use
// attributes.
type session struct {
	imapserver.SessionIMAP4rev2
	s *Server
}

func (ss *session) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	readOnly := options != nil && options.ReadOnly
	ss.s.boxMu.Lock()
	if readOnly {
		ss.s.examines++
		ss.s.commands = append(ss.s.commands, "EXAMINE")
	} else {
		ss.s.selects++
		ss.s.commands = append(ss.s.commands, "SELECT")
	}
	ss.s.boxMu.Unlock()
	data, err := ss.SessionIMAP4rev2.Select(mailbox, options)
	if err == nil && ss.s.permanentFlags != nil {
		data.PermanentFlags = ss.s.permanentFlags(mailbox, readOnly, data.PermanentFlags)
	}
	return data, err
}

// Copy copies the messages like imapmemserver. When the server was made
// WithoutCopyUID, it does not report the new UIDs.
func (ss *session) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	ss.s.recordWrite("COPY")
	data, err := ss.SessionIMAP4rev2.Copy(numSet, dest)
	if ss.s.noCopyUID {
		return nil, err
	}
	return data, err
}

// Append stores the message like imapmemserver. A server made WithAppendError
// may refuse it. A server made WithoutAppendUID does not report the new UID.
func (ss *session) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	ss.s.recordWrite("APPEND")
	if fn := ss.s.appendErr; fn != nil {
		if err := fn(mailbox); err != nil {
			var imapErr *imap.Error
			if errors.As(err, &imapErr) {
				return nil, imapErr
			}
			return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: err.Error()}
		}
	}
	data, err := ss.SessionIMAP4rev2.Append(mailbox, r, options)
	if ss.s.noAppendUID {
		return nil, err
	}
	return data, err
}

// limitedSession is a session that limits the size of APPEND (see
// WithAppendLimit).
type limitedSession struct {
	*session
	limit uint32
}

// AppendLimit is the largest message that APPEND accepts.
func (ls *limitedSession) AppendLimit() uint32 { return ls.limit }

// Fetch records the body sections of the command, then lets imapmemserver
// answer it.
func (ss *session) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	ss.s.boxMu.Lock()
	ss.s.commands = append(ss.s.commands, "FETCH")
	for _, sec := range options.BodySection {
		ss.s.fetched = append(ss.s.fetched, sectionString(sec))
	}
	ss.s.boxMu.Unlock()
	return ss.SessionIMAP4rev2.Fetch(w, numSet, options)
}

// sectionString writes a body section in the form a client sends it.
func sectionString(sec *imap.FetchItemBodySection) string {
	var b strings.Builder
	b.WriteString("BODY")
	if sec.Peek {
		b.WriteString(".PEEK")
	}
	b.WriteByte('[')
	for i, n := range sec.Part {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.Itoa(n))
	}
	if sec.Specifier != imap.PartSpecifierNone {
		if len(sec.Part) > 0 {
			b.WriteByte('.')
		}
		b.WriteString(string(sec.Specifier))
		if len(sec.HeaderFields) > 0 {
			b.WriteString(".FIELDS (" + strings.Join(sec.HeaderFields, " ") + ")")
		}
		if len(sec.HeaderFieldsNot) > 0 {
			b.WriteString(".FIELDS.NOT (" + strings.Join(sec.HeaderFieldsNot, " ") + ")")
		}
	}
	b.WriteByte(']')
	if p := sec.Partial; p != nil {
		b.WriteString("<" + strconv.FormatInt(p.Offset, 10) + "." + strconv.FormatInt(p.Size, 10) + ">")
	}
	return b.String()
}

func (ss *session) Create(mailbox string, options *imap.CreateOptions) error {
	ss.s.recordWrite("CREATE")
	if err := ss.SessionIMAP4rev2.Create(mailbox, options); err != nil {
		return err
	}
	name := strings.TrimRight(mailbox, string(listDelim))
	ss.s.boxMu.Lock()
	defer ss.s.boxMu.Unlock()
	ss.s.boxes[name] = struct{}{}
	if options != nil && len(options.SpecialUse) > 0 {
		ss.s.specialUse[name] = append([]imap.MailboxAttr(nil), options.SpecialUse...)
	}
	return nil
}

func (ss *session) Delete(mailbox string) error {
	ss.s.recordWrite("DELETE")
	if err := ss.SessionIMAP4rev2.Delete(mailbox); err != nil {
		return err
	}
	ss.s.forget(mailbox)
	return nil
}

func (ss *session) Rename(mailbox, newName string, options *imap.RenameOptions) error {
	ss.s.recordWrite("RENAME")
	if err := ss.SessionIMAP4rev2.Rename(mailbox, newName, options); err != nil {
		return err
	}
	newName = strings.TrimRight(newName, string(listDelim))
	ss.s.boxMu.Lock()
	defer ss.s.boxMu.Unlock()
	delete(ss.s.boxes, mailbox)
	ss.s.boxes[newName] = struct{}{}
	if attrs, ok := ss.s.specialUse[mailbox]; ok {
		delete(ss.s.specialUse, mailbox)
		ss.s.specialUse[newName] = attrs
	}
	return nil
}

// Login logs in like imapmemserver, and records the command.
func (ss *session) Login(username, password string) error {
	ss.s.record("LOGIN")
	return ss.SessionIMAP4rev2.Login(username, password)
}

// Status answers like imapmemserver, and records the command.
func (ss *session) Status(mailbox string, options *imap.StatusOptions) (*imap.StatusData, error) {
	ss.s.record("STATUS")
	return ss.SessionIMAP4rev2.Status(mailbox, options)
}

// Search searches like imapmemserver, and records the command.
func (ss *session) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	ss.s.record("SEARCH")
	return ss.SessionIMAP4rev2.Search(kind, criteria, options)
}

// Namespace answers like imapmemserver, and records the command.
func (ss *session) Namespace() (*imap.NamespaceData, error) {
	ss.s.record("NAMESPACE")
	return ss.SessionIMAP4rev2.Namespace()
}

// Unselect closes the mailbox like imapmemserver, and records the command.
func (ss *session) Unselect() error {
	ss.s.record("UNSELECT")
	return ss.SessionIMAP4rev2.Unselect()
}

// Idle waits like imapmemserver, and records the command.
func (ss *session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	ss.s.record("IDLE")
	return ss.SessionIMAP4rev2.Idle(w, stop)
}

// Store changes flags like imapmemserver, and records the command.
func (ss *session) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	ss.s.recordWrite("STORE")
	return ss.SessionIMAP4rev2.Store(w, numSet, flags, options)
}

// Move moves messages like imapmemserver, and records the command.
func (ss *session) Move(w *imapserver.MoveWriter, numSet imap.NumSet, dest string) error {
	ss.s.recordWrite("MOVE")
	return ss.SessionIMAP4rev2.Move(w, numSet, dest)
}

// Expunge removes messages like imapmemserver, and records the command.
func (ss *session) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	ss.s.recordWrite("EXPUNGE")
	return ss.SessionIMAP4rev2.Expunge(w, uids)
}

// Subscribe subscribes like imapmemserver, and records the command.
func (ss *session) Subscribe(mailbox string) error {
	ss.s.recordWrite("SUBSCRIBE")
	return ss.SessionIMAP4rev2.Subscribe(mailbox)
}

// Unsubscribe unsubscribes like imapmemserver, and records the command.
func (ss *session) Unsubscribe(mailbox string) error {
	ss.s.recordWrite("UNSUBSCRIBE")
	return ss.SessionIMAP4rev2.Unsubscribe(mailbox)
}

// listDelim is the hierarchy delimiter of imapmemserver.
const listDelim = '/'

// List answers like imapmemserver, until a mailbox has special-use
// attributes. Then it answers from its own list of mailboxes, with those
// attributes.
func (ss *session) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	ss.s.boxMu.Lock()
	ss.s.commands = append(ss.s.commands, "LIST")
	custom := len(ss.s.specialUse) > 0
	names := make([]string, 0, len(ss.s.boxes))
	for name := range ss.s.boxes {
		names = append(names, name)
	}
	attrs := make(map[string][]imap.MailboxAttr, len(ss.s.specialUse))
	for name, a := range ss.s.specialUse {
		attrs[name] = a
	}
	ss.s.boxMu.Unlock()

	if !custom || len(patterns) == 0 || options.SelectSubscribed {
		return ss.SessionIMAP4rev2.List(w, ref, patterns, options)
	}

	sort.Strings(names)
	for _, name := range names {
		match := false
		for _, pattern := range patterns {
			if imapserver.MatchList(name, listDelim, ref, pattern) {
				match = true
				break
			}
		}
		if !match || (options.SelectSpecialUse && len(attrs[name]) == 0) {
			continue
		}
		data := imap.ListData{Mailbox: name, Delim: listDelim}
		if options.ReturnSpecialUse || options.SelectSpecialUse {
			data.Attrs = append(data.Attrs, attrs[name]...)
		}
		if options.ReturnStatus != nil {
			status, err := ss.s.user.Status(name, options.ReturnStatus)
			if err != nil {
				return err
			}
			data.Status = status
		}
		if err := w.WriteList(&data); err != nil {
			return err
		}
	}
	return nil
}
