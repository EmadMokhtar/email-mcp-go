package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// The states of an outbox item. The outbox package owns the names. The store
// needs them, because its statements change one state to another.
const (
	outboxQueued    = "queued"
	outboxSending   = "sending"
	outboxSent      = "sent"
	outboxFailed    = "failed"
	outboxCancelled = "cancelled"
)

// ErrNotQueued means that CancelOutbox found the item, but the item is not
// queued any more: it is being sent, or it ended in a way other than a cancel.
var ErrNotQueued = errors.New("not queued")

// OutboxRow is one email that waits to be sent, or that was sent. The store does
// not look inside the sealed message and does not return it with the row. See
// GetOutboxMessage.
type OutboxRow struct {
	ID      string
	Tenant  account.TenantID
	Account account.AccountID
	// Status is one of "queued", "sending", "sent", "failed" and "cancelled".
	Status string
	// SendAfter is the end of the undo window. NextAttemptAt is the earliest
	// time of the next try. A new item has the same value in both. All times are
	// kept as whole milliseconds.
	SendAfter, NextAttemptAt, CreatedAt time.Time
	// SentAt is the zero time until the item is sent.
	SentAt time.Time
	// ClaimedAt is the time of the last claim. It is the zero time for an item
	// that was never claimed.
	ClaimedAt time.Time
	// Attempts is the number of tries that failed.
	Attempts int
	// Error is the text of the last error. It is empty after a success.
	Error string
	// DraftRef is the reference of the draft that the message was made from. It
	// is empty when there is none, and after the item has ended.
	DraftRef string
}

// outboxColumns lists the columns that scanOutbox reads. The message is not in
// the list.
const outboxColumns = `tenant_id, id, account_id, status, send_after, next_attempt_at, attempts,
	error_text, COALESCE(draft_ref, ''), created_at, sent_at, claimed_at`

// CreateOutbox adds an item in the state "queued", with its sealed message. The
// key version says which tenant key sealed the message. The status and the
// sent time of the row are ignored.
//
// It fails if the tenant has no account with this id, if the tenant already has
// an item with the same id, or if the message is empty.
func (s *Store) CreateOutbox(ctx context.Context, row *OutboxRow, sealed []byte, keyVersion int) error {
	return s.createOutbox(ctx, row, sealed, keyVersion, outboxQueued, sql.NullInt64{})
}

// CreateOutboxSending adds an item that is already claimed: it is in the state
// "sending", and claimedAt is the time of the claim. It is one insert. The
// caller sends the message at once, without ClaimOutbox, and no worker can take
// the item in between, because a worker takes only queued items.
//
// It is for a message that must be sent before the caller returns, with no undo
// window. If the program stops before the result is written, the item stays in
// "sending", and FailSendingOutbox marks it failed later. It is never sent again.
//
// The rules and the errors are those of CreateOutbox. The status and the sent
// time of the row are ignored.
func (s *Store) CreateOutboxSending(ctx context.Context, row *OutboxRow, sealed []byte, keyVersion int, claimedAt time.Time) error {
	return s.createOutbox(ctx, row, sealed, keyVersion, outboxSending,
		sql.NullInt64{Int64: claimedAt.UnixMilli(), Valid: true})
}

