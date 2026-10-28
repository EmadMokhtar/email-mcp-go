package imapsmtp

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
)

// baseConfig returns a config with valid SMTP settings and no IMAP address.
// MailServer.Validate checks both halves, so every test needs both.
func baseConfig() Config {
	return Config{
		Server: account.MailServer{
			SMTPHost:     "smtp.example.com",
			SMTPPort:     465,
			SMTPSecurity: account.SecurityTLS,
			Username:     "tester",
		},
		Password: secrets.Secret("s3cret-pass"),
		Local:    true,
	}
}

// testConfig returns a config that logs in to the test server.
func testConfig(srv *imaptest.Server, security account.Security) Config {
	cfg := baseConfig()
	cfg.Server.IMAPHost = srv.Host
	cfg.Server.IMAPPort = srv.Port
	cfg.Server.IMAPSecurity = security
	cfg.Server.Username = srv.User
	cfg.Password = secrets.Secret(srv.Pass)
	return cfg
}

// pinnedConfig is testConfig with the server certificate pinned.
func pinnedConfig(srv *imaptest.Server, security account.Security) Config {
	cfg := testConfig(srv, security)
	cfg.Server.PinnedIMAPCert = srv.CertSHA256
	return cfg
}

// noop is the smallest useful pooled operation.
func noop(c *imapclient.Client) error { return c.Noop().Wait() }

