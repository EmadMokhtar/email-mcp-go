package store

import (
	"context"
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

	// Writing a version again replaces it. A change of master key does this
	// when it wraps every tenant key again.
	require.NoError(t, s.PutTenantKey(ctx, tenant, 1, []byte("rewrapped-1"), "kek-b"))
	wrapped, kekID, err = s.TenantKey(ctx, tenant, 1)
	require.NoError(t, err)
	require.Equal(t, []byte("rewrapped-1"), wrapped)
	require.Equal(t, "kek-b", kekID)

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

	// A second put replaces the key. There is only ever one row.
	require.NoError(t, s.PutServerKey(ctx, []byte("wrapped-2"), "kek-b"))
	wrapped, kekID, err = s.ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("wrapped-2"), wrapped)
	require.Equal(t, "kek-b", kekID)

	var rows int
	require.NoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM server_keys").Scan(&rows))
	require.Equal(t, 1, rows)

	// The database itself refuses a second row.
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO server_keys (id, wrapped, kek_id, created_at) VALUES (2, x'00', 'kek', 0)")
	require.Error(t, err)
}
