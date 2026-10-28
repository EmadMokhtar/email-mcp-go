package store

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// pendingLife is how long a test pending action lives.
const pendingLife = 24 * time.Hour

// newPendingAccount creates the local tenant and one account in it. It returns
// the tenant and the account id.
func newPendingAccount(t *testing.T, s *Store, id account.AccountID) (account.TenantID, account.AccountID) {
	t.Helper()
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, id, testNow)))
	return tenant, id
}

// testPendingRow returns a pending row with distinct sealed values.
func testPendingRow(tenant account.TenantID, acct account.AccountID, id string, created time.Time) *PendingRow {
	return &PendingRow{
		ID:                id,
		Tenant:            tenant,
		Account:           acct,
		Kind:              "send",
		Hash:              bytes.Repeat([]byte{0xab}, 32),
		State:             "pending",
		CreatedAt:         created,
		ExpiresAt:         created.Add(pendingLife),
		Preview:           []byte("sealed preview " + id),
		PreviewKeyVersion: 2,
	}
}

// putPending stores a row with a sealed payload that depends on the id.
func putPending(t *testing.T, s *Store, row *PendingRow) {
	t.Helper()
	require.NoError(t, s.CreatePending(context.Background(), row, []byte("sealed payload "+row.ID), 3))
}

// pendingState reads the state column straight from the table.
func pendingState(t *testing.T, s *Store, tenant account.TenantID, id string) string {
	t.Helper()
	var state string
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT state FROM pending_actions WHERE tenant_id = ? AND id = ?`,
		string(tenant), id).Scan(&state))
	return state
}

func TestPendingRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")

	row := testPendingRow(tenant, acct, "p1", testNow)
	putPending(t, s, row)

	got, err := s.GetPending(ctx, tenant, "p1")
	require.NoError(t, err)
	require.Equal(t, row, got)

	version, sealed, err := s.GetPendingPayload(ctx, tenant, "p1")
	require.NoError(t, err)
	require.Equal(t, 3, version)
	require.Equal(t, []byte("sealed payload p1"), sealed)
}

func TestPendingTimesAreStoredAsMilliseconds(t *testing.T) {
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	putPending(t, s, testPendingRow(tenant, acct, "p1", testNow))

	var created, expires int64
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT created_at, expires_at FROM pending_actions WHERE id = 'p1'`).Scan(&created, &expires))
	require.Equal(t, testNow.UnixMilli(), created)
	require.Equal(t, testNow.Add(pendingLife).UnixMilli(), expires)
}

func TestGetPendingNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, _ := newPendingAccount(t, s, "a1")

	_, err := s.GetPending(ctx, tenant, "missing")
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.GetPendingPayload(ctx, tenant, "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestPendingIsScopedByTenant(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenantA, acctA := newPendingAccount(t, s, "a1")
	ensureTestTenant(t, s, "other")
	require.NoError(t, s.CreateAccount(ctx, testAccount("other", "a1", testNow)))
	putPending(t, s, testPendingRow(tenantA, acctA, "p1", testNow))

	// The same id in another tenant is a different, missing row.
	_, err := s.GetPending(ctx, "other", "p1")
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.GetPendingPayload(ctx, "other", "p1")
	require.ErrorIs(t, err, ErrNotFound)
	err = s.TransitionPending(ctx, "other", "p1", "pending", "approved", "", testNow)
	require.ErrorIs(t, err, ErrNotFound)
	rows, err := s.ListPending(ctx, "other", testNow)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Equal(t, "pending", pendingState(t, s, tenantA, "p1"))

	// Both tenants may use the same id for their own rows.
	putPending(t, s, testPendingRow("other", "a1", "p1", testNow))
	got, err := s.GetPending(ctx, "other", "p1")
	require.NoError(t, err)
	require.Equal(t, account.TenantID("other"), got.Tenant)
}

