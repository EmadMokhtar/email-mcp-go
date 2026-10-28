package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// counterStart is the start of a test window.
var counterStart = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

// newCounterAccount creates the local tenant and one account in it. It returns
// the tenant and the account id.
func newCounterAccount(t *testing.T, s *Store, id account.AccountID) (account.TenantID, account.AccountID) {
	t.Helper()
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, id, testNow)))
	return tenant, id
}

// counterValue reads one counter straight from the table. It returns 0 when
// there is no row.
func counterValue(t *testing.T, s *Store, tenant account.TenantID, acct account.AccountID, kind string, start time.Time) int {
	t.Helper()
	var n int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT coalesce(sum(count), 0) FROM rate_counters
		WHERE tenant_id = ? AND account_id = ? AND kind = ? AND window_start = ?`,
		string(tenant), string(acct), kind, start.Unix()).Scan(&n)
	require.NoError(t, err)
	return n
}

// rowCount returns the number of rows of one account in rate_counters.
func rowCount(t *testing.T, s *Store, tenant account.TenantID, acct account.AccountID) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM rate_counters WHERE tenant_id = ? AND account_id = ?`,
		string(tenant), string(acct)).Scan(&n))
	return n
}

func TestReserveCountersAddsUpToTheLimit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newCounterAccount(t, s, "a1")
	checks := []CounterCheck{{Kind: "send_hour", WindowStart: counterStart, Limit: 3}}

	full, err := s.ReserveCounters(ctx, tenant, acct, checks, 2, time.Time{})
	require.NoError(t, err)
	require.Empty(t, full)
	require.Equal(t, 2, counterValue(t, s, tenant, acct, "send_hour", counterStart))

	// 2 + 2 is over the limit of 3. Nothing is added.
	full, err = s.ReserveCounters(ctx, tenant, acct, checks, 2, time.Time{})
	require.NoError(t, err)
	require.Equal(t, []int{0}, full)
	require.Equal(t, 2, counterValue(t, s, tenant, acct, "send_hour", counterStart))

	// The limit itself is allowed: 2 + 1 = 3.
	full, err = s.ReserveCounters(ctx, tenant, acct, checks, 1, time.Time{})
	require.NoError(t, err)
	require.Empty(t, full)
	require.Equal(t, 3, counterValue(t, s, tenant, acct, "send_hour", counterStart))
}

func TestReserveCountersAddsToNoCounterWhenOneIsFull(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newCounterAccount(t, s, "a1")
	checks := []CounterCheck{
		{Kind: "send_hour", WindowStart: counterStart, Limit: 10},
		{Kind: "send_day", WindowStart: counterStart.Truncate(24 * time.Hour), Limit: 1},
	}

	full, err := s.ReserveCounters(ctx, tenant, acct, checks, 2, time.Time{})
	require.NoError(t, err)
	// Only the second counter is over its limit. The first one still gets no
	// increment.
	require.Equal(t, []int{1}, full)
	require.Equal(t, 0, counterValue(t, s, tenant, acct, "send_hour", counterStart))
	require.Equal(t, 0, counterValue(t, s, tenant, acct, "send_day", counterStart.Truncate(24*time.Hour)))
}

func TestReserveCountersReportsEveryFullCounter(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newCounterAccount(t, s, "a1")
	checks := []CounterCheck{
		{Kind: "send_hour", WindowStart: counterStart, Limit: 1},
		{Kind: "send_day", WindowStart: counterStart.Truncate(24 * time.Hour), Limit: 1},
	}

	full, err := s.ReserveCounters(ctx, tenant, acct, checks, 2, time.Time{})
	require.NoError(t, err)
	require.Equal(t, []int{0, 1}, full)
}

func TestReserveCountersWithLimitZeroAllowsNothing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newCounterAccount(t, s, "a1")
	checks := []CounterCheck{{Kind: "trash_day", WindowStart: counterStart, Limit: 0}}

	full, err := s.ReserveCounters(ctx, tenant, acct, checks, 1, time.Time{})
	require.NoError(t, err)
	require.Equal(t, []int{0}, full)
	require.Equal(t, 0, counterValue(t, s, tenant, acct, "trash_day", counterStart))
}

func TestReserveCountersKeepsWindowsAndKindsApart(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newCounterAccount(t, s, "a1")
	next := counterStart.Add(time.Hour)

	for _, c := range []CounterCheck{
		{Kind: "send_hour", WindowStart: counterStart, Limit: 1},
		{Kind: "send_hour", WindowStart: next, Limit: 1},
		{Kind: "trash_day", WindowStart: counterStart, Limit: 1},
	} {
		full, err := s.ReserveCounters(ctx, tenant, acct, []CounterCheck{c}, 1, time.Time{})
		require.NoError(t, err)
		require.Empty(t, full, "%s %v", c.Kind, c.WindowStart)
	}
	require.Equal(t, 1, counterValue(t, s, tenant, acct, "send_hour", counterStart))
	require.Equal(t, 1, counterValue(t, s, tenant, acct, "send_hour", next))
	require.Equal(t, 1, counterValue(t, s, tenant, acct, "trash_day", counterStart))
}

func TestReserveCountersStoresWindowStartAsUnixSeconds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newCounterAccount(t, s, "a1")

	_, err := s.ReserveCounters(ctx, tenant, acct,
		[]CounterCheck{{Kind: "send_hour", WindowStart: counterStart, Limit: 5}}, 1, time.Time{})
	require.NoError(t, err)

	var start int64
	require.NoError(t, s.db.QueryRowContext(ctx,
		`SELECT window_start FROM rate_counters WHERE tenant_id = ? AND account_id = ?`,
		string(tenant), string(acct)).Scan(&start))
	require.Equal(t, counterStart.Unix(), start)
}

