package imapsmtp

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// fakeSMTP is a raw TCP server that follows a script. It lets a test make SMTP
// behaviour that a real SMTP server library does not make: a server that
// offers STARTTLS and then refuses it, or one that stays in plain text after
// it said "go ahead". It also lets a test read the exact commands the client
// sent.
//
// It listens on 127.0.0.1, sends the greeting, then answers each command line
// with the text that reply returns. It reuses fakeThen of fakeIMAP.
type fakeSMTP struct {
	host string
	port int

	mu       sync.Mutex
	received strings.Builder
	conns    []net.Conn
}

// smtpReply decides the answer to one command line (without the line break).
// The returned text is written as it is, so it must end with CRLF.
type smtpReply func(line string) (reply string, then fakeThen)

// newFakeSMTP starts the server.
func newFakeSMTP(t *testing.T, greeting string, reply smtpReply) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	host, portText, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	f := &fakeSMTP{host: host, port: port}
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, conn)
			f.mu.Unlock()
			go f.serve(conn, greeting, reply)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(conn net.Conn, greeting string, reply smtpReply) {
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(greeting)); err != nil {
		return
	}
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		f.mu.Lock()
		f.received.WriteString(line)
		f.mu.Unlock()
		if err != nil {
			return
		}
		text, then := reply(strings.TrimRight(line, "\r\n"))
		if text != "" {
			if _, err := conn.Write([]byte(text)); err != nil {
				return
			}
		}
		if then != fakeStay {
			return
		}
	}
}

// Received returns every byte the clients sent to the server so far.
func (f *fakeSMTP) Received() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.received.String()
}

// config returns a config that reaches the fake server. The IMAP settings are
// valid but unused.
func (f *fakeSMTP) config(security account.Security) Config {
	cfg := baseConfig()
	cfg.Server.IMAPHost = "imap.example.com"
	cfg.Server.IMAPPort = 993
	cfg.Server.IMAPSecurity = account.SecurityTLS
	cfg.Server.SMTPHost = f.host
	cfg.Server.SMTPPort = f.port
	cfg.Server.SMTPSecurity = security
	cfg.TokenScope = testScope
	return cfg
}

// smtpVerb returns the command word of a line, in upper case.
func smtpVerb(line string) string {
	word, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	return strings.ToUpper(word)
}
