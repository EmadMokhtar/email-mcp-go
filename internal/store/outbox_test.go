package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// outboxStart is the time of the first item in the outbox tests. It has no
// part smaller than a millisecond, because the store keeps whole milliseconds.
var outboxStart = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

// newOutboxStore returns a store with the tenant "local" and one account.
func newOutboxStore(t *testing.T) (*Store, account.AccountID) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	acct := account.AccountID("acct1")
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, acct, outboxStart)))
	return s, acct
}

// outboxItem returns a row for a new item that is due at sendAfter.
func outboxItem(id string, acct account.AccountID, sendAfter time.Time) *OutboxRow {
	return &OutboxRow{
		ID:            id,
		Tenant:        LocalTenantID,
		Account:       acct,
		SendAfter:     sendAfter,
		NextAttemptAt: sendAfter,
		CreatedAt:     outboxStart,
	}
}

func createOutbox(t *testing.T, s *Store, row *OutboxRow) {
	t.Helper()
	require.NoError(t, s.CreateOutbox(context.Background(), row, []byte("sealed-"+row.ID), 3))
}

func TestOutboxCreateAndGet(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	row := outboxItem("item1", acct, outboxStart.Add(30*time.Second))
	row.DraftRef = "drafts:9"
	require.NoError(t, s.CreateOutbox(ctx, row, []byte("sealed message"), 3))

	got, err := s.GetOutbox(ctx, LocalTenantID, "item1")
	require.NoError(t, err)
	require.Equal(t, "queued", got.Status, "a new item is always queued")
	require.Equal(t, LocalTenantID, got.Tenant)
	require.Equal(t, acct, got.Account)
	require.Equal(t, outboxStart.Add(30*time.Second), got.SendAfter)
	require.Equal(t, outboxStart.Add(30*time.Second), got.NextAttemptAt)
	require.Equal(t, outboxStart, got.CreatedAt)
	require.True(t, got.SentAt.IsZero())
	require.Zero(t, got.Attempts)
	require.Empty(t, got.Error)
	require.Equal(t, "drafts:9", got.DraftRef)

	version, sealed, err := s.GetOutboxMessage(ctx, LocalTenantID, "item1")
	require.NoError(t, err)
	require.Equal(t, 3, version)
	require.Equal(t, []byte("sealed message"), sealed)

	// An item without a draft has an empty reference.
	createOutbox(t, s, outboxItem("item2", acct, outboxStart))
	got, err = s.GetOutbox(ctx, LocalTenantID, "item2")
	require.NoError(t, err)
	require.Empty(t, got.DraftRef)
}

func TestOutboxCreateRefusesBadInput(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	good := func() *OutboxRow { return outboxItem("item1", acct, outboxStart) }

	row := good()
	row.ID = ""
	require.ErrorIs(t, s.CreateOutbox(ctx, row, []byte("x"), 1), ErrInvalidID)
	row = good()
	row.ID = "a|b"
	require.ErrorIs(t, s.CreateOutbox(ctx, row, []byte("x"), 1), ErrInvalidID)
	row = good()
	row.Tenant = "a|b"
	require.ErrorIs(t, s.CreateOutbox(ctx, row, []byte("x"), 1), ErrInvalidID)
	row = good()
	row.Account = ""
	require.ErrorIs(t, s.CreateOutbox(ctx, row, []byte("x"), 1), ErrInvalidID)
	require.Error(t, s.CreateOutbox(ctx, good(), nil, 1), "the message is required")
	require.Error(t, s.CreateOutbox(ctx, good(), []byte{}, 1), "the message is required")

	// Unknown account: the foreign key refuses it.
	row = good()
	row.Account = "nope"
	require.Error(t, s.CreateOutbox(ctx, row, []byte("x"), 1))

	// A second item with the same id.
	createOutbox(t, s, good())
	require.Error(t, s.CreateOutbox(ctx, good(), []byte("x"), 1))
}

