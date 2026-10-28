package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
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
	c := f.newCache()
	old := mustMail(t, c, a)
	require.True(t, works(t, old))

	// The command line tool is another process. It changes the stored password
	// and cannot call Forget here. The new password is the same text. Only the
	// sealed bytes change, and that must be enough.
	f.setPassword(a, srv.Pass)

	fresh := mustMail(t, c, a)
	require.NotSame(t, old, fresh)
	require.False(t, works(t, old), "the old backend must be closed")
	require.True(t, works(t, fresh))
	require.Same(t, fresh, mustMail(t, c, a), "and the new one is cached")
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
	c := f.newCache()
	old := mustMail(t, c, a)
	require.True(t, works(t, old))

	changed := f.reload(a)
	changed.Mail.SaveSentCopy = !changed.Mail.SaveSentCopy

	fresh := mustMail(t, c, changed)
	require.NotSame(t, old, fresh)
	require.False(t, works(t, old))
	require.True(t, works(t, fresh))

	// A different port is a different setting too.
	moved := f.reload(a)
	moved.Mail.IMAPPort++
	third := mustMail(t, c, moved)
	require.NotSame(t, fresh, third)
	require.False(t, works(t, fresh))
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
