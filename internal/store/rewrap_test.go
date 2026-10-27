package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// keySnapshot is a copy of every stored wrapped key. A test takes one before a
// call and compares it afterwards to see exactly what the call changed.
type keySnapshot struct {
	tenantKeys    []TenantKeyRow
	serverWrapped []byte
	serverKEKID   string
	hasServerKey  bool
}

func takeKeySnapshot(t *testing.T, s *Store) keySnapshot {
	t.Helper()
	ctx := context.Background()
	rows, err := s.ListTenantKeys(ctx)
	require.NoError(t, err)
	snap := keySnapshot{tenantKeys: rows}
	snap.serverWrapped, snap.serverKEKID, err = s.ServerKey(ctx)
	if err == nil {
		snap.hasServerKey = true
	} else {
		require.ErrorIs(t, err, ErrNotFound)
	}
	return snap
}

// seedKeys stores two tenants with keys and a server key, all "kek-a".
func seedKeys(t *testing.T, s *Store) (local account.TenantID) {
	t.Helper()
	ctx := context.Background()
	local, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")
	require.NoError(t, s.PutTenantKey(ctx, local, 1, []byte("local-1"), "kek-a"))
	require.NoError(t, s.PutTenantKey(ctx, local, 2, []byte("local-2"), "kek-a"))
	require.NoError(t, s.PutTenantKey(ctx, "other", 1, []byte("other-1"), "kek-a"))
	require.NoError(t, s.PutServerKey(ctx, []byte("server"), "kek-a"))
	return local
}

func TestReplaceWrappedKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local := seedKeys(t, s)

	err := s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
		{Tenant: local, Version: 1, Wrapped: []byte("local-1-new"), KEKID: "kek-b"},
		{Tenant: local, Version: 2, Wrapped: []byte("local-2-new"), KEKID: "kek-b"},
		{Tenant: "other", Version: 1, Wrapped: []byte("other-1-new"), KEKID: "kek-b"},
	}, []byte("server-new"), "kek-b")
	require.NoError(t, err)

	snap := takeKeySnapshot(t, s)
	require.Equal(t, []TenantKeyRow{
		{Tenant: local, Version: 1, Wrapped: []byte("local-1-new"), KEKID: "kek-b"},
		{Tenant: local, Version: 2, Wrapped: []byte("local-2-new"), KEKID: "kek-b"},
		{Tenant: "other", Version: 1, Wrapped: []byte("other-1-new"), KEKID: "kek-b"},
	}, snap.tenantKeys)
	require.True(t, snap.hasServerKey)
	require.Equal(t, []byte("server-new"), snap.serverWrapped)
	require.Equal(t, "kek-b", snap.serverKEKID)
}

func TestReplaceWrappedKeysOnlyTouchesGivenRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local := seedKeys(t, s)

	// One row, and no server key (nil means: leave the server key as it is).
	err := s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
		{Tenant: local, Version: 2, Wrapped: []byte("local-2-new"), KEKID: "kek-b"},
	}, nil, "")
	require.NoError(t, err)

	snap := takeKeySnapshot(t, s)
	require.Equal(t, []TenantKeyRow{
		{Tenant: local, Version: 1, Wrapped: []byte("local-1"), KEKID: "kek-a"},
		{Tenant: local, Version: 2, Wrapped: []byte("local-2-new"), KEKID: "kek-b"},
		{Tenant: "other", Version: 1, Wrapped: []byte("other-1"), KEKID: "kek-a"},
	}, snap.tenantKeys)
	require.Equal(t, []byte("server"), snap.serverWrapped)
	require.Equal(t, "kek-a", snap.serverKEKID)
}

func TestReplaceWrappedKeysServerKeyOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedKeys(t, s)
	before := takeKeySnapshot(t, s)

	require.NoError(t, s.ReplaceWrappedKeys(ctx, nil, []byte("server-new"), "kek-b"))

	after := takeKeySnapshot(t, s)
	require.Equal(t, before.tenantKeys, after.tenantKeys)
	require.Equal(t, []byte("server-new"), after.serverWrapped)
	require.Equal(t, "kek-b", after.serverKEKID)
}