func TestOutboxIsScopedToTheTenant(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	createOutbox(t, s, outboxItem("item1", acct, outboxStart))
	other := account.TenantID("other")
	ensureTestTenant(t, s, other)
	now := outboxStart.Add(time.Hour)

	_, err := s.GetOutbox(ctx, other, "item1")
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.GetOutboxMessage(ctx, other, "item1")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.ClaimOutbox(ctx, other, "item1", now)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, s.CancelOutbox(ctx, other, "item1"), ErrNotFound)

	// Make the item "sending" and try the result calls with the wrong tenant.
	_, err = s.ClaimOutbox(ctx, LocalTenantID, "item1", now)
	require.NoError(t, err)
	require.ErrorIs(t, s.FinishOutboxSent(ctx, other, "item1", now), ErrNotFound)
	require.ErrorIs(t, s.FailOutbox(ctx, other, "item1", "x"), ErrNotFound)
	require.ErrorIs(t, s.RetryOutbox(ctx, other, "item1", now, "x"), ErrNotFound)
	require.ErrorIs(t, s.ReleaseOutbox(ctx, other, "item1"), ErrNotFound)

	got, err := s.GetOutbox(ctx, LocalTenantID, "item1")
	require.NoError(t, err)
	require.Equal(t, "sending", got.Status, "the other tenant changed nothing")
}

