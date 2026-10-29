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
	putPending(t, s, testPendingRow(tenant, acct, "p2", testNow))
	atExpiry := row.ExpiresAt

	// One millisecond after its time the row has expired. Nothing moves it to
	// "approved". (The attempt records the expiry. See the test about that.)
	err := s.TransitionPending(ctx, tenant, "p1", "pending", "approved", "", atExpiry.Add(time.Millisecond))
	require.ErrorIs(t, err, ErrNotFound)
	require.Equal(t, "expired", pendingState(t, s, tenant, "p1"))

	// Another row is still live at its expiry time, to the millisecond.
	require.NoError(t, s.TransitionPending(ctx, tenant, "p2", "pending", "approved", "", atExpiry))

	// Expiry applies to "pending" only. An approved row must still be able to
	// finish after the pending time has passed.
	late := atExpiry.Add(time.Hour)
	require.NoError(t, s.TransitionPending(ctx, tenant, "p2", "approved", "done", "m", late))
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
	require.NoError(t, s.ClaimPending(ctx, tenant, "p1", row.Hash, row.ExpiresAt))
	late := testPendingRow(tenant, acct, "p-late", testNow)
	putPending(t, s, late)
	require.ErrorIs(t, s.ClaimPending(ctx, tenant, "p-late", late.Hash, late.ExpiresAt.Add(time.Millisecond)), ErrNotFound)
	require.Equal(t, "expired", pendingState(t, s, tenant, "p-late"), "the attempt records the expiry")

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

