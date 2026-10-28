package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
)

// works reports whether the backend can still serve a call. A closed backend
// refuses every new call.
func works(t *testing.T, b mail.Backend) bool {
	t.Helper()
	_, err := b.ListFolders(context.Background())
	return err == nil
}

func mustMail(t *testing.T, c *BackendCache, a *account.Account) mail.Backend {
	t.Helper()
	b, err := c.Mail(context.Background(), a)
	require.NoError(t, err)
	return b
}

func TestBackendCachedAndForgotten(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	a := f.addAccount(f.tenant, "me@example.com", serverSettings(srv), srv.Pass)
	c := f.newCache()

	first := mustMail(t, c, a)
	second := mustMail(t, c, a)
	require.Same(t, first, second, "the second call must return the cached backend")
	// A fresh copy of the account with the same settings uses the same backend.
	require.Same(t, first, mustMail(t, c, f.reload(a)))
	require.True(t, works(t, first), "the backend must log in with the stored password")

	c.Forget(a.ID)

	require.False(t, works(t, first), "Forget must close the backend")
	third := mustMail(t, c, a)
	require.NotSame(t, first, third, "after Forget the next call builds a new backend")
	require.True(t, works(t, third))
}

func TestForgetKeepsOtherAccounts(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	a := f.addAccount(f.tenant, "a@example.com", serverSettings(srv), srv.Pass)
	b := f.addAccount(f.tenant, "b@example.com", serverSettings(srv), srv.Pass)
	c := f.newCache()
	backendA := mustMail(t, c, a)
	backendB := mustMail(t, c, b)

	c.Forget(a.ID)

	require.False(t, works(t, backendA))
	require.True(t, works(t, backendB))
	require.Same(t, backendB, mustMail(t, c, b))
}

// Two tenants can have an account with the same id. Forget takes an id only, so
// it closes both.
func TestForgetClosesEveryEntryWithThatID(t *testing.T) {
	f := newFixture(t)
	f.addTenant("other")
	srv := imaptest.New(t)
	mine := f.addAccount(f.tenant, "a@example.com", serverSettings(srv), srv.Pass)
	theirs := f.addAccount("other", "b@example.com", serverSettings(srv), srv.Pass, func(a *account.Account) {
		a.ID = mine.ID
	})
	c := f.newCache()
	backendMine := mustMail(t, c, mine)
	backendTheirs := mustMail(t, c, theirs)
	require.NotSame(t, backendMine, backendTheirs, "the cache is keyed by tenant and account")

	c.Forget(mine.ID)

	require.False(t, works(t, backendMine))
	require.False(t, works(t, backendTheirs))
}

func TestBackendRebuiltWhenPasswordChanges(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	a := f.addAccount(f.tenant, "me@example.com", serverSettings(srv), srv.Pass)
	timers := &fakeTimers{}
	c := f.newCacheWithTimers(timers)
	old := mustMail(t, c, a)
	require.True(t, works(t, old))

	// The command line tool is another process. It changes the stored password
	// and cannot call Forget here. The new password is the same text. Only the
	// sealed bytes change, and that must be enough.
	f.setPassword(a, srv.Pass)

	fresh := mustMail(t, c, a)
	require.NotSame(t, old, fresh)
	require.True(t, works(t, fresh))
	require.Same(t, fresh, mustMail(t, c, a), "and the new one is cached")
	// The old backend is retired and not closed: a use case that makes several
	// calls may still use it. It closes when the timer fires.
	require.True(t, works(t, old), "the old backend must stay open for a caller that still uses it")
	require.Equal(t, 1, timers.count())
	timers.fire(0)
	require.False(t, works(t, old), "the old backend must be closed when it is retired")
	require.True(t, works(t, fresh), "the new backend must not be touched")
}

func TestBackendUsesTheNewPassword(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	a := f.addAccount(f.tenant, "me@example.com", serverSettings(srv), "wrong-password")
	c := f.newCache()
	broken := mustMail(t, c, a)
	require.False(t, works(t, broken), "the wrong password must not log in")

	f.setPassword(a, srv.Pass)

	fixed := mustMail(t, c, a)
	require.NotSame(t, broken, fixed)
	require.True(t, works(t, fixed))
}

