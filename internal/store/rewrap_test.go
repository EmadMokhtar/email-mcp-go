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

// TestReplaceWrappedKeysMustCoverEveryKey checks that the call refuses a list
// that leaves out a stored tenant key. A key that is left out would stay
// wrapped by the old master key.
func TestReplaceWrappedKeysMustCoverEveryKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local := seedKeys(t, s)
	before := takeKeySnapshot(t, s)

	// Version 2 of "local" is left out.
	err := s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
		{Tenant: local, Version: 1, Wrapped: []byte("local-1-new"), KEKID: "kek-b"},
		{Tenant: "other", Version: 1, Wrapped: []byte("other-1-new"), KEKID: "kek-b"},
	}, []byte("server-new"), "kek-b")

	require.ErrorIs(t, err, ErrConflict)
	require.NotErrorIs(t, err, ErrNotFound)
	require.ErrorContains(t, err, "run the rotation again")
	require.Equal(t, before, takeKeySnapshot(t, s), "a refused call must change nothing")
}

// TestReplaceWrappedKeysMustCoverTheServerKey checks that nil server bytes
// mean "there is no server key", and are refused when there is one.
func TestReplaceWrappedKeysMustCoverTheServerKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local := seedKeys(t, s)
	before := takeKeySnapshot(t, s)

	err := s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
		{Tenant: local, Version: 1, Wrapped: []byte("local-1-new"), KEKID: "kek-b"},
		{Tenant: local, Version: 2, Wrapped: []byte("local-2-new"), KEKID: "kek-b"},
		{Tenant: "other", Version: 1, Wrapped: []byte("other-1-new"), KEKID: "kek-b"},
	}, nil, "")

	require.ErrorIs(t, err, ErrConflict)
	require.Equal(t, before, takeKeySnapshot(t, s), "a refused call must change nothing")
}

// TestReplaceWrappedKeysRefusesAKeyAddedAfterTheListing is the race that the
// check exists for. The caller lists the keys, then another program adds a
// key, then the caller replaces what it listed. The new key is wrapped by the
// old master key, and the caller never saw it.
func TestReplaceWrappedKeysRefusesAKeyAddedAfterTheListing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedKeys(t, s)
	listed, err := s.ListTenantKeys(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 3)
	for i := range listed {
		listed[i].Wrapped = []byte("rewrapped")
		listed[i].KEKID = "kek-b"
	}

	require.NoError(t, s.PutTenantKey(ctx, "other", 2, []byte("added-later"), "kek-a"))
	before := takeKeySnapshot(t, s)
	require.Len(t, before.tenantKeys, 4)

	err = s.ReplaceWrappedKeys(ctx, listed, []byte("server-new"), "kek-b")

	require.ErrorIs(t, err, ErrConflict)
	require.Equal(t, before, takeKeySnapshot(t, s), "a refused call must change nothing")
}

func TestReplaceWrappedKeysRefusesAServerKeyAddedAfterTheListing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.PutTenantKey(ctx, local, 1, []byte("local-1"), "kek-a"))
	listed, err := s.ListTenantKeys(ctx)
	require.NoError(t, err)
	listed[0].Wrapped = []byte("rewrapped")
	listed[0].KEKID = "kek-b"

	// The caller saw no server key, and another program created one.
	require.NoError(t, s.PutServerKey(ctx, []byte("server"), "kek-a"))
	before := takeKeySnapshot(t, s)

	err = s.ReplaceWrappedKeys(ctx, listed, nil, "")

	require.ErrorIs(t, err, ErrConflict)
	require.Equal(t, before, takeKeySnapshot(t, s), "a refused call must change nothing")
}

// TestReplaceWrappedKeysCountsEachKeyOnce checks that a key that appears twice
// in the list is one key. If it counted twice, a duplicate could hide a key
// that was left out.
func TestReplaceWrappedKeysCountsEachKeyOnce(t *testing.T) {
	ctx := context.Background()
	row := func(tenant account.TenantID, version int) TenantKeyRow {
		return TenantKeyRow{Tenant: tenant, Version: version, Wrapped: []byte("new"), KEKID: "kek-b"}
	}

	t.Run("a duplicate of a stored key is accepted", func(t *testing.T) {
		s := newTestStore(t)
		local := seedKeys(t, s)

		err := s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
			row(local, 1), row(local, 1), row(local, 2), row("other", 1),
		}, []byte("server-new"), "kek-b")

		require.NoError(t, err)
		for _, k := range takeKeySnapshot(t, s).tenantKeys {
			require.Equal(t, []byte("new"), k.Wrapped)
		}
	})

	t.Run("a duplicate does not hide a missing key", func(t *testing.T) {
		s := newTestStore(t)
		local := seedKeys(t, s)
		before := takeKeySnapshot(t, s)

		// Three entries, but only two keys. Three keys are stored.
		err := s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
			row(local, 1), row(local, 1), row(local, 2),
		}, []byte("server-new"), "kek-b")

		require.ErrorIs(t, err, ErrConflict)
		require.Equal(t, before, takeKeySnapshot(t, s))
	})
}

// TestReplaceWrappedKeysWithOneKindOfKey checks the cases where the database
// holds only some kinds of key. The call must accept exactly what is stored.
func TestReplaceWrappedKeysWithOneKindOfKey(t *testing.T) {
	ctx := context.Background()

	t.Run("only a server key", func(t *testing.T) {
		s := newTestStore(t)
		require.NoError(t, s.PutServerKey(ctx, []byte("server"), "kek-a"))

		require.NoError(t, s.ReplaceWrappedKeys(ctx, nil, []byte("server-new"), "kek-b"))

		snap := takeKeySnapshot(t, s)
		require.Empty(t, snap.tenantKeys)
		require.Equal(t, []byte("server-new"), snap.serverWrapped)
		require.Equal(t, "kek-b", snap.serverKEKID)
	})

	t.Run("only tenant keys", func(t *testing.T) {
		s := newTestStore(t)
		local, err := s.EnsureLocalTenant(ctx)
		require.NoError(t, err)
		require.NoError(t, s.PutTenantKey(ctx, local, 1, []byte("local-1"), "kek-a"))

		err = s.ReplaceWrappedKeys(ctx, []TenantKeyRow{
			{Tenant: local, Version: 1, Wrapped: []byte("local-1-new"), KEKID: "kek-b"},
		}, nil, "")

		require.NoError(t, err)
		snap := takeKeySnapshot(t, s)
		require.Equal(t, []TenantKeyRow{{Tenant: local, Version: 1, Wrapped: []byte("local-1-new"), KEKID: "kek-b"}}, snap.tenantKeys)
		require.False(t, snap.hasServerKey, "no server key may be created")
	})

	t.Run("no keys at all", func(t *testing.T) {
		s := newTestStore(t)
		require.NoError(t, s.ReplaceWrappedKeys(ctx, nil, nil, ""))
		require.Equal(t, keySnapshot{}, takeKeySnapshot(t, s))
	})
}

// TestReplaceWrappedKeysNothingGivenButKeysStored checks that an empty call is
// not a no-op: it says "there are no keys", and that must be true.
func TestReplaceWrappedKeysNothingGivenButKeysStored(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedKeys(t, s)
	before := takeKeySnapshot(t, s)

	err := s.ReplaceWrappedKeys(ctx, nil, nil, "")

	require.ErrorIs(t, err, ErrConflict)
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