// pendingSecretsAreNull reads the two sealed columns straight from the table.
func pendingSecretsAreNull(t *testing.T, s *Store, tenant account.TenantID, id string) (payload, preview bool) {
	t.Helper()
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT payload IS NULL, preview IS NULL FROM pending_actions WHERE tenant_id = ? AND id = ?`,
		string(tenant), id).Scan(&payload, &preview))
	return payload, preview
}

// A finished action keeps no secret. The statement that moves it to a final
// state also sets the sealed payload and the sealed preview to NULL.
func TestTransitionPendingClearsTheSecretsOfFinalStates(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	now := testNow.Add(time.Minute)

	for _, from := range []string{"pending", "approved"} {
		for _, to := range []string{"done", "rejected", "expired"} {
			id := from + "-to-" + to
			putPending(t, s, testPendingRow(tenant, acct, id, testNow))
			if from == "approved" {
				require.NoError(t, s.TransitionPending(ctx, tenant, id, "pending", "approved", "", now))
			}
			payloadNull, previewNull := pendingSecretsAreNull(t, s, tenant, id)
			require.False(t, payloadNull, "%s: payload before", id)
			require.False(t, previewNull, "%s: preview before", id)

			require.NoError(t, s.TransitionPending(ctx, tenant, id, from, to, "r-"+id, now))

			payloadNull, previewNull = pendingSecretsAreNull(t, s, tenant, id)
			require.True(t, payloadNull, "%s: payload after", id)
			require.True(t, previewNull, "%s: preview after", id)

			// The row itself stays, with the rest of its data.
			got, err := s.GetPending(ctx, tenant, id)
			require.NoError(t, err)
			require.Equal(t, to, got.State)
			require.Equal(t, "r-"+id, got.ResultID)
			require.Empty(t, got.Preview, "the preview is gone")
			require.Equal(t, bytes.Repeat([]byte{0xab}, 32), got.Hash)

			// The payload is gone too: this is "not found", not an empty value.
			_, _, err = s.GetPendingPayload(ctx, tenant, id)
			require.ErrorIs(t, err, ErrNotFound, id)
		}
	}
}

// A move to "approved" is not the end. The user acted, and the server still
// needs the payload until it has finished.
func TestTransitionPendingKeepsTheSecretsOfApprovedRows(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	putPending(t, s, testPendingRow(tenant, acct, "p1", testNow))

	require.NoError(t, s.TransitionPending(ctx, tenant, "p1", "pending", "approved", "", testNow))

	payloadNull, previewNull := pendingSecretsAreNull(t, s, tenant, "p1")
	require.False(t, payloadNull)
	require.False(t, previewNull)
	_, sealed, err := s.GetPendingPayload(ctx, tenant, "p1")
	require.NoError(t, err)
	require.Equal(t, []byte("sealed payload p1"), sealed)
}

// A failed move changes nothing, also not the secrets.
func TestTransitionPendingThatChangesNothingKeepsTheSecrets(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	ensureTestTenant(t, s, "other")
	putPending(t, s, testPendingRow(tenant, acct, "p1", testNow))

	// A wrong "from", and another tenant.
	require.ErrorIs(t, s.TransitionPending(ctx, tenant, "p1", "approved", "done", "", testNow), ErrNotFound)
	require.ErrorIs(t, s.TransitionPending(ctx, "other", "p1", "pending", "done", "", testNow), ErrNotFound)
	// A wrong hash on a live row is not an expiry.
	require.ErrorIs(t, s.ClaimPending(ctx, tenant, "p1", bytes.Repeat([]byte{1}, 32), testNow), ErrNotFound)

	require.Equal(t, "pending", pendingState(t, s, tenant, "p1"))
	payloadNull, previewNull := pendingSecretsAreNull(t, s, tenant, "p1")
	require.False(t, payloadNull)
	require.False(t, previewNull)
}

// An attempt to move or claim a waiting row that is past its time records the
// expiry and clears the secrets. The attempt itself still fails.
func TestAnAttemptOnAnExpiredWaitingRowRecordsTheExpiry(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	late := testNow.Add(pendingLife + time.Millisecond)

	for name, attempt := range map[string]func(row *PendingRow) error{
		"transition": func(row *PendingRow) error {
			return s.TransitionPending(ctx, tenant, row.ID, "pending", "approved", "", late)
		},
		"transition to rejected": func(row *PendingRow) error {
			return s.TransitionPending(ctx, tenant, row.ID, "pending", "rejected", "", late)
		},
		"transition from approved": func(row *PendingRow) error {
			return s.TransitionPending(ctx, tenant, row.ID, "approved", "done", "", late)
		},
		"claim": func(row *PendingRow) error {
			return s.ClaimPending(ctx, tenant, row.ID, row.Hash, late)
		},
	} {
		row := testPendingRow(tenant, acct, "p-"+name, testNow)
		putPending(t, s, row)

		require.ErrorIs(t, attempt(row), ErrNotFound, name)

		require.Equal(t, "expired", pendingState(t, s, tenant, row.ID), name)
		payloadNull, previewNull := pendingSecretsAreNull(t, s, tenant, row.ID)
		require.True(t, payloadNull, name)
		require.True(t, previewNull, name)
	}
}

// Reading never changes a row. Only an attempt to change it does.
func TestReadingAnExpiredWaitingRowDoesNotChangeIt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	putPending(t, s, testPendingRow(tenant, acct, "p1", testNow))
	late := testNow.Add(2 * pendingLife)

	_, err := s.GetPending(ctx, tenant, "p1")
	require.NoError(t, err)
	_, err = s.ListPending(ctx, tenant, late)
	require.NoError(t, err)
	_, _, err = s.GetPendingPayload(ctx, tenant, "p1")
	require.NoError(t, err)

	require.Equal(t, "pending", pendingState(t, s, tenant, "p1"))
	payloadNull, previewNull := pendingSecretsAreNull(t, s, tenant, "p1")
	require.False(t, payloadNull)
	require.False(t, previewNull)
}

// pendingIDs returns the ids of all pending actions in the table, in order.
func pendingIDs(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `SELECT id FROM pending_actions ORDER BY id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

func TestPrunePendingDeletesOnlyOldFinalRowsAcrossTenants(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	ensureTestTenant(t, s, "other")
	require.NoError(t, s.CreateAccount(ctx, testAccount("other", "a1", testNow)))

	old := testNow
	recent := testNow.Add(72 * time.Hour)
	now := recent.Add(time.Hour)
	// Rows older than 48 hours are old.
	createdBefore := now.Add(-48 * time.Hour)

	put := func(tenantID account.TenantID, id string, created time.Time, finalState string) {
		putPending(t, s, testPendingRow(tenantID, acct, id, created))
		switch finalState {
		case "approved":
			require.NoError(t, s.TransitionPending(ctx, tenantID, id, "pending", "approved", "", created))
		case "done", "rejected", "expired":
			require.NoError(t, s.TransitionPending(ctx, tenantID, id, "pending", finalState, "", created))
		}
	}
	put(tenant, "old-done", old, "done")
	put(tenant, "old-rejected", old, "rejected")
	put(tenant, "old-expired", old, "expired")
	put(tenant, "old-waiting-past-time", old, "") // "pending" for 72 hours: expired by the clock
	put("other", "old-other-tenant", old, "done")
	put(tenant, "old-approved", old, "approved") // the outcome is not known: kept
	put(tenant, "new-done", recent, "done")
	put(tenant, "new-rejected", recent, "rejected")
	put(tenant, "new-waiting", recent, "") // live

	n, err := s.PrunePending(ctx, createdBefore, now)
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Equal(t, []string{"new-done", "new-rejected", "new-waiting", "old-approved"}, pendingIDs(t, s))

	// A second call has nothing left to do.
	n, err = s.PrunePending(ctx, createdBefore, now)
	require.NoError(t, err)
	require.Zero(t, n)
}

// A waiting row that is old but still live is not deleted: its user can still
// confirm it.
func TestPrunePendingKeepsALiveWaitingRowThatIsOld(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	putPending(t, s, testPendingRow(tenant, acct, "live", testNow))
	putPending(t, s, testPendingRow(tenant, acct, "done", testNow))
	require.NoError(t, s.TransitionPending(ctx, tenant, "done", "pending", "done", "", testNow))

	now := testNow.Add(2 * time.Hour)
	n, err := s.PrunePending(ctx, now.Add(-time.Hour), now)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, []string{"live"}, pendingIDs(t, s))
}