// createOutbox inserts an item in the given state. claimedAt must be valid when
// the state is "sending".
func (s *Store) createOutbox(ctx context.Context, row *OutboxRow, sealed []byte, keyVersion int, status string, claimedAt sql.NullInt64) error {
	if err := validateAccountIDs(row.Tenant, row.Account); err != nil {
		return err
	}
	if err := validateID("outbox item", row.ID); err != nil {
		return err
	}
	if len(sealed) == 0 {
		return errors.New("store: create outbox item: the message is empty")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO outbox (tenant_id, id, account_id, status, send_after, next_attempt_at, attempts,
			error_text, message, key_version, draft_ref, created_at, claimed_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, '', ?, ?, NULLIF(?, ''), ?, ?)`,
		string(row.Tenant), row.ID, string(row.Account), status,
		row.SendAfter.UnixMilli(), row.NextAttemptAt.UnixMilli(),
		sealed, keyVersion, row.DraftRef, row.CreatedAt.UnixMilli(), claimedAt)
	if err != nil {
		return fmt.Errorf("store: create outbox item: %w", err)
	}
	return nil
}

// GetOutbox returns one item of a tenant, without its message. It returns
// ErrNotFound if the tenant has no item with this id.
func (s *Store) GetOutbox(ctx context.Context, tenant account.TenantID, id string) (*OutboxRow, error) {
	if err := validateOutboxKey(tenant, id); err != nil {
		return nil, err
	}
	row, err := scanOutbox(s.db.QueryRowContext(ctx,
		`SELECT `+outboxColumns+` FROM outbox WHERE tenant_id = ? AND id = ?`, string(tenant), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get outbox item: %w", err)
	}
	return row, nil
}

// GetOutboxMessage returns the sealed message of an item and the version of the
// tenant key that sealed it. It returns ErrNotFound if the tenant has no item
// with this id, or if the item has ended and its message is deleted.
func (s *Store) GetOutboxMessage(ctx context.Context, tenant account.TenantID, id string) (keyVersion int, sealed []byte, err error) {
	if err := validateOutboxKey(tenant, id); err != nil {
		return 0, nil, err
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT key_version, message FROM outbox WHERE tenant_id = ? AND id = ? AND message IS NOT NULL`,
		string(tenant), id).Scan(&keyVersion, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	if err != nil {
		return 0, nil, fmt.Errorf("store: get outbox message: %w", err)
	}
	return keyVersion, sealed, nil
}

// DueOutbox returns up to limit queued items that are due at now: both
// SendAfter and NextAttemptAt are not later than now.
//
// The order is round-robin by account (an account is a tenant and an account
// id), so the limit is fair: the first due item of every account comes before
// the second due item of any account, and so on. An account with more due
// items than the limit then does not keep the items of other accounts out of
// the list. Inside one account, and among the items of the same round, the item
// that has been due the longest comes first (then the oldest, then the lowest
// id).
//
// It looks at the items of ALL tenants. This is on purpose: the worker of the
// program sends the items of every tenant. No call that is made for one user
// may use it.
func (s *Store) DueOutbox(ctx context.Context, now time.Time, limit int) ([]*OutboxRow, error) {
	// The inner query ranks each due item inside its account. It lists its
	// columns by name and leaves out the message, so the ranked rows do not
	// hold the sealed emails. The outer query reads the columns of
	// outboxColumns from it.
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+outboxColumns+` FROM (
			SELECT tenant_id, id, account_id, status, send_after, next_attempt_at, attempts,
				error_text, draft_ref, created_at, sent_at, claimed_at,
				ROW_NUMBER() OVER (
					PARTITION BY tenant_id, account_id
					ORDER BY MAX(send_after, next_attempt_at), created_at, id
				) AS account_round
			FROM outbox
			WHERE status = ? AND send_after <= ? AND next_attempt_at <= ?
		)
		ORDER BY account_round, MAX(send_after, next_attempt_at), created_at, id
		LIMIT ?`,
		outboxQueued, now.UnixMilli(), now.UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: list due outbox items: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var list []*OutboxRow
	for rows.Next() {
		row, err := scanOutbox(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list due outbox items: %w", err)
		}
		list = append(list, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list due outbox items: %w", err)
	}
	return list, nil
}

// NextOutboxDue returns the earliest time at which a queued item becomes due.
// The bool is false when no item is queued. The time can be in the past, for an
// item that is due already.
//
// Like DueOutbox, it looks at the items of ALL tenants, on purpose.
func (s *Store) NextOutboxDue(ctx context.Context) (time.Time, bool, error) {
	var next sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(MAX(send_after, next_attempt_at)) FROM outbox WHERE status = ?`,
		outboxQueued).Scan(&next)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("store: next outbox due time: %w", err)
	}
	if !next.Valid {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(next.Int64).UTC(), true, nil
}

// ClaimOutbox moves an item from "queued" to "sending", saves now as the time of
// the claim, and returns the item. It is the only way to start a send. The check
// of the state, the check that the item is due at now, and the change are one
// statement, so of two callers that claim
// the same item (in this program or in another one that uses the same database
// file), one succeeds and the other gets ErrNotFound.
//
// It returns ErrNotFound if the tenant has no item with this id, if the item is
// not queued, or if it is not due at now.
func (s *Store) ClaimOutbox(ctx context.Context, tenant account.TenantID, id string, now time.Time) (*OutboxRow, error) {
	if err := validateOutboxKey(tenant, id); err != nil {
		return nil, err
	}
	row, err := scanOutbox(s.db.QueryRowContext(ctx,
		`UPDATE outbox SET status = ?, claimed_at = ?
		WHERE tenant_id = ? AND id = ? AND status = ? AND send_after <= ? AND next_attempt_at <= ?
		RETURNING `+outboxColumns,
		outboxSending, now.UnixMilli(), string(tenant), id, outboxQueued, now.UnixMilli(), now.UnixMilli()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: claim outbox item: %w", err)
	}
	return row, nil
}

// ReleaseOutbox moves an item from "sending" back to "queued", for a caller that
// claimed the item and then found that it did not send it. The try does not
// count as failed. It returns ErrNotFound if the item is not in the state
// "sending".
func (s *Store) ReleaseOutbox(ctx context.Context, tenant account.TenantID, id string) error {
	return s.outboxResult(ctx, "release outbox item", tenant, id,
		`UPDATE outbox SET status = ? WHERE tenant_id = ? AND id = ? AND status = ?`,
		outboxQueued, string(tenant), id, outboxSending)
}

// FinishOutboxSent moves an item from "sending" to "sent". It saves the time,
// clears the error and deletes the message and the draft reference. It returns
// ErrNotFound if the tenant has no item with this id or the item is not in the
// state "sending".
func (s *Store) FinishOutboxSent(ctx context.Context, tenant account.TenantID, id string, now time.Time) error {
	return s.outboxResult(ctx, "finish outbox item", tenant, id,
		`UPDATE outbox SET status = ?, sent_at = ?, error_text = '', message = NULL, draft_ref = NULL
		WHERE tenant_id = ? AND id = ? AND status = ?`,
		outboxSent, now.UnixMilli(), string(tenant), id, outboxSending)
}

// FailOutbox moves an item from "sending" to "failed". It counts the try as
// failed, saves the error text and deletes the message and the draft reference.
// It returns ErrNotFound if the tenant has no item with this id or the item is
// not in the state "sending".
func (s *Store) FailOutbox(ctx context.Context, tenant account.TenantID, id, errText string) error {
	return s.outboxResult(ctx, "fail outbox item", tenant, id,
		`UPDATE outbox SET status = ?, attempts = attempts + 1, error_text = ?, message = NULL, draft_ref = NULL
		WHERE tenant_id = ? AND id = ? AND status = ?`,
		outboxFailed, errText, string(tenant), id, outboxSending)
}

// RetryOutbox moves an item from "sending" back to "queued" after a failed try.
// It counts the try as failed, saves the error text, and sets the time of the
// next try. The message is kept. It returns ErrNotFound if the tenant has no
// item with this id or the item is not in the state "sending".
func (s *Store) RetryOutbox(ctx context.Context, tenant account.TenantID, id string, nextAttemptAt time.Time, errText string) error {
	return s.outboxResult(ctx, "retry outbox item", tenant, id,
		`UPDATE outbox SET status = ?, attempts = attempts + 1, next_attempt_at = ?, error_text = ?
		WHERE tenant_id = ? AND id = ? AND status = ?`,
		outboxQueued, nextAttemptAt.UnixMilli(), errText, string(tenant), id, outboxSending)
}

// CancelOutbox moves an item from "queued" to "cancelled" and deletes the
// message and the draft reference. The check of the state and the change are one
// statement.
//
// It returns ErrNotFound if the tenant has no item with this id, and ErrNotQueued
// if the item is in another state. An item that is cancelled already is not an
// error: the caller wanted it cancelled, and it is.
func (s *Store) CancelOutbox(ctx context.Context, tenant account.TenantID, id string) error {
	if err := validateOutboxKey(tenant, id); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE outbox SET status = ?, message = NULL, draft_ref = NULL
		WHERE tenant_id = ? AND id = ? AND status = ?`,
		outboxCancelled, string(tenant), id, outboxQueued)
	if err != nil {
		return fmt.Errorf("store: cancel outbox item: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: cancel outbox item: %w", err)
	}
	if n == 1 {
		return nil
	}

	// The statement changed nothing. Find out why, to give the right error.
	var status string
	err = s.db.QueryRowContext(ctx,
		`SELECT status FROM outbox WHERE tenant_id = ? AND id = ?`, string(tenant), id).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("store: cancel outbox item: %w", err)
	case status == outboxCancelled:
		return nil
	default:
		return ErrNotQueued
	}
}