func TestOutboxRefusesBadIDs(t *testing.T) {
	s, _ := newOutboxStore(t)
	ctx := context.Background()
	now := outboxStart

	for _, id := range []string{"", "a|b"} {
		_, err := s.GetOutbox(ctx, LocalTenantID, id)
		require.ErrorIs(t, err, ErrInvalidID)
		_, _, err = s.GetOutboxMessage(ctx, LocalTenantID, id)
		require.ErrorIs(t, err, ErrInvalidID)
		_, err = s.ClaimOutbox(ctx, LocalTenantID, id, now)
		require.ErrorIs(t, err, ErrInvalidID)
		require.ErrorIs(t, s.ReleaseOutbox(ctx, LocalTenantID, id), ErrInvalidID)
		require.ErrorIs(t, s.FinishOutboxSent(ctx, LocalTenantID, id, now), ErrInvalidID)
		require.ErrorIs(t, s.FailOutbox(ctx, LocalTenantID, id, "x"), ErrInvalidID)
		require.ErrorIs(t, s.RetryOutbox(ctx, LocalTenantID, id, now, "x"), ErrInvalidID)
		require.ErrorIs(t, s.CancelOutbox(ctx, LocalTenantID, id), ErrInvalidID)
	}
	_, err := s.GetOutbox(ctx, "a|b", "item1")
	require.ErrorIs(t, err, ErrInvalidID)
	_, err = s.GetOutbox(ctx, "", "item1")
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestOutboxClaimNeedsADueQueuedItem(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	due := outboxStart.Add(time.Minute)
	createOutbox(t, s, outboxItem("item1", acct, due))

	_, err := s.ClaimOutbox(ctx, LocalTenantID, "item1", due.Add(-time.Millisecond))
	require.ErrorIs(t, err, ErrNotFound, "not due yet")
	_, err = s.ClaimOutbox(ctx, LocalTenantID, "missing", due)
	require.ErrorIs(t, err, ErrNotFound)

	row, err := s.ClaimOutbox(ctx, LocalTenantID, "item1", due)
	require.NoError(t, err, "due at exactly send_after")
	require.Equal(t, "sending", row.Status)
	require.Equal(t, "item1", row.ID)
	require.Equal(t, acct, row.Account)
	require.Equal(t, due, row.ClaimedAt, "the claim saves its time")
	stored, err := s.GetOutbox(ctx, LocalTenantID, "item1")
	require.NoError(t, err)
	require.Equal(t, due, stored.ClaimedAt)

	_, err = s.ClaimOutbox(ctx, LocalTenantID, "item1", due)
	require.ErrorIs(t, err, ErrNotFound, "a claimed item cannot be claimed again")
}

// The retry time counts too: an item that waits for its next try is not due,
// even if send_after has passed.
func TestOutboxClaimRespectsTheNextAttemptTime(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	row := outboxItem("item1", acct, outboxStart)
	row.NextAttemptAt = outboxStart.Add(time.Minute)
	createOutbox(t, s, row)

	_, err := s.ClaimOutbox(ctx, LocalTenantID, "item1", outboxStart.Add(30*time.Second))
	require.ErrorIs(t, err, ErrNotFound)
	due, err := s.DueOutbox(ctx, outboxStart.Add(30*time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, due)
	_, err = s.ClaimOutbox(ctx, LocalTenantID, "item1", outboxStart.Add(time.Minute))
	require.NoError(t, err)
}

func TestOutboxClaimIsExclusive(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	createOutbox(t, s, outboxItem("item1", acct, outboxStart))

	const callers = 16
	var wins, misses atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.ClaimOutbox(ctx, LocalTenantID, "item1", outboxStart)
			switch err {
			case nil:
				wins.Add(1)
			case ErrNotFound:
				misses.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.EqualValues(t, 1, wins.Load())
	require.EqualValues(t, callers-1, misses.Load())
}

func TestOutboxResultsNeedTheStateSending(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	now := outboxStart.Add(time.Minute)
	createOutbox(t, s, outboxItem("item1", acct, outboxStart))

	// Queued: none of the result calls applies.
	require.ErrorIs(t, s.FinishOutboxSent(ctx, LocalTenantID, "item1", now), ErrNotFound)
	require.ErrorIs(t, s.FailOutbox(ctx, LocalTenantID, "item1", "x"), ErrNotFound)
	require.ErrorIs(t, s.RetryOutbox(ctx, LocalTenantID, "item1", now, "x"), ErrNotFound)
	require.ErrorIs(t, s.ReleaseOutbox(ctx, LocalTenantID, "item1"), ErrNotFound)
	got, err := s.GetOutbox(ctx, LocalTenantID, "item1")
	require.NoError(t, err)
	require.Equal(t, "queued", got.Status)

	// A result is written once.
	_, err = s.ClaimOutbox(ctx, LocalTenantID, "item1", now)
	require.NoError(t, err)
	require.NoError(t, s.FinishOutboxSent(ctx, LocalTenantID, "item1", now))
	require.ErrorIs(t, s.FinishOutboxSent(ctx, LocalTenantID, "item1", now), ErrNotFound)
	require.ErrorIs(t, s.FailOutbox(ctx, LocalTenantID, "item1", "x"), ErrNotFound)
}

func TestOutboxFinishSent(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	now := outboxStart.Add(time.Minute)
	row := outboxItem("item1", acct, outboxStart)
	row.DraftRef = "drafts:1"
	createOutbox(t, s, row)
	_, err := s.ClaimOutbox(ctx, LocalTenantID, "item1", now)
	require.NoError(t, err)
	require.NoError(t, s.RetryOutbox(ctx, LocalTenantID, "item1", now, "old error"))
	_, err = s.ClaimOutbox(ctx, LocalTenantID, "item1", now)
	require.NoError(t, err)

	require.NoError(t, s.FinishOutboxSent(ctx, LocalTenantID, "item1", now))

	got, err := s.GetOutbox(ctx, LocalTenantID, "item1")
	require.NoError(t, err)
	require.Equal(t, "sent", got.Status)
	require.Equal(t, now, got.SentAt)
	require.Empty(t, got.Error, "the error of an earlier try is cleared")
	require.Empty(t, got.DraftRef)
	require.Equal(t, 1, got.Attempts, "attempts counts the failed tries")
	_, _, err = s.GetOutboxMessage(ctx, LocalTenantID, "item1")
	require.ErrorIs(t, err, ErrNotFound, "the message is deleted")
	var messageNull, draftNull bool
	require.NoError(t, s.db.QueryRowContext(ctx,
		`SELECT message IS NULL, draft_ref IS NULL FROM outbox WHERE id = 'item1'`).Scan(&messageNull, &draftNull))
	require.True(t, messageNull)
	require.True(t, draftNull)
}

func TestOutboxFailAndRetry(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	now := outboxStart.Add(time.Minute)
	createOutbox(t, s, outboxItem("retry", acct, outboxStart))
	createOutbox(t, s, outboxItem("fail", acct, outboxStart))
	for _, id := range []string{"retry", "fail"} {
		_, err := s.ClaimOutbox(ctx, LocalTenantID, id, now)
		require.NoError(t, err)
	}

	next := now.Add(30 * time.Second)
	require.NoError(t, s.RetryOutbox(ctx, LocalTenantID, "retry", next, "try again"))
	got, err := s.GetOutbox(ctx, LocalTenantID, "retry")
	require.NoError(t, err)
	require.Equal(t, "queued", got.Status)
	require.Equal(t, 1, got.Attempts)
	require.Equal(t, next, got.NextAttemptAt)
	require.Equal(t, outboxStart, got.SendAfter, "send_after does not change")
	require.Equal(t, "try again", got.Error)
	_, _, err = s.GetOutboxMessage(ctx, LocalTenantID, "retry")
	require.NoError(t, err, "a message that will be retried is kept")

	require.NoError(t, s.FailOutbox(ctx, LocalTenantID, "fail", "refused"))
	got, err = s.GetOutbox(ctx, LocalTenantID, "fail")
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, 1, got.Attempts)
	require.Equal(t, "refused", got.Error)
	_, _, err = s.GetOutboxMessage(ctx, LocalTenantID, "fail")
	require.ErrorIs(t, err, ErrNotFound, "the message is deleted")
}

func TestOutboxRelease(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	createOutbox(t, s, outboxItem("item1", acct, outboxStart))
	_, err := s.ClaimOutbox(ctx, LocalTenantID, "item1", outboxStart)
	require.NoError(t, err)

	require.NoError(t, s.ReleaseOutbox(ctx, LocalTenantID, "item1"))
	got, err := s.GetOutbox(ctx, LocalTenantID, "item1")
	require.NoError(t, err)
	require.Equal(t, "queued", got.Status)
	require.Zero(t, got.Attempts, "nothing was sent, so it is not a failed try")
	_, _, err = s.GetOutboxMessage(ctx, LocalTenantID, "item1")
	require.NoError(t, err)
}

func TestOutboxCancel(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	row := outboxItem("queued", acct, outboxStart.Add(time.Hour))
	row.DraftRef = "drafts:1"
	createOutbox(t, s, row)
	createOutbox(t, s, outboxItem("sending", acct, outboxStart))
	createOutbox(t, s, outboxItem("sent", acct, outboxStart))
	createOutbox(t, s, outboxItem("failed", acct, outboxStart))
	now := outboxStart.Add(time.Minute)
	for _, id := range []string{"sending", "sent", "failed"} {
		_, err := s.ClaimOutbox(ctx, LocalTenantID, id, now)
		require.NoError(t, err)
	}
	require.NoError(t, s.FinishOutboxSent(ctx, LocalTenantID, "sent", now))
	require.NoError(t, s.FailOutbox(ctx, LocalTenantID, "failed", "x"))

	require.NoError(t, s.CancelOutbox(ctx, LocalTenantID, "queued"))
	got, err := s.GetOutbox(ctx, LocalTenantID, "queued")
	require.NoError(t, err)
	require.Equal(t, "cancelled", got.Status)
	require.Empty(t, got.DraftRef)
	_, _, err = s.GetOutboxMessage(ctx, LocalTenantID, "queued")
	require.ErrorIs(t, err, ErrNotFound, "the message is deleted")

	// A second cancel does nothing and is not an error.
	require.NoError(t, s.CancelOutbox(ctx, LocalTenantID, "queued"))

	for _, id := range []string{"sending", "sent", "failed"} {
		require.ErrorIs(t, s.CancelOutbox(ctx, LocalTenantID, id), ErrNotQueued, id)
	}
	require.ErrorIs(t, s.CancelOutbox(ctx, LocalTenantID, "missing"), ErrNotFound)
	got, err = s.GetOutbox(ctx, LocalTenantID, "sending")
	require.NoError(t, err)
	require.Equal(t, "sending", got.Status)
}

func TestOutboxDueAndNextDue(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()

	_, ok, err := s.NextOutboxDue(ctx)
	require.NoError(t, err)
	require.False(t, ok, "nothing is queued")

	// Created in this order; due in the order c, a, b. "d" is not due yet.
	a := outboxItem("a", acct, outboxStart.Add(20*time.Second))
	b := outboxItem("b", acct, outboxStart.Add(30*time.Second))
	c := outboxItem("c", acct, outboxStart.Add(10*time.Second))
	d := outboxItem("d", acct, outboxStart.Add(time.Hour))
	for _, row := range []*OutboxRow{a, b, c, d} {
		createOutbox(t, s, row)
	}
	now := outboxStart.Add(time.Minute)

	next, ok, err := s.NextOutboxDue(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, outboxStart.Add(10*time.Second), next)

	due, err := s.DueOutbox(ctx, now, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"c", "a", "b"}, outboxIDs(due))

	limited, err := s.DueOutbox(ctx, now, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"c", "a"}, outboxIDs(limited))

	// Only queued items are due. A claimed item and a cancelled item are not.
	_, err = s.ClaimOutbox(ctx, LocalTenantID, "c", now)
	require.NoError(t, err)
	require.NoError(t, s.CancelOutbox(ctx, LocalTenantID, "a"))
	due, err = s.DueOutbox(ctx, now, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, outboxIDs(due))

	// A retry moves the due time. It does not use send_after.
	_, err = s.ClaimOutbox(ctx, LocalTenantID, "b", now)
	require.NoError(t, err)
	require.NoError(t, s.RetryOutbox(ctx, LocalTenantID, "b", now.Add(10*time.Minute), "x"))
	due, err = s.DueOutbox(ctx, now, 10)
	require.NoError(t, err)
	require.Empty(t, due)
	next, ok, err = s.NextOutboxDue(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, now.Add(10*time.Minute), next, "the retry of b comes before d")
}