// "Older than" is strict, to the millisecond.
func TestPrunePendingBoundary(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	putPending(t, s, testPendingRow(tenant, acct, "at-limit", testNow))
	putPending(t, s, testPendingRow(tenant, acct, "just-older", testNow.Add(-time.Millisecond)))
	for _, id := range []string{"at-limit", "just-older"} {
		require.NoError(t, s.TransitionPending(ctx, tenant, id, "pending", "done", "", testNow))
	}

	n, err := s.PrunePending(ctx, testNow, testNow.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, []string{"at-limit"}, pendingIDs(t, s))
}

func TestExpirePending(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	ensureTestTenant(t, s, "other")
	row := testPendingRow(tenant, acct, "p1", testNow)
	putPending(t, s, row)

	// Not past its time yet, to the millisecond: nothing changes.
	require.NoError(t, s.ExpirePending(ctx, tenant, "p1", row.ExpiresAt))
	require.Equal(t, "pending", pendingState(t, s, tenant, "p1"))
	// Another tenant has no such row.
	require.NoError(t, s.ExpirePending(ctx, "other", "p1", row.ExpiresAt.Add(time.Hour)))
	require.Equal(t, "pending", pendingState(t, s, tenant, "p1"))
	// An action that does not exist is not an error.
	require.NoError(t, s.ExpirePending(ctx, tenant, "missing", row.ExpiresAt.Add(time.Hour)))
	require.ErrorIs(t, s.ExpirePending(ctx, tenant, "p|1", testNow), ErrInvalidID)
	require.ErrorIs(t, s.ExpirePending(ctx, "", "p1", testNow), ErrInvalidID)

	// One millisecond later it has expired.
	require.NoError(t, s.ExpirePending(ctx, tenant, "p1", row.ExpiresAt.Add(time.Millisecond)))
	require.Equal(t, "expired", pendingState(t, s, tenant, "p1"))
	payloadNull, previewNull := pendingSecretsAreNull(t, s, tenant, "p1")
	require.True(t, payloadNull)
	require.True(t, previewNull)

	// An approved action is not waiting. It does not expire, however late it is.
	approved := testPendingRow(tenant, acct, "p2", testNow)
	putPending(t, s, approved)
	require.NoError(t, s.TransitionPending(ctx, tenant, "p2", "pending", "approved", "", testNow))
	require.NoError(t, s.ExpirePending(ctx, tenant, "p2", approved.ExpiresAt.Add(time.Hour)))
	require.Equal(t, "approved", pendingState(t, s, tenant, "p2"))
	payloadNull, _ = pendingSecretsAreNull(t, s, tenant, "p2")
	require.False(t, payloadNull)
}

