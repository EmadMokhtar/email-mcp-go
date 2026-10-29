// Package smtptest runs a small SMTP server for tests.
//
// The server listens on 127.0.0.1 with a random port. It has one user, it
// requires AUTH PLAIN before it takes a message, and it keeps every message it
// accepts. It uses a new self-signed certificate every time, so a test can
// check pinning (CertSHA256) and normal verification (CertPool).
//
// Use New to start a server. The server stops when the test ends.
//
//	srv := smtptest.New(t)                                         // TLS from the first byte
//	srv := smtptest.New(t, smtptest.WithMode(smtptest.ModeSTARTTLS))
//	srv := smtptest.New(t, smtptest.WithMode(smtptest.ModePlain))
//
// A test can also choose the login (see WithLogin), reject a recipient (see
// WithRcptHook and RejectRcpt),
// reject or lose a message (see WithDataHook and ErrDropConnection), slow the
// server down (see WithDelay) and read what the server saw (see Messages,
// AuthAttempts and Resets).
//
// This package is for tests only. Do not import it from production code.
package smtptest

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
	"io"
	"math/big"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// Mode is how the server protects its connections.
type Mode int

// The server modes.
const (
	// ModeTLS starts TLS on connect (implicit TLS). This is the default.
	ModeTLS Mode = iota
	// ModeSTARTTLS starts in plain text, advertises STARTTLS, and offers AUTH
	// only after the connection is upgraded.
	ModeSTARTTLS
	// ModeNoSTARTTLS never advertises STARTTLS, but it does offer AUTH on the
	// plain connection. A client that must use STARTTLS has to stop here
	// and not send the password.
	ModeNoSTARTTLS
	// ModePlain never uses TLS and offers AUTH on the plain connection.
	ModePlain
)

// ErrDropConnection is an error for a DataHook. The server keeps the message
// as accepted, but it closes the connection without the final reply. The
// client cannot know whether the message was delivered.
var ErrDropConnection = errors.New("smtptest: drop the connection")

// Message is a message that the server accepted.
type Message struct {
	// From is the address of MAIL FROM.
	From string
	// Rcpt are the addresses of RCPT TO that the server accepted, in order.
	Rcpt []string
	// Data is the message as the client sent it, with the dot-stuffing
	// removed.
	Data []byte
}

// Option changes how New starts the server.
type Option func(*options)

type options struct {
	mode     Mode
	user     string
	pass     string
	rcptHook func(addr string) error
	dataHook func(Message) error
	delay    time.Duration
}

// WithMode sets the server mode. The default is ModeTLS.
func WithMode(m Mode) Option {
	return func(o *options) { o.mode = m }
}

// WithLogin sets the user name and the password that the server accepts. The
// default is "tester" and "s3cret-pass", the login of imaptest.
func WithLogin(user, pass string) Option {
	return func(o *options) { o.user, o.pass = user, pass }
}

// WithRcptHook sets a function that decides on each RCPT TO. When it returns
// an error, the server refuses the recipient. An *smtp.SMTPError gives its own
// reply code and text. Any other error gives code 550.
func WithRcptHook(fn func(addr string) error) Option {
	return func(o *options) { o.rcptHook = fn }
}

// RejectRcpt is a WithRcptHook that refuses the given addresses with code 550.
// As many real servers do, the text of the reply repeats the address.
func RejectRcpt(addrs ...string) Option {
	return WithRcptHook(func(addr string) error {
		for _, a := range addrs {
			if a == addr {
				return &smtp.SMTPError{
					Code:         550,
					EnhancedCode: smtp.EnhancedCode{5, 1, 1},
					Message:      "<" + addr + ">: Recipient address rejected: User unknown",
				}
			}
		}
		return nil
	})
}

// WithDataHook sets a function that decides on each message, after the server
// has read it. When it returns an *smtp.SMTPError (or another error), the
// server refuses the message and keeps nothing. When it returns
// ErrDropConnection, the server keeps the message and closes the connection
// without a reply.
func WithDataHook(fn func(Message) error) Option {
	return func(o *options) { o.dataHook = fn }
}

// WithDelay makes the server wait for d before it answers AUTH, MAIL FROM,
// RCPT TO and the end of DATA.
func WithDelay(d time.Duration) Option {
	return func(o *options) { o.delay = d }
}

// Server is a running test SMTP server.
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

	t        testing.TB
	srv      *smtp.Server
	ln       net.Listener
	rcptHook func(addr string) error
	dataHook func(Message) error
	delay    time.Duration

	closeOnce sync.Once
	auths     atomic.Int64
	resets    atomic.Int64
	// conns counts the connections that the server accepted.
	conns atomic.Int64

	mu       sync.Mutex
	messages []Message
}