func TestTransitionPendingIsCompareAndSet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	putPending(t, s, testPendingRow(tenant, acct, "p1", testNow))
	now := testNow.Add(time.Minute)

	// A wrong "from" changes nothing.
	err := s.TransitionPending(ctx, tenant, "p1", "approved", "done", "r1", now)
	require.ErrorIs(t, err, ErrNotFound)
	got, err := s.GetPending(ctx, tenant, "p1")
	require.NoError(t, err)
	require.Equal(t, "pending", got.State)
	require.Empty(t, got.ResultID)

	require.NoError(t, s.TransitionPending(ctx, tenant, "p1", "pending", "approved", "", now))
	got, err = s.GetPending(ctx, tenant, "p1")
	require.NoError(t, err)
	require.Equal(t, "approved", got.State)
	require.Empty(t, got.ResultID, "an empty result id must not set anything")

	// The same change cannot run twice.
	err = s.TransitionPending(ctx, tenant, "p1", "pending", "approved", "", now)
	require.ErrorIs(t, err, ErrNotFound)

	// A result id is set when it is given, and an empty one keeps it later.
	require.NoError(t, s.TransitionPending(ctx, tenant, "p1", "approved", "done", "msg-42", now))
	got, err = s.GetPending(ctx, tenant, "p1")
	require.NoError(t, err)
	require.Equal(t, "done", got.State)
	require.Equal(t, "msg-42", got.ResultID)
}

func TestTransitionPendingRefusesAnExpiredPendingRow(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	row := testPendingRow(tenant, acct, "p1", testNow)
	putPending(t, s, row)

	// The row is still live at its expiry time, to the millisecond.
	atExpiry := row.ExpiresAt
	// One millisecond later it has expired.
	err := s.TransitionPending(ctx, tenant, "p1", "pending", "approved", "", atExpiry.Add(time.Millisecond))
	require.ErrorIs(t, err, ErrNotFound)
	require.Equal(t, "pending", pendingState(t, s, tenant, "p1"))

	require.NoError(t, s.TransitionPending(ctx, tenant, "p1", "pending", "approved", "", atExpiry))

	// Expiry applies to "pending" only. An approved row must still be able to
	// finish after the pending time has passed.
	late := atExpiry.Add(time.Hour)
	require.NoError(t, s.TransitionPending(ctx, tenant, "p1", "approved", "done", "m", late))
}

func TestClaimPendingChecksTheHashInTheSameStatement(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	row := testPendingRow(tenant, acct, "p1", testNow)
	putPending(t, s, row)
	now := testNow.Add(time.Minute)

	// A hash that is not the stored hash changes nothing, whatever its shape.
	other := bytes.Repeat([]byte{0xcd}, 32)
	almost := bytes.Clone(row.Hash)
	almost[31] ^= 1
	for name, hash := range map[string][]byte{
		"another hash":         other,
		"one bit different":    almost,
		"too short":            row.Hash[:31],
		"too long":             append(bytes.Clone(row.Hash), 0),
		"empty":                {},
		"nil":                  nil,
		"the hash as a string": []byte("abababababababababababababababab"),
	} {
		err := s.ClaimPending(ctx, tenant, "p1", hash, now)
		require.ErrorIs(t, err, ErrNotFound, name)
		require.Equal(t, "pending", pendingState(t, s, tenant, "p1"), name)
	}

	// The right hash claims the row, once.
	require.NoError(t, s.ClaimPending(ctx, tenant, "p1", row.Hash, now))
	got, err := s.GetPending(ctx, tenant, "p1")
	require.NoError(t, err)
	require.Equal(t, "approved", got.State)
	require.Empty(t, got.ResultID)
	require.ErrorIs(t, s.ClaimPending(ctx, tenant, "p1", row.Hash, now), ErrNotFound)
}

func TestClaimPendingOnlyClaimsALivePendingRowOfTheTenant(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	ensureTestTenant(t, s, "other")
	row := testPendingRow(tenant, acct, "p1", testNow)
	putPending(t, s, row)

	// Another tenant has no such row.
	require.ErrorIs(t, s.ClaimPending(ctx, "other", "p1", row.Hash, testNow), ErrNotFound)
	// The row is live up to its expiry time, to the millisecond.
	require.ErrorIs(t, s.ClaimPending(ctx, tenant, "p1", row.Hash, row.ExpiresAt.Add(time.Millisecond)), ErrNotFound)
	require.Equal(t, "pending", pendingState(t, s, tenant, "p1"))
	require.NoError(t, s.ClaimPending(ctx, tenant, "p1", row.Hash, row.ExpiresAt))

	// A row in another state is not claimed, even with the right hash.
	done := testPendingRow(tenant, acct, "p2", testNow)
	putPending(t, s, done)
	require.NoError(t, s.TransitionPending(ctx, tenant, "p2", "pending", "done", "", testNow))
	require.ErrorIs(t, s.ClaimPending(ctx, tenant, "p2", done.Hash, testNow), ErrNotFound)
	require.Equal(t, "done", pendingState(t, s, tenant, "p2"))

	require.ErrorIs(t, s.ClaimPending(ctx, tenant, "p|1", row.Hash, testNow), ErrInvalidID)
	require.ErrorIs(t, s.ClaimPending(ctx, "", "p1", row.Hash, testNow), ErrInvalidID)
}