func TestReserveCountersIsolatesAccountsAndTenants(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	local, a1 := newCounterAccount(t, s, "a1")
	require.NoError(t, s.CreateAccount(ctx, testAccount(local, "a2", testNow)))
	// The same account id under another tenant is a different account.
	other := account.TenantID("other")
	ensureTestTenant(t, s, other)
	require.NoError(t, s.CreateAccount(ctx, testAccount(other, "a1", testNow)))
	checks := []CounterCheck{{Kind: "send_hour", WindowStart: counterStart, Limit: 1}}

	for _, c := range []struct {
		tenant account.TenantID
		acct   account.AccountID
	}{{local, a1}, {local, "a2"}, {other, "a1"}} {
		full, err := s.ReserveCounters(ctx, c.tenant, c.acct, checks, 1, time.Time{})
		require.NoError(t, err)
		require.Empty(t, full, "%s/%s", c.tenant, c.acct)
	}
	// Each one is now full, but only for itself.
	full, err := s.ReserveCounters(ctx, other, "a1", checks, 1, time.Time{})
	require.NoError(t, err)
	require.Equal(t, []int{0}, full)
}

func TestReserveCountersPrunesOldWindowsOfThatAccountOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, a1 := newCounterAccount(t, s, "a1")
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "a2", testNow)))
	old := counterStart.Add(-72 * time.Hour)
	recent := counterStart.Add(-24 * time.Hour)
	cutoff := counterStart.Add(-48 * time.Hour)

	for _, acct := range []account.AccountID{a1, "a2"} {
		for _, start := range []time.Time{old, recent} {
			_, err := s.ReserveCounters(ctx, tenant, acct,
				[]CounterCheck{{Kind: "send_day", WindowStart: start, Limit: 10}}, 1, time.Time{})
			require.NoError(t, err)
		}
	}
	require.Equal(t, 2, rowCount(t, s, tenant, a1))

	// This reserve prunes windows that start before the cutoff for a1 only.
	_, err := s.ReserveCounters(ctx, tenant, a1,
		[]CounterCheck{{Kind: "send_hour", WindowStart: counterStart, Limit: 10}}, 1, cutoff)
	require.NoError(t, err)

	require.Equal(t, 0, counterValue(t, s, tenant, a1, "send_day", old))
	require.Equal(t, 1, counterValue(t, s, tenant, a1, "send_day", recent))
	require.Equal(t, 1, counterValue(t, s, tenant, a1, "send_hour", counterStart))
	require.Equal(t, 1, counterValue(t, s, tenant, "a2", "send_day", old), "other account")
}

func TestReserveCountersPrunesEvenWhenALimitIsFull(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newCounterAccount(t, s, "a1")
	old := counterStart.Add(-72 * time.Hour)
	_, err := s.ReserveCounters(ctx, tenant, acct,
		[]CounterCheck{{Kind: "send_day", WindowStart: old, Limit: 10}}, 1, time.Time{})
	require.NoError(t, err)

	full, err := s.ReserveCounters(ctx, tenant, acct,
		[]CounterCheck{{Kind: "send_hour", WindowStart: counterStart, Limit: 0}}, 1, counterStart.Add(-48*time.Hour))
	require.NoError(t, err)
	require.Equal(t, []int{0}, full)
	require.Equal(t, 0, rowCount(t, s, tenant, acct))
}

func TestReserveCountersFailsForAnUnknownAccount(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, _ := newCounterAccount(t, s, "a1")

	_, err := s.ReserveCounters(ctx, tenant, "missing",
		[]CounterCheck{{Kind: "send_hour", WindowStart: counterStart, Limit: 5}}, 1, time.Time{})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "missing", "error text has no account id")
}

func TestReserveCountersChecksIDsAndCount(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newCounterAccount(t, s, "a1")
	checks := []CounterCheck{{Kind: "send_hour", WindowStart: counterStart, Limit: 5}}

	_, err := s.ReserveCounters(ctx, "", acct, checks, 1, time.Time{})
	require.ErrorIs(t, err, ErrInvalidID)
	_, err = s.ReserveCounters(ctx, tenant, "a|b", checks, 1, time.Time{})
	require.ErrorIs(t, err, ErrInvalidID)

	// A zero or negative count must never lower a counter.
	for _, n := range []int{0, -1} {
		_, err = s.ReserveCounters(ctx, tenant, acct, checks, n, time.Time{})
		require.Error(t, err, "n=%d", n)
	}
	_, err = s.ReserveCounters(ctx, tenant, acct, nil, 1, time.Time{})
	require.Error(t, err, "no counters")
	// The same counter twice would be checked once and added to twice.
	_, err = s.ReserveCounters(ctx, tenant, acct, []CounterCheck{checks[0], checks[0]}, 1, time.Time{})
	require.Error(t, err, "the same counter twice")
	require.Equal(t, 0, rowCount(t, s, tenant, acct))
}

func TestDeleteAccountDeletesItsCounters(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newCounterAccount(t, s, "a1")
	_, err := s.ReserveCounters(ctx, tenant, acct,
		[]CounterCheck{{Kind: "send_hour", WindowStart: counterStart, Limit: 5}}, 1, time.Time{})
	require.NoError(t, err)
	require.Equal(t, 1, rowCount(t, s, tenant, acct))

	require.NoError(t, s.DeleteAccount(ctx, tenant, acct))
	require.Equal(t, 0, rowCount(t, s, tenant, acct))
}