// New starts a server and registers its shutdown with t.Cleanup. It stops the
// test with t.Fatal if the server cannot start.
func New(t testing.TB, opts ...Option) *Server {
	t.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	cert, der, err := selfSignedCert()
	if err != nil {
		t.Fatalf("smtptest: create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("smtptest: parse certificate: %v", err)
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
		rcptHook:   o.rcptHook,
		dataHook:   o.dataHook,
		delay:      o.delay,
	}

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv := smtp.NewServer(&backend{s: s})
	srv.Domain = "localhost"
	srv.ErrorLog = discardLogger{}
	switch o.mode {
	case ModeTLS, ModeSTARTTLS:
		// With implicit TLS, the connections are TLS already, so the server
		// does not advertise STARTTLS on them.
		srv.TLSConfig = tlsConfig
	case ModeNoSTARTTLS, ModePlain:
		srv.AllowInsecureAuth = true
	}
	s.srv = srv

	var ln net.Listener
	ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtptest: listen: %v", err)
	}
	s.Addr = ln.Addr().String()
	host, portText, err := net.SplitHostPort(s.Addr)
	if err != nil {
		t.Fatalf("smtptest: parse address: %v", err)
	}
	s.Host = host
	s.Port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("smtptest: parse port: %v", err)
	}
	// The counting listener sits under the TLS listener, so it counts every
	// connection, also one that fails the handshake or never says anything.
	ln = &countingListener{Listener: ln, n: &s.conns}
	if o.mode == ModeTLS {
		ln = tls.NewListener(ln, tlsConfig)
	}
	s.ln = ln
	go func() {
		// Serve returns after Close. Nothing can use its result.
		_ = srv.Serve(ln)
	}()

	t.Cleanup(s.Close)
	return s
}

// Messages returns the messages that the server accepted, oldest first.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, len(s.messages))
	for i, m := range s.messages {
		out[i] = Message{
			From: m.From,
			Rcpt: append([]string(nil), m.Rcpt...),
			Data: append([]byte(nil), m.Data...),
		}
	}
	return out
}

// AuthAttempts returns how many AUTH commands reached the server. A client
// that must not send AUTH (for example before TLS) leaves this at zero.
func (s *Server) AuthAttempts() int { return int(s.auths.Load()) }

// Connections returns how many connections the server has accepted since it
// started, also the ones that failed the TLS handshake or never sent a
// command. A test uses it to show that nothing connected to the server.
func (s *Server) Connections() int { return int(s.conns.Load()) }

// countingListener counts the connections that it accepts.
type countingListener struct {
	net.Listener
	n *atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.n.Add(1)
	}
	return c, err
}

// Resets returns how many times the server reset a mail transaction: for an
// RSET command, and also after each message it read.
func (s *Server) Resets() int { return int(s.resets.Load()) }

// Close stops the server and closes all connections. It is safe to call more
// than once. New registers it with t.Cleanup, so a test does not need to call
// it.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		// The listener is closed here too. smtp.Server.Close closes only the
		// listeners that Serve has registered, and Serve may not have run yet.
		// The errors of both calls tell nothing a test can use.
		_ = s.ln.Close()
		_ = s.srv.Close()
	})
}

func (s *Server) wait() {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
}

type backend struct{ s *Server }

func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &session{s: b.s, conn: c}, nil
}

// session is one SMTP session. It lives until the client says QUIT, starts
// TLS, or the connection ends.
type session struct {
	s    *Server
	conn *smtp.Conn

	authed bool
	from   string
	rcpt   []string
}

var _ smtp.AuthSession = (*session)(nil)

func (ss *session) AuthMechanisms() []string { return []string{sasl.Plain} }

func (ss *session) Auth(mech string) (sasl.Server, error) {
	ss.s.auths.Add(1)
	if mech != sasl.Plain {
		return nil, smtp.ErrAuthUnknownMechanism
	}
	return sasl.NewPlainServer(func(_, username, password string) error {
		ss.s.wait()
		if username != ss.s.User || password != ss.s.Pass {
			// Many servers repeat the user name in the text. A client must
			// not pass that text on.
			return &smtp.SMTPError{
				Code:         535,
				EnhancedCode: smtp.EnhancedCode{5, 7, 8},
				Message:      "Authentication failed for " + username,
			}
		}
		ss.authed = true
		return nil
	}), nil
}

func (ss *session) Mail(from string, _ *smtp.MailOptions) error {
	if !ss.authed {
		return smtp.ErrAuthRequired
	}
	ss.s.wait()
	ss.from = from
	return nil
}

func (ss *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	ss.s.wait()
	if hook := ss.s.rcptHook; hook != nil {
		if err := hook(to); err != nil {
			var smtpErr *smtp.SMTPError
			if errors.As(err, &smtpErr) {
				return smtpErr
			}
			return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: err.Error()}
		}
	}
	ss.rcpt = append(ss.rcpt, to)
	return nil
}

func (ss *session) Data(r io.Reader) error {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		return err
	}
	ss.s.wait()
	msg := Message{From: ss.from, Rcpt: append([]string(nil), ss.rcpt...), Data: buf.Bytes()}
	if hook := ss.s.dataHook; hook != nil {
		switch err := hook(msg); {
		case errors.Is(err, ErrDropConnection):
			ss.s.keep(msg)
			// Closing the connection stops the final reply. The error of
			// Close tells nothing a test can use.
			_ = ss.conn.Conn().Close()
			return err
		case err != nil:
			var smtpErr *smtp.SMTPError
			if errors.As(err, &smtpErr) {
				return smtpErr
			}
			return &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 0, 0}, Message: err.Error()}
		}
	}
	ss.s.keep(msg)
	return nil
}

func (s *Server) keep(m Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, m)
}

func (ss *session) Reset() {
	ss.s.resets.Add(1)
	ss.from, ss.rcpt = "", nil
}

func (ss *session) Logout() error { return nil }

// discardLogger hides the server's log lines, so test output stays clean.
type discardLogger struct{}

func (discardLogger) Printf(string, ...interface{}) {}
func (discardLogger) Println(...interface{})        {}

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