// FailSendingOutbox moves the stale items in the state "sending" to "failed",
// saves the error text, and deletes the messages and the draft references. An
// item is stale when it was claimed before claimedBefore (strictly earlier). It
// returns the number of items it changed.
//
// It is for items that a program left in "sending" when it stopped. Nobody knows
// if the server got the message, so the item must not be sent again. Other
// programs that use the same database file may be sending right now, and their
// items are "sending" too. The caller chooses claimedBefore so that no send can
// still be running at that age. Newer items are not touched.
//
// It changes the items of ALL tenants, on purpose.
func (s *Store) FailSendingOutbox(ctx context.Context, errText string, claimedBefore time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE outbox SET status = ?, attempts = attempts + 1, error_text = ?, message = NULL, draft_ref = NULL
		WHERE status = ? AND claimed_at < ?`,
		outboxFailed, errText, outboxSending, claimedBefore.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: fail sending outbox items: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: fail sending outbox items: %w", err)
	}
	return int(n), nil
}

// outboxResult runs one statement that changes one item and returns ErrNotFound
// if it changed no row. what names the call in error messages.
func (s *Store) outboxResult(ctx context.Context, what string, tenant account.TenantID, id, query string, args ...any) error {
	if err := validateOutboxKey(tenant, id); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	return requireOneRow(result, what)
}

// validateOutboxKey checks the ids that every call on one item gets.
func validateOutboxKey(tenant account.TenantID, id string) error {
	if err := validateID("tenant", string(tenant)); err != nil {
		return err
	}
	return validateID("outbox item", id)
}

// scanOutbox reads one row with the columns of outboxColumns. Times are
// returned in UTC.
func scanOutbox(row rowScanner) (*OutboxRow, error) {
	var (
		r                                 OutboxRow
		sendAfter, nextAttempt, createdAt int64
		sentAt, claimedAt                 sql.NullInt64
	)
	err := row.Scan(&r.Tenant, &r.ID, &r.Account, &r.Status, &sendAfter, &nextAttempt, &r.Attempts,
		&r.Error, &r.DraftRef, &createdAt, &sentAt, &claimedAt)
	if err != nil {
		return nil, err
	}
	r.SendAfter = time.UnixMilli(sendAfter).UTC()
	r.NextAttemptAt = time.UnixMilli(nextAttempt).UTC()
	r.CreatedAt = time.UnixMilli(createdAt).UTC()
	if sentAt.Valid {
		r.SentAt = time.UnixMilli(sentAt.Int64).UTC()
	}
	if claimedAt.Valid {
		r.ClaimedAt = time.UnixMilli(claimedAt.Int64).UTC()
	}
	return &r, nil
}