// ---- Fix round 1 ----

// When the store cannot record the expiry after a missed change, the caller
// still gets ErrNotFound (the change did not happen), and the error of the
// recording is there too, for the log.
func TestAnExpiryThatCannotBeRecordedStillGivesErrNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	row := testPendingRow(tenant, acct, "p1", testNow)
	putPending(t, s, row)
	late := row.ExpiresAt.Add(time.Millisecond)
	// A trigger refuses the UPDATE that records the expiry.
	_, err := s.db.ExecContext(ctx, `CREATE TRIGGER no_expire BEFORE UPDATE ON pending_actions
		WHEN NEW.state = 'expired' BEGIN SELECT RAISE(ABORT, 'expire failed'); END`)
	require.NoError(t, err)

	for name, attempt := range map[string]func() error{
		"transition": func() error { return s.TransitionPending(ctx, tenant, "p1", "pending", "approved", "", late) },
		"claim":      func() error { return s.ClaimPending(ctx, tenant, "p1", row.Hash, late) },
	} {
		err := attempt()
		require.ErrorIs(t, err, ErrNotFound, name)
		require.ErrorContains(t, err, "expire failed", name)
	}
	require.Equal(t, "pending", pendingState(t, s, tenant, "p1"))

	// A call that finds nothing to expire does not touch the trigger.
	require.ErrorIs(t, s.TransitionPending(ctx, tenant, "missing", "pending", "approved", "", late), ErrNotFound)
}