// mustDial dials and closes the client when the test ends.
func mustDial(t *testing.T, cfg Config) *imapclient.Client {
	t.Helper()
	c, err := dialIMAP(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// newTestPool returns a pool that is closed when the test ends.
func newTestPool(t *testing.T, cfg Config) *pool {
	t.Helper()
	p := newPool(cfg)
	t.Cleanup(p.Close)
	return p
}

func TestDialRejectsUnknownSelfSignedCert(t *testing.T) {
	srv := imaptest.New(t)

	_, err := dialIMAP(t.Context(), testConfig(srv, account.SecurityTLS))

	var certErr *CertificateUnknownError
	require.ErrorAs(t, err, &certErr)
	require.Equal(t, srv.CertSHA256, certErr.FingerprintSHA256)
	require.Equal(t, srv.Host, certErr.Host)
}

func TestDialAcceptsPinnedCert(t *testing.T) {
	srv := imaptest.New(t)

	c := mustDial(t, pinnedConfig(srv, account.SecurityTLS))
	require.NoError(t, noop(c))

	t.Run("pin in upper case", func(t *testing.T) {
		cfg := pinnedConfig(srv, account.SecurityTLS)
		cfg.Server.PinnedIMAPCert = strings.ToUpper(srv.CertSHA256)
		require.NoError(t, noop(mustDial(t, cfg)))
	})
}

func TestDialRejectsWrongPin(t *testing.T) {
	srv := imaptest.New(t)
	wrong := strings.Repeat("0", 64)

	t.Run("pin only", func(t *testing.T) {
		cfg := testConfig(srv, account.SecurityTLS)
		cfg.Server.PinnedIMAPCert = wrong

		_, err := dialIMAP(t.Context(), cfg)

		var certErr *CertificateUnknownError
		require.ErrorAs(t, err, &certErr)
		// The error carries the certificate the server showed, not the pin.
		require.Equal(t, srv.CertSHA256, certErr.FingerprintSHA256)
	})

	t.Run("a trusted root does not replace the pin", func(t *testing.T) {
		cfg := testConfig(srv, account.SecurityTLS)
		cfg.Server.PinnedIMAPCert = wrong
		cfg.RootCAs = srv.CertPool

		_, err := dialIMAP(t.Context(), cfg)

		var certErr *CertificateUnknownError
		require.ErrorAs(t, err, &certErr)
	})
}

func TestPlainRefusedForRemoteHost(t *testing.T) {
	cfg := baseConfig()
	cfg.Server.IMAPHost = "mail.example.com"
	cfg.Server.IMAPPort = 143
	cfg.Server.IMAPSecurity = account.SecurityPlain
	// A cancelled context stops any dial at once. If the error is not the
	// context error, the check ran before any network step.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := dialIMAP(ctx, cfg)

	require.Error(t, err)
	require.NotErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "loopback")
}

func TestLoginFailureNotMaskedByLogout(t *testing.T) {
	srv := imaptest.New(t)
	cfg := pinnedConfig(srv, account.SecurityTLS)
	cfg.Password = secrets.Secret("wrong-password-93")

	_, err := dialIMAP(t.Context(), cfg)

	require.Error(t, err)
	require.ErrorContains(t, err, "login")
	require.NotContains(t, err.Error(), "wrong-password-93")
	// The response code from the server is in the message.
	require.ErrorContains(t, err, "[AUTHENTICATIONFAILED]")
	// The failed connection is closed.
	require.Eventually(t, func() bool { return srv.OpenConns() == 0 }, 5*time.Second, 10*time.Millisecond)
}

func TestPoolReusesIdleConnection(t *testing.T) {
	srv := imaptest.New(t)
	p := newTestPool(t, pinnedConfig(srv, account.SecurityTLS))

	for range 3 {
		require.NoError(t, p.with(t.Context(), noop))
	}

	require.Equal(t, 1, srv.TotalConns())
}

func TestPoolReconnectsAfterServerDrop(t *testing.T) {
	srv := imaptest.New(t)
	p := newTestPool(t, pinnedConfig(srv, account.SecurityTLS))
	require.NoError(t, p.with(t.Context(), noop))
	require.Equal(t, 1, srv.TotalConns())

	srv.DropConnections()

	require.NoError(t, p.with(t.Context(), noop))
	// One new connection, and no third one.
	require.Equal(t, 2, srv.TotalConns())
}

func TestPoolLimitsConnections(t *testing.T) {
	srv := imaptest.New(t)
	p := newTestPool(t, pinnedConfig(srv, account.SecurityTLS))

	// The first two calls wait for each other, so the test knows that the pool
	// really ran two calls at the same time.
	var inside, mostInside atomic.Int32
	bothInside := make(chan struct{})
	var once sync.Once
	fn := func(c *imapclient.Client) error {
		n := inside.Add(1)
		defer inside.Add(-1)
		for {
			m := mostInside.Load()
			if n <= m || mostInside.CompareAndSwap(m, n) {
				break
			}
		}
		if n == 2 {
			once.Do(func() { close(bothInside) })
		}
		select {
		case <-bothInside:
		case <-time.After(5 * time.Second):
			return errors.New("the pool never ran two calls at once")
		}
		return noop(c)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- p.with(t.Context(), fn)
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(2), mostInside.Load())
	require.Equal(t, 2, srv.PeakConns())
	require.Equal(t, 2, srv.TotalConns(), "the pool must reuse its two connections")
}

func TestPoolRetriesNetworkFailureOnce(t *testing.T) {
	for name, failure := range map[string]error{
		"unexpected EOF": io.ErrUnexpectedEOF,
		"EOF":            io.EOF,
	} {
		t.Run(name+" then success", func(t *testing.T) {
			srv := imaptest.New(t)
			p := newTestPool(t, pinnedConfig(srv, account.SecurityTLS))
			calls := 0

			err := p.with(t.Context(), func(c *imapclient.Client) error {
				calls++
				if calls == 1 {
					return failure
				}
				return noop(c)
			})

			require.NoError(t, err)
			require.Equal(t, 2, calls)
			// The connection that failed is not reused.
			require.Equal(t, 2, srv.TotalConns())
		})

		t.Run(name+" twice gives up", func(t *testing.T) {
			srv := imaptest.New(t)
			p := newTestPool(t, pinnedConfig(srv, account.SecurityTLS))
			calls := 0

			err := p.with(t.Context(), func(*imapclient.Client) error {
				calls++
				return failure
			})

			require.ErrorIs(t, err, failure)
			require.Equal(t, 2, calls, "one try and one retry")
		})
	}
}

func TestPoolDoesNotRetryServerResponse(t *testing.T) {
	srv := imaptest.New(t)
	p := newTestPool(t, pinnedConfig(srv, account.SecurityTLS))
	no := &imap.Error{Type: imap.StatusResponseTypeNo, Text: "nope"}
	calls := 0

	err := p.with(t.Context(), func(*imapclient.Client) error {
		calls++
		return no
	})

	require.ErrorIs(t, err, no)
	require.Equal(t, 1, calls)
	// The connection is still fine after a NO, so the pool keeps it.
	require.NoError(t, p.with(t.Context(), noop))
	require.Equal(t, 1, srv.TotalConns())
}

func TestPoolWithNoRetryDoesNotRetry(t *testing.T) {
	srv := imaptest.New(t)
	p := newTestPool(t, pinnedConfig(srv, account.SecurityTLS))
	calls := 0

	err := p.withNoRetry(t.Context(), func(*imapclient.Client) error {
		calls++
		return io.ErrUnexpectedEOF
	})

	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, 1, calls)
	require.Equal(t, 1, srv.TotalConns(), "no second dial")

	// The pool still works, on a new connection.
	require.NoError(t, p.withNoRetry(t.Context(), noop))
	require.Equal(t, 2, srv.TotalConns())
}

