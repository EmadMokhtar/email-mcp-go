package store

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

func TestCreateAccountWithSecret(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	a := testAccount(tenant, "acct1", testNow)
	a.Address = "Älice@Example.com"
	require.NoError(t, s.CreateAccountWithSecret(ctx, a, "password", 2, []byte("sealed-1")))

	got, err := s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	require.Equal(t, a, got)
	version, sealed, err := s.GetAccountSecret(ctx, tenant, "acct1", "password")
	require.NoError(t, err)
	require.Equal(t, 2, version)
	require.Equal(t, []byte("sealed-1"), sealed)
}

func TestCreateAccountWithSecretRefusesADuplicateAddress(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")

	first := testAccount(tenant, "acct1", testNow)
	first.Address = "Älice@Example.com"
	require.NoError(t, s.CreateAccountWithSecret(ctx, first, "password", 1, []byte("sealed-1")))

	// The same address in another case is the same address. The letters outside
	// ASCII count too.
	for i, address := range []string{"Älice@Example.com", "älice@example.com", "ÄLICE@EXAMPLE.COM"} {
		dup := testAccount(tenant, account.AccountID("dup"+strconv.Itoa(i)), testNow)
		dup.Address = address
		err := s.CreateAccountWithSecret(ctx, dup, "password", 1, []byte("sealed-2"))
		require.ErrorIs(t, err, ErrExists, address)
		_, err = s.GetAccount(ctx, tenant, dup.ID)
		require.ErrorIs(t, err, ErrNotFound, "nothing of the refused account is stored")
	}
	_, sealed, err := s.GetAccountSecret(ctx, tenant, "acct1", "password")
	require.NoError(t, err)
	require.Equal(t, []byte("sealed-1"), sealed, "the first secret is not changed")

	// Another tenant may connect the same address.
	other := testAccount("other", "acct2", testNow)
	other.Address = first.Address
	require.NoError(t, s.CreateAccountWithSecret(ctx, other, "password", 1, []byte("sealed-3")))

	// An account with another address is fine.
	second := testAccount(tenant, "acct3", testNow)
	second.Address = "bob@example.com"
	require.NoError(t, s.CreateAccountWithSecret(ctx, second, "password", 1, []byte("sealed-4")))
}

func TestCreateAccountWithSecretIsAllOrNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	// The secret row needs a sealed value (NOT NULL), so its insert fails after
	// the account insert worked. The account must not stay.
	a := testAccount(tenant, "acct1", testNow)
	require.Error(t, s.CreateAccountWithSecret(ctx, a, "password", 1, nil))
	_, err = s.GetAccount(ctx, tenant, "acct1")
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.GetAccountSecret(ctx, tenant, "acct1", "password")
	require.ErrorIs(t, err, ErrNotFound)

	// An unsafe id or an unknown tenant stores nothing either.
	require.Error(t, s.CreateAccountWithSecret(ctx, testAccount(tenant, "a|b", testNow), "password", 1, []byte("x")))
	require.Error(t, s.CreateAccountWithSecret(ctx, testAccount("ghost", "acct2", testNow), "password", 1, []byte("x")))
	accounts, err := s.ListAccounts(ctx, tenant)
	require.NoError(t, err)
	require.Empty(t, accounts)
}

func TestCreateAccountWithSecretOnceUnderRace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	const callers = 8
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			a := testAccount(tenant, account.AccountID("acct"+strconv.Itoa(i)), testNow)
			a.Address = "same@example.com"
			errs[i] = s.CreateAccountWithSecret(ctx, a, "password", 1, []byte("sealed"))
		})
	}
	wg.Wait()

	created := 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrExists):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	require.Equal(t, 1, created)
	accounts, err := s.ListAccounts(ctx, tenant)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
}