func outboxIDs(rows []*OutboxRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// DueOutbox and FailSendingOutbox look at every tenant on purpose: the worker
// serves all of them.
func TestOutboxCrossTenantCalls(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	other := account.TenantID("other")
	ensureTestTenant(t, s, other)
	otherAcct := account.AccountID("acct2")
	require.NoError(t, s.CreateAccount(ctx, testAccount(other, otherAcct, outboxStart)))
	createOutbox(t, s, outboxItem("mine", acct, outboxStart))
	theirs := outboxItem("theirs", otherAcct, outboxStart.Add(time.Second))
	theirs.Tenant = other
	createOutbox(t, s, theirs)
	createOutbox(t, s, outboxItem("waiting", acct, outboxStart.Add(time.Hour)))

	now := outboxStart.Add(time.Minute)
	due, err := s.DueOutbox(ctx, now, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"mine", "theirs"}, outboxIDs(due))

	_, err = s.ClaimOutbox(ctx, LocalTenantID, "mine", now)
	require.NoError(t, err)
	_, err = s.ClaimOutbox(ctx, other, "theirs", now)
	require.NoError(t, err)

	n, err := s.FailSendingOutbox(ctx, "stopped", now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, 2, n)
	for _, key := range []struct {
		tenant account.TenantID
		id     string
	}{{LocalTenantID, "mine"}, {other, "theirs"}} {
		got, err := s.GetOutbox(ctx, key.tenant, key.id)
		require.NoError(t, err)
		require.Equal(t, "failed", got.Status)
		require.Equal(t, "stopped", got.Error)
		require.Equal(t, 1, got.Attempts)
		_, _, err = s.GetOutboxMessage(ctx, key.tenant, key.id)
		require.ErrorIs(t, err, ErrNotFound)
	}
	got, err := s.GetOutbox(ctx, LocalTenantID, "waiting")
	require.NoError(t, err)
	require.Equal(t, "queued", got.Status)

	n, err = s.FailSendingOutbox(ctx, "stopped", now.Add(time.Hour))
	require.NoError(t, err)
	require.Zero(t, n)
}

