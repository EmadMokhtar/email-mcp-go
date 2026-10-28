package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// CounterCheck names one counter for ReserveCounters: the uses of one kind in
// one fixed window.
type CounterCheck struct {
	// Kind says what the counter counts, for example "send_hour".
	Kind string
	// WindowStart is the start of the window. The store keeps whole seconds.
	WindowStart time.Time
	// Limit is the highest total that the window may reach. A limit of 0 allows
	// nothing.
	Limit int
}

// ReserveCounters adds n to every counter in checks, but only if none of them
// would pass its limit. It does the check and the change in one transaction.
//
// The connection takes the write lock when the transaction starts. Two callers
// (in this program or in another one that uses the same database file) cannot
// both read the same total and both add to it. The second caller waits and then
// sees the new total.
//
// If every counter has room, ReserveCounters adds n to all of them and returns
// no indexes. If a counter would pass its limit, it adds nothing to any counter
// and returns the index in checks of each counter that is too full. The caller
// uses the indexes to say which window is full.
//
// In the same transaction, ReserveCounters deletes the counters of this account
// that have a window start before pruneBefore. A zero pruneBefore deletes
// nothing. The deletion also happens when the reserve is refused.
//
// n must be at least 1, so a counter never goes down. checks must not be empty,
// and it must not name the same counter twice. It fails if the tenant has no
// account with this id.
func (s *Store) ReserveCounters(ctx context.Context, tenant account.TenantID, acct account.AccountID, checks []CounterCheck, n int, pruneBefore time.Time) ([]int, error) {
	if err := validateAccountIDs(tenant, acct); err != nil {
		return nil, err
	}
	if n < 1 {
		return nil, errors.New("store: reserve counters: count must be at least 1")
	}
	if err := validateChecks(checks); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: reserve counters: %w", err)
	}
	// Rollback after a successful Commit does nothing.
	defer func() { _ = tx.Rollback() }()

	if !pruneBefore.IsZero() {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM rate_counters WHERE tenant_id = ? AND account_id = ? AND window_start < ?`,
			string(tenant), string(acct), pruneBefore.Unix()); err != nil {
			return nil, fmt.Errorf("store: reserve counters: %w", err)
		}
	}

	var full []int
	for i, c := range checks {
		var current int
		err := tx.QueryRowContext(ctx,
			`SELECT count FROM rate_counters
			WHERE tenant_id = ? AND account_id = ? AND kind = ? AND window_start = ?`,
			string(tenant), string(acct), c.Kind, c.WindowStart.Unix()).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("store: reserve counters: %w", err)
		}
		if current+n > c.Limit {
			full = append(full, i)
		}
	}

	if len(full) == 0 {
		for _, c := range checks {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO rate_counters (tenant_id, account_id, kind, window_start, count)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (tenant_id, account_id, kind, window_start)
				DO UPDATE SET count = rate_counters.count + excluded.count`,
				string(tenant), string(acct), c.Kind, c.WindowStart.Unix(), n); err != nil {
				return nil, fmt.Errorf("store: reserve counters: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: reserve counters: %w", err)
	}
	return full, nil
}

// validateChecks rejects an empty list and a list that names one counter twice.
// A counter named twice would pass the limit check once and then get n added
// twice.
func validateChecks(checks []CounterCheck) error {
	if len(checks) == 0 {
		return errors.New("store: reserve counters: no counters given")
	}
	type counterKey struct {
		kind  string
		start int64
	}
	seen := make(map[counterKey]bool, len(checks))
	for _, c := range checks {
		key := counterKey{c.Kind, c.WindowStart.Unix()}
		if seen[key] {
			return errors.New("store: reserve counters: the same counter is given twice")
		}
		seen[key] = true
	}
	return nil
}