func TestPoolDoesNotRetryLoginFailure(t *testing.T) {
	srv := imaptest.New(t)
	cfg := pinnedConfig(srv, account.SecurityTLS)
	cfg.Password = secrets.Secret("wrong-password-93")
	p := newTestPool(t, cfg)

	err := p.with(t.Context(), noop)

	require.ErrorContains(t, err, "login")
	// A second login with the same bad password could lock the account.
	require.Equal(t, 1, srv.TotalConns())
}

func TestPoolClosesIdleConnectionAfterIdleTimeout(t *testing.T) {
	srv := imaptest.New(t)
	cfg := pinnedConfig(srv, account.SecurityTLS)
	cfg.IdleTimeout = time.Minute
	p := newTestPool(t, cfg)
	now := time.Now()
	p.now = func() time.Time { return now }
	require.NoError(t, p.with(t.Context(), noop))

	now = now.Add(30 * time.Second)
	require.NoError(t, p.with(t.Context(), noop))
	require.Equal(t, 1, srv.TotalConns(), "a connection that is not idle for long enough is reused")

	now = now.Add(2 * time.Minute)
	require.NoError(t, p.with(t.Context(), noop))
	require.Equal(t, 2, srv.TotalConns(), "an old idle connection is replaced")
	require.Eventually(t, func() bool { return srv.OpenConns() == 1 }, 5*time.Second, 10*time.Millisecond,
		"the old connection must be closed")
}

func TestPoolCloseClosesIdleConnections(t *testing.T) {
	srv := imaptest.New(t)
	p := newPool(pinnedConfig(srv, account.SecurityTLS))
	require.NoError(t, p.with(t.Context(), noop))
	require.Equal(t, 1, srv.OpenConns())

	p.Close()
	p.Close() // a second call does nothing

	require.Eventually(t, func() bool { return srv.OpenConns() == 0 }, 5*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, p.with(t.Context(), noop), errPoolClosed)
}

func TestPoolWaitStopsWhenContextEnds(t *testing.T) {
	srv := imaptest.New(t)
	cfg := pinnedConfig(srv, account.SecurityTLS)
	cfg.PoolSize = 1
	p := newTestPool(t, cfg)

	entered := make(chan struct{})
	release := make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- p.with(t.Context(), func(*imapclient.Client) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-holder:
		t.Fatalf("the first call ended before it ran: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := p.with(ctx, noop)

	require.ErrorIs(t, err, context.DeadlineExceeded)

	close(release)
	require.NoError(t, <-holder)
	// The slot is free again.
	require.NoError(t, p.with(t.Context(), noop))
}

// hangingNoop returns a fake server that logs in and then never answers NOOP.
func hangingNoop(t *testing.T) *fakeIMAP {
	t.Helper()
	return newFakeIMAP(t, "* OK [CAPABILITY IMAP4rev1] ready\r\n", func(tag, cmd string) (string, fakeThen) {
		if cmd == "LOGIN" {
			return tag + " OK LOGIN completed\r\n", fakeStay
		}
		return "", fakeStay
	})
}

func TestPoolCommandTimeout(t *testing.T) {
	fake := hangingNoop(t)
	cfg := fake.plainConfig()
	cfg.CommandTimeout = 150 * time.Millisecond
	p := newTestPool(t, cfg)

	start := time.Now()
	err := p.with(t.Context(), noop)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
	// A command that timed out is not sent a second time.
	require.Equal(t, 1, strings.Count(fake.Received(), "NOOP"))
}

func TestPoolStopsRunningCommandWhenContextEnds(t *testing.T) {
	fake := hangingNoop(t)
	p := newTestPool(t, fake.plainConfig()) // the command timeout is 60 s
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	err := p.with(ctx, noop)

	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 5*time.Second)
}

// resetOnFirstNoop returns a fake server that resets (RST) the connection on
// the first NOOP it gets, and answers every later NOOP.
func resetOnFirstNoop(t *testing.T) *fakeIMAP {
	t.Helper()
	var noops atomic.Int32
	return newFakeIMAP(t, "* OK [CAPABILITY IMAP4rev1] ready\r\n", func(tag, cmd string) (string, fakeThen) {
		switch cmd {
		case "LOGIN":
			return tag + " OK LOGIN completed\r\n", fakeStay
		case "NOOP":
			if noops.Add(1) == 1 {
				return "", fakeReset
			}
			return tag + " OK NOOP completed\r\n", fakeStay
		}
		return "", fakeStay
	})
}

// A reset gives imapclient an error that has lost its network error type (it
// formats the reader error with %v). The pool must decide by the state of the
// connection, not by the type of the error.
func TestPoolRetriesAfterConnectionReset(t *testing.T) {
	fake := resetOnFirstNoop(t)
	p := newTestPool(t, fake.plainConfig())

	err := p.with(t.Context(), noop)

	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(fake.Received(), "NOOP"), "one try and one retry")
	require.Equal(t, 2, fake.Accepted(), "the retry uses a new connection")
}

