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
	"strconv"
	"sync"
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
	mode Mode
}

// WithMode sets the server mode. The default is ModeTLS.
func WithMode(m Mode) Option {
	return func(o *options) { o.mode = m }
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

	closeOnce sync.Once

	mu    sync.Mutex
	live  map[*trackedConn]struct{}
	total int
	peak  int
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

	s := &Server{
		CertSHA256: hex.EncodeToString(sum[:]),
		CertPool:   pool,
		User:       "tester",
		Pass:       "s3cret-pass",
		t:          t,
		live:       make(map[*trackedConn]struct{}),
	}

	mem := imapmemserver.New()
	s.user = imapmemserver.NewUser(s.User, s.Pass)
	if err := s.user.Create("INBOX", nil); err != nil {
		t.Fatalf("imaptest: create INBOX: %v", err)
	}
	mem.AddUser(s.user)

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srvOpts := &imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:   imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
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
	var ln net.Listener = &trackingListener{Listener: raw, s: s}
	if o.mode == ModeTLS {
		ln = tls.NewListener(ln, tlsConfig)
	}
	go func() {
		// Serve returns after Close. Nothing can use its result.
		_ = s.srv.Serve(ln)
	}()

	t.Cleanup(s.Close)
	return s
}

// Append stores a raw message (RFC 5322 text) in a mailbox with the given
// flags. It creates the mailbox if it does not exist. It stops the test with
// t.Fatal on an error.
func (s *Server) Append(mailbox string, raw []byte, flags ...imap.Flag) {
	s.t.Helper()
	if err := s.user.Create(mailbox, nil); err != nil {
		var imapErr *imap.Error
		if !errors.As(err, &imapErr) || imapErr.Code != imap.ResponseCodeAlreadyExists {
			s.t.Fatalf("imaptest: create mailbox: %v", err)
		}
	}
	// bytes.Reader has Read and Size, so it is a valid imap.LiteralReader.
	if _, err := s.user.Append(mailbox, bytes.NewReader(raw), &imap.AppendOptions{Flags: flags}); err != nil {
		s.t.Fatalf("imaptest: append: %v", err)
	}
}

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
}

func (l *trackingListener) Accept() (net.Conn, error) {
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