func TestReplaceWrappedKeysNothingToDo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedKeys(t, s)
	before := takeKeySnapshot(t, s)

	require.NoError(t, s.ReplaceWrappedKeys(ctx, nil, nil, ""))

	require.Equal(t, before, takeKeySnapshot(t, s))
}

// TestReplaceWrappedKeysIsAllOrNothing checks that one missing row undoes the
// rows that were updated before it. The missing row is in the middle, so rows
// both before and after it, and the server key, must stay as they were.
func TestReplaceWrappedKeysIsAllOrNothing(t *testing.T) {
	missing := []struct {
		name string
		row  TenantKeyRow
	}{
		{"version that does not exist", TenantKeyRow{Tenant: "local", Version: 3, Wrapped: []byte("x"), KEKID: "kek-b"}},
		{"tenant that has no keys", TenantKeyRow{Tenant: "ghost", Version: 1, Wrapped: []byte("x"), KEKID: "kek-b"}},
		{"version zero", TenantKeyRow{Tenant: "local", Version: 0, Wrapped: []byte("x"), KEKID: "kek-b"}},
	}
	for _, tc := range missing {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			local := seedKeys(t, s)
			before := takeKeySnapshot(t, s)

			err := s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
				{Tenant: local, Version: 1, Wrapped: []byte("local-1-new"), KEKID: "kek-b"},
				tc.row,
				{Tenant: "other", Version: 1, Wrapped: []byte("other-1-new"), KEKID: "kek-b"},
			}, []byte("server-new"), "kek-b")

			require.ErrorIs(t, err, ErrNotFound)
			require.Equal(t, before, takeKeySnapshot(t, s), "a failed call must change nothing")
		})
	}
}

func TestReplaceWrappedKeysMissingServerKeyRollsBack(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.PutTenantKey(ctx, local, 1, []byte("local-1"), "kek-a"))
	before := takeKeySnapshot(t, s)
	require.False(t, before.hasServerKey)

	// The tenant key row exists and is updated first. The server key row does
	// not exist, so the update of the tenant key must be undone, and no server
	// key may be created.
	err = s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
		{Tenant: local, Version: 1, Wrapped: []byte("local-1-new"), KEKID: "kek-b"},
	}, []byte("server-new"), "kek-b")

	require.ErrorIs(t, err, ErrNotFound)
	require.Equal(t, before, takeKeySnapshot(t, s))
}

func TestReplaceWrappedKeysRejectsUnsafeTenantIDs(t *testing.T) {
	for _, bad := range []account.TenantID{"", "a|b"} {
		s := newTestStore(t)
		ctx := context.Background()
		local := seedKeys(t, s)
		before := takeKeySnapshot(t, s)

		err := s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
			{Tenant: local, Version: 1, Wrapped: []byte("local-1-new"), KEKID: "kek-b"},
			{Tenant: bad, Version: 1, Wrapped: []byte("x"), KEKID: "kek-b"},
		}, []byte("server-new"), "kek-b")

		require.ErrorIs(t, err, ErrInvalidID)
		require.Equal(t, before, takeKeySnapshot(t, s), "a failed call must change nothing")
	}
}

func TestReplaceWrappedKeysIsScopedByTenant(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")
	require.NoError(t, s.PutTenantKey(ctx, local, 1, []byte("local-1"), "kek-a"))
	before := takeKeySnapshot(t, s)

	// "other" has no version 1. The row of "local" with the same version must
	// not be changed in its place.
	err = s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
		{Tenant: "other", Version: 1, Wrapped: []byte("evil"), KEKID: "kek-b"},
	}, nil, "")

	require.ErrorIs(t, err, ErrNotFound)
	require.Equal(t, before, takeKeySnapshot(t, s))
}

func TestReplaceWrappedKeysEmptyWrappedRollsBack(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local := seedKeys(t, s)
	before := takeKeySnapshot(t, s)

	// A nil wrapped value breaks the NOT NULL rule of the column. The database
	// error must also undo the rows that were updated before it.
	err := s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
		{Tenant: local, Version: 1, Wrapped: []byte("local-1-new"), KEKID: "kek-b"},
		{Tenant: local, Version: 2, Wrapped: nil, KEKID: "kek-b"},
	}, []byte("server-new"), "kek-b")

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotFound)
	require.Equal(t, before, takeKeySnapshot(t, s))
}