func TestPoolDoesNotReuseConnectionThatDied(t *testing.T) {
	// The server resets every connection on NOOP. imapclient marks the
	// connection as ended a few microseconds after it reports the error, so the
	// check is repeated: the pool must never keep a connection that died, also
	// when the "closed" signal is late.
	fake := newFakeIMAP(t, "* OK [CAPABILITY IMAP4rev1] ready\r\n", func(tag, cmd string) (string, fakeThen) {
		switch cmd {
		case "LOGIN":
			return tag + " OK LOGIN completed\r\n", fakeStay
		case "NOOP":
			return "", fakeReset
		}
		return "", fakeStay
	})
	p := newTestPool(t, fake.plainConfig())

	const rounds = 100
	for i := range rounds {
		require.Error(t, p.withNoRetry(t.Context(), noop))
		p.mu.Lock()
		idle := len(p.idle)
		p.mu.Unlock()
		require.Zero(t, idle, "round %d: a dead connection went to the idle list", i)
	}
	require.Equal(t, rounds, fake.Accepted(), "every call needs a new connection")
}

// startUnreadFetch leaves a FETCH with a message body unread. imapclient then
// waits for the caller to read the body, and it reads nothing else from the
// connection. This is a call that breaks the rule "close every command".
func startUnreadFetch(c *imapclient.Client) error {
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		return err
	}
	_ = c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{{}}})
	return nil
}

// waitUntilReaderIsStuck gives the reader of imapclient time to receive the
// FETCH response and to stop at the unread body. Without it, a close right
// after the call could win the race, and the test would not test the stuck
// state. A longer wait cannot make a correct pool fail.
func waitUntilReaderIsStuck() { time.Sleep(300 * time.Millisecond) }

func TestPoolDoesNotHangOnUnreadFetch(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", []byte("Subject: hi\r\n\r\nbody\r\n"))
	cfg := pinnedConfig(srv, account.SecurityTLS)
	cfg.PoolSize = 1
	cfg.CommandTimeout = 300 * time.Millisecond
	p := newPool(cfg)
	require.NoError(t, p.with(t.Context(), startUnreadFetch))

	// The next call finds the stuck connection. It must end near the command
	// timeout and not wait for the connection.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start := time.Now()
	err := p.with(ctx, noop)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 3*time.Second)

	// With a pool size of 1, the next call can run only if the slot was
	// released. It uses a new connection.
	require.NoError(t, p.with(ctx, noop))
	require.Equal(t, 2, srv.TotalConns())
	// The stuck connection was closed.
	require.Eventually(t, func() bool { return srv.OpenConns() == 1 }, 5*time.Second, 10*time.Millisecond)

	closed := make(chan struct{})
	go func() {
		p.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for a stuck connection")
	}
}

func TestPoolCloseAndIdleSweepDoNotWaitForStuckConnection(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", []byte("Subject: hi\r\n\r\nbody\r\n"))
	cfg := pinnedConfig(srv, account.SecurityTLS)
	cfg.IdleTimeout = time.Minute
	p := newPool(cfg)
	now := time.Now()
	p.now = func() time.Time { return now }
	require.NoError(t, p.with(t.Context(), startUnreadFetch))
	waitUntilReaderIsStuck()

	// The stuck connection is old now. Taking a connection must close it and
	// go on at once.
	now = now.Add(2 * time.Minute)
	done := make(chan error, 1)
	go func() { done <- p.with(t.Context(), noop) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the idle sweep waited for a stuck connection")
	}

	// Close must not wait for a stuck connection either.
	require.NoError(t, p.with(t.Context(), startUnreadFetch))
	waitUntilReaderIsStuck()
	closed := make(chan struct{})
	go func() {
		p.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for a stuck connection")
	}
}

func TestPoolRepeatsPanicOfCall(t *testing.T) {
	srv := imaptest.New(t)
	p := newTestPool(t, pinnedConfig(srv, account.SecurityTLS))

	var got any
	func() {
		defer func() { got = recover() }()
		_ = p.with(t.Context(), func(*imapclient.Client) error { panic("boom") })
	}()
	// The panic reaches the caller, which can recover it. It does not stop the
	// program from a goroutine of the pool.
	require.Contains(t, got, "boom")
	// The slot and the connection are not lost.
	require.NoError(t, p.with(t.Context(), noop))
	require.NoError(t, p.with(t.Context(), noop))
}