func TestBackendRebuiltWhenServerSettingsChange(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	a := f.addAccount(f.tenant, "me@example.com", serverSettings(srv), srv.Pass)
	timers := &fakeTimers{}
	c := f.newCacheWithTimers(timers)
	old := mustMail(t, c, a)
	require.True(t, works(t, old))

	changed := f.reload(a)
	changed.Mail.SaveSentCopy = !changed.Mail.SaveSentCopy

	fresh := mustMail(t, c, changed)
	require.NotSame(t, old, fresh)
	require.True(t, works(t, fresh))
	require.True(t, works(t, old), "retired, not closed at once")
	timers.fire(0)
	require.False(t, works(t, old))

	// A different port is a different setting too.
	moved := f.reload(a)
	moved.Mail.IMAPPort++
	third := mustMail(t, c, moved)
	require.NotSame(t, fresh, third)
	require.Equal(t, 2, timers.count())
	timers.fire(1)
	require.False(t, works(t, fresh))
}

// A replaced backend waits two minutes before it closes. Forget and Close are
// for an account that is gone and for the end of the program, so they close at
// once, and they include the backends that wait.
func TestRetiredBackends(t *testing.T) {
	// replaced returns a cache in which the account has a retired backend.
	type setup struct {
		f       *fixture
		srv     *imaptest.Server
		a       *account.Account
		c       *BackendCache
		timers  *fakeTimers
		retired mail.Backend
		current mail.Backend
	}
	replaced := func(t *testing.T) *setup {
		t.Helper()
		f := newFixture(t)
		srv := imaptest.New(t)
		a := f.addAccount(f.tenant, "me@example.com", serverSettings(srv), srv.Pass)
		timers := &fakeTimers{}
		c := f.newCacheWithTimers(timers)
		retired := mustMail(t, c, a)
		require.True(t, works(t, retired))
		f.setPassword(a, srv.Pass)
		current := mustMail(t, c, a)
		require.Equal(t, 1, timers.count())
		require.True(t, works(t, retired))
		return &setup{f: f, srv: srv, a: a, c: c, timers: timers, retired: retired, current: current}
	}

	t.Run("the delay is two minutes", func(t *testing.T) {
		s := replaced(t)
		require.Equal(t, 2*time.Minute, retireAfter)
		require.Equal(t, retireAfter, s.timers.get(0).delay)
	})
	t.Run("Close closes retired backends too", func(t *testing.T) {
		s := replaced(t)

		s.c.Close()

		require.False(t, works(t, s.retired))
		require.False(t, works(t, s.current))
		require.True(t, s.timers.get(0).stopped, "the timer of a closed backend must be stopped")
	})
	t.Run("Forget closes the retired backends of that account", func(t *testing.T) {
		s := replaced(t)

		s.c.Forget(s.a.ID)

		require.False(t, works(t, s.retired))
		require.False(t, works(t, s.current))
		require.True(t, s.timers.get(0).stopped)
	})
	t.Run("Forget keeps the retired backends of other accounts", func(t *testing.T) {
		s := replaced(t)

		s.c.Forget(account.NewAccountID())

		require.True(t, works(t, s.retired))
		require.True(t, works(t, s.current))
		require.False(t, s.timers.get(0).stopped)
	})
	t.Run("the timer closes only the retired backend", func(t *testing.T) {
		s := replaced(t)

		s.timers.fire(0)

		require.False(t, works(t, s.retired))
		require.True(t, works(t, s.current))
		s.c.Forget(s.a.ID) // the timer has run already: nothing is closed twice, nothing breaks
	})
	t.Run("each replacement starts its own timer", func(t *testing.T) {
		s := replaced(t)
		s.f.setPassword(s.a, s.srv.Pass)

		third := mustMail(t, s.c, s.a)

		require.NotSame(t, s.current, third)
		require.Equal(t, 2, s.timers.count())
		require.True(t, works(t, s.retired), "the first retired backend waits for its own timer")
		require.True(t, works(t, s.current))
		s.timers.fire(1)
		require.False(t, works(t, s.current))
		require.True(t, works(t, s.retired))
		require.True(t, works(t, third))
	})
}

func TestBackendMissingPassword(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	a := f.addAccount(f.tenant, "me@example.com", serverSettings(srv), srv.Pass)
	c := f.newCache()
	cached := mustMail(t, c, a)
	require.True(t, works(t, cached))

	require.NoError(t, f.store.DeleteAccountSecrets(context.Background(), a.TenantID, a.ID))

	_, err := c.Mail(context.Background(), a)
	require.Error(t, err)
	require.NotErrorIs(t, err, store.ErrNotFound, "must not look like a missing account")
	require.NotErrorIs(t, err, ErrAccountNotFound)
	require.False(t, works(t, cached), "a backend built with a password that is gone must be closed")
}

