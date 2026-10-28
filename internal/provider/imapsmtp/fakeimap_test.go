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

// fakeIMAP is a raw TCP server that follows a script. It lets a test make
// server behaviour that the in-memory IMAP server cannot make: a refused
// STARTTLS, a server that never answers, or a failed login that echoes text.
//
// It listens on 127.0.0.1, sends the greeting, then answers each command line
// with the text that reply returns.
type fakeIMAP struct {
	host string
	port int

	mu       sync.Mutex
	received strings.Builder
	conns    []net.Conn
}

// fakeReply decides the answer to one command. tag and cmd are the first two
// words of the line (cmd in upper case). The returned text is written as is,
// so it must end with CRLF. When closeAfter is true the server then closes
// the connection.
type fakeReply func(tag, cmd string) (reply string, closeAfter bool)

// newFakeIMAP starts the server. An empty greeting means the server stays
// silent after the connection opens.
func newFakeIMAP(t *testing.T, greeting string, reply fakeReply) *fakeIMAP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	host, portText, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	f := &fakeIMAP{host: host, port: port}
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

func (f *fakeIMAP) serve(conn net.Conn, greeting string, reply fakeReply) {
	defer func() { _ = conn.Close() }()
	if greeting != "" {
		if _, err := conn.Write([]byte(greeting)); err != nil {
			return
		}
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
		words := strings.Fields(line)
		if len(words) < 2 {
			continue
		}
		text, closeAfter := reply(words[0], strings.ToUpper(words[1]))
		if text != "" {
			if _, err := conn.Write([]byte(text)); err != nil {
				return
			}
		}
		if closeAfter {
			return
		}
	}
}

// Received returns every byte the clients sent to the server so far.
func (f *fakeIMAP) Received() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.received.String()
}

// plainConfig returns a config that reaches the fake server without TLS.
func (f *fakeIMAP) plainConfig() Config {
	cfg := baseConfig()
	cfg.Server.IMAPHost = f.host
	cfg.Server.IMAPPort = f.port
	cfg.Server.IMAPSecurity = account.SecurityPlain
	return cfg
}