// The table keeps the rule about secrets itself: a waiting or approved action
// has its payload and its preview, and an action that has ended has neither. A
// bug cannot leave a secret on disk, or lose one that is needed.
func TestPendingTableRulesAboutSecrets(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	putPending(t, s, testPendingRow(tenant, acct, "live", testNow))
	putPending(t, s, testPendingRow(tenant, acct, "approved", testNow))
	require.NoError(t, s.TransitionPending(ctx, tenant, "approved", "pending", "approved", "", testNow))
	putPending(t, s, testPendingRow(tenant, acct, "ended", testNow))
	require.NoError(t, s.TransitionPending(ctx, tenant, "ended", "pending", "done", "", testNow))

	refused := map[string]string{
		"end a row and keep its secrets":      `UPDATE pending_actions SET state = 'done' WHERE id = 'live'`,
		"reject a row and keep its secrets":   `UPDATE pending_actions SET state = 'rejected' WHERE id = 'approved'`,
		"expire a row and keep its secrets":   `UPDATE pending_actions SET state = 'expired' WHERE id = 'live'`,
		"clear the payload of a waiting row":  `UPDATE pending_actions SET payload = NULL WHERE id = 'live'`,
		"clear the preview of a waiting row":  `UPDATE pending_actions SET preview = NULL WHERE id = 'live'`,
		"clear the payload of an approved":    `UPDATE pending_actions SET payload = NULL WHERE id = 'approved'`,
		"give a payload back to an ended row": `UPDATE pending_actions SET payload = x'0102' WHERE id = 'ended'`,
		"give a preview back to an ended row": `UPDATE pending_actions SET preview = x'0102' WHERE id = 'ended'`,
		"bring an ended row back to waiting":  `UPDATE pending_actions SET state = 'pending' WHERE id = 'ended'`,
		"an unknown state with secrets":       `UPDATE pending_actions SET state = 'sent' WHERE id = 'live'`,
	}
	for name, statement := range refused {
		_, err := s.db.ExecContext(ctx, statement)
		require.Error(t, err, name)
		require.ErrorContains(t, err, "CHECK constraint failed", name)
	}

	// A new row must follow the rule too.
	ended := testPendingRow(tenant, acct, "new-ended", testNow)
	ended.State = "done"
	require.Error(t, s.CreatePending(ctx, ended, []byte("payload"), 1), "an ended row with secrets")

	// The rows are as they were, and the allowed changes still work.
	require.Equal(t, "pending", pendingState(t, s, tenant, "live"))
	require.Equal(t, "approved", pendingState(t, s, tenant, "approved"))
	require.Equal(t, "done", pendingState(t, s, tenant, "ended"))
	require.NoError(t, s.TransitionPending(ctx, tenant, "approved", "approved", "done", "r", testNow))
	require.NoError(t, s.ExpirePending(ctx, tenant, "live", testNow.Add(2*pendingLife)))
	payloadNull, previewNull := pendingSecretsAreNull(t, s, tenant, "live")
	require.True(t, payloadNull)
	require.True(t, previewNull)
}

// An approved action with no result whose time to be approved ended long ago is
// left over from a program that stopped between the approval and the end of
// the action. ExpireStaleApprovedPending ends it, in every tenant. It keeps
// every other row as it is.
func TestExpireStaleApprovedPending(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	ensureTestTenant(t, s, "other")
	require.NoError(t, s.CreateAccount(ctx, testAccount("other", "a1", testNow)))

	put := func(tenantID account.TenantID, id string, created time.Time, to, resultID string) {
		putPending(t, s, testPendingRow(tenantID, acct, id, created))
		if to != "" {
			require.NoError(t, s.TransitionPending(ctx, tenantID, id, "pending", to, resultID, created))
		}
	}
	// testPendingRow lives 24 hours. The cut is one hour after the end of the
	// life of the old rows.
	old := testNow
	cut := old.Add(pendingLife + time.Hour)
	put(tenant, "stale", old, "approved", "")
	put("other", "stale-other-tenant", old, "approved", "")
	put(tenant, "approved-with-result", old, "approved", "item-1")
	put(tenant, "approved-recent", cut.Add(-pendingLife), "approved", "") // ends exactly at the cut
	put(tenant, "waiting", old, "", "")
	put(tenant, "done", old, "done", "item-2")

	n, err := s.ExpireStaleApprovedPending(ctx, cut)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	require.Equal(t, "expired", pendingState(t, s, tenant, "stale"))
	require.Equal(t, "expired", pendingState(t, s, "other", "stale-other-tenant"))
	payloadNull, previewNull := pendingSecretsAreNull(t, s, tenant, "stale")
	require.True(t, payloadNull, "an expired action keeps no payload")
	require.True(t, previewNull, "an expired action keeps no preview")

	require.Equal(t, "approved", pendingState(t, s, tenant, "approved-with-result"))
	require.Equal(t, "approved", pendingState(t, s, tenant, "approved-recent"), "the cut is strict")
	require.Equal(t, "pending", pendingState(t, s, tenant, "waiting"))
	require.Equal(t, "done", pendingState(t, s, tenant, "done"))

	n, err = s.ExpireStaleApprovedPending(ctx, cut)
	require.NoError(t, err)
	require.Zero(t, n, "a second call has nothing left to do")
}