// FailSendingOutbox takes only the items that were claimed strictly before the
// given time. A newer item may belong to a program that is sending it now.
func TestOutboxFailSendingTakesOnlyStaleItems(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	createOutbox(t, s, outboxItem("old", acct, outboxStart))
	createOutbox(t, s, outboxItem("new", acct, outboxStart))
	oldClaim := outboxStart.Add(time.Minute)
	newClaim := oldClaim.Add(14 * time.Minute)
	_, err := s.ClaimOutbox(ctx, LocalTenantID, "old", oldClaim)
	require.NoError(t, err)
	_, err = s.ClaimOutbox(ctx, LocalTenantID, "new", newClaim)
	require.NoError(t, err)

	n, err := s.FailSendingOutbox(ctx, "stale", oldClaim)
	require.NoError(t, err)
	require.Zero(t, n, "claimed at exactly the given time is not stale")

	n, err = s.FailSendingOutbox(ctx, "stale", oldClaim.Add(time.Millisecond))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	got, err := s.GetOutbox(ctx, LocalTenantID, "old")
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, "stale", got.Error)
	got, err = s.GetOutbox(ctx, LocalTenantID, "new")
	require.NoError(t, err)
	require.Equal(t, "sending", got.Status, "a newer item is left alone")
	_, _, err = s.GetOutboxMessage(ctx, LocalTenantID, "new")
	require.NoError(t, err, "and so is its message")

	// A claim that is retried later gets a new time.
	require.NoError(t, s.RetryOutbox(ctx, LocalTenantID, "new", newClaim, "x"))
	later := newClaim.Add(time.Hour)
	again, err := s.ClaimOutbox(ctx, LocalTenantID, "new", later)
	require.NoError(t, err)
	require.Equal(t, later, again.ClaimedAt)
}