func TestBackendPasswordThatCannotBeDecrypted(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "very-secret-password")
	other := f.addAccount(f.tenant, "other@example.com", offlineSettings(), "pw")
	ctx := context.Background()
	// Put the sealed password of one account under another account. The
	// associated data does not match, so opening it must fail.
	version, sealed, err := f.store.GetAccountSecret(ctx, a.TenantID, a.ID, passwordSecret)
	require.NoError(t, err)
	require.NoError(t, f.store.PutAccountSecret(ctx, other.TenantID, other.ID, passwordSecret, version, sealed))
	c := f.newCache()

	_, err = c.Mail(ctx, other)

	require.ErrorIs(t, err, secrets.ErrDecrypt)
	require.NotContains(t, err.Error(), "very-secret-password")
}

func TestBackendAccountWithoutMailSettings(t *testing.T) {
	f := newFixture(t)
	c := f.newCache()

	_, err := c.Mail(context.Background(), &account.Account{ID: "a", TenantID: f.tenant})

	require.Error(t, err)
}

// The token scope is "<tenant>/<account>", so a page token of one account does
// not open for another account, even on the same server with the same signer.
func TestBackendTokensAreBoundToTheAccount(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("one", "1"))
	srv.Append("INBOX", plainMail("two", "2"))
	a := f.addAccount(f.tenant, "a@example.com", serverSettings(srv), srv.Pass)
	b := f.addAccount(f.tenant, "b@example.com", serverSettings(srv), srv.Pass)
	c := f.newCache()
	ctx := context.Background()

	page, err := mustMail(t, c, a).Search(ctx, mail.Query{Folder: "@inbox", Limit: 1}, "")
	require.NoError(t, err)
	require.NotEmpty(t, page.Next)

	_, err = mustMail(t, c, a).Search(ctx, mail.Query{Folder: "@inbox", Limit: 1}, page.Next)
	require.NoError(t, err, "the token opens for its own account")
	_, err = mustMail(t, c, b).Search(ctx, mail.Query{Folder: "@inbox", Limit: 1}, page.Next)
	require.ErrorIs(t, err, signing.ErrInvalid, "and not for another account")
}

func TestBackendCloseClosesAllAndCacheStaysUsable(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	a := f.addAccount(f.tenant, "a@example.com", serverSettings(srv), srv.Pass)
	b := f.addAccount(f.tenant, "b@example.com", serverSettings(srv), srv.Pass)
	c := f.newCache()
	backendA, backendB := mustMail(t, c, a), mustMail(t, c, b)

	c.Close()

	require.False(t, works(t, backendA))
	require.False(t, works(t, backendB))
	require.True(t, works(t, mustMail(t, c, a)))
}

func TestNewBackendFactoryPanicsWithoutParts(t *testing.T) {
	f := newFixture(t)
	require.Panics(t, func() { NewBackendFactory(nil, f.vault, f.signer, true, nil) })
	require.Panics(t, func() { NewBackendFactory(f.store, nil, f.signer, true, nil) })
	require.Panics(t, func() { NewBackendFactory(f.store, f.vault, nil, true, nil) })
	require.NotPanics(t, func() { NewBackendFactory(f.store, f.vault, f.signer, true, nil) })
}

func TestFingerprintChangesWithEveryInput(t *testing.T) {
	server := offlineSettings()
	base, err := fingerprintOf(&server, 1, []byte("sealed"))
	require.NoError(t, err)
	again, err := fingerprintOf(&server, 1, []byte("sealed"))
	require.NoError(t, err)
	require.Equal(t, base, again, "the same input gives the same fingerprint")

	changedServer := server
	changedServer.Username = "someone-else"
	cases := map[string]struct {
		server  *account.MailServer
		version int
		sealed  []byte
	}{
		"server":      {&changedServer, 1, []byte("sealed")},
		"key version": {&server, 2, []byte("sealed")},
		"sealed":      {&server, 1, []byte("sealed2")},
	}
	for name, in := range cases {
		got, err := fingerprintOf(in.server, in.version, in.sealed)
		require.NoError(t, err, name)
		require.NotEqual(t, base, got, name)
	}
}

