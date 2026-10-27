package store

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTenantKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	// A tenant without keys has version 0.
	latest, err := s.LatestTenantKeyVersion(ctx, tenant)
	require.NoError(t, err)
	require.Zero(t, latest)
	_, _, err = s.TenantKey(ctx, tenant, 1)
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, s.PutTenantKey(ctx, tenant, 1, []byte("wrapped-1"), "kek-a"))
	require.NoError(t, s.PutTenantKey(ctx, tenant, 2, []byte("wrapped-2"), "kek-a"))

	latest, err = s.LatestTenantKeyVersion(ctx, tenant)
	require.NoError(t, err)
	require.Equal(t, 2, latest)

	wrapped, kekID, err := s.TenantKey(ctx, tenant, 1)
	require.NoError(t, err)
	require.Equal(t, []byte("wrapped-1"), wrapped)
	require.Equal(t, "kek-a", kekID)

	_, _, err = s.TenantKey(ctx, tenant, 3)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestTenantKeysAreScopedByTenant(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")

	require.NoError(t, s.PutTenantKey(ctx, local, 1, []byte("local-key"), "kek"))

	latest, err := s.LatestTenantKeyVersion(ctx, "other")
	require.NoError(t, err)
	require.Zero(t, latest)
	_, _, err = s.TenantKey(ctx, "other", 1)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestPutTenantKeyChecks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	// Version 0 means "no key" in LatestTenantKeyVersion, so it is not allowed.
	require.Error(t, s.PutTenantKey(ctx, tenant, 0, []byte("w"), "kek"))
	require.Error(t, s.PutTenantKey(ctx, tenant, -1, []byte("w"), "kek"))
	// The tenant must exist.
	require.Error(t, s.PutTenantKey(ctx, "ghost", 1, []byte("w"), "kek"))
}

func TestListTenantKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")

	rows, err := s.ListTenantKeys(ctx)
	require.NoError(t, err)
	require.Empty(t, rows)

	// Written out of order. The list is sorted by tenant, then version.
	require.NoError(t, s.PutTenantKey(ctx, "other", 1, []byte("o1"), "kek-a"))
	require.NoError(t, s.PutTenantKey(ctx, local, 2, []byte("l2"), "kek-a"))
	require.NoError(t, s.PutTenantKey(ctx, local, 1, []byte("l1"), "kek-b"))

	rows, err = s.ListTenantKeys(ctx)
	require.NoError(t, err)
	require.Equal(t, []TenantKeyRow{
		{Tenant: local, Version: 1, Wrapped: []byte("l1"), KEKID: "kek-b"},
		{Tenant: local, Version: 2, Wrapped: []byte("l2"), KEKID: "kek-a"},
		{Tenant: "other", Version: 1, Wrapped: []byte("o1"), KEKID: "kek-a"},
	}, rows)
}

func TestServerKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, _, err := s.ServerKey(ctx)
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, s.PutServerKey(ctx, []byte("wrapped-1"), "kek-a"))
	wrapped, kekID, err := s.ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("wrapped-1"), wrapped)
	require.Equal(t, "kek-a", kekID)

	var rows int
	require.NoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM server_keys").Scan(&rows))
	require.Equal(t, 1, rows)

	// The database itself refuses a second row.
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO server_keys (id, wrapped, kek_id, created_at) VALUES (2, x'00', 'kek', 0)")
	require.Error(t, err)
}

func TestPutTenantKeyDoesNotReplace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	require.NoError(t, s.PutTenantKey(ctx, tenant, 1, []byte("first"), "kek-a"))

	// The second put for the same tenant and version is refused.
	err = s.PutTenantKey(ctx, tenant, 1, []byte("second"), "kek-b")
	require.ErrorIs(t, err, ErrExists)

	// The stored key is the first one, bytes and master key id.
	wrapped, kekID, err := s.TenantKey(ctx, tenant, 1)
	require.NoError(t, err)
	require.Equal(t, []byte("first"), wrapped)
	require.Equal(t, "kek-a", kekID)

	// Another version is a different key and can be created.
	require.NoError(t, s.PutTenantKey(ctx, tenant, 2, []byte("v2"), "kek-a"))
}

func TestRewrapTenantKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.PutTenantKey(ctx, tenant, 1, []byte("wrapped-1"), "kek-a"))
	require.NoError(t, s.PutTenantKey(ctx, tenant, 2, []byte("wrapped-2"), "kek-a"))

	require.NoError(t, s.RewrapTenantKey(ctx, tenant, 1, []byte("rewrapped-1"), "kek-b"))

	wrapped, kekID, err := s.TenantKey(ctx, tenant, 1)
	require.NoError(t, err)
	require.Equal(t, []byte("rewrapped-1"), wrapped)
	require.Equal(t, "kek-b", kekID)

	// The other version is not touched.
	wrapped, kekID, err = s.TenantKey(ctx, tenant, 2)
	require.NoError(t, err)
	require.Equal(t, []byte("wrapped-2"), wrapped)
	require.Equal(t, "kek-a", kekID)
}

func TestRewrapTenantKeyNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")
	require.NoError(t, s.PutTenantKey(ctx, local, 1, []byte("local-key"), "kek-a"))

	// A version that does not exist is not created.
	require.ErrorIs(t, s.RewrapTenantKey(ctx, local, 2, []byte("x"), "kek-b"), ErrNotFound)
	require.ErrorIs(t, s.RewrapTenantKey(ctx, local, 0, []byte("x"), "kek-b"), ErrNotFound)
	_, _, err = s.TenantKey(ctx, local, 2)
	require.ErrorIs(t, err, ErrNotFound)

	// Another tenant cannot rewrap this tenant's version, and its own tenant
	// has no such version either.
	require.ErrorIs(t, s.RewrapTenantKey(ctx, "other", 1, []byte("evil"), "kek-b"), ErrNotFound)
	require.ErrorIs(t, s.RewrapTenantKey(ctx, "ghost", 1, []byte("evil"), "kek-b"), ErrNotFound)

	wrapped, kekID, err := s.TenantKey(ctx, local, 1)
	require.NoError(t, err)
	require.Equal(t, []byte("local-key"), wrapped)
	require.Equal(t, "kek-a", kekID)
	_, _, err = s.TenantKey(ctx, "other", 1)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestPutServerKeyDoesNotReplace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.PutServerKey(ctx, []byte("first"), "kek-a"))

	err := s.PutServerKey(ctx, []byte("second"), "kek-b")
	require.ErrorIs(t, err, ErrExists)

	wrapped, kekID, err := s.ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("first"), wrapped)
	require.Equal(t, "kek-a", kekID)
}

func TestRewrapServerKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// There is nothing to rewrap yet, and the call does not create a key.
	require.ErrorIs(t, s.RewrapServerKey(ctx, []byte("x"), "kek-b"), ErrNotFound)
	_, _, err := s.ServerKey(ctx)
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, s.PutServerKey(ctx, []byte("wrapped"), "kek-a"))
	require.NoError(t, s.RewrapServerKey(ctx, []byte("rewrapped"), "kek-b"))

	wrapped, kekID, err := s.ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("rewrapped"), wrapped)
	require.Equal(t, "kek-b", kekID)
}

// TestKeyCreationRace starts many creators at the same time, like several
// programs that find no key and each make one. Exactly one must win, and the
// stored key must be the winner's.
func TestKeyCreationRace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	const workers = 8
	payload := func(worker int) []byte { return fmt.Appendf(nil, "worker-%d", worker) }
	tenantErrs := make([]error, workers)
	serverErrs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mine := payload(i)
			tenantErrs[i] = s.PutTenantKey(ctx, tenant, 1, mine, "kek")
			serverErrs[i] = s.PutServerKey(ctx, mine, "kek")
		}()
	}
	wg.Wait()

	winner := func(errs []error) int {
		won := -1
		for i, err := range errs {
			if err == nil {
				require.Equal(t, -1, won, "more than one creator won")
				won = i
				continue
			}
			require.ErrorIs(t, err, ErrExists)
		}
		require.NotEqual(t, -1, won, "no creator won")
		return won
	}

	wrapped, _, err := s.TenantKey(ctx, tenant, 1)
	require.NoError(t, err)
	require.Equal(t, payload(winner(tenantErrs)), wrapped)

	wrapped, _, err = s.ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, payload(winner(serverErrs)), wrapped)
}