func TestOutboxIsDeletedWithTheAccount(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	createOutbox(t, s, outboxItem("item1", acct, outboxStart))
	require.NoError(t, s.DeleteAccount(ctx, LocalTenantID, acct))
	_, err := s.GetOutbox(ctx, LocalTenantID, "item1")
	require.ErrorIs(t, err, ErrNotFound)
}

// The table itself keeps two rules, so a mistake in the code cannot break them:
// an item that waits or is being sent has its message, and an item that has
// ended does not.
func TestOutboxTableRules(t *testing.T) {
	s, acct := newOutboxStore(t)
	ctx := context.Background()
	createOutbox(t, s, outboxItem("item1", acct, outboxStart))

	_, err := s.db.ExecContext(ctx, `UPDATE outbox SET status = 'sent' WHERE id = 'item1'`)
	require.Error(t, err, "a finished item must not keep its message")
	_, err = s.db.ExecContext(ctx, `UPDATE outbox SET message = NULL WHERE id = 'item1'`)
	require.Error(t, err, "a queued item must keep its message")
	_, err = s.db.ExecContext(ctx, `UPDATE outbox SET status = 'unknown' WHERE id = 'item1'`)
	require.Error(t, err, "the status must be one of the five")
	_, err = s.db.ExecContext(ctx, `UPDATE outbox SET attempts = -1 WHERE id = 'item1'`)
	require.Error(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE outbox SET status = 'sending' WHERE id = 'item1'`)
	require.Error(t, err, "an item in sending must have its claim time, or nobody could find it when it is stale")
}