// Many goroutines ask for the same account, and others forget it, all at once.
// The race detector must stay quiet, and at the end exactly one backend that was
// handed out may still be alive: the one in the cache.
func TestBackendCacheConcurrent(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	a := f.addAccount(f.tenant, "a@example.com", serverSettings(srv), srv.Pass)
	b := f.addAccount(f.tenant, "b@example.com", serverSettings(srv), srv.Pass)
	c := f.newCache()
	ctx := context.Background()

	t.Run("same account at once gives one backend", func(t *testing.T) {
		const callers = 32
		got := make([]mail.Backend, callers)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range callers {
			wg.Go(func() {
				<-start
				m, err := c.Mail(ctx, a)
				if err == nil {
					got[i] = m
				}
			})
		}
		close(start)
		wg.Wait()

		for i := range got {
			require.NotNil(t, got[i])
			require.Same(t, got[0], got[i], "two concurrent calls must share one backend")
		}
		c.Close()
	})

	t.Run("calls and Forget at the same time", func(t *testing.T) {
		var (
			mu   sync.Mutex
			seen = map[mail.Backend]struct{}{}
			wg   sync.WaitGroup
		)
		note := func(m mail.Backend) {
			mu.Lock()
			seen[m] = struct{}{}
			mu.Unlock()
		}
		for _, acct := range []*account.Account{a, b} {
			for range 8 {
				wg.Go(func() {
					for range 20 {
						m, err := c.Mail(ctx, acct)
						if err != nil {
							continue
						}
						note(m)
						// The backend may be closed by a Forget at this moment.
						// Then the call fails. That is fine.
						_, _ = m.ListFolders(ctx)
					}
				})
			}
			wg.Go(func() {
				for range 20 {
					c.Forget(acct.ID)
					time.Sleep(time.Millisecond)
				}
			})
		}
		wg.Wait()

		// Whatever is cached now is the only backend that may be alive. Every
		// other backend that was handed out must be closed.
		live := map[mail.Backend]bool{}
		for _, acct := range []*account.Account{a, b} {
			live[mustMail(t, c, acct)] = true
		}
		for m := range seen {
			if !live[m] {
				require.False(t, works(t, m), "a backend that left the cache must be closed")
			}
		}
		// Each live backend holds at most its pool of connections, and every
		// other one has given its connections back.
		require.Eventually(t, func() bool { return srv.OpenConns() <= 2*2 }, 5*time.Second, 10*time.Millisecond,
			fmt.Sprintf("open connections: %d", srv.OpenConns()))
	})
}

// The factory must work with a secret store that is not the real one.
type failingSecrets struct{ err error }

func (s failingSecrets) GetAccountSecret(context.Context, account.TenantID, account.AccountID, string) (int, []byte, error) {
	return 0, nil, s.err
}

func TestBackendSecretStoreError(t *testing.T) {
	f := newFixture(t)
	boom := errors.New("database is down")
	c := NewBackendFactory(failingSecrets{err: boom}, f.vault, f.signer, true, nil)
	a := account.NewIMAPAccount(f.tenant, "me@example.com", offlineSettings(), testStart)

	_, err := c.Mail(context.Background(), a)

	require.ErrorIs(t, err, boom)
}

// The password is decrypted only when a backend is built. A cache hit reads the
// sealed bytes to compare the fingerprint, and it does not decrypt.
func TestBackendDecryptsOnlyWhenBuilding(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "first-password")
	c := f.newCache()
	var decrypts atomic.Int32
	real := c.decrypt
	c.decrypt = func(ctx context.Context, tenant account.TenantID, version int, aad string, sealed []byte) ([]byte, error) {
		decrypts.Add(1)
		return real(ctx, tenant, version, aad, sealed)
	}

	for range 3 {
		mustMail(t, c, a)
	}
	require.EqualValues(t, 1, decrypts.Load(), "three calls build one backend and decrypt once")

	f.setPassword(a, "second-password")
	mustMail(t, c, a)
	require.EqualValues(t, 2, decrypts.Load(), "a new password is decrypted once")
	mustMail(t, c, a)
	mustMail(t, c, a)
	require.EqualValues(t, 2, decrypts.Load())
}