func TestListPendingReturnsLivePendingRowsNewestFirst(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")

	old := testNow.Add(-time.Hour)
	putPending(t, s, testPendingRow(tenant, acct, "p-old", old))
	putPending(t, s, testPendingRow(tenant, acct, "p-new", testNow))
	putPending(t, s, testPendingRow(tenant, acct, "p-same-a", testNow.Add(time.Minute)))
	putPending(t, s, testPendingRow(tenant, acct, "p-same-b", testNow.Add(time.Minute)))
	putPending(t, s, testPendingRow(tenant, acct, "p-done", testNow))
	require.NoError(t, s.TransitionPending(ctx, tenant, "p-done", "pending", "done", "", testNow))
	putPending(t, s, testPendingRow(tenant, acct, "p-gone", testNow.Add(-2*pendingLife)))

	rows, err := s.ListPending(ctx, tenant, testNow.Add(2*time.Minute))
	require.NoError(t, err)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	// Newest first. Rows with the same time are ordered by id, largest first.
	require.Equal(t, []string{"p-same-b", "p-same-a", "p-new", "p-old"}, ids)
}

func TestPendingIsDeletedWithItsAccount(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	putPending(t, s, testPendingRow(tenant, acct, "p1", testNow))

	require.NoError(t, s.DeleteAccount(ctx, tenant, acct))

	_, err := s.GetPending(ctx, tenant, "p1")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCreatePendingRefusesBadRows(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	putPending(t, s, testPendingRow(tenant, acct, "p1", testNow))

	t.Run("an account that does not exist", func(t *testing.T) {
		err := s.CreatePending(ctx, testPendingRow(tenant, "nobody", "p2", testNow), []byte("x"), 1)
		require.Error(t, err)
	})
	t.Run("an account of another tenant", func(t *testing.T) {
		ensureTestTenant(t, s, "other")
		err := s.CreatePending(ctx, testPendingRow("other", acct, "p2", testNow), []byte("x"), 1)
		require.Error(t, err)
	})
	t.Run("a duplicate id", func(t *testing.T) {
		err := s.CreatePending(ctx, testPendingRow(tenant, acct, "p1", testNow), []byte("x"), 1)
		require.Error(t, err)
	})
	t.Run("a hash that is not 32 bytes", func(t *testing.T) {
		row := testPendingRow(tenant, acct, "p3", testNow)
		row.Hash = []byte("short")
		require.Error(t, s.CreatePending(ctx, row, []byte("x"), 1))
	})
	t.Run("unsafe ids", func(t *testing.T) {
		for _, bad := range []struct{ tenant, acct, id string }{
			{"", "a1", "p9"},
			{"local", "", "p9"},
			{"local", "a1", ""},
			{"lo|cal", "a1", "p9"},
			{"local", "a|1", "p9"},
			{"local", "a1", "p|9"},
		} {
			row := testPendingRow(account.TenantID(bad.tenant), account.AccountID(bad.acct), bad.id, testNow)
			require.ErrorIs(t, s.CreatePending(ctx, row, []byte("x"), 1), ErrInvalidID, "%+v", bad)
		}
		err := s.TransitionPending(ctx, tenant, "p|1", "pending", "done", "", testNow)
		require.ErrorIs(t, err, ErrInvalidID)
		err = s.TransitionPending(ctx, "", "p1", "pending", "done", "", testNow)
		require.ErrorIs(t, err, ErrInvalidID)
	})
}