// The settings of the factory reach the backend, and the password is the one
// that was stored.
func TestBackendConfigReachesTheBackend(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "the-stored-password")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	for _, local := range []bool{true, false} {
		t.Run(fmt.Sprintf("local=%v", local), func(t *testing.T) {
			c := NewBackendFactory(f.store, f.vault, f.signer, local, logger)
			t.Cleanup(c.Close)
			var (
				got       imapsmtp.Config
				gotSigner *signing.Signer
				built     int
			)
			c.newBackend = func(cfg imapsmtp.Config, signer *signing.Signer) *imapsmtp.Backend {
				built++
				got, gotSigner = cfg, signer
				return imapsmtp.New(cfg, signer)
			}

			mustMail(t, c, a)

			require.Equal(t, 1, built)
			require.Equal(t, *a.Mail, got.Server)
			require.Equal(t, "the-stored-password", got.Password.Reveal())
			require.Equal(t, local, got.Local)
			require.Same(t, logger, got.Logger)
			require.Equal(t, string(a.TenantID)+"/"+string(a.ID), got.TokenScope)
			require.Same(t, f.signer, gotSigner)
		})
	}
}

// blockedDecrypt makes the decrypt function of the cache wait for one account.
// The first decrypt for that account signals on entered and then waits until
// release is called. Other accounts and later calls go straight through. It must
// be set before any goroutine uses the cache.
type blockedDecrypt struct {
	entered chan struct{}
	release func()
}

func blockDecryptOf(t *testing.T, c *BackendCache, a *account.Account) *blockedDecrypt {
	t.Helper()
	var (
		first   atomic.Bool
		gate    = make(chan struct{})
		once    sync.Once
		release = func() { once.Do(func() { close(gate) }) }
		entered = make(chan struct{}, 1)
		real    = c.decrypt
		marker  = "account:" + string(a.ID)
	)
	t.Cleanup(release)
	c.decrypt = func(ctx context.Context, tenant account.TenantID, version int, aad string, sealed []byte) ([]byte, error) {
		if strings.Contains(aad, marker) && first.CompareAndSwap(false, true) {
			entered <- struct{}{}
			<-gate
		}
		return real(ctx, tenant, version, aad, sealed)
	}
	return &blockedDecrypt{entered: entered, release: release}
}

// mailAsync calls Mail in a goroutine and returns the channel of its result.
type mailResult struct {
	backend mail.Backend
	err     error
}

func mailAsync(c *BackendCache, ctx context.Context, a *account.Account) <-chan mailResult {
	out := make(chan mailResult, 1)
	go func() {
		b, err := c.Mail(ctx, a)
		out <- mailResult{b, err}
	}()
	return out
}

// waitFor returns the result, or fails the test when it does not come in time.
func waitFor(t *testing.T, ch <-chan mailResult, limit time.Duration, what string) mailResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(limit):
		require.FailNow(t, "timeout: "+what)
		return mailResult{}
	}
}

// A slow build for one account (here: a slow decrypt) must not stall the other
// accounts, whether they are cached or not.
func TestBackendCacheConcurrentSlowDecrypt(t *testing.T) {
	f := newFixture(t)
	slow := f.addAccount(f.tenant, "slow@example.com", offlineSettings(), "pw")
	cached := f.addAccount(f.tenant, "cached@example.com", offlineSettings(), "pw")
	other := f.addAccount(f.tenant, "other@example.com", offlineSettings(), "pw")
	c := f.newCache()
	cachedBackend := mustMail(t, c, cached)
	gate := blockDecryptOf(t, c, slow)
	ctx := context.Background()

	slowResult := mailAsync(c, ctx, slow)
	<-gate.entered // the build of the slow account holds its decrypt now

	hit := waitFor(t, mailAsync(c, ctx, cached), 2*time.Second, "a cached account waited for a slow build")
	require.NoError(t, hit.err)
	require.Same(t, cachedBackend, hit.backend)
	miss := waitFor(t, mailAsync(c, ctx, other), 2*time.Second, "an uncached account waited for a slow build")
	require.NoError(t, miss.err)
	require.NotNil(t, miss.backend)

	gate.release()
	done := waitFor(t, slowResult, 2*time.Second, "the slow build never finished")
	require.NoError(t, done.err)
	require.NotNil(t, done.backend)
}

// A call that waits for a busy account must stop when its context ends. It must
// not wait for the slow build.
func TestBackendCacheConcurrentWaitCanBeCancelled(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "a@example.com", offlineSettings(), "pw")
	c := f.newCache()
	gate := blockDecryptOf(t, c, a)

	first := mailAsync(c, context.Background(), a)
	<-gate.entered

	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		waiting := mailAsync(c, ctx, a)
		time.Sleep(50 * time.Millisecond) // let the call reach the wait
		cancel()

		r := waitFor(t, waiting, 2*time.Second, "a waiting call did not stop on cancel")

		require.ErrorIs(t, r.err, context.Canceled)
		require.Nil(t, r.backend)
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		r := waitFor(t, mailAsync(c, ctx, a), 2*time.Second, "a waiting call did not stop at its deadline")

		require.ErrorIs(t, r.err, context.DeadlineExceeded)
	})

	// The calls that gave up did not harm the account.
	gate.release()
	done := waitFor(t, first, 2*time.Second, "the first build never finished")
	require.NoError(t, done.err)
	again, err := c.Mail(context.Background(), a)
	require.NoError(t, err)
	require.Same(t, done.backend, again)
}

// Forget has to wait for a build that is running, and then it closes the result.
// Calls that waited for the account must end with a working backend.
func TestBackendCacheConcurrentForgetDuringBuild(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	a := f.addAccount(f.tenant, "a@example.com", serverSettings(srv), srv.Pass)
	c := f.newCache()
	gate := blockDecryptOf(t, c, a)
	ctx := context.Background()
	// Remember every backend that is built, to show at the end that none leaked.
	var (
		builtMu sync.Mutex
		built   []*imapsmtp.Backend
	)
	realNew := c.newBackend
	c.newBackend = func(cfg imapsmtp.Config, signer *signing.Signer) *imapsmtp.Backend {
		b := realNew(cfg, signer)
		builtMu.Lock()
		built = append(built, b)
		builtMu.Unlock()
		return b
	}

	building := mailAsync(c, ctx, a)
	<-gate.entered
	waiting := mailAsync(c, ctx, a)
	forgotten := make(chan struct{})
	go func() {
		c.Forget(a.ID)
		close(forgotten)
	}()
	select {
	case <-forgotten:
		require.FailNow(t, "Forget returned while a build was running")
	case <-time.After(100 * time.Millisecond):
	}

	gate.release()
	first := waitFor(t, building, 2*time.Second, "the build never finished")
	require.NoError(t, first.err)
	late := waitFor(t, waiting, 2*time.Second, "the waiting call never finished")
	require.NoError(t, late.err)
	select {
	case <-forgotten:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Forget never returned")
	}

	// The account was forgotten, so a backend that was built before is closed.
	// The next call builds a new one that works.
	require.False(t, works(t, first.backend), "Forget must close the backend that the running build made")
	next := mustMail(t, c, a)
	require.True(t, works(t, next))
	require.Same(t, next, mustMail(t, c, a))

	// At the end of the program, no backend of this test may stay open. A call
	// that waited for the account must not leave a backend in a slot that the
	// cache has forgotten.
	c.Close()
	builtMu.Lock()
	defer builtMu.Unlock()
	require.NotEmpty(t, built)
	for _, b := range built {
		require.False(t, works(t, b), "every backend that was built must be closed")
	}
}

// A call can get the slot of an account, and Forget can then remove the slot
// before the call takes it. The call must not build a backend in that slot: no
// one could close it. It must ask for the new slot.
func TestBackendCacheDeadSlotIsNotUsed(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "a@example.com", offlineSettings(), "pw")
	c := f.newCache()
	var built atomic.Int32
	realNew := c.newBackend
	c.newBackend = func(cfg imapsmtp.Config, signer *signing.Signer) *imapsmtp.Backend {
		built.Add(1)
		return realNew(cfg, signer)
	}
	key := backendKey{tenant: a.TenantID, id: a.ID}
	old := c.slotFor(key)

	c.Forget(a.ID)
	b, again, err := c.mailOn(context.Background(), old, key, a)

	require.NoError(t, err)
	require.True(t, again, "the call must start again with a new slot")
	require.Nil(t, b)
	require.Zero(t, built.Load(), "no backend may be built in a slot that the cache has forgotten")
	require.NotSame(t, old, c.slotFor(key), "Forget must take the slot out of the cache")

	// Mail itself finishes with a working, cached backend.
	first := mustMail(t, c, a)
	require.Same(t, first, mustMail(t, c, a))
	require.EqualValues(t, 1, built.Load())
}
